package main

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestFindToolUsesPathLookupFirst(t *testing.T) {
	p, err := findTool(func(name string) (string, error) {
		return filepath.Join("/usr/bin", name), nil
	}, "qemu-system-x86_64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != filepath.Join("/usr/bin", "qemu-system-x86_64") {
		t.Errorf("expected PATH lookup result, got %q", p)
	}
}

func TestFindToolErrorsWhenNotOnPathAndNoWindowsRoots(t *testing.T) {
	_, err := findTool(func(name string) (string, error) {
		return "", fmt.Errorf("not found")
	}, "does-not-exist-anywhere")
	if err == nil {
		t.Fatal("expected an error when the tool is on neither PATH nor a Windows install dir")
	}
}

func TestProbeWindowsInstallDirsNoMatchReturnsFalse(t *testing.T) {
	_, _, ok := probeWindowsInstallDirs(filepath.Join("definitely", "not", "a", "real", "path.exe"))
	if ok {
		t.Fatal("expected no match for a nonexistent relative path")
	}
}

func TestWindowsInstallRootsDeduplicates(t *testing.T) {
	roots := windowsInstallRoots()
	seen := map[string]bool{}
	for _, r := range roots {
		if seen[r] {
			t.Fatalf("windowsInstallRoots returned duplicate root %q: %v", r, roots)
		}
		seen[r] = true
	}
}
