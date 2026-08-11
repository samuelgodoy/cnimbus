// Package pieces fetches the prebuilt "ready pieces" a Nimbusfile build
// assembles from: the kernel image and a static BusyBox binary + its
// applet symlink manifest. No compiling, no Docker -- these are
// produced once (by `cnimbus prepare`, using the Docker-based pipeline in
// internal/compileagent) and published somewhere `cnimbus build-disk` can
// fetch them from: a plain HTTP(S) URL prefix, or a local directory
// for offline/development use.
//
// BusyBox's install tree ships as a single binary plus a symlink
// manifest (relative path -> symlink target), not as a directory of
// real symlinks: on a Windows host, both Docker Desktop bind mounts
// and Go's own os.Symlink lose or mangle POSIX symlinks, so the
// manifest is the only representation that survives the trip
// losslessly on every host OS.
package pieces

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Applet is one BusyBox applet symlink: Path (e.g. "bin/ls") -> Target
// (e.g. "busybox", the relative symlink target).
type Applet struct {
	Path   string
	Target string
}

// Set is the resolved, in-memory prebuilt pieces bundle.
type Set struct {
	Vmlinuz         []byte
	BusyboxBinary   []byte
	BusyboxApplets  []Applet
	BusyboxManifest []byte // raw busybox-manifest.tsv bytes, kept for e.g. a build lockfile's own hash
	// Iptables is the static iptables-legacy multi-call binary (see
	// internal/compileagent/iptables.go), nil if the pieces source
	// predates this feature -- FIREWALL then falls back to its original
	// "COPY your own iptables in" behavior, exactly as if this field
	// didn't exist.
	Iptables []byte
	// Supplicant is the static wpa_supplicant binary (F6.4, see
	// internal/compileagent/wpasupplicant.go), nil unless the pieces
	// source was built with HARDBOOT wifi -- same optional-piece
	// tolerance as Iptables above.
	Supplicant []byte
	// WifiFirmware is the curated firmware set (F6.3, see
	// internal/compileagent/wifi.go's wifiFirmwareBlobs and design.md's
	// D3), keyed by each blob's own /lib/firmware-relative path (e.g.
	// "ath9k_htc/htc_9271-1.4.0.fw") -- nil unless the pieces source was
	// built with HARDBOOT wifi.
	WifiFirmware map[string][]byte
	// EthernetFirmware (AD-057) mirrors WifiFirmware for the wired-NIC
	// driver family (see internal/compileagent/ethernet.go's
	// ethernetFirmwareBlobs) -- nil unless the pieces source was built
	// with a HARDBOOT profile that includes eth ("eth" or "eth+wifi").
	EthernetFirmware map[string][]byte
	// HashesVerified is true if source published a pieces.sha256 and
	// every file matched it. false means no pieces.sha256 was found --
	// not that verification failed (a mismatch is always a hard error,
	// never reflected here).
	HashesVerified bool
	// Provenance is pieces.json's parsed contents (T59), nil if the
	// source has no pieces.json at all (an older `prepare` output, or a
	// source that never had one -- not an error, same tolerance as
	// Iptables above). Callers use this to catch a `prepare`-time
	// setting (ARCH, VGA) mismatching the Nimbusfile being assembled
	// against it, which used to assemble silently into a broken image.
	Provenance *Provenance
}

// Provenance is the subset of pieces.json (see
// internal/compileagent.PiecesProvenance, the schema `cmd/thunder`
// actually writes) that build-disk itself has a reason to read. Kept as
// its own small, decoupled type -- rather than importing
// internal/compileagent's full type here -- so this package's own doc
// comment ("an opaque file... reads the rest only to the extent its own
// callers need") stays true of everything beyond these two fields:
// encoding/json ignores every JSON key with no matching struct field, so
// this can never fail to parse a pieces.json with more fields than it
// looks at.
type Provenance struct {
	Arch string `json:"arch"`
	VGA  bool   `json:"vga"`
	// BootProfile is "" for a pieces.json predating this field -- callers
	// must treat that the same as "none", since every pieces set built
	// before HARDBOOT existed is a "none" (VM-only) build. One of "none",
	// "eth", "wifi", or "eth+wifi" (see internal/nimbusfile's HARDBOOT
	// doc comment) for anything built since.
	BootProfile string `json:"boot_profile"`
	// WifiFirmware lists which /lib/firmware-relative paths this pieces
	// set's WiFi-driver HARDBOOT build actually published (F6.3) --
	// Resolve uses this list to know which "firmware/<path>" files to
	// fetch and verify; empty for "none"/"eth". Deliberately only
	// captures Path here (encoding/json ignores the SourceURL/Version/
	// SHA256 fields internal/compileagent.WifiFirmwareBlob also carries
	// in the full pieces.json) -- same decoupling as this type's own doc
	// comment describes.
	WifiFirmware []struct {
		Path string `json:"path"`
	} `json:"wifi_firmware"`
	// EthernetFirmware (AD-057) mirrors WifiFirmware above for the
	// wired-NIC driver family; empty for "none"/"wifi".
	EthernetFirmware []struct {
		Path string `json:"path"`
	} `json:"ethernet_firmware"`
}

// hasWifiDriver reports whether profile is a WiFi-driver boot profile:
// "wifi", or its explicit-both-drivers spelling "eth+wifi" (see
// internal/nimbusfile's HARDBOOT doc comment).
func hasWifiDriver(profile string) bool {
	return profile == "wifi" || profile == "eth+wifi"
}

// hashesFileName is written by `cnimbus prepare` alongside vmlinuz/busybox/
// busybox-manifest.tsv: one "<sha256-hex>  <filename>" line per file,
// the same format `sha256sum` produces (so it's independently
// verifiable by hand, e.g. `sha256sum -c pieces.sha256`).
const hashesFileName = "pieces.sha256"

// ResolveOptions controls Resolve's network trust posture and caching.
type ResolveOptions struct {
	// AllowInsecureHTTP permits a plain http:// (not https://) pieces
	// source. Off by default: an http:// source is trivially MITM'able,
	// silently substituting an arbitrary kernel/BusyBox into the image.
	AllowInsecureHTTP bool
	// CacheDir, if set, caches an http(s) source's vmlinuz/busybox
	// locally, keyed by source+arch -- skipping the (typically tens of
	// MB) re-download on a later build entirely once cached. Only ever
	// used to skip re-fetching, never to skip verification: the small
	// pieces.sha256 file is always fetched fresh first, and the cache is
	// only trusted if its own hashes still match that fresh answer --
	// so a source that gets republished with different bits is
	// detected and re-fetched, never silently served stale. A source
	// with no pieces.sha256 at all can't be cached this way (there's
	// nothing cheap to check freshness against) and always fetches
	// fully, exactly as if CacheDir were unset. No effect on local
	// directory sources, which are already as fast as a cache would be.
	CacheDir string
	// RequireVerifiedPieces fails Resolve outright if an http(s) source
	// has no pieces.sha256 at all, instead of silently falling back to
	// unverified pieces (Set.HashesVerified=false, only a printed
	// warning from the caller). No effect on a local directory source
	// -- see runBuild's own flag help for why.
	RequireVerifiedPieces bool
	// VerifyKey, if set, is checked against pieces.sha256.sig (T81 step
	// 1): a detached Ed25519 signature over pieces.sha256's exact bytes,
	// written by `cnimbus prepare --pieces-sign-key`. This is the one
	// hop that turns "these bytes match what pieces.sha256 claims"
	// (integrity, already covered by the hash check above) into "these
	// bytes were published by whoever holds the matching private key"
	// (authenticity) -- a source with no pieces.sha256.sig, or one that
	// doesn't verify against this key, is refused outright, the same way
	// a hash mismatch already is. nil (the default) skips this check
	// entirely, exactly as if signing didn't exist -- unsigned pieces
	// keep working precisely as they did before this option existed.
	VerifyKey ed25519.PublicKey
}

// Resolve fetches vmlinuz, busybox, and busybox-manifest.tsv from
// source+"/"+arch, where source is either a local directory path or an
// http(s) URL prefix -- matching the arch-namespaced layout `cnimbus
// prepare` writes (<out>/<arch>/vmlinuz, ...), so one pieces source
// can hold both amd64 and arm64 pieces side by side.
//
// If source publishes a pieces.sha256 (written by `cnimbus prepare`
// alongside the three files it produces), every fetched file's SHA-256
// is checked against it -- a mismatch is a hard error, never a warning:
// this is the one thing standing between "trust whatever bytes a URL
// happens to serve" and actually verifying a build's inputs. Its
// absence (an older `prepare` output, or a source that never had one)
// only produces a warning, printed by the caller via Set.HashesVerified.
func Resolve(source, arch string, opts ResolveOptions) (*Set, error) {
	source = strings.TrimSuffix(source, "/") + "/" + arch
	isHTTP := strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")

	if strings.HasPrefix(source, "http://") && !opts.AllowInsecureHTTP {
		return nil, fmt.Errorf("refusing plain http:// pieces source %q: unauthenticated HTTP lets anyone "+
			"on the network path substitute an arbitrary kernel/BusyBox into your image undetected. "+
			"Use an https:// source, or pass --pieces-insecure-http if you understand the risk (e.g. "+
			"a trusted LAN mirror with no TLS)", source)
	}

	// The (small) hash manifest is always fetched first and fresh, even
	// when a local cache exists: it's the one thing that lets a cached
	// vmlinuz/busybox be trusted without re-downloading them just to
	// check -- if the source ever republishes different pieces under
	// the same URL, this fresh fetch is what notices and forces a
	// re-download, rather than a cache silently going stale forever.
	var hashes map[string]string
	hashData, err := readOptionalPiece(source, hashesFileName)
	if err != nil {
		// A real fetch error (network failure, 5xx, permission denied --
		// anything but "genuinely not there"), not mere absence: read as
		// a hard failure, never silently reinterpreted as "no manifest
		// exists". Treating the two the same is exactly the gap that lets
		// a MITM (or a flaky mirror) force a fetch down to unverified
		// pieces just by breaking the one small request for pieces.sha256.
		return nil, fmt.Errorf("fetching %s: %w", hashesFileName, err)
	}
	if hashData != nil {
		hashes, err = parseHashes(hashData)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", hashesFileName, err)
		}
	} else if isHTTP && opts.RequireVerifiedPieces {
		return nil, fmt.Errorf("%s: no %s published -- refusing to build from unverified pieces "+
			"(pass --pieces-allow-unverified to opt out)", source, hashesFileName)
	}

	// T81 step 1: a --pieces-verify-key pins authenticity, not just
	// integrity -- checked against pieces.sha256's own bytes (hashData),
	// fetched fresh above regardless of any cache, so this applies
	// identically on a cache hit or a cold fetch.
	if opts.VerifyKey != nil {
		if hashData == nil {
			return nil, fmt.Errorf("%w: --pieces-verify-key was given but %s has no %s published to sign in "+
				"the first place", ErrSignatureInvalid, source, hashesFileName)
		}
		sigData, err := readOptionalPiece(source, sigFileName)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", sigFileName, err)
		}
		if sigData == nil {
			return nil, fmt.Errorf("%w: --pieces-verify-key was given but %s has no %s published -- "+
				"re-run \"cnimbus prepare --pieces-sign-key\" against the matching private key",
				ErrSignatureInvalid, source, sigFileName)
		}
		if err := verifySignature(opts.VerifyKey, hashData, strings.TrimSpace(string(sigData))); err != nil {
			return nil, err
		}
	}

	if isHTTP && opts.CacheDir != "" && hashes != nil {
		if set, ok := loadFromCache(cacheDirFor(opts.CacheDir, source), hashes); ok {
			return set, nil
		}
	}

	vmlinuz, err := readPiece(source, "vmlinuz")
	if err != nil {
		return nil, err
	}
	busybox, err := readPiece(source, "busybox")
	if err != nil {
		return nil, err
	}
	manifest, err := readPiece(source, "busybox-manifest.tsv")
	if err != nil {
		return nil, err
	}

	applets, err := parseManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("parsing busybox-manifest.tsv: %w", err)
	}

	// Optional: only present in pieces published by a `prepare` build
	// new enough to produce it (see internal/compileagent/iptables.go).
	// Its absence is not an error -- FIREWALL just falls back to
	// requiring a COPY'd binary, exactly as before this feature existed.
	iptablesBin, iptablesErr := readPiece(source, "iptables")
	haveIptables := iptablesErr == nil

	// Optional (T59), same tolerance as iptables above -- an older
	// `prepare` output predates pieces.json entirely. Read via
	// readOptionalPiece, not readPiece: a genuine fetch error (not mere
	// absence) must still be fatal, same reasoning as hashesFileName's
	// own read a few lines up.
	provData, err := readOptionalPiece(source, "pieces.json")
	if err != nil {
		return nil, fmt.Errorf("fetching pieces.json: %w", err)
	}
	var provenance *Provenance
	if provData != nil {
		var p Provenance
		if err := json.Unmarshal(provData, &p); err != nil {
			return nil, fmt.Errorf("parsing pieces.json: %w", err)
		}
		provenance = &p
	}

	// F6.4: unlike iptables (built unconditionally for every profile, so
	// always worth a fetch attempt), wpa_supplicant only ever exists for
	// a WiFi-driver HARDBOOT build ("wifi" or "eth+wifi") -- gated on
	// provenance saying so, rather than attempted unconditionally, so
	// every "none"/"eth" pieces set (the overwhelming majority) costs
	// zero extra requests fetching a file that provenance already says
	// can't be there. A pieces source with no pieces.json at all
	// (predates HARDBOOT) is treated the same as "none" here, same
	// tolerance BootProfile's own doc comment describes.
	var supplicantBin []byte
	haveSupplicant := false
	if provenance != nil && hasWifiDriver(provenance.BootProfile) {
		bin, err := readPiece(source, "wpa_supplicant")
		haveSupplicant = err == nil
		supplicantBin = bin
	}

	// F6.3: fetched by the exact path list pieces.json's own
	// WifiFirmware recorded (see Provenance's doc comment) -- nil for
	// every profile but "wifi", in which case this loop simply doesn't
	// run and wifiFirmware stays nil, same tolerance as haveIptables/
	// haveSupplicant above.
	var wifiFirmware map[string][]byte
	if provenance != nil && len(provenance.WifiFirmware) > 0 {
		wifiFirmware = make(map[string][]byte, len(provenance.WifiFirmware))
		for _, ref := range provenance.WifiFirmware {
			data, err := readPiece(source, "firmware/"+ref.Path)
			if err != nil {
				return nil, fmt.Errorf("fetching firmware/%s (listed in pieces.json): %w", ref.Path, err)
			}
			wifiFirmware[ref.Path] = data
		}
	}

	// AD-057: mirrors wifiFirmware above for the wired-NIC driver family.
	var ethernetFirmware map[string][]byte
	if provenance != nil && len(provenance.EthernetFirmware) > 0 {
		ethernetFirmware = make(map[string][]byte, len(provenance.EthernetFirmware))
		for _, ref := range provenance.EthernetFirmware {
			data, err := readPiece(source, "firmware/"+ref.Path)
			if err != nil {
				return nil, fmt.Errorf("fetching firmware/%s (listed in pieces.json): %w", ref.Path, err)
			}
			ethernetFirmware[ref.Path] = data
		}
	}

	hashesVerified := false
	if hashes != nil {
		if err := checkHash(hashes, "vmlinuz", vmlinuz); err != nil {
			return nil, err
		}
		if err := checkHash(hashes, "busybox", busybox); err != nil {
			return nil, err
		}
		if err := checkHash(hashes, "busybox-manifest.tsv", manifest); err != nil {
			return nil, err
		}
		if haveIptables {
			if err := checkHash(hashes, "iptables", iptablesBin); err != nil {
				return nil, err
			}
		}
		if haveSupplicant {
			if err := checkHash(hashes, "wpa_supplicant", supplicantBin); err != nil {
				return nil, err
			}
		}
		for path, data := range wifiFirmware {
			if err := checkHash(hashes, "firmware/"+path, data); err != nil {
				return nil, err
			}
		}
		for path, data := range ethernetFirmware {
			if err := checkHash(hashes, "firmware/"+path, data); err != nil {
				return nil, err
			}
		}
		if provData != nil {
			if err := checkHash(hashes, "pieces.json", provData); err != nil {
				return nil, err
			}
		}
		hashesVerified = true
	}

	if isHTTP && opts.CacheDir != "" && hashesVerified {
		// Best-effort: a cache write failure shouldn't fail a build that
		// otherwise fully succeeded, only cost it the speedup next time.
		// wpa_supplicant/WifiFirmware deliberately don't participate in
		// this local dev cache (unlike iptables/pieces.json) -- HB-P-004's
		// "verified on every run including cache hits" requirement is
		// about compileagent.FetchSupplicant's own source-tarball fetch
		// during `prepare` (already always-verified, same as BusyBox/
		// iptables), not about this unrelated build-disk-side HTTP cache,
		// so skipping it here costs only a re-download on a cache hit,
		// never a verification gap.
		_ = saveToCache(cacheDirFor(opts.CacheDir, source), vmlinuz, busybox, manifest, iptablesBin, provData)
	}

	set := &Set{
		Vmlinuz:         vmlinuz,
		BusyboxBinary:   busybox,
		BusyboxApplets:  applets,
		BusyboxManifest: manifest,
		HashesVerified:  hashesVerified,
		Provenance:      provenance,
	}
	if haveIptables {
		set.Iptables = iptablesBin
	}
	if haveSupplicant {
		set.Supplicant = supplicantBin
	}
	if wifiFirmware != nil {
		set.WifiFirmware = wifiFirmware
	}
	if ethernetFirmware != nil {
		set.EthernetFirmware = ethernetFirmware
	}
	return set, nil
}

// cacheDirFor derives a stable, filesystem-safe cache directory name
// from the resolved source URL (already arch-namespaced by Resolve).
func cacheDirFor(baseCacheDir, source string) string {
	sum := sha256.Sum256([]byte(source))
	return filepath.Join(baseCacheDir, hex.EncodeToString(sum[:]))
}

// loadFromCache returns a Set built from previously-cached files if
// all three are present and match wantHashes exactly -- any mismatch
// or missing file is treated as a cache miss (fall through to a real
// fetch), never an error.
func loadFromCache(cacheDir string, wantHashes map[string]string) (*Set, bool) {
	vmlinuz, err := os.ReadFile(filepath.Join(cacheDir, "vmlinuz"))
	if err != nil {
		return nil, false
	}
	busybox, err := os.ReadFile(filepath.Join(cacheDir, "busybox"))
	if err != nil {
		return nil, false
	}
	manifest, err := os.ReadFile(filepath.Join(cacheDir, "busybox-manifest.tsv"))
	if err != nil {
		return nil, false
	}
	if checkHash(wantHashes, "vmlinuz", vmlinuz) != nil ||
		checkHash(wantHashes, "busybox", busybox) != nil ||
		checkHash(wantHashes, "busybox-manifest.tsv", manifest) != nil {
		return nil, false
	}
	applets, err := parseManifest(manifest)
	if err != nil {
		return nil, false
	}
	set := &Set{
		Vmlinuz:         vmlinuz,
		BusyboxBinary:   busybox,
		BusyboxApplets:  applets,
		BusyboxManifest: manifest,
		HashesVerified:  true,
	}
	// iptables is optional even within a cache hit: an older cached
	// entry (written before this feature existed) simply won't have it.
	if iptablesBin, err := os.ReadFile(filepath.Join(cacheDir, "iptables")); err == nil {
		// Same "no entry means refuse" rule checkHash already enforces for
		// every other piece: a cached iptables with no verified hash entry
		// is exactly as untrustworthy as one that fails the hash check.
		if checkHash(wantHashes, "iptables", iptablesBin) != nil {
			return nil, false // cached iptables exists but no longer matches the source (or was never verified) -- treat the whole entry as stale
		}
		set.Iptables = iptablesBin
	}
	// pieces.json (T59), same optional-within-a-cache-hit treatment as
	// iptables above: without this, a cache *hit* skipped ARCH/VGA
	// mismatch detection entirely (only ever checked on a fresh fetch),
	// which is exactly the kind of silent gap this ticket exists to
	// close.
	if provData, err := os.ReadFile(filepath.Join(cacheDir, "pieces.json")); err == nil {
		if checkHash(wantHashes, "pieces.json", provData) != nil {
			return nil, false
		}
		var p Provenance
		if err := json.Unmarshal(provData, &p); err == nil {
			set.Provenance = &p
		}
	}
	return set, true
}

// saveToCache writes the fetched files into cacheDir, via a
// temp-then-rename per file so a process killed mid-write never leaves
// a corrupt file behind for loadFromCache's hash check to (correctly,
// but wastefully) reject on every subsequent run. iptablesBin/provData
// may be nil (source predates that feature) -- simply not written in
// that case.
func saveToCache(cacheDir string, vmlinuz, busybox, manifest, iptablesBin, provData []byte) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{
		"vmlinuz":              vmlinuz,
		"busybox":              busybox,
		"busybox-manifest.tsv": manifest,
	}
	if iptablesBin != nil {
		files["iptables"] = iptablesBin
	}
	if provData != nil {
		files["pieces.json"] = provData
	}
	for name, data := range files {
		dst := filepath.Join(cacheDir, name)
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	return nil
}

func parseHashes(data []byte) (map[string]string, error) {
	hashes := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed line %q (expected \"<sha256-hex>  <filename>\")", line)
		}
		hashes[fields[1]] = strings.ToLower(fields[0])
	}
	return hashes, nil
}

// ErrHashMismatch wraps both checkHash failure modes (T50): a missing
// manifest entry (refused as unverified) and an actual SHA-256 mismatch
// (corrupted download or tampering). A caller can errors.Is against it
// to map "the fetched pieces don't match what they claim to be" to its
// own exit code, distinct from a transient network failure fetching them
// in the first place.
var ErrHashMismatch = errors.New("pieces hash verification failed")

func checkHash(hashes map[string]string, name string, data []byte) error {
	want, ok := hashes[name]
	if !ok {
		return fmt.Errorf("%w: %s has no entry in %s -- refusing to trust an unverified file", ErrHashMismatch, name, hashesFileName)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("%w: SHA-256 mismatch for %s: expected %s, got %s -- the file does not match %s "+
			"(corrupted download, or the source was tampered with)", ErrHashMismatch, name, want, got, hashesFileName)
	}
	return nil
}

func readPiece(source, name string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		url := strings.TrimSuffix(source, "/") + "/" + name
		client := &http.Client{Timeout: 10 * time.Minute}
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", url, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching %s: HTTP %s", url, resp.Status)
		}
		return io.ReadAll(resp.Body)
	}

	path := filepath.Join(source, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return data, nil
}

// readOptionalPiece is readPiece for a file that may legitimately not
// exist (only ever used for pieces.sha256, which predates some
// published pieces sets): a genuine 404 (http/https) or a
// non-existent path (local dir) returns (nil, nil) -- distinct from
// (nil, err) for any other failure, which callers must treat as fatal
// rather than reinterpreting as "the file was never there".
func readOptionalPiece(source, name string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		url := strings.TrimSuffix(source, "/") + "/" + name
		client := &http.Client{Timeout: 10 * time.Minute}
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", url, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching %s: HTTP %s", url, resp.Status)
		}
		return io.ReadAll(resp.Body)
	}

	path := filepath.Join(source, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return data, nil
}

func parseManifest(data []byte) ([]Applet, error) {
	var applets []Applet
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		path, target, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed line %q (expected path<TAB>target)", line)
		}
		applets = append(applets, Applet{Path: path, Target: target})
	}
	return applets, nil
}
