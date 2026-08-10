package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// vmwareBackend implements backend by generating a throwaway .vmx (and,
// for a FORMAT raw image, a flat-VMDK wrapper around it -- see
// writeFlatVMDK) and starting it headless via vmrun, VMware Workstation/
// Player's own official automation CLI. This is host-side automation
// only, distinct from the Nimbusfile's AGENT vmware kind (see
// cmd/cnimbusagent), which is a guest-side protocol implementation; the
// two don't depend on each other.
type vmwareBackend struct{}

func (vmwareBackend) name() string { return "vmware" }

func (vmwareBackend) available() error {
	_, _, err := findVMrun()
	return err
}

// launch generates a throwaway .vmx/.vmdk under a temp work directory
// and starts it via vmrun.
//
// amd64 only: VMware Workstation on Windows doesn't support arm64
// guests at all (unlike QEMU/VirtualBox). --hostfwd isn't automated
// here -- VMware's NAT port forwarding lives in a shared, global
// vmnetnat.conf, not per-VM, and editing shared host network config
// automatically is out of scope for a throwaway dev VM.
func (vmwareBackend) launch(spec launchSpec) error {
	imagePath, isISO, arch, uefi := spec.imagePath, spec.isISO, spec.arch, spec.uefi
	memMB, vmName, keep := spec.memMB, spec.vmName, spec.keep

	if arch != "amd64" {
		return fmt.Errorf("--backend vmware only supports --arch amd64 (VMware Workstation on Windows doesn't support arm64 guests)")
	}
	// FORMAT raw (internal/rawimage) has no MBR boot code at all -- GPT +
	// ESP only, UEFI-only by design -- so a raw image is forced to UEFI
	// regardless of --uefi, the same reasoning T42 already applied to
	// Hyper-V Generation 2 (T95).
	uefi = effectiveUEFI(isISO, uefi)
	vmrun, hostType, err := findVMrun()
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "cnimbus-vmware-*")
	if err != nil {
		return fmt.Errorf("creating VMware work directory: %w", err)
	}
	// This backend's own throwaway setup artifact is workDir, only ever
	// cleaned up on a pre-start failure (never once vmrun start has
	// actually succeeded -- see the end of launch, which always leaves
	// workDir in place since the running VM's .vmx still references the
	// files inside it) -- so started is always false here, unlike vbox's
	// cleanupUnlessStartedOrKept call (run.go), which can genuinely reach
	// a running state before its own equivalent guard fires.
	cleanupWorkDir := func() {
		cleanupUnlessStartedOrKept(false, keep, fmt.Sprintf("leaving VM files in place (--vm-keep): %s", workDir), func() {
			if err := os.RemoveAll(workDir); err != nil {
				fmt.Printf("cleanup: could not remove %s: %v\n", workDir, err)
			}
		})
	}

	var diskLines []string
	if isISO {
		diskLines = []string{
			`ide1:0.present = "TRUE"`,
			`ide1:0.deviceType = "cdrom-image"`,
			`ide1:0.fileName = "` + imagePath + `"`,
			`ide1:0.startConnected = "TRUE"`,
		}
	} else {
		vmdkPath := filepath.Join(workDir, "disk.vmdk")
		if err := writeFlatVMDK(vmdkPath, imagePath); err != nil {
			cleanupWorkDir()
			return err
		}
		diskLines = []string{
			`ide0:0.present = "TRUE"`,
			`ide0:0.deviceType = "disk"`,
			`ide0:0.fileName = "disk.vmdk"`,
		}
	}

	serialLogPath := filepath.Join(workDir, "serial.log")
	vmx := buildVMX(vmName, memMB, uefi, diskLines, serialLogPath)
	vmxPath := filepath.Join(workDir, vmName+".vmx")
	if err := os.WriteFile(vmxPath, []byte(vmx), 0o644); err != nil { // #nosec G306 -- a .vmx is plain VM config, not a secret
		cleanupWorkDir()
		return fmt.Errorf("writing %s: %w", vmxPath, err)
	}

	fmt.Printf("starting VMware VM %q (headless)...\n", vmName)
	// #nosec G204 -- vmrun resolved via findVMrun (PATH or a fixed known
	// install location); vmxPath is this function's own generated file.
	cmd := exec.Command(vmrun, "-T", hostType, "start", vmxPath, "nogui")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanupWorkDir()
		return fmt.Errorf("vmrun start: %s: %w", strings.TrimSpace(string(out)), err)
	}

	fmt.Printf("VM running. Serial console output: %s\n", serialLogPath)
	fmt.Printf("Stop it with:\n  %s -T %s stop %q\n", vmrun, hostType, vmxPath)
	if keep {
		fmt.Printf("VM files kept at %s (--vm-keep)\n", workDir)
	} else {
		fmt.Printf("Remove its files afterward with:\n  rmdir /s /q %q\n", workDir)
	}
	fmt.Println(vmReturnsImmediatelyMsg)
	return nil
}

// buildVMX writes the minimal .vmx VMware Workstation/Player needs to
// boot a BIOS-mode Linux VM headless: a plain PC-compatible virtual
// machine, one NIC (NAT), and whichever disk/cdrom line the caller
// supplies. Every field here is documented in VMware's own public
// "Virtual Machine Configuration" reference; nothing here depends on an
// undocumented format.
func buildVMX(vmName string, memMB int, uefi bool, diskLines []string, serialLogPath string) string {
	var b strings.Builder
	b.WriteString(".encoding = \"UTF-8\"\n")
	b.WriteString("config.version = \"8\"\n")
	b.WriteString("virtualHW.version = \"19\"\n")
	fmt.Fprintf(&b, "displayName = %q\n", vmName)
	b.WriteString("guestOS = \"other5xlinux-64\"\n")
	b.WriteString("numvcpus = \"2\"\n")
	fmt.Fprintf(&b, "memsize = \"%d\"\n", memMB)
	// "efi"/"bios" are VMware's own documented values for this key (T95)
	// -- a FORMAT raw image (GPT+ESP, no MBR boot code at all) cannot
	// boot under "bios" at all, and --uefi lets a user request it for an
	// ISO too.
	if uefi {
		b.WriteString("firmware = \"efi\"\n")
	} else {
		b.WriteString("firmware = \"bios\"\n")
	}
	for _, l := range diskLines {
		b.WriteString(l + "\n")
	}
	b.WriteString("ethernet0.present = \"TRUE\"\n")
	b.WriteString("ethernet0.connectionType = \"nat\"\n")
	b.WriteString("ethernet0.virtualDev = \"e1000\"\n")
	b.WriteString("ethernet0.startConnected = \"TRUE\"\n")
	// Redirects the guest's ttyS0 (the same serial console QEMU's
	// -serial stdio and VirtualBox's own default already surface) to a
	// plain host file -- otherwise a headless (nogui) vmrun VM gives no
	// way at all to see boot output or confirm it came up.
	b.WriteString("serial0.present = \"TRUE\"\n")
	b.WriteString("serial0.fileType = \"file\"\n")
	fmt.Fprintf(&b, "serial0.fileName = %q\n", serialLogPath)
	b.WriteString("serial0.startConnected = \"TRUE\"\n")
	return b.String()
}

// findVMrun locates vmrun: on PATH first (assumed Workstation's "ws"
// host type, vmrun's own default), then Windows' default VMware
// Workstation/Player install locations (vmrun isn't added to PATH by
// the Windows installer either, same situation as VBoxManage). Returns
// the vmrun path and the "-T" host type vmrun itself requires ("ws" for
// Workstation, "player" for the free VMware Player -- they're separate
// products with separate install directories, and vmrun rejects the
// wrong one).
// findVMrun locates vmrun and, unlike the other three findTool (T99)
// callers, also needs to know *which* product matched (VMware Workstation
// vs Player use different -T host types) -- so it calls
// probeWindowsInstallDirs directly instead of the plain findTool wrapper,
// and maps the matched relative path back to a host type.
func findVMrun() (path, hostType string, err error) {
	if p, err := exec.LookPath("vmrun"); err == nil {
		return p, "ws", nil
	}
	if runtime.GOOS != "windows" {
		return "", "", fmt.Errorf("vmrun not found on PATH (or the default Windows install location) -- install VMware Workstation/Player, or use a different --backend")
	}
	relPaths := map[string]string{
		filepath.Join("VMware", "VMware Workstation", "vmrun.exe"): "ws",
		filepath.Join("VMware", "VMware Player", "vmrun.exe"):      "player",
	}
	var rels []string
	for rel := range relPaths {
		rels = append(rels, rel)
	}
	if p, matchedRel, ok := probeWindowsInstallDirs(rels...); ok {
		return p, relPaths[matchedRel], nil
	}
	return "", "", fmt.Errorf("vmrun not found on PATH (or the default Windows install location) -- install VMware Workstation/Player, or use a different --backend")
}
