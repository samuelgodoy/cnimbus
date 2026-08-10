package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cnimbus/internal/assets"
	"cnimbus/internal/dockerrun"
	"cnimbus/internal/kernelinfo"
	"cnimbus/internal/nimbusfile"
	"cnimbus/internal/pieces"
)

const (
	builderImageTag = "cnimbus-builder"
	// goBuilderImage compiles Thunder itself, matching the Nimbusfile's
	// declared architecture -- a throwaway container, never published,
	// never embedded. Pinned by both tag (for readability) and digest
	// (so a registry-side tag mutation, accidental or malicious, can't
	// silently swap out the build environment) -- verified with `docker
	// inspect --format='{{index .RepoDigests 0}}' golang:1.26.5`. Bump
	// both together alongside Go releases.
	goBuilderImage = "golang:1.26.5@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647"
)

// defaultPiecesDir is where `prepare` writes by default and where
// `build-disk` looks automatically when no --pieces/CNIMBUS_PIECES is
// given, so the two-command happy path needs no flags at all.
const defaultPiecesDir = "./pieces"

// runPrepare is the one command in `cnimbus` that touches Docker. It:
//  1. compiles Thunder from source, for the target architecture, in a
//     throwaway `golang` container -- so Thunder itself is arm64 when
//     the Nimbusfile says ARCH arm64, with no local Go toolchain needed
//     and nothing arm64 pre-baked into `cnimbus` itself;
//  2. builds the kernel-compiling image around that fresh Thunder,
//     running it with `--platform linux/<arch>` so the container *is*
//     that architecture (native gcc, no cross-compiler);
//  3. runs it to produce the "pieces" that `build-disk` later assembles
//     into an image, with no container involved in that step at all.
//
// KERNEL/BUSYBOX/ARCH default from the Nimbusfile in the current
// directory, if one exists (same file `build-disk` reads) -- so editing
// one Nimbusfile and running `prepare` then `build-disk` in sequence uses
// consistent versions throughout. Flags override whatever the Nimbusfile
// says, and everything still works with no Nimbusfile present at all
// (kernel.org's latest stable release, BusyBox's own built-in default).
func runPrepare(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	nimbusfilePath := fs.String("f", "Nimbusfile", "Nimbusfile to read KERNEL/BUSYBOX/ARCH defaults from, if present")
	kernelVersion := fs.String("kernel", "", `kernel version: "latest-stable", "latest-longterm", or explicit (e.g. "6.9.4"); overrides the Nimbusfile`)
	busyboxVersion := fs.String("busybox", "", `busybox version; overrides the Nimbusfile`)
	arch := fs.String("arch", "", "target architecture: amd64 or arm64; overrides the Nimbusfile")
	outDir := fs.String("out", defaultPiecesDir, "directory to write pieces into (arch-namespaced: <out>/<arch>/)")
	vga := fs.Bool("vga", false, "enable a real VGA/framebuffer console (backs console=tty0). Off by "+
		"default: most VMs only need the serial console; turn this on to see boot output in a GUI "+
		"hypervisor's own display window (VirtualBox chief among them)")
	hardboot := fs.String("hardboot", "", `bare-metal boot profile: "none" (default), "eth", "wifi", or `+
		`"eth+wifi". "none" and "eth" are implemented (eth's physical-hardware USB boot still awaits `+
		`real-hardware evidence, see F6.1); "wifi" builds too (kernel + curated driver set + firmware + `+
		`a static WPA supplicant, per F6.3/F6.4) but its D2 real-hardware association proof is still `+
		`pending -- no physical WiFi radio has validated it yet; "eth+wifi" is the explicit spelling of `+
		`what "wifi" already builds (both driver families merged into one kernel) for a machine with `+
		`both a wired NIC and a WiFi radio -- same requirements and same pending real-hardware proof as `+
		`"wifi"; overrides the Nimbusfile's HARDBOOT directive`)
	insecureSkipKernelVerify := fs.Bool("insecure-skip-kernel-verify", false, "skip PGP signature "+
		"verification of the downloaded kernel tarball (see internal/compileagent.VerifyKernelTarball). "+
		"Only for a trusted offline mirror without a matching .tar.sign -- NOT recommended otherwise")
	piecesSignKey := fs.String("pieces-sign-key", "", "path to a file holding a hex-encoded Ed25519 "+
		"private key seed (see \"cnimbus keygen\"); if set, signs the freshly-written pieces.sha256 and "+
		"writes pieces.sha256.sig alongside it, so a later \"build-disk --pieces-verify-key\" (or a "+
		"Nimbusfile PIECESKEY directive) can authenticate these pieces, not just check their integrity")
	jobs := fs.Int("jobs", 0, "make -j parallelism for the kernel/BusyBox/iptables builds; 0 (default) "+
		"auto-detects from CPU count bounded by the container's own cgroup memory/CPU-quota limits "+
		"(avoids an OOM-killed build on a wide-CPU/low-memory container)")
	buildArgs := buildArgFlag{}
	fs.Var(buildArgs, "build-arg", "set a value for an ARG directive (NAME=VALUE); repeatable -- "+
		"only relevant if KERNEL/BUSYBOX/ARCH/VGA themselves reference an ARG")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// One uniform rule for every setting that exists in both places: the
	// Nimbusfile declares it, an explicitly-passed flag overrides it. Keyed
	// on "was this flag actually passed" rather than "is it still the zero
	// value" so that a deliberate opt-out (`--vga=false` against a
	// Nimbusfile saying `VGA true`) is honored instead of being
	// indistinguishable from not passing the flag at all.
	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })

	agentVMware := false
	hf, err := nimbusfile.Parse(*nimbusfilePath, buildArgs)
	if err != nil && !os.IsNotExist(errors.Unwrap(err)) {
		// A Nimbusfile that exists but fails to parse (e.g. an invalid
		// HARDBOOT value) must fail loudly here -- silently falling back
		// to CLI-flag-only defaults, as a prior version of this code did,
		// means an invalid Nimbusfile is indistinguishable from no
		// Nimbusfile at all, and `prepare` proceeds against a
		// configuration the user never actually declared.
		return err
	}
	if err == nil {
		if !passed["kernel"] {
			*kernelVersion = hf.KernelVersion
		}
		if !passed["busybox"] {
			*busyboxVersion = hf.BusyboxVersion
		}
		if !passed["arch"] {
			*arch = hf.Arch
		}
		if !passed["vga"] {
			*vga = hf.VGA
		}
		if !passed["hardboot"] {
			*hardboot = hf.BootProfile
		}
		// AGENT vmware needs CONFIG_X86_IOPL_IOPERM (T71's opt-in
		// agent-vmware.fragment), which is not a flag -- it follows the
		// Nimbusfile's own AGENT directive directly, with no CLI override,
		// same as any other kconfig-affecting fact this project derives
		// from the Nimbusfile rather than a flag.
		agentVMware = hf.Agent != nil && hf.Agent.Kind == "vmware"
		fmt.Printf("using %s for KERNEL/BUSYBOX/ARCH/VGA/HARDBOOT (flags passed on the command line override it)\n", *nimbusfilePath)
	}
	if *hardboot == "" {
		*hardboot = "none"
	}
	if *hardboot != "none" && *hardboot != "eth" && *hardboot != "wifi" && *hardboot != "eth+wifi" {
		return fmt.Errorf(`--hardboot must be "none", "eth", "wifi", or "eth+wifi", got %q`, *hardboot)
	}
	// "eth" (F6.1/F6.2), "wifi" (F6.3/F6.4), and "eth+wifi" all reach this
	// point with no refusal: their kernel fragments, firmware, and (for
	// wifi/eth+wifi) the static WPA supplicant piece are all real and
	// wired into `prepare`. None has a real-hardware proof behind it yet,
	// though -- "eth"'s physical-hardware USB boot (F6.1) and "wifi"'s D2
	// radio-association proof (F6.3, no WiFi chipset emulation exists in
	// QEMU/VirtualBox to even virtually pre-check it, unlike "eth"'s e1000
	// pre-check) both remain pending real hardware -- see Tasks.md's
	// F6.1/F6.3 entries. "eth+wifi" carries the same two pending proofs
	// (it builds strictly the union of what "eth" and "wifi" each need).
	if *kernelVersion == "" {
		*kernelVersion = "latest-stable"
	}
	if *busyboxVersion == "latest" {
		// "latest" is the Nimbusfile's own way of saying "no opinion" --
		// compileagent.BusyboxSpec instead expects "" to mean exactly
		// that (its built-in default version), so normalize here.
		*busyboxVersion = ""
	}
	if *arch == "" {
		*arch = "amd64"
	}
	if *arch != "amd64" && *arch != "arm64" {
		return fmt.Errorf("--arch must be \"amd64\" or \"arm64\", got %q", *arch)
	}
	platform := dockerrun.Platform(*arch)

	fmt.Println("checking docker...")
	if err := dockerrun.CheckAvailable(); err != nil {
		return err
	}

	fmt.Printf("resolving kernel version %q...\n", *kernelVersion)
	resolved, err := kernelinfo.Resolve(*kernelVersion)
	if err != nil {
		return fmt.Errorf("resolving kernel version: %w", err)
	}
	fmt.Printf("kernel %s -> %s\n", resolved.Version, resolved.Source)

	thunderBinary, err := buildThunder(ctx, *arch, platform)
	if err != nil {
		return fmt.Errorf("building thunder for %s: %w", *arch, err)
	}

	buildCtx, err := materializeBuildContext(thunderBinary)
	if err != nil {
		return fmt.Errorf("preparing build context: %w", err)
	}
	defer func() { _ = os.RemoveAll(buildCtx) }() // best-effort temp-dir cleanup

	imageTag := builderImageTag + "-" + *arch + ":latest"
	fmt.Printf("building %s builder image...\n", *arch)
	if err := dockerrun.BuildImage(ctx, buildCtx, imageTag, platform); err != nil {
		return err
	}
	// Best-effort: recorded into pieces.json for later provenance
	// auditing (see cmd/thunder's writeProvenance) -- a digest lookup
	// failing here shouldn't fail the whole build, since it's purely
	// informational.
	builderImageDigest, err := dockerrun.ImageDigest(imageTag)
	if err != nil {
		fmt.Printf("warning: could not resolve builder image digest: %v\n", err)
	}

	// Arch-namespaced so a single --out directory can hold pieces for
	// both architectures (e.g. published together at the same URL
	// prefix); `build-disk` looks under <pieces-source>/<ARCH>/ to match.
	absOut, err := filepath.Abs(filepath.Join(*outDir, *arch))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return err
	}

	env := map[string]string{
		"CNIMBUS_KERNEL_VERSION":    resolved.Version,
		"CNIMBUS_KERNEL_SOURCE_URL": resolved.Source,
		"CNIMBUS_KERNEL_PGP_URL":    resolved.PGP,
		"CNIMBUS_KERNEL_ARCH":       *arch,
	}
	if builderImageDigest != "" {
		env["CNIMBUS_BUILDER_IMAGE_DIGEST"] = builderImageDigest
	}
	if *busyboxVersion != "" {
		env["CNIMBUS_BUSYBOX_VERSION"] = *busyboxVersion
	}
	if *vga {
		env["CNIMBUS_VGA"] = "true"
	}
	if *hardboot != "none" {
		// "eth", "wifi", and "eth+wifi" all reach here now. Thunder reads
		// this to decide which fragment(s) to merge: "eth" gets
		// kconfig/baremetal-eth.fragment; "wifi" and "eth+wifi" both get
		// kconfig/baremetal-wifi.fragment (which is additive on top of
		// baremetal-eth.fragment, never a replacement for it) plus the
		// firmware/supplicant pieces (see cmd/thunder/main.go).
		env["CNIMBUS_HARDBOOT"] = *hardboot
	}
	if agentVMware {
		env["CNIMBUS_AGENT_VMWARE"] = "true"
	}
	if *jobs > 0 {
		env["CNIMBUS_JOBS"] = strconv.Itoa(*jobs)
	}
	if *insecureSkipKernelVerify {
		env["CNIMBUS_INSECURE_SKIP_KERNEL_VERIFY"] = "true"
	}
	if resolved.PGP == "" {
		fmt.Println("warning: kernel.org published no signature URL for this release -- the kernel tarball will be unverified")
	} else if !*insecureSkipKernelVerify {
		fmt.Println("kernel tarball will be verified against known kernel.org signer keys (fetched live via WKD)")
	}

	fmt.Printf("compiling kernel + busybox for %s (this takes a while the first time)...\n", *arch)
	// Named deterministically (T45): a Ctrl-C here previously killed only
	// the `cnimbus` process and the `docker run` client attached to it,
	// leaving the kernel build itself running server-side, orphaned, for
	// as long as it takes to finish on its own. A named container gives
	// Run's ctx-cancellation path (see dockerrun.Run's cmd.Cancel) a
	// target to `docker rm -f`.
	err = dockerrun.Run(ctx, dockerrun.RunOptions{
		Image:    imageTag,
		Platform: platform,
		Env:      env,
		Name:     fmt.Sprintf("cnimbus-prepare-%s-%d", *arch, os.Getpid()),
		Mounts: []dockerrun.Mount{
			{HostPath: "cnimbus-cache-" + *arch, ContainerPath: "/cache", IsVolume: true},
			{HostPath: absOut, ContainerPath: "/out"},
		},
	})
	if err != nil {
		return err
	}

	if err := writePiecesHashes(absOut); err != nil {
		return fmt.Errorf("writing pieces.sha256: %w", err)
	}

	signed := ""
	if *piecesSignKey != "" {
		if err := signPiecesHashes(absOut, *piecesSignKey); err != nil {
			return fmt.Errorf("signing pieces.sha256: %w", err)
		}
		signed = ", pieces.sha256.sig"
	}

	fmt.Printf("done: %s (vmlinuz, busybox, busybox-manifest.tsv, iptables, pieces.json, pieces.sha256%s)\n", absOut, signed)
	fmt.Printf("use it with: cnimbus build-disk --pieces %s\n", filepath.Dir(absOut))
	return nil
}

// writePiecesHashes computes the SHA-256 of every file `prepare` just
// produced and writes it as pieces.sha256, in sha256sum(1)'s own
// "<hex>  <filename>" format -- both independently checkable by hand
// and read back by internal/pieces.Resolve to verify a later
// `build-disk --pieces` fetch actually got these exact bytes. Written via
// writeFileAtomic (T47): a process kill (or a full disk) between create
// and the last write previously left a truncated-but-syntactically-valid
// pieces.sha256 -- e.g. missing the "iptables" entry -- which a later
// `build-disk` would then (correctly, per T5) hard-fail against with an
// error pointing at the manifest rather than at the interrupted prepare
// that actually caused it.
func writePiecesHashes(dir string) error {
	names := []string{"vmlinuz", "busybox", "busybox-manifest.tsv", "iptables", "pieces.json"}
	var b strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	// F6.4's wpa_supplicant: present only for a "wifi"-profile build (see
	// cmd/thunder's own bootProfile=="wifi" gate) -- same
	// present-only-when-built optionality as iptables would have if it
	// weren't unconditional. Not one of the fixed `names` above because,
	// unlike iptables, this file genuinely does not exist for "none"/"eth".
	if data, err := os.ReadFile(filepath.Join(dir, "wpa_supplicant")); err == nil {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), "wpa_supplicant")
	} else if !os.IsNotExist(err) {
		return err
	}
	// F6.3's curated firmware set (design.md D3): walked rather than
	// listed by name, so this stays correct as the curated set widens
	// (F6.6) without a second hardcoded filename list to keep in sync --
	// hashed by its path relative to dir (e.g. "firmware/ath9k_htc/
	// htc_9271-1.4.0.fw"), matching pieces.json's WifiFirmware entries
	// and the file's own destination under /lib/firmware in the image.
	fwDir := filepath.Join(dir, "firmware")
	if info, err := os.Stat(fwDir); err == nil && info.IsDir() {
		var fwPaths []string
		if err := filepath.WalkDir(fwDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					return err
				}
				fwPaths = append(fwPaths, filepath.ToSlash(rel))
			}
			return nil
		}); err != nil {
			return err
		}
		sort.Strings(fwPaths) // deterministic pieces.sha256 output, run to run
		for _, rel := range fwPaths {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), rel)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "pieces.sha256"), []byte(b.String()), 0o644)
}

// signPiecesHashes signs dir's already-written pieces.sha256 with the
// hex-encoded Ed25519 private key seed read from keyPath, writing the
// hex-encoded detached signature to pieces.sha256.sig (T81 step 1) --
// same writeFileAtomic reasoning as writePiecesHashes above: a process
// kill between create and the final write must never leave a
// syntactically-valid-but-truncated signature file for Resolve's
// hex.DecodeString to (correctly, but confusingly) reject later.
func signPiecesHashes(dir, keyPath string) error {
	keyHex, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", keyPath, err)
	}
	key, err := pieces.ParsePrivateKeyHex(strings.TrimSpace(string(keyHex)))
	if err != nil {
		return err
	}
	hashData, err := os.ReadFile(filepath.Join(dir, "pieces.sha256"))
	if err != nil {
		return err
	}
	sig := pieces.SignHashes(key, hashData)
	return writeFileAtomic(filepath.Join(dir, "pieces.sha256.sig"), []byte(sig+"\n"), 0o644)
}

// buildThunder compiles Thunder from its embedded source (see
// internal/assets.ThunderSrc) for the given arch, inside a throwaway
// golang container, and returns the resulting binary's bytes.
func buildThunder(ctx context.Context, arch, platform string) ([]byte, error) {
	fmt.Printf("compiling thunder for %s...\n", arch)

	srcDir, err := os.MkdirTemp("", "thunder-src-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(srcDir) }() // best-effort temp-dir cleanup
	// embed.FS keeps the full path from the package directory
	// (internal/assets/), i.e. "data/thunder-src/...", not just the
	// contents; fs.Sub gives back a view rooted at the actual tree.
	thunderSrc, err := fs.Sub(assets.ThunderSrc, "data/thunder-src")
	if err != nil {
		return nil, err
	}
	if err := writeEmbedFS(thunderSrc, srcDir); err != nil {
		return nil, fmt.Errorf("materializing thunder source: %w", err)
	}
	// go.mod is embedded as "go.mod.embed": go:embed refuses to embed a
	// directory containing a go.mod at all, treating it as crossing into
	// "a different module". Renaming it back here is the fix.
	if err := os.Rename(filepath.Join(srcDir, "go.mod.embed"), filepath.Join(srcDir, "go.mod")); err != nil {
		return nil, fmt.Errorf("restoring go.mod: %w", err)
	}

	err = dockerrun.Run(ctx, dockerrun.RunOptions{
		Image:    goBuilderImage,
		Platform: platform,
		Workdir:  "/src",
		Env: map[string]string{
			"CGO_ENABLED": "0",
			"GOOS":        "linux",
			"GOARCH":      arch,
			"GOCACHE":     "/tmp/gocache",
		},
		Name: fmt.Sprintf("cnimbus-prepare-thunder-%s-%d", arch, os.Getpid()),
		Mounts: []dockerrun.Mount{
			{HostPath: srcDir, ContainerPath: "/src"},
		},
		// -trimpath (T102): internal/assets/genagent already builds
		// cnimbusagent with -trimpath specifically so its output is
		// byte-reproducible (assets_agent_sync_test.go asserts this).
		// Thunder itself was missing it -- harmless today only because
		// /src is a fixed mount path every build uses, so the embedded
		// path happens to be stable; that stability was accidental, not
		// declared, and would silently break the moment the mount path
		// ever changed.
		Args: []string{"go", "build", "-trimpath", "-ldflags=-s -w", "-o", "/src/thunder", "./cmd/thunder"},
	})
	if err != nil {
		return nil, err
	}

	return os.ReadFile(filepath.Join(srcDir, "thunder"))
}

// writeEmbedFS writes every file in an embed.FS out to a real
// directory on disk, preserving its tree structure.
func writeEmbedFS(src fs.FS, destDir string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// materializeBuildContext writes the embedded Dockerfile, the
// freshly-built Thunder binary, and kconfig fragments out to a temp
// directory laid out the way the Dockerfile's COPY instructions
// expect, so `docker build` can use it as a normal build context.
func materializeBuildContext(thunderBinary []byte) (string, error) {
	dir, err := os.MkdirTemp("", "cnimbus-prepare-ctx-*")
	if err != nil {
		return "", err
	}

	files := map[string][]byte{
		"Dockerfile":                         assets.ForgeDockerfile,
		"agent/thunder":                      thunderBinary,
		"kconfig/minimal.fragment":           assets.KconfigMinimal,
		"kconfig/vm-amd64.fragment":          assets.KconfigVMAmd64,
		"kconfig/vm-arm64.fragment":          assets.KconfigVMArm64,
		"kconfig/vga.fragment":               assets.KconfigVGA,
		"kconfig/agent-vmware.fragment":      assets.KconfigAgentVMware,
		"kconfig/security-baseline.fragment": assets.KconfigSecurityBaseline,
		"kconfig/baremetal-eth.fragment":     assets.KconfigBaremetalEth,
		"kconfig/baremetal-usb.fragment":     assets.KconfigBaremetalUsb,
		"kconfig/baremetal-wifi.fragment":    assets.KconfigBaremetalWifi,
	}

	for rel, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			_ = os.RemoveAll(dir) // best-effort cleanup; the mkdir error above is what's returned
			return "", err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			_ = os.RemoveAll(dir) // best-effort cleanup; the write error above is what's returned
			return "", err
		}
	}
	return dir, nil
}
