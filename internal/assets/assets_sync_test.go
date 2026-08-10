package assets

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestThunderSourceInSyncWithEmbeddedCopy fails if cmd/thunder or
// internal/compileagent's real, independently-buildable source has
// drifted from the embedded copy under data/thunder-src that `cnimbus
// prepare` actually compiles inside a container. See
// internal/assets/gensync and CONTRIBUTING.md: `go generate
// ./internal/assets` re-syncs it.
func TestThunderSourceInSyncWithEmbeddedCopy(t *testing.T) {
	real := map[string]string{
		filepath.Join("..", "..", "cmd", "thunder", "main.go"): "data/thunder-src/cmd/thunder/main.go",
	}

	compileagentDir := filepath.Join("..", "compileagent")
	entries, err := os.ReadDir(compileagentDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || len(e.Name()) > 8 && e.Name()[len(e.Name())-8:] == "_test.go" {
			continue
		}
		real[filepath.Join(compileagentDir, e.Name())] = "data/thunder-src/internal/compileagent/" + e.Name()
	}

	for realPath, embedPath := range real {
		wantData, err := os.ReadFile(realPath)
		if err != nil {
			t.Fatalf("reading real source %s: %v", realPath, err)
		}
		gotData, err := fs.ReadFile(ThunderSrc, embedPath)
		if err != nil {
			t.Fatalf("reading embedded copy %s: %v", embedPath, err)
		}
		if string(wantData) != string(gotData) {
			t.Errorf("%s has drifted from its embedded copy %s -- run `go generate ./internal/assets` and commit the result", realPath, embedPath)
		}
	}
}
