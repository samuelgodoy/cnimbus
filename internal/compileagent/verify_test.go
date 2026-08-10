package compileagent

import (
	"crypto/sha1"
	"os"
	"testing"
)

// zbase32 known-answer tests, cross-checked against real kernel.org WKD
// lookups performed by hand while building this feature (fetching
// https://kernel.org/.well-known/openpgpkey/hu/<hash>?l=<local> for each
// local-part below returned HTTP 200 with a real key -- see verify.go's
// doc comment). This guards against a subtle zbase32 implementation bug
// silently causing every WKD lookup to 404, which would otherwise only
// surface as "could not fetch any known kernel.org signer's key" with no
// clue *why*.
func TestZbase32KnownAnswers(t *testing.T) {
	tests := []struct {
		local string
		want  string
	}{
		{"gregkh", "e3n9xnm94c5apezqnj1pmrfuaoyfm8cf"},
		{"torvalds", "pf113mfnx1f3eb1yiwhsipa91xfc7o4x"},
		{"sashal", "7j1cnb5wfkj3ts7cgy4xz1gwsz9xs5mj"},
	}
	for _, tt := range tests {
		h := sha1.Sum([]byte(tt.local))
		got := zbase32(h[:])
		if got != tt.want {
			t.Errorf("zbase32(sha1(%q)) = %q, want %q", tt.local, got, tt.want)
		}
	}
}

// TestVerifyKernelTarballLive is a real, network-dependent end-to-end
// check against an actual, historical kernel.org release + its
// published signature, fetching the signer's key live via WKD -- no
// mocking, because the whole point of this feature is that it talks to
// the real kernel.org. Skipped in `go test -short` (CI's default) since
// it downloads a full kernel tarball (~150MB); run explicitly with
// `go test -run TestVerifyKernelTarballLive ./internal/compileagent`.
func TestVerifyKernelTarballLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent, ~150MB download in -short mode")
	}
	if os.Getenv("CNIMBUS_TEST_NETWORK") == "" {
		t.Skip("set CNIMBUS_TEST_NETWORK=1 to run this live kernel.org integration test")
	}

	tmp := t.TempDir()
	tarballPath := tmp + "/linux-7.1.5.tar.xz"
	if err := downloadFile("https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.5.tar.xz", tarballPath); err != nil {
		t.Skipf("could not reach kernel.org (offline test environment?): %v", err)
	}

	signedBy, err := VerifyKernelTarball(tarballPath, "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.5.tar.sign")
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
	if signedBy == "" {
		t.Error("expected a non-empty signer identity")
	}
	t.Logf("verified, signed by: %s", signedBy)
}
