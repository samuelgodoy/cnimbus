package pieces

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLocalPieces(t *testing.T, arch string, vmlinuz, busybox, manifest []byte, withHashes bool) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, arch)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"vmlinuz":              vmlinuz,
		"busybox":              busybox,
		"busybox-manifest.tsv": manifest,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withHashes {
		var b strings.Builder
		for _, name := range []string{"vmlinuz", "busybox", "busybox-manifest.tsv"} {
			sum := sha256.Sum256(files[name])
			fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
		if err := os.WriteFile(filepath.Join(dir, "pieces.sha256"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveLocalDirNoHashes(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(set.Vmlinuz) != "KERNEL" || string(set.BusyboxBinary) != "BUSYBOX" {
		t.Errorf("Set = %+v", set)
	}
	if len(set.BusyboxApplets) != 1 || set.BusyboxApplets[0].Path != "bin/ls" || set.BusyboxApplets[0].Target != "busybox" {
		t.Errorf("BusyboxApplets = %+v", set.BusyboxApplets)
	}
	if set.HashesVerified {
		t.Error("HashesVerified should be false with no pieces.sha256 present")
	}
}

func TestResolveLocalDirWithValidHashes(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), true)
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !set.HashesVerified {
		t.Error("HashesVerified should be true when pieces.sha256 matches")
	}
}

// T59: Resolve must parse pieces.json when present and return it in
// Set.Provenance -- build.go compares Arch/VGA against the Nimbusfile
// being assembled.
func TestResolveParsesPiecesJSONProvenance(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	provJSON := []byte(`{"schema_version":2,"arch":"amd64","vga":true}`)
	if err := os.WriteFile(filepath.Join(root, "amd64", "pieces.json"), provJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Provenance == nil {
		t.Fatal("expected Provenance to be populated from pieces.json")
	}
	if set.Provenance.Arch != "amd64" || !set.Provenance.VGA {
		t.Errorf("Provenance = %+v, want Arch=amd64 VGA=true", set.Provenance)
	}
}

// F6.2: Resolve must also parse boot_profile when present, and an absent
// field (a pre-HARDBOOT pieces.json) must unmarshal to "" -- build.go
// normalizes that to "none" itself, not this package.
func TestResolveParsesBootProfileProvenance(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	provJSON := []byte(`{"schema_version":2,"arch":"amd64","vga":false,"boot_profile":"eth"}`)
	if err := os.WriteFile(filepath.Join(root, "amd64", "pieces.json"), provJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Provenance == nil || set.Provenance.BootProfile != "eth" {
		t.Errorf("Provenance = %+v, want BootProfile=eth", set.Provenance)
	}
}

// TestResolveFetchesSupplicantForEthPlusWifi confirms hasWifiDriver's
// gating extends to "eth+wifi", not just the exact string "wifi" --
// wpa_supplicant must be fetched and returned in Set.Supplicant for a
// boot_profile of "eth+wifi" exactly as it would for "wifi" alone.
func TestResolveFetchesSupplicantForEthPlusWifi(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	provJSON := []byte(`{"schema_version":2,"arch":"amd64","vga":false,"boot_profile":"eth+wifi"}`)
	if err := os.WriteFile(filepath.Join(root, "amd64", "pieces.json"), provJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "amd64", "wpa_supplicant"), []byte("SUPPLICANT"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(set.Supplicant) != "SUPPLICANT" {
		t.Errorf("Supplicant = %q, want it fetched for boot_profile=eth+wifi same as boot_profile=wifi", set.Supplicant)
	}
}

func TestResolveParsesPiecesJSONWithNoBootProfileField(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	provJSON := []byte(`{"schema_version":2,"arch":"amd64","vga":false}`)
	if err := os.WriteFile(filepath.Join(root, "amd64", "pieces.json"), provJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Provenance == nil || set.Provenance.BootProfile != "" {
		t.Errorf("Provenance = %+v, want BootProfile=\"\" for a pieces.json predating HARDBOOT", set.Provenance)
	}
}

func TestResolveNoPiecesJSONMeansNilProvenance(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Provenance != nil {
		t.Errorf("expected nil Provenance with no pieces.json present, got %+v", set.Provenance)
	}
}

// A pieces.sha256 that covers pieces.json must still be checked against
// it, same as every other piece -- a tampered ARCH/VGA claim is exactly
// as much an integrity concern as a tampered vmlinuz.
func TestResolvePiecesJSONCoveredByHashesAndTamperDetected(t *testing.T) {
	vmlinuz, busybox, manifest := []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n")
	root := writeLocalPieces(t, "amd64", vmlinuz, busybox, manifest, false)
	provJSON := []byte(`{"schema_version":2,"arch":"amd64","vga":false}`)
	dir := filepath.Join(root, "amd64")
	if err := os.WriteFile(filepath.Join(dir, "pieces.json"), provJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for name, data := range map[string][]byte{
		"vmlinuz": vmlinuz, "busybox": busybox, "busybox-manifest.tsv": manifest, "pieces.json": provJSON,
	} {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "pieces.sha256"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := Resolve(root, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !set.HashesVerified || set.Provenance == nil || set.Provenance.VGA {
		t.Errorf("expected verified hashes and Provenance.VGA=false, got HashesVerified=%v Provenance=%+v", set.HashesVerified, set.Provenance)
	}

	// Tamper with pieces.json after pieces.sha256 was computed from the
	// original bytes -- a fake "VGA=true" claim must be rejected, not
	// silently trusted.
	tampered := []byte(`{"schema_version":2,"arch":"amd64","vga":true}`)
	if err := os.WriteFile(filepath.Join(dir, "pieces.json"), tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "amd64", ResolveOptions{}); err == nil {
		t.Error("expected a hash-mismatch error after tampering with pieces.json")
	}
}

func TestResolveLocalDirCorruptedFileFailsHash(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), true)
	// Corrupt vmlinuz after pieces.sha256 was computed from the original bytes.
	if err := os.WriteFile(filepath.Join(root, "amd64", "vmlinuz"), []byte("TAMPERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(root, "amd64", ResolveOptions{})
	if err == nil {
		t.Fatal("expected a hash mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %v, want mismatch", err)
	}
}

func TestResolveLocalDirMissingHashEntryFails(t *testing.T) {
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	// Write a pieces.sha256 that only covers vmlinuz, not busybox/manifest.
	sum := sha256.Sum256([]byte("KERNEL"))
	partial := fmt.Sprintf("%s  vmlinuz\n", hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(root, "amd64", "pieces.sha256"), []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(root, "amd64", ResolveOptions{})
	if err == nil {
		t.Fatal("expected an error for a file missing from pieces.sha256")
	}
}

func TestResolveArchNamespacing(t *testing.T) {
	root := writeLocalPieces(t, "arm64", []byte("ARM-KERNEL"), []byte("ARM-BB"), []byte(""), false)
	// amd64 doesn't exist under root -- must fail, proving arch namespacing is respected.
	if _, err := Resolve(root, "amd64", ResolveOptions{}); err == nil {
		t.Fatal("expected error resolving a missing arch subdirectory")
	}
	set, err := Resolve(root, "arm64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(set.Vmlinuz) != "ARM-KERNEL" {
		t.Errorf("Vmlinuz = %q", set.Vmlinuz)
	}
}

func TestResolveOverHTTPS(t *testing.T) {
	files := map[string][]byte{
		"vmlinuz":              []byte("KERNEL"),
		"busybox":              []byte("BUSYBOX"),
		"busybox-manifest.tsv": []byte("bin/ls\tbusybox\n"),
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/amd64/")
		data, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()

	// httptest.NewTLSServer's own client trusts its self-signed cert;
	// swap the default http.Client used inside readPiece isn't directly
	// possible without a package-level override, so this exercises the
	// URL-prefix branch via the server's own client by hitting it
	// through a transport override at the process level instead.
	orig := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = orig }()

	set, err := Resolve(srv.URL, "amd64", ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(set.Vmlinuz) != "KERNEL" {
		t.Errorf("Vmlinuz = %q", set.Vmlinuz)
	}
}

func TestResolveRefusesPlainHTTPByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
	}))
	defer srv.Close()

	_, err := Resolve(srv.URL, "amd64", ResolveOptions{})
	if err == nil {
		t.Fatal("expected plain http:// source to be refused by default")
	}
	if !strings.Contains(err.Error(), "refusing plain http") {
		t.Errorf("error = %v, want a clear refusal message", err)
	}
}

func TestResolveAllowsPlainHTTPWithOptIn(t *testing.T) {
	files := map[string][]byte{
		"vmlinuz":              []byte("KERNEL"),
		"busybox":              []byte("BUSYBOX"),
		"busybox-manifest.tsv": []byte(""),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/amd64/")
		data, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()

	set, err := Resolve(srv.URL, "amd64", ResolveOptions{AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(set.Vmlinuz) != "KERNEL" {
		t.Errorf("Vmlinuz = %q", set.Vmlinuz)
	}
}

func TestResolveHTTPWithNoHashesFailsWhenRequired(t *testing.T) {
	files := map[string][]byte{
		"vmlinuz":              []byte("KERNEL"),
		"busybox":              []byte("BUSYBOX"),
		"busybox-manifest.tsv": []byte(""),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/amd64/")
		data, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound) // no pieces.sha256 published
			return
		}
		w.Write(data)
	}))
	defer srv.Close()

	if _, err := Resolve(srv.URL, "amd64", ResolveOptions{
		AllowInsecureHTTP:     true,
		RequireVerifiedPieces: true,
	}); err == nil {
		t.Fatal("expected an http(s) source with no pieces.sha256 to be refused when RequireVerifiedPieces is set")
	}

	// The opt-out still works, same as before this option existed.
	set, err := Resolve(srv.URL, "amd64", ResolveOptions{AllowInsecureHTTP: true, RequireVerifiedPieces: false})
	if err != nil {
		t.Fatalf("expected RequireVerifiedPieces=false to allow building from unverified pieces: %v", err)
	}
	if set.HashesVerified {
		t.Error("HashesVerified should be false: no pieces.sha256 was published")
	}
}

func TestResolveHTTPHashesFetchErrorIsFatalNotTreatedAsAbsent(t *testing.T) {
	files := map[string][]byte{
		"vmlinuz":              []byte("KERNEL"),
		"busybox":              []byte("BUSYBOX"),
		"busybox-manifest.tsv": []byte(""),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/amd64/")
		if name == "pieces.sha256" {
			w.WriteHeader(http.StatusInternalServerError) // a real fetch failure, not "not found"
			return
		}
		data, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()

	// Even with RequireVerifiedPieces off, a genuine fetch error (as
	// opposed to a 404) must still be fatal -- it must never be silently
	// reinterpreted as "no pieces.sha256 exists".
	if _, err := Resolve(srv.URL, "amd64", ResolveOptions{AllowInsecureHTTP: true}); err == nil {
		t.Fatal("expected a 500 fetching pieces.sha256 to be a hard error, not treated as absence")
	}
}

func TestResolveCachesHTTPSourceAndSkipsRedownload(t *testing.T) {
	files := map[string][]byte{
		"vmlinuz":              []byte("KERNEL-v1"),
		"busybox":              []byte("BUSYBOX-v1"),
		"busybox-manifest.tsv": []byte("bin/ls\tbusybox\n"),
	}
	sha := func(name string) string {
		sum := sha256.Sum256(files[name])
		return hex.EncodeToString(sum[:])
	}
	hashesFile := func() []byte {
		var b strings.Builder
		for _, name := range []string{"vmlinuz", "busybox", "busybox-manifest.tsv"} {
			fmt.Fprintf(&b, "%s  %s\n", sha(name), name)
		}
		return []byte(b.String())
	}

	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		name := strings.TrimPrefix(r.URL.Path, "/amd64/")
		if name == hashesFileName {
			w.Write(hashesFile())
			return
		}
		data, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()

	orig := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = orig }()

	cacheDir := t.TempDir()

	set1, err := Resolve(srv.URL, "amd64", ResolveOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if string(set1.Vmlinuz) != "KERNEL-v1" {
		t.Fatalf("Vmlinuz = %q", set1.Vmlinuz)
	}
	firstRunRequests := requests
	if firstRunRequests != 6 { // pieces.sha256 + vmlinuz + busybox + manifest + iptables (404) + pieces.json (404) -- this source predates both
		t.Fatalf("expected 6 requests on a cold cache, got %d", firstRunRequests)
	}

	requests = 0
	set2, err := Resolve(srv.URL, "amd64", ResolveOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if string(set2.Vmlinuz) != "KERNEL-v1" {
		t.Fatalf("Vmlinuz (cached) = %q", set2.Vmlinuz)
	}
	if !set2.HashesVerified {
		t.Error("a cache hit should still report HashesVerified=true")
	}
	if requests != 1 {
		t.Errorf("expected only the pieces.sha256 request on a warm cache hit, got %d requests", requests)
	}
}

func TestResolveCacheMissOnChangedSourceRefetches(t *testing.T) {
	files := map[string][]byte{
		"vmlinuz":              []byte("KERNEL-v1"),
		"busybox":              []byte("BUSYBOX-v1"),
		"busybox-manifest.tsv": []byte("bin/ls\tbusybox\n"),
	}
	hashesFile := func() []byte {
		var b strings.Builder
		for _, name := range []string{"vmlinuz", "busybox", "busybox-manifest.tsv"} {
			sum := sha256.Sum256(files[name])
			fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
		return []byte(b.String())
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/amd64/")
		if name == hashesFileName {
			w.Write(hashesFile())
			return
		}
		data, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	orig := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = orig }()

	cacheDir := t.TempDir()
	if _, err := Resolve(srv.URL, "amd64", ResolveOptions{CacheDir: cacheDir}); err != nil {
		t.Fatal(err)
	}

	// The source republishes different bits under the same URL.
	files["vmlinuz"] = []byte("KERNEL-v2-CHANGED")

	set, err := Resolve(srv.URL, "amd64", ResolveOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if string(set.Vmlinuz) != "KERNEL-v2-CHANGED" {
		t.Errorf("Vmlinuz = %q, want the new content -- a stale cache must never be served once the source's own hashes changed", set.Vmlinuz)
	}
}

func TestParseManifestMalformedLine(t *testing.T) {
	_, err := parseManifest([]byte("no-tab-here\n"))
	if err == nil {
		t.Fatal("expected error for line without a tab separator")
	}
}

func TestParseManifestEmpty(t *testing.T) {
	applets, err := parseManifest([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(applets) != 0 {
		t.Errorf("expected no applets, got %v", applets)
	}
}

func TestParseHashesMalformedLine(t *testing.T) {
	_, err := parseHashes([]byte("onlyonefield\n"))
	if err == nil {
		t.Fatal("expected error for malformed hashes line")
	}
}

func TestParseHashesCaseInsensitive(t *testing.T) {
	sum := sha256.Sum256([]byte("data"))
	upper := strings.ToUpper(hex.EncodeToString(sum[:]))
	hashes, err := parseHashes([]byte(upper + "  vmlinuz\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkHash(hashes, "vmlinuz", []byte("data")); err != nil {
		t.Errorf("checkHash should accept uppercase hex in the file: %v", err)
	}
}
