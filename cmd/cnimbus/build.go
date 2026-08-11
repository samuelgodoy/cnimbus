package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cnimbus/internal/assets"
	"cnimbus/internal/isoimage"
	"cnimbus/internal/nimbusfile"
	"cnimbus/internal/pieces"
	"cnimbus/internal/rawimage"
	"cnimbus/internal/rootfs"
	"cnimbus/internal/secureboot"
)

// defaultSecurebootDir is where `cnimbus build-disk --secureboot` (with
// no explicit --secureboot-key/--secureboot-cert) auto-generates and
// then reuses its Secure Boot signing identity -- see
// internal/secureboot.LoadOrGenerate's doc comment for the "generate
// once, never silently regenerate" guarantee this relies on. Deliberately
// NOT inside --pieces (that directory is prepare's own output, fetched
// fresh from a URL by some callers -- a signing *private* key has no
// business living next to a publicly-fetched pieces set) and not inside
// the output image's own directory either (a throwaway --tmpdir or a
// one-off -o path shouldn't decide where a long-lived signing identity
// ends up) -- a fixed, cwd-relative default, the same kind of "no flag
// needed for the common case" default defaultPiecesDir already is.
const defaultSecurebootDir = "./secureboot"

func runBuild(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	nimbusfilePath := fs.String("f", "Nimbusfile", "Nimbusfile to read")
	outPath := fs.String("o", "", `output image path (default "<hostname>.iso")`)
	piecesSrc := fs.String("pieces", os.Getenv("CNIMBUS_PIECES"), "prebuilt pieces source: local dir or http(s):// URL prefix")
	arch := fs.String("arch", "", "target architecture: amd64 or arm64; overrides the Nimbusfile's ARCH")
	piecesInsecureHTTP := fs.Bool("pieces-insecure-http", false, "allow a plain http:// --pieces source "+
		"(refused by default -- unauthenticated HTTP lets anyone on the network path substitute an "+
		"arbitrary kernel/BusyBox into your image undetected)")
	piecesCacheDir := fs.String("pieces-cache-dir", defaultPiecesCacheDir(), "local cache directory for "+
		"http(s) --pieces sources, keyed by source+arch -- avoids re-downloading vmlinuz/busybox on a later "+
		"build when the source's pieces.sha256 hasn't changed (a fresh copy of that small file is still "+
		"fetched every time to detect exactly that)")
	noPiecesCache := fs.Bool("no-pieces-cache", false, "disable the pieces cache entirely, always re-downloading")
	piecesAllowUnverified := fs.Bool("pieces-allow-unverified", false, "for an http(s) --pieces source with no "+
		"pieces.sha256 published, build from it anyway with a warning instead of failing outright (has no "+
		"effect on a local directory source, or on a source that does publish one -- a hash mismatch there "+
		"is always a hard error)")
	piecesVerifyKey := fs.String("pieces-verify-key", "", "an Ed25519 public key, hex-encoded (see "+
		"\"cnimbus keygen\"); if set (here or via a Nimbusfile PIECESKEY line -- this flag wins if both are "+
		"given), refuses to build unless the pieces source published a pieces.sha256.sig that verifies "+
		"against it (T81 step 1: authenticity, not just the hash check above)")
	buildArgs := buildArgFlag{}
	fs.Var(buildArgs, "build-arg", "set a value for an ARG directive (NAME=VALUE); repeatable")
	noLockfile := fs.Bool("no-lockfile", false, "skip writing <output>.lock (records resolved pieces/image "+
		"hashes for this specific build -- useful given \"KERNEL latest-stable\" means the same Nimbusfile "+
		"can produce a different image on a different day)")
	tmpDir := fs.String("tmpdir", "", "directory for the build's temporary workspace files (default: the "+
		"output image's own directory) -- override this if that directory isn't writable/large enough; "+
		"the default deliberately avoids the OS temp dir (small system drive on many Windows machines) "+
		"when the output path names a different, presumably large-enough disk")
	securebootFlag := fs.Bool("secureboot", false, "sign the shipped EFI-stub kernel with a pure-Go Authenticode "+
		"implementation (F2/AD-042), so a Secure Boot-enabled firmware whose db carries the matching "+
		"certificate will load it (and refuse anything else). No Docker, no external tool of any kind -- "+
		"pure Go, same as the rest of build-disk (see internal/secureboot) -- opt-in, same as --uefi/HARDBOOT: "+
		"never silently added to a build that didn't ask for it. Off by default")
	ukiFlag := fs.Bool("uki", false, "assemble a signed Unified Kernel Image (kernel+initramfs+cmdline merged "+
		"into one PE binary, systemd-stub style) instead of shipping the kernel and initramfs as two "+
		"separate EFI-boot-image files. Implies --secureboot. No Docker needed (pure Go, see internal/secureboot)")
	securebootKey := fs.String("secureboot-key", "", "path to a PEM-encoded RSA private key for --secureboot/"+
		"--uki signing (bring-your-own-certificate; mirrors --ovmf-code/--ovmf-vars on \"cnimbus run\"). "+
		"Must be given together with --secureboot-cert. If neither is given, a keypair is auto-generated "+
		"once under --secureboot-dir and reused on every later build (never silently regenerated)")
	securebootCert := fs.String("secureboot-cert", "", "path to the PEM-encoded X.509 certificate matching "+
		"--secureboot-key (the file to enroll into a hypervisor's/board's UEFI db)")
	securebootDir := fs.String("secureboot-dir", defaultSecurebootDir, "directory to auto-generate/reuse the "+
		"default Secure Boot signing keypair in, when --secureboot-key/--secureboot-cert are not given "+
		"(see \"cnimbus keygen --secureboot\" to pre-generate one explicitly instead)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ukiFlag {
		*securebootFlag = true
	}
	if (*securebootKey == "") != (*securebootCert == "") {
		return fmt.Errorf("--secureboot-key and --secureboot-cert must be given together (got only one)")
	}

	if *piecesSrc == "" {
		// Same default `prepare --out` writes to: if it's there, use it
		// with no flag needed at all -- `cnimbus prepare && cnimbus init &&
		// cnimbus build-disk` just works.
		if info, err := os.Stat(defaultPiecesDir); err == nil && info.IsDir() {
			*piecesSrc = defaultPiecesDir
		}
	}
	if *piecesSrc == "" {
		return fmt.Errorf(`no pieces source given: pass --pieces <dir-or-url> or set CNIMBUS_PIECES
Pieces (vmlinuz, busybox, busybox-manifest.tsv) are produced by "cnimbus prepare"; point this at
wherever you published them (run "cnimbus prepare" first if you haven't)`)
	}

	hf, err := nimbusfile.Parse(*nimbusfilePath, buildArgs)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", *nimbusfilePath, err)
	}
	// Same override rule `prepare` uses: the Nimbusfile declares ARCH, an
	// explicitly-passed --arch wins, so both commands can be pointed at a
	// different architecture without editing the file.
	if *arch != "" {
		if *arch != "amd64" && *arch != "arm64" {
			return fmt.Errorf("--arch must be \"amd64\" or \"arm64\", got %q", *arch)
		}
		hf.Arch = *arch
	}

	ext := ".iso"
	switch hf.Format {
	case "raw":
		ext = ".img"
	case "vhd":
		ext = ".vhd"
	}
	out := *outPath
	if out == "" {
		out = hf.Hostname + ext
	}
	// T79: default the build workspace to the output image's own
	// directory rather than the OS temp dir -- on Windows that's almost
	// always the (often small) system drive regardless of where -o
	// points, so a large image build could ENOSPC on a drive the user
	// never intended to write to, after doing all the work.
	effectiveTmpDir := *tmpDir
	if effectiveTmpDir == "" {
		effectiveTmpDir = filepath.Dir(out) // "." for a bare filename -- a valid, and the correct, workspace dir
	}

	// Same override rule as ARCH above: the Nimbusfile declares PIECESKEY,
	// an explicitly-passed --pieces-verify-key wins.
	verifyKeyHex := hf.PiecesKey
	if *piecesVerifyKey != "" {
		verifyKeyHex = *piecesVerifyKey
	}
	var verifyKey ed25519.PublicKey
	if verifyKeyHex != "" {
		verifyKey, err = pieces.ParsePublicKeyHex(verifyKeyHex)
		if err != nil {
			return fmt.Errorf("--pieces-verify-key/PIECESKEY: %w", err)
		}
	}

	fmt.Printf("fetching %s pieces from %s\n", hf.Arch, *piecesSrc)
	cacheDir := *piecesCacheDir
	if *noPiecesCache {
		cacheDir = ""
	}
	set, err := pieces.Resolve(*piecesSrc, hf.Arch, pieces.ResolveOptions{
		AllowInsecureHTTP:     *piecesInsecureHTTP,
		CacheDir:              cacheDir,
		RequireVerifiedPieces: !*piecesAllowUnverified,
		VerifyKey:             verifyKey,
	})
	if err != nil {
		return fmt.Errorf("resolving pieces: %w", err)
	}
	if set.HashesVerified {
		fmt.Println("pieces.sha256 found: vmlinuz/busybox/manifest integrity verified")
		if verifyKey != nil {
			fmt.Println("pieces.sha256.sig verified against --pieces-verify-key: pieces authenticity confirmed")
		}
	} else {
		fmt.Println("warning: no pieces.sha256 found at the pieces source -- integrity unverified " +
			"(re-run \"cnimbus prepare\" to produce one, or publish it alongside older pieces)")
	}
	// T59: without this, a `prepare`-time setting with no other trace
	// anywhere (VGA chief among them) could silently mismatch the
	// Nimbusfile being assembled against these pieces -- e.g. pieces
	// built without --vga against a Nimbusfile declaring "VGA true"
	// assembles an ISO whose cmdline says console=tty0 against a kernel
	// with no framebuffer console compiled in: a permanently black
	// window in VirtualBox, with nothing in the build output or the
	// image saying why. No pieces.json at all (an older `prepare`
	// output) skips this check entirely, same tolerance as
	// HashesVerified above.
	if set.Provenance != nil {
		if set.Provenance.Arch != "" && set.Provenance.Arch != hf.Arch {
			return fmt.Errorf("pieces were built for ARCH %s but this Nimbusfile declares ARCH %s -- "+
				"re-run \"cnimbus prepare --arch %s\"", set.Provenance.Arch, hf.Arch, hf.Arch)
		}
		if set.Provenance.VGA != hf.VGA {
			return fmt.Errorf("pieces were built with VGA=%t but this Nimbusfile declares VGA %t -- "+
				"re-run \"cnimbus prepare --vga=%t\"", set.Provenance.VGA, hf.VGA, hf.VGA)
		}
		// BootProfile follows the same reasoning as Arch/VGA above (F6.2):
		// pieces built for the wrong HARDBOOT profile are missing (or, for
		// "none" pieces built against a "wifi" Nimbusfile, entirely lacking)
		// the driver support the image now expects. An absent field (pieces
		// built before HARDBOOT existed) is normalized to "none" -- that's
		// what every such pieces set actually is.
		effectiveBootProfile := set.Provenance.BootProfile
		if effectiveBootProfile == "" {
			effectiveBootProfile = "none"
		}
		if effectiveBootProfile != hf.BootProfile {
			return fmt.Errorf("pieces were built with HARDBOOT %s but this Nimbusfile declares HARDBOOT %s -- "+
				"re-run \"cnimbus prepare --hardboot=%s\"", effectiveBootProfile, hf.BootProfile, hf.BootProfile)
		}
	}

	extraFiles, err := resolveCopies(hf)
	if err != nil {
		return err
	}
	extraFiles = append(extraFiles, releaseFile(hf))

	applets := make([]rootfs.BusyboxApplet, len(set.BusyboxApplets))
	for i, a := range set.BusyboxApplets {
		applets[i] = rootfs.BusyboxApplet{Path: a.Path, Target: a.Target}
	}

	fmt.Println("assembling stage-1 initramfs + squashfs root")
	images, err := rootfs.BuildImages(rootfs.PiecesSpec{
		BusyboxBinary:    set.BusyboxBinary,
		BusyboxApplets:   applets,
		Hostname:         hf.Hostname,
		DHCP:             hf.DHCP,
		StaticIP:         staticIP(hf),
		DNS:              hf.DNS,
		NTP:              hf.NTP,
		Volumes:          volumes(hf),
		Firewall:         hf.Firewall,
		Firewall6:        hf.Firewall6,
		FirewallOnError:  hf.FirewallOnError,
		Iptables:         set.Iptables,
		Supplicant:       set.Supplicant,
		WifiFirmware:     set.WifiFirmware,
		EthernetFirmware: set.EthernetFirmware,
		BootProfile:      hf.BootProfile,
		WifiSSID:         hf.WiFiSSID,
		WifiPSK:          hf.WiFiPSK,
		WifiCountry:      hf.WiFiCountry,
		Env:              envVars(hf),
		User:             hf.User,
		ExtraFiles:       extraFiles,
		Services:         services(hf),
		Agent:            agent(hf),
		Workdir:          hf.Workdir,
		Healthcheck:      healthcheck(hf),
		TmpfsSize:        hf.TmpfsSize,
		TmpDir:           effectiveTmpDir,
		StopGrace:        hf.StopGrace,
		VGA:              hf.VGA,
	})
	if err != nil {
		return fmt.Errorf("building images: %w", err)
	}
	// SquashfsRootPath (T75) is a temp file BuildImages created and
	// handed ownership of to this caller -- removed once isoimage.Write/
	// rawimage.Write below have streamed it into the final image,
	// regardless of whether that succeeds.
	defer func() { _ = os.Remove(images.SquashfsRootPath) }()

	// effectiveVmlinuz is what actually lands as /EFI/BOOT/BOOTX64.EFI(or
	// AA64)/-- either set.Vmlinuz unchanged (the common case), a
	// sbsign-signed copy of it (--secureboot), or a signed Unified Kernel
	// Image merging it with the initramfs BuildImages just produced
	// (--uki). See applySecureboot's own doc comment for the exact
	// signing/assembly steps and why --uki signs the *assembled* UKI as
	// one PE rather than signing vmlinuz first and merging afterward
	// (merging via objcopy after signing would invalidate that
	// signature -- a signature covers exact bytes, and objcopy's own
	// --add-section necessarily changes them).
	effectiveVmlinuz := set.Vmlinuz
	if *securebootFlag {
		effectiveVmlinuz, err = applySecureboot(ctx, set.Vmlinuz, images.Stage1, *ukiFlag, *securebootKey, *securebootCert, *securebootDir)
		if err != nil {
			return fmt.Errorf("--secureboot: %w", err)
		}
	}

	// Written to out+".partial" first, renamed over the real name only on
	// success (T60): isoimage.Write/rawimage.Write take several seconds
	// to a few minutes for a real image, and an interrupt or ENOSPC partway
	// through previously left a partial, invalid image at exactly the path
	// a user would next try to boot -- indistinguishable, from the
	// filename alone, from a successful build.
	fmt.Printf("writing %s\n", out)
	partial := out + ".partial"
	switch hf.Format {
	case "raw":
		err = rawimage.Write(partial, rawimage.Image{
			Arch:            hf.Arch,
			Vmlinuz:         effectiveVmlinuz,
			InitramfsImg:    images.Stage1,
			SquashfsImgPath: images.SquashfsRootPath,
		})
	case "vhd":
		// A VHD is a raw GPT disk (the exact same layout FORMAT raw
		// produces) with a Fixed-VHD footer appended (see run_vhd.go,
		// written for --backend hyperv's own on-the-fly wrapping of a
		// FORMAT raw image -- reused here verbatim so FORMAT vhd needs no
		// separate assembly logic, just the same raw bytes plus the same
		// footer). Built as a temporary raw image first, then wrapped;
		// build-disk has zero external dependencies either way, this is
		// pure Go start to finish.
		rawTmp := partial + ".rawtmp"
		if err = rawimage.Write(rawTmp, rawimage.Image{
			Arch:            hf.Arch,
			Vmlinuz:         effectiveVmlinuz,
			InitramfsImg:    images.Stage1,
			SquashfsImgPath: images.SquashfsRootPath,
		}); err == nil {
			err = writeFixedVHD(partial, rawTmp)
		}
		_ = os.Remove(rawTmp) // best-effort; the raw image is a temp file regardless of outcome
	default:
		err = isoimage.Write(partial, isoimage.Image{
			VolumeLabel:     strings.ToUpper(hf.Hostname),
			Arch:            hf.Arch,
			IsolinuxBin:     assets.IsolinuxBin,
			LdlinuxC32:      assets.LdlinuxC32,
			IsolinuxCfg:     []byte(isolinuxCfg(hf.Hostname)),
			Vmlinuz:         effectiveVmlinuz,
			InitramfsImg:    images.Stage1,
			SquashfsImgPath: images.SquashfsRootPath,
			Metadata:        []byte(cnimbusMetadataCfg(hf)),
			TmpDir:          effectiveTmpDir,
		})
	}
	if errors.Is(err, isoimage.ErrEFIPayloadTooLarge) {
		// T77: isoimage itself only ever sees the already-assembled
		// bytes, so its own error message can name El Torito and the
		// sector-count ceiling but not the Nimbusfile line that actually
		// caused it. Here, extraFiles (the COPY/ADD destinations) and
		// which of them landed in stage 1's tmpfs (rootfs.IsShadowedPath)
		// are still known -- naming the largest ones turns "the
		// kernel+initramfs are too large" into an actionable message
		// instead of a dead end.
		err = fmt.Errorf("%w\n%s", err, describeOversizedShadowedCopies(extraFiles))
	}
	if err != nil {
		_ = os.Remove(partial) // best-effort; the write error above is what's returned
		return fmt.Errorf("writing image: %w", err)
	}
	if err := os.Rename(partial, out); err != nil {
		_ = os.Remove(partial) // best-effort; the rename error above is what's returned
		return fmt.Errorf("renaming %s to %s: %w", partial, out, err)
	}

	if !*noLockfile {
		nimbusfileData, err := os.ReadFile(*nimbusfilePath)
		if err != nil {
			return fmt.Errorf("reading %s for the lockfile: %w", *nimbusfilePath, err)
		}
		outSHA256, err := sha256File(out)
		if err != nil {
			return fmt.Errorf("hashing %s for the lockfile: %w", out, err)
		}
		lock := BuildLock{
			CnimbusVersion:       version,
			BuiltAt:              nowRFC3339(),
			Nimbusfile:           *nimbusfilePath,
			NimbusfileSHA256:     sha256Hex(nimbusfileData),
			Arch:                 hf.Arch,
			Format:               hf.Format,
			PiecesSource:         *piecesSrc,
			VmlinuzSHA256:        sha256Hex(effectiveVmlinuz), // the bytes actually shipped -- signed/UKI-merged when --secureboot/--uki is used
			BusyboxSHA256:        sha256Hex(set.BusyboxBinary),
			ManifestSHA256:       sha256Hex(set.BusyboxManifest),
			PiecesHashesVerified: set.HashesVerified,
			OutputImage:          out,
			OutputImageSHA256:    outSHA256,
		}
		if set.Iptables != nil {
			lock.IptablesSHA256 = sha256Hex(set.Iptables)
		}
		if set.Supplicant != nil {
			lock.SupplicantSHA256 = sha256Hex(set.Supplicant)
		}
		if a := agent(hf); a != nil {
			lock.AgentSHA256 = sha256Hex(a.AgentBinary)
		}
		lockPath := buildLockfilePath(out)
		if err := writeLockfile(lockPath, lock); err != nil {
			return fmt.Errorf("writing %s: %w", lockPath, err)
		}
		fmt.Printf("wrote %s\n", lockPath)
	}

	fmt.Printf("done: %s\n", out)
	return nil
}

// cnimbusMetadataCfg (AD-050) builds CNIMBUS.CFG's content: a small,
// plain-text identity manifest written to the ISO9660 tree's top
// level, readable before ever mounting SQUASHFS.IMG. Motivated by a
// real multiboot-USB (Ventoy) boot where the generic boot-media scan
// (internal/rootfs/stage1.go) found more than one candidate .iso and
// had no way to identify which one it had actually landed on -- this
// gives it (and a human inspecting the stick's contents directly) a
// name to go by. hf.Hostname is the same string already used as the
// ISO's own VolumeLabel; this is a fuller, structured version of the
// same identity, not a second, independently-maintained one.
func cnimbusMetadataCfg(hf *nimbusfile.Nimbusfile) string {
	return fmt.Sprintf("HOSTNAME=%s\nARCH=%s\nFORMAT=%s\nCNIMBUS_VERSION=%s\n",
		hf.Hostname, hf.Arch, hf.Format, version)
}

// isolinuxCfg builds the BIOS boot menu. label is the Nimbusfile's
// HOSTNAME, shown as the boot entry's cosmetic "MENU LABEL" -- isolinux
// itself ignores MENU LABEL unless the image also carries menu.c32 (it
// doesn't; this project's syslinux payload is isolinux.bin+ldlinux.c32
// only), so this is purely informational for anyone inspecting
// ISOLINUX.CFG by hand, not something that changes boot behavior.
func isolinuxCfg(label string) string {
	// KERNEL/initrd= point at /EFI/BOOT/ (T78), not a separate /BOOT/
	// copy: isolinux loads a bzImage regardless of its filename, and the
	// EFI-stub kernel *is* that same bzImage, so no second copy is
	// needed just to give BIOS boot its own path to read from. This
	// removes one of the three copies of the kernel/initramfs the ISO
	// used to carry (the EFIBOOT.IMG copies stay -- El Torito's EFI
	// entry loads that FAT image directly, and firmware/tools that read
	// the ISO9660 tree themselves for Ventoy-style direct boot still
	// need /EFI/BOOT/ present).
	return "DEFAULT cnimbus\n" +
		"PROMPT 0\n" +
		"TIMEOUT 1\n" +
		"NOESCAPE 1\n" +
		"ALLOWOPTIONS 0\n" +
		"LABEL cnimbus\n" +
		"  MENU LABEL " + label + "\n" +
		"  KERNEL /EFI/BOOT/BOOTX64.EFI\n" +
		"  APPEND initrd=/EFI/BOOT/INITRD.IMG console=tty0 console=ttyS0,115200n8 " +
		"panic=10 oops=panic " + bootQuirks + "\n"
}

// bootQuirks are cmdline parameters working around a specific
// hypervisor's own bug, harmless (and unparsed) everywhere else --
// same one-kernel-boots-everywhere reasoning as the CONFIG_HYPERV*
// kconfig additions.
//
//   - no-vmw-sched-clock: verified empirically against a real VMware
//     Player VM that this kernel's CONFIG_HYPERVISOR_GUEST=y (added for
//     Hyper-V support -- see vm-amd64.fragment) has an unwanted side
//     effect on VMware specifically: arch/x86/kernel/cpu/vmware.o is
//     unconditionally bundled into the same build object as Hyper-V's
//     own detection code (both gated by the one CONFIG_HYPERVISOR_GUEST
//     symbol -- Kbuild has no finer-grained switch between them), and
//     once it detects a real VMware host it installs its own
//     paravirtualized sched_clock(). Boot got to "eth0 NIC Link is Up"
//     and then hung forever -- no further kernel or userspace output at
//     all, consistent with sched_clock() (which nearly every
//     timer/scheduling operation depends on, including udhcpc's own
//     request timeout) never returning a sane value on this specific
//     VMware Player build.
//     A blanket "nopv" was considered and rejected: neither vmware.c
//     nor mshyperv.c set the `ignore_nopv` flag hypervisor.c checks, so
//     "nopv" would disable Hyper-V's own detection right along with
//     VMware's -- undoing the fix vm-amd64.fragment's CONFIG_HYPERV*
//     block exists for. "no-vmw-sched-clock" is the one parameter
//     narrow enough to hit only the specific hook suspected here,
//     parsed by vmware.c alone and inert everywhere else.
const bootQuirks = "no-vmw-sched-clock"

// services builds the full list of supervised, respawned processes:
// the Nimbusfile's ENTRYPOINT/CMD first (named "entrypoint", matching
// Docker's own semantics -- with ENTRYPOINT set, CMD supplies its
// default arguments; with only CMD set, CMD is the whole command),
// then one per SERVICE directive.
func services(hf *nimbusfile.Nimbusfile) []rootfs.Service {
	var svcs []rootfs.Service

	var argv []string
	switch {
	case len(hf.Entrypoint) > 0:
		argv = append(argv, hf.Entrypoint...)
		argv = append(argv, hf.Cmd...)
	case len(hf.Cmd) > 0:
		argv = hf.Cmd
	}
	if len(argv) > 0 {
		svcs = append(svcs, rootfs.Service{Name: "entrypoint", Argv: argv, Restart: hf.EntrypointRestart})
	}

	for _, s := range hf.Services {
		svcs = append(svcs, rootfs.Service{Name: s.Name, Argv: s.Argv, Restart: s.Restart})
	}
	return svcs
}

func healthcheck(hf *nimbusfile.Nimbusfile) *rootfs.Healthcheck {
	if hf.Healthcheck == nil {
		return nil
	}
	return &rootfs.Healthcheck{
		Argv:     hf.Healthcheck.Argv,
		Interval: hf.Healthcheck.Interval,
		Retries:  hf.Healthcheck.Retries,
	}
}

// describeOversizedShadowedCopies is T77's fix: when isoimage.Write
// fails with ErrEFIPayloadTooLarge, the raw El Torito error names the
// sector-count ceiling but has no visibility into which Nimbusfile
// COPY/ADD line actually grew stage 1's initramfs past it (isoimage only
// ever sees the already-assembled bytes). extraFiles and
// rootfs.IsShadowedPath are still known here, so this names the largest
// shadow-destined files (stage 1's four tmpfs exec dirs -- see
// stage1.go) and suggests the two real ways out: FORMAT raw (no El
// Torito ceiling at all) or moving the file outside those four
// directories (only files destined for bin/sbin/usr/bin/usr/sbin ever
// travel through stage 1 in the first place).
func describeOversizedShadowedCopies(extraFiles []rootfs.ExtraFile) string {
	type shadowed struct {
		path string
		size int
	}
	var files []shadowed
	for _, f := range extraFiles {
		if rootfs.IsShadowedPath(f.Path) {
			files = append(files, shadowed{f.Path, len(f.Data)})
		}
	}
	if len(files) == 0 {
		return "no COPY/ADD destinations landed in stage 1's tmpfs (bin/sbin/usr/bin/usr/sbin) -- " +
			"the kernel and/or BusyBox pieces themselves are what grew past the limit here"
	}
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
	var b strings.Builder
	b.WriteString("largest COPY/ADD destinations shadowed into stage 1's tmpfs " +
		"(bin/sbin/usr/bin/usr/sbin -- these are what pushed the EFI boot image past the limit):\n")
	for i, f := range files {
		if i >= 5 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(files)-5)
			break
		}
		fmt.Fprintf(&b, "  %s (%d bytes)\n", f.path, f.size)
	}
	b.WriteString("fix: either \"FORMAT raw\" (no El Torito size ceiling at all), " +
		"or move the file(s) above outside bin/sbin/usr/bin/usr/sbin")
	return b.String()
}

// releaseFile builds /etc/cnimbus-release: cnimbus's own build version,
// the Nimbusfile's declared ARCH, and every LABEL/EXPOSE the Nimbusfile
// set -- a simple os-release-style KEY=VALUE file, useful for debugging
// a running image in the field ("what build is this, actually?") when
// there's no shell to ask from inside it.
func releaseFile(hf *nimbusfile.Nimbusfile) rootfs.ExtraFile {
	var b strings.Builder
	fmt.Fprintf(&b, "CNIMBUS_VERSION=%s\n", version)
	fmt.Fprintf(&b, "HOSTNAME=%s\n", hf.Hostname)
	fmt.Fprintf(&b, "ARCH=%s\n", hf.Arch)
	fmt.Fprintf(&b, "FORMAT=%s\n", hf.Format)
	for _, l := range hf.Labels {
		fmt.Fprintf(&b, "%s=%s\n", l.Key, l.Value)
	}
	for _, e := range hf.Exposed {
		fmt.Fprintf(&b, "EXPOSE=%d/%s\n", e.Port, e.Proto)
	}
	return rootfs.ExtraFile{Path: "etc/cnimbus-release", Perm: 0o644, Data: []byte(b.String())}
}

func staticIP(hf *nimbusfile.Nimbusfile) *rootfs.StaticIP {
	if hf.StaticIP == nil {
		return nil
	}
	return &rootfs.StaticIP{
		Address: hf.StaticIP.Address,
		Netmask: hf.StaticIP.Netmask,
		Gateway: hf.StaticIP.Gateway,
	}
}

func volumes(hf *nimbusfile.Nimbusfile) []rootfs.Volume {
	vols := make([]rootfs.Volume, len(hf.Volumes))
	for i, v := range hf.Volumes {
		vols[i] = rootfs.Volume{Device: v.Device, Mountpoint: v.Mountpoint, FSType: v.FSType, Required: v.Required}
	}
	return vols
}

func agent(hf *nimbusfile.Nimbusfile) *rootfs.Agent {
	if hf.Agent == nil {
		return nil
	}
	a := &rootfs.Agent{Kind: hf.Agent.Kind, URL: hf.Agent.URL, Interval: hf.Agent.Interval}
	for _, h := range hf.Agent.Headers {
		a.Headers = append(a.Headers, rootfs.AgentHeader{Name: h.Name, Value: h.Value})
	}
	// Every kind uses the same cnimbusagent binary now (see
	// internal/rootfs's buildInittab) -- only the architecture varies.
	if hf.Arch == "arm64" {
		a.AgentBinary = assets.CnimbusAgentArm64
	} else {
		a.AgentBinary = assets.CnimbusAgentAmd64
	}
	return a
}

// defaultPiecesCacheDir returns the OS-conventional per-user cache
// directory (e.g. %LocalAppData%\cnimbus\pieces on Windows,
// ~/.cache/cnimbus/pieces on Linux) or "" if it can't be determined
// (some minimal/sandboxed environments have no such directory) --
// caching is simply skipped in that case, exactly like --no-pieces-cache.
func defaultPiecesCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "cnimbus", "pieces")
}

func envVars(hf *nimbusfile.Nimbusfile) []rootfs.EnvVar {
	vars := make([]rootfs.EnvVar, len(hf.Env))
	for i, e := range hf.Env {
		vars[i] = rootfs.EnvVar{Key: e.Key, Value: e.Value}
	}
	return vars
}

// resolveCopies materializes every COPY/ADD directive into an
// in-memory ExtraFile, relative to the Nimbusfile's own directory.
// Local (non-URL, non-tarball) sources support three shapes, matching
// Docker's own COPY/ADD semantics: a single file (dest is the exact
// destination path), a directory (its *contents* are copied under
// dest, not the directory itself), or a glob (each match copied under
// dest, which must then behave as a directory).
func resolveCopies(hf *nimbusfile.Nimbusfile) ([]rootfs.ExtraFile, error) {
	var files []rootfs.ExtraFile

	for _, c := range hf.Copies {
		destSlash := filepath.ToSlash(c.Dest)
		destIsDir := strings.HasSuffix(destSlash, "/")
		dest := strings.TrimSuffix(strings.TrimPrefix(destSlash, "/"), "/")
		// Not derived from the host file's own permission bits by
		// default: on Windows, os.FileMode never reports a real POSIX
		// execute bit (NTFS has none), so that check always came back
		// "not executable" and every COPY'd binary landed unrunnable in
		// the image. Files placed by COPY/ADD in this tool are
		// overwhelmingly binaries meant to run at boot, and a stray
		// execute bit on a plain config file is harmless -- --chmod
		// overrides this when a specific mode actually matters.
		perm := uint32(0o755)
		if c.Chmod != 0 {
			perm = c.Chmod
		}

		switch {
		case c.IsURL:
			if !c.IsAdd {
				return nil, fmt.Errorf("COPY does not support URL sources (%s); use ADD", c.Src)
			}
			data, err := fetchURL(c.Src)
			if err != nil {
				return nil, err
			}
			files = append(files, rootfs.ExtraFile{Path: dest, Perm: perm, Data: data})

		case c.IsAdd && isTarball(c.Src):
			srcPath := filepath.Join(hf.BaseDir, c.Src)
			entries, err := extractLocalTarball(srcPath, dest)
			if err != nil {
				return nil, fmt.Errorf("ADD %s: %w", c.Src, err)
			}
			if c.Chmod != 0 {
				for i := range entries {
					entries[i].Perm = c.Chmod
				}
			}
			files = append(files, entries...)

		default:
			entries, err := resolveLocalCopy(hf.BaseDir, c.Src, dest, perm, destIsDir)
			if err != nil {
				return nil, err
			}
			files = append(files, entries...)
		}
	}
	return files, nil
}

// resolveLocalCopy expands src (a plain path or a glob pattern,
// relative to baseDir) into ExtraFiles under dest.
func resolveLocalCopy(baseDir, src, dest string, perm uint32, destIsDir bool) ([]rootfs.ExtraFile, error) {
	pattern := filepath.Join(baseDir, src)
	var matches []string
	if strings.ContainsAny(src, "*?[") {
		var err error
		matches, err = filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid glob pattern: %w", src, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: no files matched this glob pattern", src)
		}
	} else {
		matches = []string{pattern}
	}

	// A glob expanding to more than one entry, or a dest the Nimbusfile
	// itself wrote with a trailing slash, forces dest to behave as a
	// directory prefix -- the same rule Docker's own COPY/ADD apply.
	asDirPrefix := len(matches) > 1 || destIsDir

	var files []rootfs.ExtraFile
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		if info.IsDir() {
			entries, err := walkLocalDir(m, dest, perm)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", src, err)
			}
			files = append(files, entries...)
			continue
		}
		filePath := dest
		if asDirPrefix {
			filePath = dest + "/" + filepath.Base(m)
		}
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		files = append(files, rootfs.ExtraFile{Path: filePath, Perm: perm, Data: data})
	}
	return files, nil
}

// walkLocalDir copies dir's *contents* (not the directory itself) into
// dest, recursively, matching Docker's own "COPY a-directory dest"
// semantics -- "COPY ./dist/ /app/" lands ./dist/index.html at
// /app/index.html, not /app/dist/index.html.
func walkLocalDir(dir, dest string, perm uint32) ([]rootfs.ExtraFile, error) {
	var files []rootfs.ExtraFile
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, rootfs.ExtraFile{
			Path: dest + "/" + filepath.ToSlash(rel),
			Perm: perm,
			Data: data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func isTarball(name string) bool {
	return strings.HasSuffix(name, ".tar") ||
		strings.HasSuffix(name, ".tar.gz") ||
		strings.HasSuffix(name, ".tgz")
}

func fetchURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: HTTP %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// safeImagePath joins destPrefix and a tar entry's name into the image
// path it lands at, rejecting anything (e.g. "../../etc/passwd" in the
// tar entry's own name) that would resolve outside destPrefix inside the
// image -- image paths are always "/"-separated regardless of the host
// OS, so this uses path.Clean, not filepath.Clean.
func safeImagePath(destPrefix, name string) (string, error) {
	joined := path.Clean(destPrefix + "/" + name)
	prefix := path.Clean(destPrefix)
	if joined != prefix && !strings.HasPrefix(joined, prefix+"/") {
		return "", fmt.Errorf("escapes destination %q", destPrefix)
	}
	return joined, nil
}

func extractLocalTarball(path, destPrefix string) ([]rootfs.ExtraFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") || strings.HasSuffix(path, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}

	var files []rootfs.ExtraFile
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			// Directories need no ExtraFile of their own (the SquashFS/
			// tmpfs writers create parent directories automatically); a
			// symlink or hardlink, though, quietly disappearing from the
			// extracted tree is the kind of thing worth a loud warning --
			// there's no ExtraFile representation for either today (see
			// ROADMAP.md's SquashFS symlink gap), so it's dropped, not
			// silently ignored.
			if hdr.Typeflag != tar.TypeDir {
				fmt.Fprintf(os.Stderr, "cnimbus: warning: ADD %s: skipping non-regular tar entry %q (type %q) -- not representable in a cnimbus image today\n",
					path, hdr.Name, string(hdr.Typeflag))
			}
			continue
		}
		imgPath, err := safeImagePath(destPrefix, hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("ADD %s: tar entry %q: %w", path, hdr.Name, err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files = append(files, rootfs.ExtraFile{
			Path: imgPath,
			Perm: uint32(hdr.Mode & 0o777),
			Data: data,
		})
	}
}

// applySecureboot is F2's signing/UKI step, called only when
// --secureboot (or --uki, which implies it) was actually passed --
// never unconditionally, mirroring how --uefi/HARDBOOT never silently
// change a build that didn't ask for them. Resolves the signing
// keypair (explicit --secureboot-key/--secureboot-cert if both are
// given, "bring your own certificate" -- otherwise
// secureboot.LoadOrGenerate against securebootDir, auto-generating
// once and reusing thereafter), then either:
//
//   - uki=false: signs vmlinuz directly with a pure-Go Authenticode
//     implementation (AD-042 -- no Docker, no sbsign). A real bzImage/
//     Image built with CONFIG_EFI_STUB=y (see minimal.fragment) is
//     itself a valid PE32+ EFI application, so no UKI assembly is
//     needed just to make it Secure-Boot-signable -- this is the
//     "EFI-stub kernel signing" half of F2 on its own.
//   - uki=true: assembles a Unified Kernel Image from vmlinuz+
//     initramfs (kernel cmdline section left empty -- see
//     internal/secureboot.BuildAndSignUKI's own doc comment for why:
//     this project's CONFIG_CMDLINE_OVERRIDE=y, set in
//     vm-amd64.fragment/vm-arm64.fragment, makes the kernel's own
//     compiled-in CONFIG_CMDLINE win over any LoadOptions/.cmdline-
//     section value regardless, so a populated .cmdline section here
//     would be spec-compliant but inert for this kernel config
//     specifically) and signs the *assembled* UKI as one PE -- signing
//     vmlinuz first and merging the UKI's extra sections in afterward
//     would invalidate that signature, since it covers exact bytes and
//     appending a section necessarily changes them.
//
// Returns the bytes that should replace set.Vmlinuz as this build's
// isoimage.Image.Vmlinuz/rawimage.Image.Vmlinuz field -- the caller
// (runBuild) doesn't otherwise need to know signing happened at all.
func applySecureboot(ctx context.Context, vmlinuz, initramfs []byte, uki bool, keyPath, certPath, securebootDir string) ([]byte, error) {
	var kp secureboot.Keypair
	if keyPath != "" && certPath != "" {
		var err error
		kp, err = secureboot.Load(keyPath, certPath)
		if err != nil {
			return nil, err
		}
		fmt.Printf("secureboot: using --secureboot-key %s / --secureboot-cert %s\n", keyPath, certPath)
	} else {
		var generated bool
		var err error
		kp, generated, err = secureboot.LoadOrGenerate(securebootDir, "cnimbus")
		if err != nil {
			return nil, err
		}
		if generated {
			fmt.Printf("secureboot: no --secureboot-key/--secureboot-cert given -- generated a new RSA-3072 "+
				"signing identity under %s (reused on every later build; enroll %s/%s into your hypervisor's "+
				"UEFI db -- see \"cnimbus keygen --secureboot --out-dir\" to pre-generate one explicitly instead)\n",
				securebootDir, securebootDir, secureboot.CertPEMName)
		} else {
			fmt.Printf("secureboot: reusing existing signing identity from %s\n", securebootDir)
		}
	}

	if uki {
		fmt.Println("secureboot: assembling + signing a Unified Kernel Image (pure Go, no Docker -- AD-042)")
		return secureboot.BuildAndSignUKI(ctx, vmlinuz, initramfs, "", kp)
	}
	fmt.Println("secureboot: signing the EFI-stub kernel (pure Go Authenticode, no Docker -- AD-042)")
	return secureboot.SignPE(ctx, vmlinuz, kp)
}
