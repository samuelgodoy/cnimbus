package compileagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeKconfigFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestVerifyFragmentsAppliedPasses(t *testing.T) {
	dir := t.TempDir()
	writeKconfigFile(t, dir, ".config", "CONFIG_FOO=y\nCONFIG_BAR=m\n")
	frag := writeKconfigFile(t, dir, "frag.fragment", "CONFIG_FOO=y\n")

	if err := verifyFragmentsApplied(dir, []string{frag}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestVerifyFragmentsAppliedCatchesMissingSymbol(t *testing.T) {
	dir := t.TempDir()
	writeKconfigFile(t, dir, ".config", "CONFIG_BAR=m\n")
	frag := writeKconfigFile(t, dir, "frag.fragment", "CONFIG_FOO=y\n")

	err := verifyFragmentsApplied(dir, []string{frag})
	if err == nil {
		t.Fatal("expected error for a symbol dropped entirely, got nil")
	}
	if !strings.Contains(err.Error(), "CONFIG_FOO") || !strings.Contains(err.Error(), "dropped by Kconfig") {
		t.Fatalf("error should name the dropped symbol: %v", err)
	}
}

func TestVerifyFragmentsAppliedCatchesModuleDowngrade(t *testing.T) {
	dir := t.TempDir()
	// Requested =y but Kconfig resolved it to =m -- this project's images
	// have no modprobe/depmod, so a module is as good as absent (T65).
	writeKconfigFile(t, dir, ".config", "CONFIG_FOO=m\n")
	frag := writeKconfigFile(t, dir, "frag.fragment", "CONFIG_FOO=y\n")

	err := verifyFragmentsApplied(dir, []string{frag})
	if err == nil {
		t.Fatal("expected error for a =y request that resolved to =m, got nil")
	}
	if !strings.Contains(err.Error(), "CONFIG_FOO requested =y but resolved to =m") {
		t.Fatalf("error should name the requested-vs-resolved mismatch: %v", err)
	}
}

func TestVerifyFragmentsAppliedIgnoresNonBooleanValueDrift(t *testing.T) {
	dir := t.TempDir()
	// Non-boolean (string/int) symbols stay presence-only by design --
	// the resolved value legitimately differs from the fragment's literal
	// text (e.g. CONFIG_CMDLINE gets merged/quoted differently).
	writeKconfigFile(t, dir, ".config", `CONFIG_CMDLINE="console=ttyS0 different"`+"\n")
	frag := writeKconfigFile(t, dir, "frag.fragment", `CONFIG_CMDLINE="console=ttyS0"`+"\n")

	if err := verifyFragmentsApplied(dir, []string{frag}); err != nil {
		t.Fatalf("expected no error for non-boolean value drift, got %v", err)
	}
}

func TestCheckMergeConfigConflictsPassesOnCleanMerge(t *testing.T) {
	// Representative of merge_config.sh's real stdout when fragments don't
	// collide -- no "is redefined by fragment" line anywhere.
	output := "Using .config as base\n" +
		"Merging minimal.fragment\n" +
		"Merging vm-amd64.fragment\n" +
		"#\n# merged configuration written to .config (needs make)\n#\n"
	if err := checkMergeConfigConflicts(output); err != nil {
		t.Fatalf("expected no error for a clean merge, got %v", err)
	}
}

func TestCheckMergeConfigConflictsIgnoresOrdinaryFirstApplication(t *testing.T) {
	// Real, observed merge_config.sh behavior (found by an actual amd64
	// build, not by reading the code): it prints "is redefined by
	// fragment" for the *first* fragment to ever touch a symbol too --
	// the ordinary transition away from tinyconfig's "# CONFIG_X is not
	// set" baseline -- not only for a genuine second, conflicting
	// fragment. A single fragment touching a symbol once must never be
	// treated as a conflict, or this check would fail on every real
	// build (exactly what happened before this test/fix existed).
	output := "Using .config as base\n" +
		"Merging minimal.fragment\n" +
		"Value of CONFIG_MODULES is redefined by fragment /opt/cnimbus/kconfig/minimal.fragment:\n" +
		"Previous value: # CONFIG_MODULES is not set\n" +
		"New value: CONFIG_MODULES=n\n" +
		"#\n# merged configuration written to .config (needs make)\n#\n"

	if err := checkMergeConfigConflicts(output); err != nil {
		t.Fatalf("expected no error for a symbol touched by only one fragment, got %v", err)
	}
}

func TestCheckMergeConfigConflictsCatchesRedefinition(t *testing.T) {
	// A genuine conflict: the *same* symbol redefined by *two distinct*
	// fragments -- merge_config.sh's actual message shape is "Value of
	// CONFIG_X is redefined by fragment <path>:" followed by "Previous
	// value: ..." / "New value: ...." lines.
	output := "Using .config as base\n" +
		"Merging minimal.fragment\n" +
		"Value of CONFIG_MODULES is redefined by fragment /opt/cnimbus/kconfig/minimal.fragment:\n" +
		"Previous value: # CONFIG_MODULES is not set\n" +
		"New value: CONFIG_MODULES=n\n" +
		"Merging vm-amd64.fragment\n" +
		"Value of CONFIG_MODULES is redefined by fragment /opt/cnimbus/kconfig/vm-amd64.fragment:\n" +
		"Previous value: CONFIG_MODULES=n\n" +
		"New value: CONFIG_MODULES=y\n"

	err := checkMergeConfigConflicts(output)
	if err == nil {
		t.Fatal("expected an error for a symbol set by two distinct fragments, got nil")
	}
	if !strings.Contains(err.Error(), "CONFIG_MODULES") {
		t.Fatalf("error should name the conflicting symbol: %v", err)
	}
	if !strings.Contains(err.Error(), "minimal.fragment") || !strings.Contains(err.Error(), "vm-amd64.fragment") {
		t.Fatalf("error should name both conflicting fragments: %v", err)
	}
}

func TestVerifyFragmentsAppliedIgnoresExplicitlyDisabledSymbols(t *testing.T) {
	dir := t.TempDir()
	writeKconfigFile(t, dir, ".config", "CONFIG_BAR=y\n")
	frag := writeKconfigFile(t, dir, "frag.fragment", "CONFIG_FOO=n\n")

	if err := verifyFragmentsApplied(dir, []string{frag}); err != nil {
		t.Fatalf("expected no error for an explicitly-disabled symbol, got %v", err)
	}
}
