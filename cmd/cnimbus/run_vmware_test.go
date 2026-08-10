package main

import (
	"strings"
	"testing"
)

// T95: FORMAT raw has no MBR boot code at all (GPT+ESP only), so it
// cannot boot under "firmware = bios" -- buildVMX must actually honor
// the uefi flag rather than always emitting "bios".
func TestBuildVMXFirmwareLine(t *testing.T) {
	tests := []struct {
		uefi bool
		want string
	}{
		{false, `firmware = "bios"`},
		{true, `firmware = "efi"`},
	}
	for _, tt := range tests {
		vmx := buildVMX("test-vm", 512, tt.uefi, nil, "serial.log")
		if !strings.Contains(vmx, tt.want) {
			t.Errorf("buildVMX(uefi=%v) missing %q:\n%s", tt.uefi, tt.want, vmx)
		}
	}
}
