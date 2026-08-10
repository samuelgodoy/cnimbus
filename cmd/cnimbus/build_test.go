package main

import (
	"strings"
	"testing"

	"cnimbus/internal/nimbusfile"
)

// T57: cmd/cnimbus had essentially no unit tests, so a change like T7/
// T10 (which both edited isolinuxCfg blind) had nothing to catch a
// regression. This is the specific gap the ticket names: isolinuxCfg's
// PROMPT 0/TIMEOUT 1/NOESCAPE 1 (T7) and the APPEND line's
// panic=10 oops=panic (T10) are now asserted directly.
func TestIsolinuxCfg(t *testing.T) {
	cfg := isolinuxCfg("myvm")
	for _, want := range []string{
		"PROMPT 0",
		"TIMEOUT 1",
		"NOESCAPE 1",
		"ALLOWOPTIONS 0",
		"MENU LABEL myvm",
		// T78: KERNEL/initrd= point at /EFI/BOOT/ now, not a separate
		// /BOOT/ copy -- isolinux loads a bzImage regardless of its
		// filename, and the EFI-stub kernel is that same bzImage.
		"KERNEL /EFI/BOOT/BOOTX64.EFI",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("isolinuxCfg missing %q: %q", want, cfg)
		}
	}
	appendLine := "APPEND initrd=/EFI/BOOT/INITRD.IMG console=tty0 console=ttyS0,115200n8 panic=10 oops=panic"
	if !strings.Contains(cfg, appendLine) {
		t.Errorf("isolinuxCfg missing APPEND line %q: %q", appendLine, cfg)
	}
}

// AD-050: CNIMBUS.CFG's content -- a boot-media scan that finds more
// than one candidate .iso on a multiboot USB stick needs something to
// identify each one by; HOSTNAME is the field that matters most, since
// it's what a Nimbusfile author actually names their image.
func TestCnimbusMetadataCfg(t *testing.T) {
	hf := &nimbusfile.Nimbusfile{Hostname: "myvm", Arch: "amd64", Format: "iso"}
	cfg := cnimbusMetadataCfg(hf)
	for _, want := range []string{"HOSTNAME=myvm", "ARCH=amd64", "FORMAT=iso"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cnimbusMetadataCfg missing %q: %q", want, cfg)
		}
	}
}
