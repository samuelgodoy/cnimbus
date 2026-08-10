package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T58: the output image's hash must be computed by streaming the file
// rather than reading it whole into memory -- sha256File is the
// streaming replacement for what used to be os.ReadFile + sha256Hex.
func TestSha256FileMatchesSha256Hex(t *testing.T) {
	data := []byte("some file contents, doesn't matter what")
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	want := sha256Hex(data)
	if got != want {
		t.Errorf("sha256File(%s) = %s, want %s (matching sha256Hex of the same bytes)", path, got, want)
	}

	sum := sha256.Sum256(data)
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256File(%s) = %s, doesn't match a direct sha256.Sum256", path, got)
	}
}

func TestSha256FileErrorsOnMissingFile(t *testing.T) {
	if _, err := sha256File(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

// T57: writeLockfile's JSON shape had no test at all -- a field rename
// or an accidentally-dropped `omitempty` would have shipped silently.
// Golden-shape assertion rather than a byte-for-byte golden file: this
// project's own JSON tags are the contract to protect, not incidental
// key ordering.
func TestWriteLockfileJSONShape(t *testing.T) {
	lock := BuildLock{
		CnimbusVersion:       "1.2.3",
		BuiltAt:              "2026-07-31T00:00:00Z",
		Nimbusfile:           "Nimbusfile",
		NimbusfileSHA256:     "aaaa",
		Arch:                 "amd64",
		Format:               "iso",
		PiecesSource:         "./pieces",
		VmlinuzSHA256:        "bbbb",
		BusyboxSHA256:        "cccc",
		ManifestSHA256:       "dddd",
		PiecesHashesVerified: true,
		OutputImage:          "out.iso",
		OutputImageSHA256:    "eeee",
	}
	path := filepath.Join(t.TempDir(), "out.iso.lock")
	if err := writeLockfile(path, lock); err != nil {
		t.Fatalf("writeLockfile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("lockfile is not valid JSON: %v\n%s", err, data)
	}
	for _, key := range []string{
		"cnimbus_version", "built_at", "nimbusfile", "nimbusfile_sha256",
		"arch", "format", "pieces_source", "vmlinuz_sha256", "busybox_sha256",
		"busybox_manifest_sha256", "pieces_hashes_verified", "output_image",
		"output_image_sha256",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("lockfile JSON missing expected key %q: %s", key, data)
		}
	}
	// IptablesSHA256/AgentSHA256 are omitempty (unset here) -- must be
	// absent, not present-as-empty-string, or a consumer checking
	// presence to detect "no FIREWALL/AGENT directive" would misfire.
	for _, key := range []string{"iptables_sha256", "agent_sha256"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("lockfile JSON should omit unset omitempty key %q: %s", key, data)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(string(data)), "}") {
		t.Errorf("expected lockfile JSON to end cleanly: %s", data)
	}
}
