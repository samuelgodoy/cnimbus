package assets

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCnimbusAgentInSyncWithEmbeddedCopy fails if cmd/cnimbusagent's
// source has drifted from the prebuilt binaries embedded under
// data/cnimbusagent/ -- the ones `cnimbus build-disk` actually ships.
// Rebuilds fresh copies with the exact same flags as
// internal/assets/genagent and compares bytes; -trimpath alone is not
// enough for this to be reproducible across separate build invocations
// (the linker's build ID varies build-to-build even for identical
// source/flags/Go version) -- -buildid= is what actually makes it
// deterministic, confirmed empirically. Run `go generate
// ./internal/assets` and commit the result if this fails.
func TestCnimbusAgentInSyncWithEmbeddedCopy(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	embedded := map[string][]byte{
		"amd64": CnimbusAgentAmd64,
		"arm64": CnimbusAgentArm64,
	}

	for arch, want := range embedded {
		out := filepath.Join(t.TempDir(), "cnimbusagent-"+arch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -buildid=", "-o", out, "./cmd/cnimbusagent")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building cnimbusagent-%s: %v\n%s", arch, err, out)
		}
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("reading freshly built cnimbusagent-%s: %v", arch, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("cnimbusagent-%s has drifted from its embedded copy -- run `go generate ./internal/assets` and commit the result", arch)
		}
	}
}
