package compileagent

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ulikunitz/xz"
)

// ErrVerification wraps a genuine PGP signature-verification failure
// (T50) -- the tarball's signature did not check out against any known
// kernel.org signer key. Distinct from a fetch/network failure (nothing
// was even checked yet): errors.Is against this lets a caller (e.g.
// cmd/cnimbus/main.go) map "the download is fine but doesn't match what
// it claims to be" to its own exit code, separate from a transient
// upstream-unreachable failure a CI pipeline might reasonably retry.
var ErrVerification = errors.New("kernel tarball signature verification failed")

// releaseSigner pairs a kernel.org identity (its @kernel.org WKD
// local-part) with its primary key's fingerprint, pinned as a
// constant.
type releaseSigner struct {
	localPart   string
	fingerprint string // 40 hex chars, no spaces, uppercase -- v4 primary key fingerprint
}

// releaseSigners is the set of kernel.org identities this verifies a
// release tarball's detached signature against. No key *material* is
// embedded in this binary anywhere -- every key is still fetched
// fresh, over HTTPS, from kernel.org's own Web Key Directory at verify
// time, the same trust boundary kernelinfo.Resolve already relies on
// for releases.json itself. What *is* pinned here is each identity's
// fingerprint: fetchSignerKeyring rejects any WKD-fetched key whose
// primary fingerprint doesn't match before it ever reaches the
// verification keyring, closing the gap where a compromised or
// MITM'd WKD response could substitute an attacker's own key under
// the same @kernel.org local-part with nothing else to catch it.
// Fingerprints verified against https://www.kernel.org/signature.html
// (kernel.org's own documented fingerprint list) at implementation
// time, independently of the WKD fetch path they're pinned against.
// Covers mainline/rc (Torvalds), and stable/longterm (Kroah-Hartman,
// Levin) -- kernel.org's three most common release signers in recent
// history. A release signed by someone outside this list (it happens,
// longterm maintainers rotate) fails closed with an actionable error
// rather than silently skipping verification -- see
// --insecure-skip-kernel-verify for the deliberate opt-out.
var releaseSigners = []releaseSigner{
	{localPart: "gregkh", fingerprint: "647F28654894E3BD457199BE38DBBDC86092693E"},
	{localPart: "torvalds", fingerprint: "ABAF11C65A2970B130ABE3C479BE3E4300411886"},
	{localPart: "sashal", fingerprint: "E27E5D8A3403A2EF66873BBCDEA66FF797772CDC"},
}

// releaseSignerLocalParts is releaseSigners' local-parts alone, for
// error messages that just need to name the trusted identities without
// spelling out every fingerprint.
func releaseSignerLocalParts() []string {
	parts := make([]string, len(releaseSigners))
	for i, s := range releaseSigners {
		parts[i] = s.localPart
	}
	return parts
}

// primaryFingerprint returns e's primary key's fingerprint as
// uppercase hex, no spaces -- the same form releaseSigners pins.
func primaryFingerprint(e *openpgp.Entity) string {
	if e.PrimaryKey == nil {
		return ""
	}
	return strings.ToUpper(fmt.Sprintf("%x", e.PrimaryKey.Fingerprint))
}

const wkdDomain = "kernel.org"

// zbase32Alphabet is the alphabet WKD's key-lookup hash uses -- distinct
// from RFC 4648 base32, per the Web Key Directory spec (draft-koch).
const zbase32Alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"

func zbase32(data []byte) string {
	var sb strings.Builder
	bits, value := 0, 0
	for _, b := range data {
		value = (value << 8) | int(b)
		bits += 8
		for bits >= 5 {
			sb.WriteByte(zbase32Alphabet[(value>>(bits-5))&0x1F])
			bits -= 5
		}
	}
	if bits > 0 {
		sb.WriteByte(zbase32Alphabet[(value<<(5-bits))&0x1F])
	}
	return sb.String()
}

// fetchWKDKey fetches one identity's OpenPGP public key from kernel.org's
// Web Key Directory, "direct method" (no openpgpkey.<domain> subdomain --
// verified empirically that kernel.org only serves the direct-method path
// at <domain>/.well-known/openpgpkey/hu/<hash>, not the advanced method's
// subdomain, which has no DNS entry at all).
func fetchWKDKey(client *http.Client, localPart string) (openpgp.EntityList, error) {
	h := sha1.Sum([]byte(strings.ToLower(localPart)))
	url := fmt.Sprintf("https://%s/.well-known/openpgpkey/hu/%s?l=%s", wkdDomain, zbase32(h[:]), localPart)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching WKD key for %s@%s: %w", localPart, wkdDomain, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching WKD key for %s@%s: HTTP %s", localPart, wkdDomain, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return openpgp.ReadKeyRing(bytes.NewReader(data))
}

// fetchSignerKeyring fetches every releaseSigners identity's key,
// tolerating individual failures (a candidate no longer publishing a WKD
// record isn't fatal as long as at least one does) -- combined into one
// keyring so CheckDetachedSignature can match whichever one the
// signature's issuer key ID actually points to. Every fetched entity's
// primary-key fingerprint is checked against its pinned
// releaseSigner.fingerprint before being added to the keyring -- a
// compromised or MITM'd WKD response substituting a different key
// under the same @kernel.org local-part is rejected outright rather
// than silently trusted just because the *address* matched.
func fetchSignerKeyring() (openpgp.EntityList, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	var keys openpgp.EntityList
	var errs []string
	for _, signer := range releaseSigners {
		k, err := fetchWKDKey(client, signer.localPart)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, e := range k {
			got := primaryFingerprint(e)
			if got != signer.fingerprint {
				errs = append(errs, fmt.Sprintf("%s@%s: fetched key fingerprint %s does not match pinned %s -- rejected",
					signer.localPart, wkdDomain, got, signer.fingerprint))
				continue
			}
			keys = append(keys, e)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("could not fetch any known kernel.org signer's key via WKD: %s", strings.Join(errs, "; "))
	}
	return keys, nil
}

// VerifyKernelTarball checks tarballPath's detached PGP signature
// (fetched from sigURL, kernel.org's own "<version>.tar.sign") against
// known kernel.org release-signer keys, fetched live via WKD.
//
// Per kernel.org's own documented verification procedure, the signature
// covers the *decompressed* tar stream, not the .tar.xz bytes on disk --
// verified empirically against a real stable release tarball+signature:
// checking the raw .tar.xz bytes fails RSA verification even against the
// correct key, while checking the xz-decompressed stream succeeds. This
// decompresses via the already-vendored github.com/ulikunitz/xz (pure
// Go, no external xz binary) purely to compute the signed hash; it does
// not write the decompressed bytes anywhere -- extraction still happens
// separately, streaming its own decompression pass over the same file.
func VerifyKernelTarball(tarballPath, sigURL string) (signedBy string, err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sigURL)
	if err != nil {
		return "", fmt.Errorf("fetching signature %s: %w", sigURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching signature %s: HTTP %s", sigURL, resp.Status)
	}
	sigData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	block, err := armor.Decode(bytes.NewReader(sigData))
	if err != nil {
		return "", fmt.Errorf("decoding armored signature from %s: %w", sigURL, err)
	}

	keys, err := fetchSignerKeyring()
	if err != nil {
		return "", err
	}

	f, err := os.Open(tarballPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only handle; nothing meaningful to do with a close error here

	xr, err := xz.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("opening %s as xz for verification: %w", tarballPath, err)
	}

	signer, err := openpgp.CheckDetachedSignature(keys, xr, block.Body, nil)
	if err != nil {
		return "", fmt.Errorf("%w against known kernel.org signer keys (%s): %w", ErrVerification, strings.Join(releaseSignerLocalParts(), ", "), err)
	}
	for id := range signer.Identities {
		signedBy = id
		break
	}
	return signedBy, nil
}
