// Command gensync copies cmd/thunder's and internal/compileagent's real,
// normal-Go-module source into the embedded copy under
// internal/assets/data/thunder-src, which `cnimbus prepare` compiles
// fresh inside a container (see BUILD.md's "Modifying Thunder's
// source"). Invoked via `go generate ./internal/assets`.
//
// The two trees exist because Thunder is its own tiny Go module
// (embedded as *source*, not a prebuilt binary -- go.mod.embed, renamed
// at compile time since go:embed refuses a directory containing a real
// go.mod), while cmd/thunder and internal/compileagent are also normal,
// independently-buildable packages of cnimbus's own module (so `go
// build`/`go test` can compile and test them directly, without a
// container). Keeping both in sync by hand was error-prone -- see
// assets_sync_test.go, which fails CI the moment they drift.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}

	copies := []struct{ src, dst string }{
		{filepath.Join(root, "cmd", "thunder", "main.go"), filepath.Join(root, "internal", "assets", "data", "thunder-src", "cmd", "thunder", "main.go")},
	}

	compileagentDir := filepath.Join(root, "internal", "compileagent")
	entries, err := os.ReadDir(compileagentDir)
	if err != nil {
		fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || isTestFile(e.Name()) {
			continue
		}
		copies = append(copies, struct{ src, dst string }{
			filepath.Join(compileagentDir, e.Name()),
			filepath.Join(root, "internal", "assets", "data", "thunder-src", "internal", "compileagent", e.Name()),
		})
	}

	for _, c := range copies {
		if err := copyFile(c.src, c.dst); err != nil {
			fatal(fmt.Errorf("copying %s -> %s: %w", c.src, c.dst, err))
		}
		fmt.Printf("synced %s\n", filepath.Base(c.dst))
	}
	fmt.Println("done -- if internal/compileagent's dependencies changed, also re-run:")
	fmt.Println("  cd internal/assets/data/thunder-src && mv go.mod.embed go.mod && go mod tidy && go mod vendor && mv go.mod go.mod.embed")
}

func isTestFile(name string) bool {
	return len(name) > 8 && name[len(name)-8:] == "_test.go"
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// repoRoot finds the module root by walking up from the current
// directory (go:generate runs with cwd set to the package it's declared
// in) until go.mod is found.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root (no go.mod in any parent of %s)", dir)
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gensync:", err)
	os.Exit(1)
}
