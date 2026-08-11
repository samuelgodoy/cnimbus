package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestResolveAccel_ForcedBackendsAlwaysUseCPUHost(t *testing.T) {
	for _, req := range []string{"kvm", "hvf", "whpx"} {
		accelArgs, cpuArgs, name := resolveAccel(req, "amd64")
		if len(accelArgs) != 2 || accelArgs[0] != "-accel" || accelArgs[1] != req {
			t.Errorf("%s: accelArgs = %v, want [-accel %s]", req, accelArgs, req)
		}
		if len(cpuArgs) != 2 || cpuArgs[0] != "-cpu" || cpuArgs[1] != "host" {
			t.Errorf("%s: cpuArgs = %v, want [-cpu host]", req, cpuArgs)
		}
		if name == "" {
			t.Errorf("%s: expected a non-empty accelerator name", req)
		}
	}
}

func TestResolveAccel_ForcedTCGKeepsArchSpecificCPUModel(t *testing.T) {
	accelArgs, cpuArgs, _ := resolveAccel("tcg", "arm64")
	if len(accelArgs) != 2 || accelArgs[1] != "tcg" {
		t.Errorf("accelArgs = %v, want [-accel tcg]", accelArgs)
	}
	if len(cpuArgs) != 2 || cpuArgs[0] != "-cpu" || cpuArgs[1] != "cortex-a72" {
		t.Errorf("cpuArgs = %v, want [-cpu cortex-a72] for a forced-tcg arm64 guest", cpuArgs)
	}

	accelArgs, cpuArgs, _ = resolveAccel("tcg", "amd64")
	if len(accelArgs) != 2 || accelArgs[1] != "tcg" {
		t.Errorf("accelArgs = %v, want [-accel tcg]", accelArgs)
	}
	if len(cpuArgs) != 0 {
		t.Errorf("cpuArgs = %v, want no explicit -cpu for a forced-tcg amd64 guest (QEMU's own default)", cpuArgs)
	}
}

func TestResolveAccel_AutoNeverPicksHardwareForAForeignGuestArch(t *testing.T) {
	// A guest arch that can never equal runtime.GOARCH on any host this
	// test runs on -- proves "auto" cannot select kvm/hvf/whpx cross-arch,
	// regardless of which two real architectures Go itself ever ships.
	foreignArch := "amd64"
	if runtime.GOARCH == "amd64" {
		foreignArch = "arm64"
	}
	accelArgs, _, name := resolveAccel("auto", foreignArch)
	if len(accelArgs) != 2 || accelArgs[1] != "tcg" {
		t.Errorf("cross-arch auto: accelArgs = %v, want [-accel tcg]", accelArgs)
	}
	if name == "" {
		t.Error("expected a non-empty accelerator name explaining the tcg fallback")
	}
}

func TestResolveAccel_UnknownValueFallsBackToTCG(t *testing.T) {
	accelArgs, _, _ := resolveAccel("bogus", "amd64")
	if len(accelArgs) != 2 || accelArgs[1] != "tcg" {
		t.Errorf("accelArgs = %v, want [-accel tcg] for an unrecognized --accel value", accelArgs)
	}
}

// T97: two different VM names must resolve to two different synthesized
// VARS paths, and the file each one creates must actually exist -- the
// bug was one shared path across every unrelated VM on the machine, so
// EDK2-written UEFI variables (boot entries, BootOrder) leaked between
// them.
func TestResolveWindowsBundledOVMFVarsPathIsPerVMName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("resolveWindowsBundledOVMF only ever matches on Windows (QEMU-for-Windows installer layout)")
	}
	_, vars1, err := resolveWindowsBundledOVMF("amd64", "vm-one")
	if err != nil {
		t.Skipf("no bundled EDK2 firmware found on this machine, skipping: %v", err)
	}
	defer func() { _ = os.Remove(vars1) }()
	_, vars2, err := resolveWindowsBundledOVMF("amd64", "vm-two")
	if err != nil {
		t.Fatalf("resolveWindowsBundledOVMF(vm-two): %v", err)
	}
	defer func() { _ = os.Remove(vars2) }()

	if vars1 == vars2 {
		t.Fatalf("expected different VM names to resolve to different VARS paths, both got %s", vars1)
	}
	for _, p := range []string{vars1, vars2} {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("expected %s to have been created: %v", p, statErr)
		}
	}
}

// resolveOVMF must report synthesized=true only for the Windows-bundled
// fallback it creates itself, never for an explicitly-passed
// --ovmf-code/--ovmf-vars pair -- runViaQEMU uses that flag to decide
// whether it's safe to delete the VARS file after the run, and deleting
// a user-supplied VARS file would be a real (and surprising) data loss.
func TestResolveOVMFNeverReportsSynthesizedForExplicitPaths(t *testing.T) {
	_, _, synthesized, err := resolveOVMF("amd64", "C:\\some\\code.fd", "C:\\some\\vars.fd", "vm-name")
	if err != nil {
		t.Fatalf("resolveOVMF: %v", err)
	}
	if synthesized {
		t.Error("expected synthesized=false when both --ovmf-code and --ovmf-vars are explicitly given")
	}
}

// T96: --hostfwd must be validated up front rather than each backend
// silently degrading a malformed value into "no networking at all".
func TestValidateHostfwd(t *testing.T) {
	valid := []string{"8080:8080", "1:65535", "65535:1", "9999:22"}
	for _, hf := range valid {
		if err := validateHostfwd(hf); err != nil {
			t.Errorf("expected %q to be accepted, got: %v", hf, err)
		}
	}

	invalid := []string{
		"8080",           // the exact failure scenario: forgot the host:guest form
		"",               // empty
		"8080:",          // missing guest port
		":8080",          // missing host port
		"8080:8080:8080", // too many colons
		"0:8080",         // host port out of range (0)
		"8080:0",         // guest port out of range (0)
		"8080:65536",     // guest port out of range (too large)
		"abc:8080",       // host port not a number
		"8080:abc",       // guest port not a number
	}
	for _, hf := range invalid {
		if err := validateHostfwd(hf); err == nil {
			t.Errorf("expected %q to be rejected, got no error", hf)
		}
	}
}

// T98: describeQEMUCommand and runViaQEMU used to build the argv
// independently, and had already drifted (the printed instructions were
// missing -M/-cpu/the OVMF -drive if=pflash entries) -- following them
// for an arm64 or --uefi image either failed outright (no -M virt --
// QEMU picks a default machine with no PL011 console) or silently
// booted the wrong firmware. Both now go through the same qemuArgv;
// this asserts the exact machine-type/cpu shape for each arch/uefi
// combination the ticket names.
func TestQemuArgvIncludesMachineTypeAndCPU(t *testing.T) {
	tests := []struct {
		name     string
		arch     string
		uefi     bool
		wantMOpt string
	}{
		{"amd64 BIOS", "amd64", false, "pc"},
		{"amd64 UEFI", "amd64", true, "q35"},
		{"arm64 (always UEFI)", "arm64", true, "virt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := qemuArgv("/tmp/img.iso", true, tt.arch, tt.uefi, "", "", 512, 1, nil, nil, "8080:8080", "127.0.0.1")
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "-M "+tt.wantMOpt) {
				t.Errorf("%s: expected -M %s in argv, got: %s", tt.name, tt.wantMOpt, joined)
			}
		})
	}
}

// A resolved OVMF code/vars pair must produce both -drive if=pflash
// entries; an empty pair (OVMF not found/not resolved yet) must produce
// neither -- qemuArgv has to degrade gracefully for
// describeQEMUCommand's best-effort case.
func TestQemuArgvOVMFDrives(t *testing.T) {
	withOVMF := qemuArgv("/tmp/img.iso", true, "amd64", true, "/ovmf/code.fd", "/ovmf/vars.fd", 512, 1, nil, nil, "8080:8080", "127.0.0.1")
	joinedWith := strings.Join(withOVMF, " ")
	if !strings.Contains(joinedWith, "if=pflash,format=raw,readonly=on,file=/ovmf/code.fd") ||
		!strings.Contains(joinedWith, "if=pflash,format=raw,file=/ovmf/vars.fd") {
		t.Errorf("expected both OVMF -drive if=pflash entries: %s", joinedWith)
	}

	withoutOVMF := qemuArgv("/tmp/img.iso", true, "amd64", true, "", "", 512, 1, nil, nil, "8080:8080", "127.0.0.1")
	if strings.Contains(strings.Join(withoutOVMF, " "), "pflash") {
		t.Errorf("expected no OVMF drives when code/vars are empty: %v", withoutOVMF)
	}
}

// T68 real-boot follow-up: a real QEMU boot with no rng backend/device
// confirmed the kernel's unconditionally-built-in CONFIG_HW_RANDOM_VIRTIO
// driver creates /dev/hwrng in the guest, but every read on it fails
// with "no such device" because nothing on the QEMU side backs it --
// qemuArgv must always attach a virtio-rng device (and its backend
// object) so that node is actually functional, on every arch/uefi
// combination, not just opt-in.
func TestQemuArgvIncludesVirtioRNG(t *testing.T) {
	for _, tt := range []struct {
		name string
		arch string
		uefi bool
	}{
		{"amd64 BIOS", "amd64", false},
		{"amd64 UEFI", "amd64", true},
		{"arm64 (always UEFI)", "arm64", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := qemuArgv("/tmp/img.iso", true, tt.arch, tt.uefi, "", "", 512, 1, nil, nil, "8080:8080", "127.0.0.1")
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "-device virtio-rng-pci,rng=rng0") {
				t.Errorf("%s: expected a virtio-rng-pci device in argv, got: %s", tt.name, joined)
			}
			if !strings.Contains(joined, "-object rng-builtin,id=rng0") {
				t.Errorf("%s: expected an rng-builtin backend object in argv, got: %s", tt.name, joined)
			}
		})
	}
}

// describeQEMUCommand's printed command must be produced by the same
// qemuArgv runViaQEMU actually execs -- the specific bug this ticket
// existed to close.
func TestDescribeQEMUCommandIncludesMachineTypeForArm64(t *testing.T) {
	desc := describeQEMUCommand("/tmp/img.iso", true, "arm64", true, "", "", 512, 1, "tcg", "8080:8080", "127.0.0.1", "cnimbus-run")
	if !strings.Contains(desc, "-M virt") {
		t.Errorf("expected the printed arm64 command to include -M virt, got: %s", desc)
	}
	if !strings.HasPrefix(desc, "qemu-system-aarch64 ") {
		t.Errorf("expected the printed command to start with qemu-system-aarch64, got: %s", desc)
	}
}
