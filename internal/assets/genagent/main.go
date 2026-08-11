// Command genagent cross-compiles cmd/cnimbusagent for every arch
// cnimbus itself targets (amd64, arm64) and writes the result into
// internal/assets/data/cnimbusagent/, where assets.go embeds it as a
// prebuilt guest binary -- used by `cnimbus build-disk`, needs no
// Docker, no kernel/BusyBox-version dependency to recompile against.
// Invoked via `go generate ./internal/assets`; see assets_sync_test.go,
// which fails if the embedded binaries drift from a fresh build of
// cmd/cnimbusagent's current source.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var arches = []string{"amd64", "arm64"}

func main() {
	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	outDir := filepath.Join(root, "internal", "assets", "data", "cnimbusagent")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	for _, arch := range arches {
		out := filepath.Join(outDir, "cnimbusagent-"+arch)
		// -trimpath: strips this machine's absolute GOPATH/GOROOT out of
		// the binary's debug info, so two different checkouts (or CI vs a
		// dev machine) produce byte-identical output from identical
		// source -- what the drift test in assets_sync_test.go relies on.
		// -ldflags "-s -w": drops the symbol table and DWARF debug info --
		// this binary is never debugged in place (it's a tiny guest
		// process with no crash-reporting story of its own), and every
		// image cnimbus builds is size-conscious by design.
		// -buildid=: -trimpath alone is NOT enough for byte-for-byte
		// reproducibility -- the linker still embeds a build ID that
		// varies between separate `go build` invocations even for
		// identical source/flags/Go version (confirmed empirically: two
		// fresh, independent containers building the exact same commit
		// produced different bytes until this flag was added). An empty
		// buildid is the standard fix for exactly this.
		// -buildvcs=false: still not sufficient by itself -- Go 1.18+
		// auto-embeds VCS info (commit hash, dirty flag) via
		// runtime/debug.ReadBuildInfo whenever the build runs inside a
		// git repo, and that embedded data varies between separate
		// `git clone` invocations of the identical commit (confirmed
		// empirically: the dirty-tree heuristic isn't stable across
		// fresh clones). This binary's own version string comes from
		// -ldflags -X elsewhere in this project already, so VCS
		// stamping adds nothing here besides non-determinism.
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid=", "-o", out, "./cmd/cnimbusagent")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fatal(fmt.Errorf("building cnimbusagent-%s: %w", arch, err))
		}
		fmt.Println("built", out)
	}
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
	fmt.Fprintln(os.Stderr, "genagent:", err)
	os.Exit(1)
}
