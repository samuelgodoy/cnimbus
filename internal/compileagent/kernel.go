package compileagent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// archInfo maps cnimbus's own arch name (matching Go/Docker's GOARCH
// convention, and the Nimbusfile's ARCH directive) to what Kbuild needs
// to build for it. There is no cross-compiler prefix here: `cnimbus
// prepare` always runs this container with `--platform linux/<arch>`
// matching the target, so the container *is* that architecture
// natively (via Docker Desktop's Rosetta/QEMU emulation when the host
// isn't) -- plain, unprefixed gcc is correct on both.
type archInfo struct {
	kernelArch  string // Kbuild ARCH= value
	imageTarget string // make target that produces the kernel image
	imagePath   string // path to that image, relative to the kernel source dir
	efiBootName string // El Torito/ESP filename UEFI firmware looks for
}

var archTable = map[string]archInfo{
	"amd64": {
		kernelArch:  "x86_64",
		imageTarget: "bzImage",
		imagePath:   "arch/x86/boot/bzImage",
		efiBootName: "BOOTX64.EFI",
	},
	"arm64": {
		kernelArch:  "arm64",
		imageTarget: "Image",
		imagePath:   "arch/arm64/boot/Image",
		efiBootName: "BOOTAA64.EFI",
	},
}

// KernelSpec is what the host CLI already resolved (internal/kernelinfo)
// and is handing down to the in-container build.
type KernelSpec struct {
	Version      string
	SourceURL    string
	PGPURL       string // detached-signature URL; empty means kernel.org published none for this release
	Arch         string // "amd64" or "arm64"
	CacheDir     string // e.g. /cache
	FragmentDirs []string

	// InsecureSkipVerify skips PGP signature verification even when
	// PGPURL is set. Only meant for a trusted offline mirror that
	// doesn't carry a matching .sign file -- see --insecure-skip-kernel-verify.
	InsecureSkipVerify bool
}

// FetchKernel downloads and extracts the kernel source, returning its
// directory (and provenance data for cmd/thunder's pieces.json -- see
// FetchResult). See VerifyKernelTarball for the PGP verification this
// performs against known kernel.org signer keys before extraction.
func FetchKernel(spec KernelSpec) (FetchResult, error) {
	tarball := fmt.Sprintf("linux-%s.tar.xz", spec.Version)
	dest := fmt.Sprintf("linux-%s", spec.Version)
	result, err := fetchExtract(spec.CacheDir, spec.SourceURL, tarball, dest, "xz", spec.PGPURL, "", spec.InsecureSkipVerify)
	if err != nil {
		return result, err
	}
	result.ResolvedVersion = spec.Version
	return result, nil
}

// BuildKernel configures (tinyconfig + cnimbus's fragments, resolved via
// merge_config.sh + olddefconfig) and compiles the kernel image,
// copying it to outDir/vmlinuz.
//
// merge_config.sh is Kbuild's own script, bundled in the kernel source
// being built -- invoking it is not shell-script orchestration on
// cnimbus's part, any more than invoking `make` is.
//
// ARCH is passed as a `make` command-line argument, not merely an
// exported environment variable: the kernel's own top-level Makefile
// does an unconditional `CC = $(CROSS_COMPILE)gcc` assignment, which in
// GNU Make silently overrides an environment-only CC/ARCH (verified
// the hard way -- an env-only override was silently ignored, while it
// happened to *look* like it worked for a native x86_64 build purely
// because the Makefile's own default already matched).
func BuildKernel(spec KernelSpec, srcDir string, nimbusFragmentPaths []string, outDir string) error {
	arch, ok := archTable[spec.Arch]
	if !ok {
		return fmt.Errorf("unsupported kernel arch %q (supported: amd64, arm64)", spec.Arch)
	}

	makeArgs := []string{"ARCH=" + arch.kernelArch}
	// KBUILD_BUILD_TIMESTAMP/USER/HOST: without these, `make` stamps the
	// kernel's own version banner ("Linux version ... (user@host) ...
	// #1 SMP <date>") with whatever the build machine's clock/whoami
	// happen to be -- two otherwise-identical builds run on different
	// machines or dates produce a different vmlinuz, breaking the
	// reproducible-build story the rest of this pipeline (PGP-verified
	// source, pinned BusyBox/iptables hashes) already tells.
	env := append(os.Environ(),
		"KBUILD_BUILD_TIMESTAMP=@0",
		"KBUILD_BUILD_USER=cnimbus",
		"KBUILD_BUILD_HOST=cnimbus",
	)

	Logf("configuring kernel: tinyconfig + cnimbus fragments (%s)", spec.Arch)
	if err := run(srcDir, env, "make", append(append([]string{}, makeArgs...), "-s", "tinyconfig")...); err != nil {
		return err
	}

	fragments := append([]string{}, nimbusFragmentPaths...)
	fragments = append(fragments, spec.FragmentDirs...)
	mergeArgs := append([]string{"scripts/kconfig/merge_config.sh", "-m", ".config"}, fragments...)
	mergeEnv := append(append([]string{}, env...), "ARCH="+arch.kernelArch)
	mergeOutput, err := runCaptured(srcDir, mergeEnv, "sh", mergeArgs...)
	if err != nil {
		return fmt.Errorf("merge_config.sh: %w", err)
	}
	if err := checkMergeConfigConflicts(mergeOutput); err != nil {
		return err
	}
	if err := run(srcDir, env, "make", append(append([]string{}, makeArgs...), "-s", "olddefconfig")...); err != nil {
		return err
	}
	if err := verifyFragmentsApplied(srcDir, fragments); err != nil {
		return err
	}

	jobs := buildJobs()
	Logf("building %s with %s jobs", arch.imageTarget, jobs)
	buildArgs := append(append([]string{}, makeArgs...), "-j"+jobs, arch.imageTarget)
	if err := run(srcDir, env, "make", buildArgs...); err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return copyFile(filepath.Join(srcDir, arch.imagePath), filepath.Join(outDir, "vmlinuz"))
}

func run(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

// runCaptured behaves like run, but also tees stdout into the returned
// string so a caller can inspect it afterward -- used for merge_config.sh,
// whose only signal that two fragments disagree on a symbol is a line in
// its own stdout (see checkMergeConfigConflicts).
func runCaptured(dir string, env []string, name string, args ...string) (string, error) {
	var buf strings.Builder
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("%s %v: %w", name, args, err)
	}
	return buf.String(), nil
}

// checkMergeConfigConflicts fails the build if merge_config.sh's output
// shows two fragments disagreeing on the same symbol.
//
// merge_config.sh takes the *last* fragment's value whenever two fragments
// set the same symbol, and its only signal that this happened is a line
// like "Value of CONFIG_X is redefined by fragment <path>:" printed to
// stdout -- easy to miss in a multi-thousand-line build log, and a class of
// silent-Kconfig failure verifyFragmentsApplied structurally cannot see
// (it compares fragments against the final .config individually, so a
// symbol that "won" the merge still verifies fine for the fragment that
// won it).
//
// The subtlety a first draft of this function got wrong, caught only by
// running a real build (not by reading the code): merge_config.sh prints
// this exact "is redefined by fragment" line for *every* symbol a
// fragment changes relative to whatever .config already holds --
// including the completely ordinary case of the *first* fragment to ever
// touch a symbol, which necessarily "redefines" it away from
// tinyconfig's near-empty baseline ("# CONFIG_X is not set" -> "y"). A
// real amd64 build of this project's own fragments produced 47 such
// lines with none of them being an actual cross-fragment collision --
// every one of this project's Kconfig symbols is only ever set by
// exactly one fragment. So presence of the marker alone is not a
// conflict signal; only a symbol attributed to *two or more distinct*
// fragment files is a genuine collision (one fragment's value, then a
// later, different fragment silently overriding it).
func checkMergeConfigConflicts(mergeOutput string) error {
	const marker = "is redefined by fragment"
	fragmentsBySymbol := map[string]map[string]bool{}
	var order []string // first-seen symbol order, for deterministic output
	for _, line := range strings.Split(mergeOutput, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		// "Value of CONFIG_X is redefined by fragment <path>:"
		before := strings.TrimPrefix(line[:idx], "Value of ")
		sym := strings.TrimSpace(before)
		after := strings.TrimSuffix(strings.TrimSpace(line[idx+len(marker):]), ":")
		frag := strings.TrimSpace(after)
		if sym == "" || frag == "" {
			continue
		}
		if fragmentsBySymbol[sym] == nil {
			fragmentsBySymbol[sym] = map[string]bool{}
			order = append(order, sym)
		}
		fragmentsBySymbol[sym][frag] = true
	}

	type conflict struct {
		symbol    string
		fragments []string
	}
	var conflicts []conflict
	for _, sym := range order {
		frags := fragmentsBySymbol[sym]
		if len(frags) < 2 {
			continue
		}
		list := make([]string, 0, len(frags))
		for f := range frags {
			list = append(list, f)
		}
		sort.Strings(list)
		conflicts = append(conflicts, conflict{symbol: sym, fragments: list})
	}
	if len(conflicts) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "kernel config: merge_config.sh found %d symbol(s) set by more than one "+
		"fragment (the later fragment silently won):\n", len(conflicts))
	for _, c := range conflicts {
		fmt.Fprintf(&b, "  %s (set by %s)\n", c.symbol, strings.Join(c.fragments, ", "))
	}
	b.WriteString("fix: make the fragments agree, or remove the redundant assignment")
	return fmt.Errorf("%s", b.String())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// 0o644, deliberately not tighter: dst is a published "piece" (see
	// internal/pieces) meant to be fetched later, possibly by a
	// different user/process or served over HTTP -- it needs to stay
	// world-readable. #nosec G302 -- see comment above.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", dst, err)
	}
	return nil
}

// KernelDefinesNetfilterXtablesLegacy reports whether srcDir's own
// net/netfilter/Kconfig declares a NETFILTER_XTABLES_LEGACY symbol at
// all (AD-047). It's a real Kconfig gate, but only starting with some
// kernel release after 6.9.4 -- absent entirely from that version's own
// Kconfig tree, confirmed empirically. IP_NF_IPTABLES_LEGACY (this
// project's iptables chain) and IP6_NF_IPTABLES_LEGACY (its ip6tables
// counterpart, see minimal.fragment) both `depends on` it once it
// exists, so cmd/thunder needs to know, per real build, whether to ask
// for it -- checked directly against the fetched source rather than a
// hardcoded version cutoff that would drift out of date as kernel.org
// ships new releases.
func KernelDefinesNetfilterXtablesLegacy(srcDir string) bool {
	data, err := os.ReadFile(filepath.Join(srcDir, "net", "netfilter", "Kconfig"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "config NETFILTER_XTABLES_LEGACY" {
			return true
		}
	}
	return false
}

// verifyFragmentsApplied fails the build if any `CONFIG_X=y` a cnimbus
// kconfig fragment asked for is absent from the final .config after
// olddefconfig has run.
//
// This exists because Kconfig's failure mode here is silence, and that
// silence has shipped four separate broken kernels in this project's
// history. A symbol whose dependencies aren't met is simply dropped:
// merge_config.sh prints a "Previous value" note that scrolls past in a
// multi-thousand-line build log, olddefconfig removes the line outright,
// `make` succeeds, the image builds, boots, and only *some specific
// runtime feature* is quietly missing. Every instance had the same
// shape -- a parent gate left at tinyconfig's default:
//
//   - CONFIG_VIRTIO_MENU off  -> every CONFIG_VIRTIO_* child dropped
//   - CONFIG_MULTIUSER off    -> setuid/setgid gone, USER directive broke
//   - CONFIG_FILE_LOCKING off -> flock() gone, FIREWALL/iptables broke
//   - CONFIG_HYPERVISOR_GUEST off -> every CONFIG_HYPERV* dropped, a
//     Hyper-V guest had no network device at all
//
// Each cost a full round of "build, boot, observe a puzzling runtime
// failure, bisect backwards to a missing symbol". Checking the resulting
// .config against what was actually requested turns all of them into one
// build-time error naming the symbol.
//
// Only `=y` lines are checked. `=n` requests are intentionally skipped:
// "absent from .config" is a correct outcome for those, indistinguishable
// from an explicit `# CONFIG_X is not set`. Non-boolean assignments
// (strings like CONFIG_CMDLINE, ints) are compared for presence of the
// symbol rather than exact value, since Kconfig legitimately normalizes
// quoting.
func verifyFragmentsApplied(srcDir string, fragmentPaths []string) error {
	applied, err := parseKconfigAssignments(filepath.Join(srcDir, ".config"))
	if err != nil {
		return fmt.Errorf("reading resulting .config: %w", err)
	}

	missing := map[string][]string{}      // symbol -> fragments that asked for it but it never applied
	wrongVal := map[string]string{}       // symbol -> the =m/other value it resolved to instead of =y
	wrongValFrag := map[string][]string{} // symbol -> fragments that requested =y for it
	for _, path := range fragmentPaths {
		requested, err := parseKconfigAssignments(path)
		if err != nil {
			return fmt.Errorf("reading fragment %s: %w", path, err)
		}
		for sym, val := range requested {
			if val == "n" {
				continue
			}
			got, ok := applied[sym]
			if !ok {
				missing[sym] = append(missing[sym], filepath.Base(path))
				continue
			}
			// Presence alone isn't enough for a boolean request: Kconfig can
			// resolve a requested =y to =m (tristate symbol, CONFIG_MODULES
			// re-enabled, a select-induced downgrade), and this project's
			// images have no modprobe/depmod/modules directory at all
			// (minimal.fragment sets CONFIG_MODULES=n precisely to avoid
			// this) -- a module that never loads is functionally identical
			// to the symbol being absent. Non-boolean values (strings,
			// ints) stay presence-only, per this function's original design.
			if val == "y" && got != "y" {
				wrongVal[sym] = got
				wrongValFrag[sym] = append(wrongValFrag[sym], filepath.Base(path))
			}
		}
	}
	if len(missing) == 0 && len(wrongVal) == 0 {
		return nil
	}

	var b strings.Builder
	if len(missing) > 0 {
		syms := make([]string, 0, len(missing))
		for sym := range missing {
			syms = append(syms, sym)
		}
		sort.Strings(syms)
		fmt.Fprintf(&b, "kernel config: %d requested symbol(s) were dropped by Kconfig "+
			"(their dependencies aren't met, so nothing that needs them will work):\n", len(syms))
		for _, sym := range syms {
			fmt.Fprintf(&b, "  %s (requested by %s)\n", sym, strings.Join(missing[sym], ", "))
		}
	}
	if len(wrongVal) > 0 {
		syms := make([]string, 0, len(wrongVal))
		for sym := range wrongVal {
			syms = append(syms, sym)
		}
		sort.Strings(syms)
		fmt.Fprintf(&b, "kernel config: %d symbol(s) requested as =y resolved to a different "+
			"value (this image has no module loader, so a module is as absent as a dropped symbol):\n", len(syms))
		for _, sym := range syms {
			fmt.Fprintf(&b, "  %s requested =y but resolved to =%s (requested by %s)\n",
				sym, wrongVal[sym], strings.Join(wrongValFrag[sym], ", "))
		}
	}
	b.WriteString("fix the fragment: enable whichever parent gate each symbol depends on " +
		"(check its `depends on` line in the kernel's own Kconfig files)")
	return fmt.Errorf("%s", b.String())
}

// parseKconfigAssignments reads the `CONFIG_X=value` lines out of a
// .config or a fragment, ignoring comments, blank lines, and Kconfig's
// own `# CONFIG_X is not set` form (which carries no request).
func parseKconfigAssignments(path string) (map[string]string, error) {
	f, err := os.Open(path) // #nosec G304 -- paths are cnimbus's own embedded fragments and the kernel's .config
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	// .config lines are short, but CONFIG_CMDLINE and friends can hold a
	// long string; the default 64KiB token limit is ample.
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "CONFIG_") {
			continue
		}
		sym, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[sym] = strings.Trim(val, `"`)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
