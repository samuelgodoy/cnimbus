package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// runRun boots a cnimbus-built image locally, so trying a change out
// doesn't require hand-assembling the same hypervisor invocation the
// README already documents every time. Four backends:
//   - qemu (default): the same one this project's own boot tests use
//     (see README's "Validated" section) -- used directly if
//     qemu-system-<arch> is on PATH.
//   - vbox: scripts VBoxManage to create a throwaway VM, attach the
//     image, and start it -- the same manual steps the README's own
//     "Reaching a service running in the guest" section walks through
//     by hand.
//   - vmware: scripts vmrun (VMware Workstation/Player's own official
//     automation CLI) against a generated .vmx -- amd64 only; VMware
//     Workstation on Windows doesn't support arm64 guests.
//   - hyperv: scripts the Hyper-V PowerShell module -- amd64 only, ISO
//     media only for now (see runViaHyperV's own doc comment for why a
//     FORMAT raw image isn't supported here yet).
//
// None of these are invented: each backend is exactly the commands that
// hypervisor's own documentation gives for launching a VM headless from
// a script.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	arch := fs.String("arch", "amd64", "image architecture: amd64 or arm64")
	uefi := fs.Bool("uefi", false, "boot via UEFI/OVMF instead of BIOS (amd64 only -- arm64 images are always UEFI)")
	ovmfCode := fs.String("ovmf-code", "", "path to OVMF_CODE.fd (auto-detected from common install paths if omitted)")
	ovmfVars := fs.String("ovmf-vars", "", "path to OVMF_VARS.fd (auto-detected alongside --ovmf-code if omitted)")
	memMB := fs.Int("mem", 512, "guest RAM in MB")
	smp := fs.Int("smp", 1, "guest vCPU count (--backend qemu only)")
	accel := fs.String("accel", "auto", `QEMU acceleration backend (--backend qemu only): "auto" (hardware acceleration when the host supports it -- KVM on Linux, HVF on macOS, WHPX on Windows -- falling back to software emulation otherwise), "kvm", "hvf", "whpx", or "tcg" to force software emulation`)
	hostfwd := fs.String("hostfwd", "8080:8080", `host:guest TCP port to forward (QEMU user-mode networking / VirtualBox NAT; for --backend hyperv, via a cnimbus-owned NAT switch -- the guest's Nimbusfile must set "IP 192.168.200.10 255.255.255.0 192.168.200.1"; not automated for --backend vmware)`)
	hostfwdBind := fs.String("hostfwd-bind", "127.0.0.1", `host address --hostfwd binds to (all three of qemu/vbox/hyperv); loopback by default -- pass "0.0.0.0" (or a specific address) to deliberately expose the forwarded port beyond this machine`)
	backend := fs.String("backend", "qemu", `hypervisor to boot with: "qemu", "vbox", "vmware", or "hyperv"`)
	vmName := fs.String("vm-name", "cnimbus-run", "VM name to create (removed after it shuts down, unless --vm-keep)")
	vmKeep := fs.Bool("vm-keep", false, "don't delete the VM after it shuts down")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *arch != "amd64" && *arch != "arm64" {
		return fmt.Errorf("--arch must be \"amd64\" or \"arm64\", got %q", *arch)
	}
	// T96: validated once here, for every backend, rather than each
	// backend silently degrading a malformed value into "no networking
	// at all" -- --hostfwd 8080 (forgetting the host:guest form, easy to
	// do since the default "8080:8080" looks like a repeated number)
	// previously produced a guest with no NIC whatsoever and no error,
	// leaving the user hunting for a kernel driver problem that doesn't
	// exist.
	if err := validateHostfwd(*hostfwd); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cnimbus run [flags] <image-path>")
	}
	imagePath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	if _, err := os.Stat(imagePath); err != nil {
		return fmt.Errorf("%s: %w", imagePath, err)
	}
	isISO := strings.EqualFold(filepath.Ext(imagePath), ".iso")

	b, ok := backends[*backend]
	if !ok {
		return fmt.Errorf(`--backend must be "qemu", "vbox", "vmware", or "hyperv", got %q`, *backend)
	}
	spec := launchSpec{
		imagePath:   imagePath,
		isISO:       isISO,
		arch:        *arch,
		uefi:        *uefi,
		memMB:       *memMB,
		smp:         *smp,
		hostfwd:     *hostfwd,
		hostfwdBind: *hostfwdBind,
		vmName:      *vmName,
		keep:        *vmKeep,
		accel:       *accel,
		ovmfCode:    *ovmfCode,
		ovmfVars:    *ovmfVars,
	}
	return b.launch(spec)
}

// validateHostfwd requires exactly one "host:guest" colon and two
// parseable TCP port numbers in 1..65535 (T96) -- every backend
// (qemu/vbox/hyperv) previously just skipped the whole hostfwd/NAT rule
// silently on anything else, producing a guest with no network adapter
// and no error at all.
func validateHostfwd(hostfwd string) error {
	parts := strings.Split(hostfwd, ":")
	if len(parts) != 2 {
		return fmt.Errorf(`--hostfwd must be "<host-port>:<guest-port>" (e.g. "8080:8080"), got %q`, hostfwd)
	}
	for _, label := range []struct {
		name, val string
	}{{"host", parts[0]}, {"guest", parts[1]}} {
		port, err := strconv.Atoi(label.val)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf(`--hostfwd: %s port %q must be a number in 1..65535 (full value: %q)`, label.name, label.val, hostfwd)
		}
	}
	return nil
}

// qemuBackend implements backend by locating qemu-system-<arch> (on
// PATH, or the official Windows installer's default location) and
// exec'ing it directly with the same argv qemuArgv builds for
// describeQEMUCommand's copy-paste instructions (T98) -- this project's
// default, most-used backend.
type qemuBackend struct{}

func (qemuBackend) name() string { return "qemu" }

// available checks both qemu-system-x86_64 and qemu-system-aarch64: see
// the backend interface's own doc comment (backend.go) for why a single
// fixed binary name would wrongly reject a host with only one of the two
// installed.
func (qemuBackend) available() error {
	if _, err := findQEMU(qemuBinName("amd64")); err == nil {
		return nil
	}
	_, err := findQEMU(qemuBinName("arm64"))
	return err
}

func (qemuBackend) launch(spec launchSpec) error {
	imagePath, isISO, arch, uefi := spec.imagePath, spec.isISO, spec.arch, spec.uefi
	ovmfCode, ovmfVars := spec.ovmfCode, spec.ovmfVars
	memMB, smp := spec.memMB, spec.smp
	accel, hostfwd, hostfwdBind := spec.accel, spec.hostfwd, spec.hostfwdBind
	vmName, vmKeep := spec.vmName, spec.keep

	binName := qemuBinName(arch)
	if arch == "arm64" {
		uefi = true // arm64 has no BIOS-equivalent boot path at all -- see README
	}
	qemuPath, err := findQEMU(binName)
	if err != nil {
		return fmt.Errorf(`%s not found on PATH (or the default Windows install location). Install QEMU, or run with --backend vbox/vmware/hyperv instead.
Manual invocation this would have run:
  %s`, binName, describeQEMUCommand(imagePath, isISO, arch, uefi, ovmfCode, ovmfVars, memMB, smp, accel, hostfwd, hostfwdBind, vmName))
	}

	accelArgs, cpuArgs, accelName := resolveAccel(accel, arch)
	fmt.Printf("acceleration: %s\n", accelName)

	var ovmfCodePath, ovmfVarsPath string
	if uefi {
		code, varsPath, synthesized, err := resolveOVMF(arch, ovmfCode, ovmfVars, vmName)
		if err != nil {
			return err
		}
		ovmfCodePath, ovmfVarsPath = code, varsPath
		// T97: a synthesized (Windows-bundled-fallback) VARS store is
		// this run's own throwaway NVRAM, not a shared system template --
		// EDK2 writes real UEFI variables into it (boot entries,
		// BootOrder, and eventually enrolled Secure Boot keys), so
		// reusing one across unrelated VMs leaked firmware state between
		// them (a stale Boot0000 entry pointing at a removed device could
		// send firmware into a boot loop that looked like an image bug).
		// Mirrors the vbox backend's own throwaway-VM lifecycle: deleted
		// after this run unless --vm-keep says otherwise.
		if synthesized && !vmKeep {
			defer func() { _ = os.Remove(varsPath) }() // best-effort cleanup of this run's own throwaway NVRAM
		}
	}

	if hostPort, guestPort, ok := splitHostfwd(hostfwd); ok {
		// hostaddr (the part between the first and second ":") is empty
		// by default in QEMU's own hostfwd syntax, which means "bind
		// every interface" -- explicit here so loopback is the default
		// instead, matching the VirtualBox/Hyper-V backends.
		fmt.Printf("forwarding host %s:%s -> guest port %s\n", hostfwdBind, hostPort, guestPort)
	}
	qArgs := qemuArgv(imagePath, isISO, arch, uefi, ovmfCodePath, ovmfVarsPath, memMB, smp, accelArgs, cpuArgs, hostfwd, hostfwdBind)

	fmt.Printf("running: %s %s\n", qemuPath, strings.Join(qArgs, " "))
	cmd := exec.Command(qemuPath, qArgs...) // #nosec G204 -- qemuPath resolved via exec.LookPath; qArgs built entirely from this function's own flag-derived values
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func qemuBinName(arch string) string {
	if arch == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// qemuArgv builds the exact qemu-system-<arch> argv (T98): both the real
// invocation runViaQEMU execs and the copy-paste instructions
// describeQEMUCommand prints when QEMU isn't installed now go through
// this one pure function, so they can never diverge again the way they
// had -- the printed instructions were missing -M/-cpu/the OVMF
// -drive if=pflash entries, so following them for an arm64 or --uefi
// image failed or silently booted the wrong firmware path. ovmfCode/
// ovmfVars empty (rather than an error) is a valid input: describeQEMUCommand
// calls this for a host that may not even have OVMF installed yet, and
// the shape without those two -drive flags is still useful to show.
func qemuArgv(imagePath string, isISO bool, arch string, uefi bool, ovmfCode, ovmfVars string, memMB, smp int, accelArgs, cpuArgs []string, hostfwd, hostfwdBind string) []string {
	qArgs := []string{"-m", fmt.Sprintf("%d", memMB), "-smp", fmt.Sprintf("%d", smp), "-serial", "stdio"}
	qArgs = append(qArgs, accelArgs...)
	qArgs = append(qArgs, cpuArgs...)
	// T68 (real-boot follow-up): the kernel unconditionally builds in
	// CONFIG_HW_RANDOM_VIRTIO on every arch this project targets, so
	// /dev/hwrng always exists in the guest -- but QEMU only exposes a
	// virtio-rng backend for it to attach to when explicitly asked.
	// Without this device, a real boot confirmed /dev/hwrng exists as a
	// character device but every read fails with "no such device": the
	// driver has nothing to bind to. rng-builtin (QEMU's own CSPRNG, no
	// host file path involved) was picked over rng-random specifically
	// because rng-random's /dev/urandom-backed source doesn't exist on
	// the official Windows QEMU build (confirmed: "-object rng-random"
	// fails there with "qom-type does not accept value 'rng-random'" --
	// only rng-builtin/rng-egd are registered), so it's the one backend
	// that actually works identically on every host OS cnimbus supports.
	// virtio-rng-pci itself is available on both "pc"/"q35" (amd64) and
	// "virt" (arm64, which ships a PCIe root complex by default), so this
	// is added unconditionally.
	qArgs = append(qArgs, "-object", "rng-builtin,id=rng0", "-device", "virtio-rng-pci,rng=rng0")
	if isISO {
		qArgs = append(qArgs, "-cdrom", imagePath)
	} else {
		qArgs = append(qArgs, "-drive", "file="+imagePath+",format=raw")
	}
	if hostPort, guestPort, ok := splitHostfwd(hostfwd); ok {
		qArgs = append(qArgs, "-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:%s:%s-:%s", hostfwdBind, hostPort, guestPort), "-device", "virtio-net-pci,netdev=n0")
	}
	if arch == "arm64" {
		qArgs = append([]string{"-M", "virt"}, qArgs...)
	} else if uefi {
		qArgs = append([]string{"-M", "q35"}, qArgs...)
	} else {
		qArgs = append([]string{"-M", "pc"}, qArgs...)
	}
	if uefi && ovmfCode != "" {
		qArgs = append(qArgs,
			"-drive", "if=pflash,format=raw,readonly=on,file="+ovmfCode,
			"-drive", "if=pflash,format=raw,file="+ovmfVars)
	}
	return qArgs
}

// resolveAccel picks a QEMU acceleration backend and matching -cpu value.
// Hardware acceleration (KVM/HVF/WHPX) only works when the guest arch
// matches the host's own CPU architecture -- none of the three can
// accelerate a foreign-arch guest, that is what -accel tcg is for -- so
// "auto" checks runtime.GOARCH against the requested guest arch before
// ever trying a hardware backend, and falls back to tcg (with the same
// -cpu model runViaQEMU used unconditionally before this ticket) whenever
// they don't match or no accelerator is available.
func resolveAccel(requested, arch string) (accelArgs, cpuArgs []string, name string) {
	tcgCPU := []string{}
	if arch == "arm64" {
		tcgCPU = []string{"-cpu", "cortex-a72"}
	}
	hostMatches := (arch == "amd64" && runtime.GOARCH == "amd64") || (arch == "arm64" && runtime.GOARCH == "arm64")

	switch requested {
	case "kvm":
		return []string{"-accel", "kvm"}, []string{"-cpu", "host"}, "kvm (forced)"
	case "hvf":
		return []string{"-accel", "hvf"}, []string{"-cpu", "host"}, "hvf (forced)"
	case "whpx":
		return []string{"-accel", "whpx"}, []string{"-cpu", "host"}, "whpx (forced)"
	case "tcg":
		return []string{"-accel", "tcg"}, tcgCPU, "tcg (forced -- software emulation)"
	case "auto":
		if hostMatches {
			switch runtime.GOOS {
			case "linux":
				if _, err := os.Stat("/dev/kvm"); err == nil {
					return []string{"-accel", "kvm"}, []string{"-cpu", "host"}, "kvm (auto-detected)"
				}
			case "darwin":
				return []string{"-accel", "hvf"}, []string{"-cpu", "host"}, "hvf (auto-detected)"
			case "windows":
				return []string{"-accel", "whpx"}, []string{"-cpu", "host"}, "whpx (auto-detected)"
			}
		}
		return []string{"-accel", "tcg"}, tcgCPU, "tcg (software emulation -- no hardware accelerator available for this host/guest-arch combination)"
	default:
		return []string{"-accel", "tcg"}, tcgCPU, "tcg (unrecognized --accel value, defaulted to software emulation)"
	}
}

// describeQEMUCommand prints the exact command runViaQEMU would exec,
// for a host where qemu-system-<arch> isn't installed (yet) to copy,
// install QEMU, and paste. Built through the same qemuArgv (T98) the
// real run uses -- previously a second, independent implementation that
// had already drifted (missing -M/-cpu/the OVMF -drive if=pflash
// entries), so following it for an arm64 or --uefi image either failed
// outright or silently booted the wrong firmware path, in exactly the
// two cases where a stuck user most needed correct instructions.
// resolveOVMF/resolveAccel are still called here (best-effort: a host
// with no QEMU installed may also have no OVMF yet, or no hardware
// accelerator) so the printed command reflects this host's own
// situation as closely as possible without requiring qemu-system-<arch>
// to already be on PATH.
func describeQEMUCommand(imagePath string, isISO bool, arch string, uefi bool, ovmfCode, ovmfVars string, memMB, smp int, accel, hostfwd, hostfwdBind, vmName string) string {
	bin := qemuBinName(arch)
	accelArgs, cpuArgs, _ := resolveAccel(accel, arch)
	var code, vars string
	if uefi {
		code, vars, _, _ = resolveOVMF(arch, ovmfCode, ovmfVars, vmName) // best-effort; empty on error, qemuArgv handles that
	}
	qArgs := qemuArgv(imagePath, isISO, arch, uefi, code, vars, memMB, smp, accelArgs, cpuArgs, hostfwd, hostfwdBind)
	return bin + " " + strings.Join(qArgs, " ")
}

// findQEMU locates a qemu-system-<arch> binary: on PATH first, then
// Windows' default install location (the official QEMU Windows
// installer doesn't add itself to PATH, same situation as VBoxManage).
// Delegates to the shared findTool (T99): this used to be its own
// independent copy of the "PATH, then Windows install dir" probe with no
// runtime.GOOS guard at all, so on Linux/macOS it harmlessly stat'd the
// literal path C:\Program Files\qemu\<bin>.exe on every call -- findTool
// guards that once for every caller instead of once per copy.
func findQEMU(binName string) (string, error) {
	return findTool(exec.LookPath, binName, filepath.Join("qemu", binName+".exe"))
}

// resolveOVMF finds OVMF_CODE.fd/OVMF_VARS.fd in a handful of common
// package install locations (Debian/Ubuntu's ovmf/qemu-efi-aarch64
// packages, Fedora's edk2-ovmf, Homebrew's qemu formula) when not given
// explicitly -- there's no single standard path across distros.
// resolveOVMF returns (code, vars, synthesized, err) -- synthesized is
// true only for the Windows-bundled-fallback VARS file resolveOVMF
// itself creates (T97), telling the caller this is a throwaway file
// safe to delete after the run, as opposed to a --ovmf-vars path the
// user passed explicitly or a distro-installed template.
func resolveOVMF(arch, code, vars, vmName string) (resolvedCode, resolvedVars string, synthesized bool, err error) {
	if code != "" && vars != "" {
		return code, vars, false, nil
	}
	candidates := map[string][2]string{
		"amd64": {"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
		"arm64": {"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"},
	}
	pair, ok := candidates[arch]
	if !ok {
		return "", "", false, fmt.Errorf("no known OVMF path for arch %q -- pass --ovmf-code/--ovmf-vars explicitly", arch)
	}
	if code == "" {
		code = pair[0]
	}
	if vars == "" {
		vars = pair[1]
	}
	if _, statErr := os.Stat(code); statErr != nil && runtime.GOOS == "windows" {
		// The official QEMU-for-Windows installer bundles its own EDK2
		// firmware under its own install dir (no separate OVMF package
		// to install, unlike Linux) -- but as *_CODE only, no matching
		// *_VARS template (verified empirically: the installer's
		// share/ directory has no edk2-x86_64-vars.fd at all). A blank,
		// zeroed file of the same size is what a brand-new (no saved
		// UEFI variables yet) VARS store looks like -- EDK2 itself
		// treats an all-zero VARS pflash as "first boot, initialize
		// defaults" rather than erroring, the same state libvirt's own
		// per-VM NVRAM-from-template copy produces before EDK2's first
		// write.
		if winCode, winVars, werr := resolveWindowsBundledOVMF(arch, vmName); werr == nil {
			code, vars = winCode, winVars
			synthesized = true
		}
	}
	if _, statErr := os.Stat(code); statErr != nil {
		return "", "", false, fmt.Errorf("OVMF firmware not found at %s (pass --ovmf-code explicitly) -- "+
			"install your distro's OVMF/edk2-ovmf package, e.g. \"apt install ovmf\" or \"dnf install edk2-ovmf\": %w", code, statErr)
	}
	return code, vars, synthesized, nil
}

// resolveWindowsBundledOVMF locates the EDK2 firmware the official QEMU
// Windows installer bundles under its own share/ directory (same
// install-dir pattern as findQEMU/findVMrun/findVBoxManage elsewhere in
// this file), and synthesizes a blank VARS file next to it since the
// installer ships none. The VARS path is per-vmName (T97): EDK2 writes
// real UEFI variables into it as the guest runs, so a shared path across
// unrelated VMs leaked firmware state between them -- a stale Boot0000
// entry pointing at a device one image no longer has could send a
// completely unrelated image's firmware into a boot loop.
func resolveWindowsBundledOVMF(arch, vmName string) (code, vars string, err error) {
	names := map[string]string{"amd64": "edk2-x86_64-code.fd", "arm64": "edk2-aarch64-code.fd"}
	name, ok := names[arch]
	if !ok {
		return "", "", fmt.Errorf("no known bundled EDK2 firmware name for arch %q", arch)
	}
	// Uses the same shared probeWindowsInstallDirs (T99) as findQEMU/
	// findVBoxManage/findVMrun -- previously its own fourth independent
	// copy of the "walk ProgramFiles + hardcoded fallback" loop.
	if codePath, _, ok := probeWindowsInstallDirs(filepath.Join("qemu", "share", name)); ok {
		info, statErr := os.Stat(codePath)
		if statErr != nil {
			return "", "", fmt.Errorf("stat %s: %w", codePath, statErr)
		}
		varsPath := filepath.Join(os.TempDir(), "cnimbus-ovmf-vars-"+arch+"-"+vmName+".fd")
		if _, statErr := os.Stat(varsPath); statErr != nil {
			if werr := os.WriteFile(varsPath, make([]byte, info.Size()), 0o600); werr != nil {
				return "", "", fmt.Errorf("creating blank OVMF vars file at %s: %w", varsPath, werr)
			}
		}
		return codePath, varsPath, nil
	}
	return "", "", fmt.Errorf("no bundled EDK2 firmware found under the QEMU install directory")
}

// vboxBackend implements backend by scripting VBoxManage to create a
// throwaway VM, attach the image, and start it headless -- exactly the
// manual steps README.md's own networking section walks through by hand.
type vboxBackend struct{}

func (vboxBackend) name() string { return "vbox" }

func (vboxBackend) available() error {
	_, err := findVBoxManage()
	return err
}

// launch creates a throwaway VirtualBox VM, attaches imagePath, and
// starts it. Cleaned up afterward unless --vm-keep.
func (vboxBackend) launch(spec launchSpec) error {
	imagePath, isISO, arch, uefi := spec.imagePath, spec.isISO, spec.arch, spec.uefi
	memMB := spec.memMB
	hostfwd, hostfwdBind, vmName, keep := spec.hostfwd, spec.hostfwdBind, spec.vmName, spec.keep

	vboxManage, err := findVBoxManage()
	if err != nil {
		return err
	}
	// FORMAT raw (internal/rawimage) has no MBR boot code at all -- GPT +
	// ESP only, UEFI-only by design (see rawimage's own package doc) --
	// so a raw image is forced to UEFI regardless of --uefi, the same
	// reasoning T42 already applied to Hyper-V Generation 2 (T95).
	uefi = effectiveUEFI(isISO, uefi)

	osType := "Linux_64"
	platformArch := "x86"
	if arch == "arm64" {
		platformArch = "arm"
	}
	// #nosec G204 -- vboxManage is resolved via findVBoxManage (PATH or a
	// fixed known install location), never user input; every args slice
	// below is this function's own fixed/flag-derived values.
	run := func(desc string, args ...string) error {
		cmd := exec.Command(vboxManage, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %s: %w", desc, strings.TrimSpace(string(out)), err)
		}
		return nil
	}

	fmt.Printf("creating VirtualBox VM %q...\n", vmName)
	if err := run("createvm", "createvm", "--name", vmName, "--platform-architecture", platformArch, "--ostype", osType, "--register"); err != nil {
		return err
	}
	// Only ever cleans up a VM that *failed* to reach a running state --
	// `startvm --type headless` itself returns as soon as the VM has
	// launched, not when it shuts down, so unconditionally deferring
	// this would unregister-and-delete a VM within a second of it
	// actually starting, directly contradicting the "keeps running
	// until you stop it" message printed below. started, set true only
	// once startvm itself succeeds, is what tells this apart from a
	// genuine setup failure worth tidying up after.
	started := false
	defer func() {
		cleanupUnlessStartedOrKept(started, keep,
			fmt.Sprintf("leaving VM %q in place (--vm-keep) -- remove it yourself with:\n  %s unregistervm %s --delete", vmName, vboxManage, vmName),
			func() {
				if out, err := exec.Command(vboxManage, "unregistervm", vmName, "--delete").CombinedOutput(); err != nil {
					fmt.Printf("cleanup: could not remove VM %q after a failed setup step: %s\n", vmName, strings.TrimSpace(string(out)))
				} else {
					fmt.Printf("cleaned up VM %q (a setup step failed before it could start)\n", vmName)
				}
			})
	}()

	// --nictype1 virtio, not left to whatever the VirtualBox ostype
	// table (osType above) happens to default to: verified empirically
	// that this can silently pick a NIC model this kernel's own
	// CONFIG_VIRTIO_NET-only NIC support doesn't drive, leaving the
	// guest with no network device at all -- virtio-net is the one
	// model both `CONFIG_VIRTIO_NET` (already enabled) and VirtualBox's
	// own emulation are guaranteed to agree on.
	if err := run("configure", "modifyvm", vmName, "--memory", fmt.Sprintf("%d", memMB), "--nic1", "nat", "--nictype1", "virtio"); err != nil {
		return err
	}
	if uefi {
		if err := run("configure-firmware", "modifyvm", vmName, "--firmware", "efi"); err != nil {
			return err
		}
	}
	if hostPort, guestPort, ok := splitHostfwd(hostfwd); ok {
		fmt.Printf("forwarding host %s:%s -> guest port %s\n", hostfwdBind, hostPort, guestPort)
		rule := fmt.Sprintf("cnimbus,tcp,%s,%s,,%s", hostfwdBind, hostPort, guestPort)
		if err := run("port-forward", "modifyvm", vmName, "--natpf1", rule); err != nil {
			return err
		}
	}
	if err := run("add-storage-controller", "storagectl", vmName, "--name", "IDE", "--add", "ide"); err != nil {
		return err
	}
	medium := "dvddrive"
	if !isISO {
		medium = "hdd"
	}
	if err := run("attach-media", "storageattach", vmName, "--storagectl", "IDE",
		"--port", "0", "--device", "0", "--type", medium, "--medium", imagePath); err != nil {
		return err
	}

	fmt.Printf("starting VM %q (headless; port %s forwarded)...\n", vmName, hostfwd)
	if err := run("start", "startvm", vmName, "--type", "headless"); err != nil {
		return err
	}
	started = true
	fmt.Printf("VM running. Stop it with:\n  %s controlvm %s poweroff\n", vboxManage, vmName)
	fmt.Printf("Remove it afterward with:\n  %s unregistervm %s --delete\n", vboxManage, vmName)
	fmt.Println(vmReturnsImmediatelyMsg)
	return nil
}

// findVBoxManage locates VBoxManage: on PATH first, then Windows'
// default install location (VBoxManage isn't normally added to PATH by
// the Windows installer, unlike Linux package managers).
// Delegates to the shared findTool (T99) -- see findQEMU's doc comment.
func findVBoxManage() (string, error) {
	p, err := findTool(exec.LookPath, "VBoxManage", filepath.Join("Oracle", "VirtualBox", "VBoxManage.exe"))
	if err != nil {
		return "", fmt.Errorf("VBoxManage not found on PATH (or the default Windows install location) -- install VirtualBox, or use a different --backend")
	}
	return p, nil
}
