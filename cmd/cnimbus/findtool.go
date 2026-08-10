package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// windowsInstallRoots returns the base directories the official Windows
// installers for QEMU/VirtualBox/VMware fall back to (T99): the
// ProgramFiles/ProgramFiles(x86) environment variables, plus their
// hardcoded fallbacks for the rare case those env vars are unset. A
// single shared list means a new install location only needs adding
// once instead of once per tool -- the exact drift T99 found (findVMrun
// was the only one of four call sites that knew about 32-bit Program
// Files, purely because it was written last, not because the others
// don't need it).
func windowsInstallRoots() []string {
	seen := map[string]bool{}
	var roots []string
	for _, base := range []string{
		os.Getenv("ProgramFiles"), `C:\Program Files`,
		os.Getenv("ProgramFiles(x86)"), `C:\Program Files (x86)`,
	} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		roots = append(roots, base)
	}
	return roots
}

// probeWindowsInstallDirs joins every windowsInstallRoots() entry against
// every relPath and returns the first one that exists on disk, along with
// which relPath matched (a caller like findVMrun needs to know which of
// several candidate products was found, not just the resulting path).
// Callers are expected to guard this on runtime.GOOS == "windows"
// themselves where that matters for their own return semantics.
func probeWindowsInstallDirs(relPaths ...string) (path, matchedRel string, ok bool) {
	for _, root := range windowsInstallRoots() {
		for _, rel := range relPaths {
			p := filepath.Join(root, rel)
			if _, err := os.Stat(p); err == nil {
				return p, rel, true
			}
		}
	}
	return "", "", false
}

// findTool locates a binary on PATH first, then (on Windows only) under
// the Windows install directories via probeWindowsInstallDirs. This is
// the shared "look on PATH, then probe the Windows install directory"
// pattern T99 found independently re-implemented four times
// (findQEMU, resolveWindowsBundledOVMF, findVBoxManage, findVMrun) with
// drifted OS guards -- findQEMU in particular used to skip the
// runtime.GOOS check entirely, so on Linux/macOS it harmlessly stat'd
// the literal Windows path C:\Program Files\qemu\<bin>.exe on every
// call. Guarding here once fixes that for every caller at once. Callers
// still craft their own "not found" error text (install instructions
// differ per tool), so a plain not-found error is returned here and
// meant to be replaced, not surfaced directly.
func findTool(pathLookup func(string) (string, error), binName string, windowsRelPaths ...string) (string, error) {
	if p, err := pathLookup(binName); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" && len(windowsRelPaths) > 0 {
		if p, _, ok := probeWindowsInstallDirs(windowsRelPaths...); ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH (or the default Windows install location)", binName)
}
