package assets

import (
	"regexp"
	"testing"
)

// T62: vm-amd64.fragment and vm-arm64.fragment each carry their own,
// separately-maintained CONFIG_CMDLINE literal -- there is nothing
// structural stopping the two from drifting apart on shared policy
// (panic behavior) the way arm64's cmdline already had, missing
// panic=/oops= entirely until this ticket. This test is the guard the
// ticket asked for: it doesn't care about arch-specific tokens
// (console=ttyS0 vs console=ttyAMA0, initrd path, etc.), only that both
// cmdlines agree on the *policy* parameters that exist on both
// architectures.
//
// ipv6.disable= used to be checked here too (both arches carried it,
// per T12's "disable IPv6 at boot" choice); AD-047 reverses that
// choice -- neither fragment sets it anymore, so it's no longer a
// shared policy parameter to assert agreement on.
func TestKconfigCmdlinePolicyParamsMatchAcrossArches(t *testing.T) {
	amd64Cmdline := extractCmdline(t, KconfigVMAmd64)
	arm64Cmdline := extractCmdline(t, KconfigVMArm64)

	for _, param := range []string{"panic=", "oops="} {
		re := regexp.MustCompile(regexp.QuoteMeta(param) + `\S*`)
		amd64Val := re.FindString(amd64Cmdline)
		arm64Val := re.FindString(arm64Cmdline)
		if amd64Val == "" {
			t.Errorf("vm-amd64.fragment's CONFIG_CMDLINE has no %s parameter: %q", param, amd64Cmdline)
		}
		if arm64Val == "" {
			t.Errorf("vm-arm64.fragment's CONFIG_CMDLINE has no %s parameter: %q", param, arm64Cmdline)
		}
		if amd64Val != "" && arm64Val != "" && amd64Val != arm64Val {
			t.Errorf("policy parameter %s differs between arches: amd64=%q arm64=%q", param, amd64Val, arm64Val)
		}
	}
}

func extractCmdline(t *testing.T, fragment []byte) string {
	t.Helper()
	re := regexp.MustCompile(`CONFIG_CMDLINE="([^"]*)"`)
	m := re.FindSubmatch(fragment)
	if m == nil {
		t.Fatalf("no CONFIG_CMDLINE=\"...\" line found in fragment:\n%s", fragment)
	}
	return string(m[1])
}
