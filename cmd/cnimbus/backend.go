package main

import (
	"fmt"
	"strings"
)

// launchSpec is the fully-resolved, backend-agnostic description of one
// `cnimbus run` invocation -- every flag runRun parses ends up in here
// exactly once, so a new backend never has to re-derive "is this ISO or
// raw media", "did the user ask to keep the VM", etc. from scratch.
// Fields marked "qemu only" still live here rather than on a
// qemu-specific struct: F3's own investigation (see Tasks.md) found that
// a *separate* per-backend flag struct is exactly what let T20/T21/T95
// each get "fixed" in only one or two of the four backends before -- one
// shared struct means a reviewer (and the compiler) can see at a glance
// which fields a given backend chooses to ignore, instead of four
// independent argument lists quietly drifting apart.
type launchSpec struct {
	imagePath   string
	isISO       bool
	arch        string
	uefi        bool
	memMB       int
	smp         int    // qemu only
	hostfwd     string
	hostfwdBind string
	vmName      string
	keep        bool
	accel       string // qemu only
	ovmfCode    string // qemu only
	ovmfVars    string // qemu only
}

// backend is the shape every `cnimbus run` hypervisor driver implements.
// Deliberately thin: F3's own investigation (see Tasks.md, "was T100")
// found that each backend's *cleanup* mechanism is genuinely different --
// VBoxManage unregistervm, a PowerShell Stop-VM/Remove-VM script, a plain
// os.RemoveAll of a work directory -- so this interface does not try to
// force a shared "cleanup()" method where the real implementations don't
// share a mechanism. What it does unify is the one shape all four
// backends already have in common: decide whether the tool this backend
// needs is even present, then launch the VM from a fully-resolved spec.
type backend interface {
	// name is the exact --backend flag value this implementation
	// answers to.
	name() string
	// available reports whether this backend's underlying tool can be
	// found at all (on PATH, or a known install location) -- a coarse,
	// spec-independent signal deliberately kept separate from launch's
	// own tool resolution. qemuBackend.available in particular checks
	// both qemu-system-x86_64 and qemu-system-aarch64 rather than one
	// fixed binary name: gating a launch on a single arch's binary would
	// have rejected a host with only the aarch64 build installed even
	// though `--arch arm64` would work fine, and this project's runRun
	// dispatch (see run.go) deliberately does not call available() as a
	// pre-flight gate before launch for exactly that reason -- launch
	// itself re-resolves the arch-specific binary and produces the
	// precise, backend-specific "not found" error. available exists on
	// the interface for callers that want a lighter-weight check without
	// attempting a real launch (and is covered directly by this
	// package's tests).
	available() error
	// launch boots one VM from spec, returning once the VM is either
	// running (vbox/vmware/hyperv all return as soon as the guest has
	// started -- see each implementation's own doc comment) or has
	// failed to start (in which case any throwaway setup artifact this
	// backend created is cleaned up first, per its own mechanism).
	// qemu's own launch is the one exception: it execs qemu-system-<arch>
	// in the foreground and blocks until the guest exits, since that
	// backend creates no persistent VM registration to clean up.
	launch(spec launchSpec) error
}

// effectiveUEFI is the shared "FORMAT raw has no MBR boot code at all --
// GPT+ESP only, UEFI-only by design (see internal/rawimage's own package
// doc)" decision vbox and vmware both apply: a raw image is forced to
// UEFI regardless of what --uefi says, the same reasoning T42 already
// applied to Hyper-V Generation 2 (T95). qemu instead keys its own
// equivalent decision off --arch (arm64 has no BIOS-equivalent boot path
// at all, handled inline in qemuBackend.launch), and hyperv's own launch
// now also supports raw images directly (F4) without forcing UEFI --
// Generation 1 (BIOS) can attach a raw-wrapped VHD too -- so this helper
// is deliberately only used by the two backends that actually share the
// force-UEFI decision, not forced onto all four.
func effectiveUEFI(isISO, uefi bool) bool {
	if !isISO {
		return true
	}
	return uefi
}

// splitHostfwd parses an already-validateHostfwd-accepted "host:guest"
// pair into its two parts. ok is false only for hyperv's own case, where
// --hostfwd forwarding is optional (the zero-value hostfwd string never
// reaches here, but a caller-constructed non-forwarding request might in
// principle) -- qemu/vbox always have a hostfwd value by the time this
// runs, since runRun's own default is "8080:8080" and every value is
// validated up front.
func splitHostfwd(hostfwd string) (hostPort, guestPort string, ok bool) {
	hg := strings.SplitN(hostfwd, ":", 2)
	if len(hg) != 2 {
		return "", "", false
	}
	return hg[0], hg[1], true
}

// vmReturnsImmediatelyMsg is the exact trailing line vbox's and vmware's
// launch both print after a successful start: VBoxManage's own
// `startvm --type headless` and vmrun's own `start ... nogui` both return
// as soon as the guest has launched, not when it shuts down -- unlike
// qemu's invocation (runs in the foreground via cmd.Run(), blocking until
// the guest exits) or hyperv's (whose own PowerShell script already waits
// for the whole setup, including the --hostfwd verification probe, to
// finish before this point is ever reached).
const vmReturnsImmediatelyMsg = "(this command returns immediately -- the VM keeps running until you stop it)"

// cleanupUnlessStartedOrKept implements the two-way decision vbox's and
// vmware's launch each apply on their own setup-failure path: leave this
// backend's throwaway artifact (a registered VM, a temp work directory)
// alone once it has reached a genuinely running state (started), or
// whenever --vm-keep was passed (keep) -- printing keptMsg in the keep
// case so the user knows where it was left -- otherwise call remove,
// this backend's own real delete mechanism (VBoxManage unregistervm,
// os.RemoveAll). started is only meaningful for a backend whose artifact
// can reach a real running state before this ever fires (vbox's
// registered-but-not-yet-started VM); vmware's own work directory only
// ever calls this from a pre-start failure path, so it always passes
// false. hyperv's own two cleanup mechanisms deliberately do not go
// through this helper: its VM-registration cleanup (Stop-VM+Remove-VM)
// has never been keep-aware (a failed setup has nothing worth keeping),
// and its raw-image VHD-workDir cleanup needs a *third*, distinct message
// for the started-but-not-kept case ("VM still running with the VHD
// attached -- remove it yourself once stopped") that this two-way helper
// has no slot for -- routing either through here would mean forcing a
// shape that doesn't fit, exactly the "fake common abstraction" F3 warns
// against.
func cleanupUnlessStartedOrKept(started, keep bool, keptMsg string, remove func()) {
	if started || keep {
		if keep {
			fmt.Println(keptMsg)
		}
		return
	}
	remove()
}

// backends maps every --backend flag value to its implementation.
// runRun's dispatch (run.go) is now a one-line map lookup plus
// b.launch(spec) instead of a four-armed switch each calling its own
// independently-shaped function -- adding a fifth backend means adding
// one entry here and one type implementing backend, not touching
// runRun's own flag parsing at all.
var backends = map[string]backend{
	"qemu":   qemuBackend{},
	"vbox":   vboxBackend{},
	"vmware": vmwareBackend{},
	"hyperv": hypervBackend{},
}
