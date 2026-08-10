package assets

import (
	"bytes"
	"testing"
)

// F1: guards against an accidental future edit silently dropping one of
// these lines from vm-arm64.fragment. This only checks the fragment's
// own requested text -- it cannot prove the symbol actually survives
// olddefconfig (that's what verifyFragmentsApplied does at a real
// `prepare` run, and was confirmed by one against kernel 7.1.7: see
// Tasks.md F1). CONFIG_ARM64_BTI_KERNEL is deliberately absent from this
// list -- see vm-arm64.fragment's own comment for why (an unconditional
// upstream "depends on !CC_IS_GCC").
func TestKconfigArm64CarriesCPUMitigationSymbols(t *testing.T) {
	for _, want := range []string{
		"CONFIG_UNMAP_KERNEL_AT_EL0=y",
		"CONFIG_MITIGATE_SPECTRE_BRANCH_HISTORY=y",
		"CONFIG_ARM64_E0PD=y",
		"CONFIG_ARM64_PTR_AUTH=y",
		"CONFIG_ARM64_PTR_AUTH_KERNEL=y",
		"CONFIG_ARM64_BTI=y",
	} {
		if !bytes.Contains(KconfigVMArm64, []byte(want)) {
			t.Errorf("vm-arm64.fragment is missing %q", want)
		}
	}
	if bytes.Contains(KconfigVMArm64, []byte("CONFIG_ARM64_BTI_KERNEL")) {
		t.Error("vm-arm64.fragment requests CONFIG_ARM64_BTI_KERNEL, which upstream currently " +
			"disallows under GCC (depends on !CC_IS_GCC) -- this would be silently dropped by " +
			"olddefconfig; if upstream has since lifted that restriction, update this test alongside it")
	}
}
