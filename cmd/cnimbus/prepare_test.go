package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareAcceptsHardbootEthPastValidation covers F6.1/F6.2's guard
// lift: "eth" must clear --hardboot's own validation and reach Docker
// (the next real step) rather than being refused outright. On a host
// with no Docker available the call fails there instead -- the point is
// *which* error comes back: never the "not implemented" refusal. Skipped
// in `go test -short` (CI's default) since a host that does have Docker
// will run a real, slow `prepare` build here, the same convention
// internal/compileagent's live-network test uses; run explicitly with
// `go test -run TestPrepareAcceptsHardbootEthPastValidation ./cmd/cnimbus`.
func TestPrepareAcceptsHardbootEthPastValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode: may run a real, slow Docker build on hosts with Docker available")
	}
	err := runPrepare(context.Background(), []string{"--hardboot", "eth"})
	if err != nil && strings.Contains(err.Error(), "not implemented") {
		t.Errorf("--hardboot=eth: err = %v, should not be refused as not-implemented anymore", err)
	}
}

// F6.3/F6.4: "wifi" must clear the same validation gate "eth" is still
// stuck behind -- verified here by checking it does NOT fail with the
// "not implemented" message eth still does. It's expected to still fail
// (this test passes no KERNEL/BUSYBOX ARG overrides and runs with
// whatever Docker state this machine happens to have -- a real,
// Docker-touching prepare run belongs in build_e2e_test.go /
// Tasks.md's own manually-run verification, not this fast unit test),
// but whatever failure occurs must come from further down the pipeline
// (Docker availability, network, etc.), never from the --hardboot
// validation guard itself.
func TestPrepareAcceptsHardbootWifiValidation(t *testing.T) {
	err := runPrepare(context.Background(), []string{"--hardboot", "wifi", "--kernel", "bogus-version-that-does-not-resolve"})
	if err == nil {
		t.Fatal("expected an error (an unresolvable kernel version), got nil")
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err = %v -- HARDBOOT wifi must not be rejected as unimplemented anymore (F6.3/F6.4)", err)
	}
}

// TestPrepareAcceptsHardbootEthPlusWifiValidation mirrors
// TestPrepareAcceptsHardbootWifiValidation: "eth+wifi" must clear
// --hardboot's own validation and fail only further down the pipeline
// (an unresolvable kernel version here), never with the "not implemented"
// refusal. No testing.Short() skip needed for the same reason the "wifi"
// case above doesn't need one: the bogus kernel version fails resolution
// before any real, slow Docker build could start.
func TestPrepareAcceptsHardbootEthPlusWifiValidation(t *testing.T) {
	err := runPrepare(context.Background(), []string{"--hardboot", "eth+wifi", "--kernel", "bogus-version-that-does-not-resolve"})
	if err == nil {
		t.Fatal("expected an error (an unresolvable kernel version), got nil")
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err = %v -- HARDBOOT eth+wifi must not be rejected as unimplemented", err)
	}
}

func TestPrepareRejectsInvalidHardbootValue(t *testing.T) {
	err := runPrepare(context.Background(), []string{"--hardboot", "bogus"})
	if err == nil || !strings.Contains(err.Error(), "--hardboot must be") {
		t.Fatalf("err = %v, want --hardboot validation error", err)
	}
}

// A Nimbusfile's HARDBOOT directive must reach the same guard as the
// --hardboot flag -- both paths funnel into the same validation. Both
// "eth" (F6.1/F6.2) and "wifi" (F6.3/F6.4) are unblocked now, so an
// invalid value is what's left to exercise the pre-Docker validation
// guard without reaching a real build.
func TestPrepareRejectsInvalidHardbootValueFromNimbusfile(t *testing.T) {
	dir := t.TempDir()
	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	content := "HARDBOOT bogus\n"
	if err := os.WriteFile(nimbusfilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runPrepare(context.Background(), []string{"-f", nimbusfilePath})
	if err == nil || !strings.Contains(err.Error(), "HARDBOOT must be") {
		t.Fatalf("err = %v, want a HARDBOOT validation error", err)
	}
}

// The same Nimbusfile path for "wifi" must NOT hit that guard anymore --
// mirrors TestPrepareAcceptsHardbootWifiValidation above, just via the
// Nimbusfile directive instead of the --hardboot flag.
func TestPrepareAcceptsHardbootWifiFromNimbusfile(t *testing.T) {
	dir := t.TempDir()
	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	content := "HARDBOOT wifi\nWIFI MyNetwork\nWIFIPSK correcthorsebattery\nWIFICOUNTRY BR\n"
	if err := os.WriteFile(nimbusfilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runPrepare(context.Background(), []string{"-f", nimbusfilePath, "--kernel", "bogus-version-that-does-not-resolve"})
	if err == nil {
		t.Fatal("expected an error (an unresolvable kernel version), got nil")
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err = %v -- HARDBOOT wifi must not be rejected as unimplemented anymore (F6.3/F6.4)", err)
	}
}

// The combined "eth+wifi" profile via the Nimbusfile directive (rather
// than the --hardboot flag) must clear the same guard, and must require
// WIFI/WIFIPSK/WIFICOUNTRY exactly like "wifi" alone does -- a Nimbusfile
// declaring HARDBOOT eth+wifi with no WIFI directives is a Nimbusfile
// parse error caught by internal/nimbusfile before runPrepare ever reaches
// Docker, same as an invalid HARDBOOT value would be.
func TestPrepareAcceptsHardbootEthPlusWifiFromNimbusfile(t *testing.T) {
	dir := t.TempDir()
	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	content := "HARDBOOT eth+wifi\nWIFI MyNetwork\nWIFIPSK correcthorsebattery\nWIFICOUNTRY BR\n"
	if err := os.WriteFile(nimbusfilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runPrepare(context.Background(), []string{"-f", nimbusfilePath, "--kernel", "bogus-version-that-does-not-resolve"})
	if err == nil {
		t.Fatal("expected an error (an unresolvable kernel version), got nil")
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err = %v -- HARDBOOT eth+wifi must not be rejected as unimplemented", err)
	}
}

// A Nimbusfile declaring HARDBOOT eth+wifi with no WIFI directive must
// fail the same way HARDBOOT wifi alone does -- before runPrepare ever
// touches Docker.
func TestPrepareRejectsHardbootEthPlusWifiWithoutWifiDirectives(t *testing.T) {
	dir := t.TempDir()
	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	content := "HARDBOOT eth+wifi\n"
	if err := os.WriteFile(nimbusfilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runPrepare(context.Background(), []string{"-f", nimbusfilePath})
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("err = %v, want a missing-WIFI-directives error", err)
	}
}
