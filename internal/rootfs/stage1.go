package rootfs

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
)

// Stage 1 is the real Linux initramfs: a small gzip'd cpio the kernel
// unpacks straight into RAM (CONFIG_BLK_DEV_INITRD), exactly like this
// project's images worked before SquashFS existed. Its only job is to
// get to the real root and hand off:
//
//  1. mount proc/sys/devtmpfs
//  2. find and mount the boot media (the ISO's own CD-ROM device, or a
//     handful of likely first-partition names for FORMAT raw)
//  3. loop-mount SQUASHFS.IMG from it, read-only, at /mnt/root
//  4. tmpfs-mount /mnt/root/{bin,sbin,usr/bin,usr/sbin} and recreate
//     BusyBox's applet symlinks there
//  5. switch_root into it
//
// Step 4 exists because of a real constraint, not a design choice:
// go-diskfs's SquashFS writer has Symlink/Link stubbed out
// (filesystem.ErrNotImplemented) as of the version this project
// vendors, and BusyBox's entire multi-call-binary model depends on
// ~400 symlinks (every applet name -> the same binary). Those four
// directories are therefore the one part of a cnimbus image that is
// *not* part of the immutable SquashFS root -- they're rebuilt fresh
// in tmpfs on every boot from the exact same manifest stage 1 already
// carries, which is also why any COPY/ADD destined for one of those
// four directories has to travel through stage 1 too (see
// splitShadowedFiles): SquashFS's own copy would just be invisible
// under the tmpfs mount stacked on top of it.
// defaultTmpfsSize is the size=<n> mount option for the four exec-dir
// tmpfs mounts (T27) when the Nimbusfile's TMPSIZE directive (T52) isn't
// given -- unchanged from what T27 originally hardcoded, so existing
// images are unaffected by TMPSIZE's introduction.
const defaultTmpfsSize = "32m"

func buildStage1Initramfs(busyboxBinary []byte, applets []BusyboxApplet, shadowed []ExtraFile, wifiFirmware map[string][]byte, tmpfsSize string) ([]byte, error) {
	tree := newFileTree()
	for _, d := range []string{"dev", "proc", "sys", "etc", "mnt/boot", "mnt/root", "mnt/isoscan"} {
		tree.addDir(d)
	}
	// AD-052: same helper, same absolute path, in both stages -- stage 1's
	// messages have to reach a physical monitor just as much as stage 2's
	// do (the "could not find/mount the SquashFS root" FATAL above all).
	tree.addFile(trimLeadingSlash(consoleSayPath), 0o755, []byte(buildConsoleSayScript()))
	tree.addFile("bin/busybox", 0o755, busyboxBinary)
	for _, a := range applets {
		tree.addSymlink(a.Path, a.Target)
	}
	for _, f := range shadowed {
		tree.addFile("shadow/"+trimLeadingSlash(f.Path), f.Perm, f.Data)
	}
	// F6.3: firmware lands directly in stage 1's own root at
	// "lib/firmware/<path>", not through the shadow/-then-copy-at-boot
	// path shadowed files above use. This is the D2-load-bearing
	// difference: a built-in driver's request_firmware() call fires
	// during kernel init, while stage 1's initramfs *is* the root
	// filesystem -- long before switch_root ever runs and long before
	// anything under shadow/ gets copied anywhere. Placing these files
	// under shadow/ instead (available only after /init's cp step, on
	// what becomes /mnt/root post-switch_root) would be exactly the
	// "too late" failure mode design.md's Option B warns about, just
	// self-inflicted a second time on this project's own stage-1 side
	// rather than the stage-2 SquashFS root Option B was rejected for.
	for path, data := range wifiFirmware {
		tree.addFile("lib/firmware/"+trimLeadingSlash(path), 0o644, data)
	}
	tree.addFile("init", 0o755, []byte(buildStage1Init(applets, shadowed, tmpfsSize)))

	raw, err := buildCPIO(tree)
	if err != nil {
		return nil, err
	}
	buf := &bytes.Buffer{}
	gz, _ := gzip.NewWriterLevel(buf, gzip.BestCompression)
	if _, err := gz.Write(raw); err != nil {
		return nil, fmt.Errorf("gzipping stage-1 initramfs: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildStage1Init(applets []BusyboxApplet, shadowed []ExtraFile, tmpfsSize string) string {
	if tmpfsSize == "" {
		tmpfsSize = defaultTmpfsSize
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("mount -t proc proc /proc\n")
	b.WriteString("mount -t sysfs sysfs /sys\n")
	b.WriteString("mount -t devtmpfs devtmpfs /dev 2>/dev/null\n")
	b.WriteString("echo\n")
	fmt.Fprintf(&b, "%s \"=== cnimbus: $(uname -s) $(uname -r) ===\"\n", consoleSayCmd)
	b.WriteString("echo\n")

	// The ISO's own boot device is always the CD-ROM drive -- true on
	// every hypervisor this project has actually been boot-tested on
	// (QEMU, VirtualBox, and by extension Proxmox, which is QEMU
	// underneath).
	//
	// Each candidate is mounted, checked for the actual image, and
	// unmounted again on a miss before trying the next -- committing to
	// the first device that merely *mounts* (the previous behavior) is
	// wrong whenever more than one candidate mounts successfully but
	// only one of them actually carries SQUASHFS.IMG: any hypervisor
	// with a second attached disk (or, in principle, a second optical
	// drive) could previously mount the wrong one and then fail outright
	// at the SQUASHFS.IMG check below, even though a correct candidate
	// existed further down the list.
	// /dev/vda and /dev/vdb (whole-disk, no partition suffix) are in this
	// loop too: real boot-tested on aarch64 QEMU's -M virt machine
	// (T105), where -cdrom attaches the ISO as a whole virtio-blk disk
	// with no partition table at all (there is no legacy IDE/SATA/SCSI
	// CD-ROM controller on that machine type, so /dev/sr0 never exists)
	// -- confirmed via serial log ("virtio_blk virtio1: [vda] ...",
	// immediately followed by "/dev/sr0: Can't lookup blockdev"). Without
	// this, FORMAT iso could not boot under QEMU on arm64 at all,
	// regardless of any kconfig fix.
	//
	// /dev/sda and /dev/sdb (also whole-disk) were added after a real
	// bare-metal boot failure: a FORMAT iso image dd'd onto a physical
	// USB drive on real UEFI hardware enumerates as a plain SCSI/ATA
	// disk (this project's own ISO has no MBR/GPT partition table of its
	// own -- T80 -- so there is no "/dev/sdaN" to look for here, just
	// the whole device), which the original vda/vdb-only list never
	// covered -- confirmed via a real serial capture showing "sda:
	// sda1 sda2 sda3" (the machine's *own* internal disk, irrelevant to
	// this boot) immediately followed by the every-candidate-missing
	// "Can't lookup blockdev" spam and a "kill init" panic.
	//
	// The whole scan (both this ISO9660 attempt and the FORMAT raw
	// second-partition attempt below) is wrapped in a bounded retry loop
	// for the same real-hardware finding: a USB mass-storage device's
	// SCSI negotiation can legitimately take a few seconds *after* the
	// kernel's own "new SuperSpeed USB device" log line before its
	// /dev/sdX node actually exists -- something no hypervisor's
	// emulated/virtio disk (attached instantly at guest boot) ever has
	// to wait for, so this was never exercised before a real USB-stick
	// boot. Ten one-second attempts is a deliberately bounded wait (this
	// project's whole thesis is boot latency -- never an unbounded
	// retry), generous enough to cover realistic USB enumeration delays
	// without hanging forever on a genuinely missing/corrupt image.
	b.WriteString("ROOT_MOUNTED=0\n")
	b.WriteString("for attempt in 1 2 3 4 5 6 7 8 9 10; do\n")
	b.WriteString("  for dev in /dev/sr0 /dev/sr1 /dev/vda /dev/vdb /dev/sda /dev/sdb; do\n")
	// AD-057: skip devices that don't exist at all before ever calling
	// mount on them. A real bare-metal machine's own candidate list
	// (6 devices) is almost never all present at once -- a machine with
	// NVMe system disks and a USB stick has exactly one of these six
	// real, so every attempt (times 10 retries) previously asked the
	// kernel to mount five nonexistent devices, each printing its own
	// "Can't lookup blockdev" line -- confirmed on a real console
	// capture where this alone was most of the visible boot log, with
	// none of it carrying any diagnostic value beyond "yes, still
	// missing". A shell existence check costs nothing and produces
	// exactly the same behavior (falls through to the next candidate)
	// without asking the kernel to look anything up at all.
	b.WriteString("    [ -e \"$dev\" ] || continue\n")
	b.WriteString("    mount -t iso9660 -o ro \"$dev\" /mnt/boot 2>/dev/null || continue\n")
	b.WriteString("    if [ -e /mnt/boot/SQUASHFS.IMG ] || [ -e /mnt/boot/squashfs.img ]; then break; fi\n")
	b.WriteString("    umount /mnt/boot 2>/dev/null\n")
	b.WriteString("  done\n")
	b.WriteString("  if mountpoint -q /mnt/boot; then\n")
	// Without Rock Ridge/Joliet extensions (neither enabled -- this
	// project's ISO9660 tree is plain 9660), Linux's iso9660 driver
	// exposes every name lowercased -- verified empirically (a mounted
	// image's /SQUASHFS.IMG showed up as /squashfs.img).
	b.WriteString("    SQ=/mnt/boot/SQUASHFS.IMG\n")
	b.WriteString("    [ -e \"$SQ\" ] || SQ=/mnt/boot/squashfs.img\n")
	// Explicit losetup + mount, not "mount -o loop": BusyBox's mount
	// convenience loop-association proved unreliable in practice (fails
	// with "Can't lookup blockdev" -- verified empirically). BusyBox's
	// losetup is also a stripped-down reimplementation, not util-linux's:
	// it has no "--show" (that's util-linux-only syntax -- verified
	// empirically too, "losetup: unrecognized option '--show'"), so
	// there's no way to ask it which free device it picked. Sidestep
	// that entirely by always using a fixed device: this is the very
	// first thing stage 1 does with a loop device, on a system nothing
	// else has touched yet, so /dev/loop0 is guaranteed free.
	b.WriteString("    [ -e /dev/loop0 ] || mknod /dev/loop0 b 7 0\n")
	b.WriteString("    if losetup /dev/loop0 \"$SQ\" && mount -t squashfs -o ro /dev/loop0 /mnt/root; then\n")
	b.WriteString("      ROOT_MOUNTED=1\n")
	writeIdentifyBootImage(&b, "/mnt/boot", "      ")
	b.WriteString("    fi\n")
	b.WriteString("  fi\n")

	// T76: FORMAT raw no longer carries SQUASHFS.IMG as a file inside the
	// ESP -- it's the second GPT partition's own raw contents, mounted
	// directly off the block device (no losetup/mknod, no ESP mount even
	// needed for this). Candidates cover the first-*second*-partition
	// names real hypervisors hand out for virtio/SATA/NVMe/Xen disks,
	// widened for a *second* attached disk too (e.g. a VOLUME disk that
	// happens to enumerate before the boot disk) -- same reasoning as the
	// ISO9660 candidate list above, just one partition number over.
	b.WriteString("  if [ \"$ROOT_MOUNTED\" != 1 ]; then\n")
	b.WriteString("    for dev in /dev/vda2 /dev/vdb2 /dev/sda2 /dev/sdb2 /dev/xvda2 /dev/xvdb2 /dev/nvme0n1p2 /dev/nvme1n1p2; do\n")
	// AD-057: same fix as the ISO9660 loop above, same reason -- eight
	// candidates covering virtio/SATA/Xen/NVMe naming, of which a given
	// real machine has at most one or two.
	b.WriteString("      [ -e \"$dev\" ] || continue\n")
	b.WriteString("      if mount -t squashfs -o ro \"$dev\" /mnt/root 2>/dev/null; then ROOT_MOUNTED=1; break; fi\n")
	b.WriteString("    done\n")
	b.WriteString("  fi\n")

	// Generic loopback-ISO scan (AD-049): the same mechanism every major
	// distro's own initramfs (Ubuntu's casper, Fedora/RHEL's dracut
	// iso-scan) already ships, generalized rather than tied to any one
	// tool -- unlike a plain dd'd USB stick (the whole disk *is* our
	// ISO9660 tree, handled by the loop above), a grub-loopback-based
	// multiboot USB (Ventoy is the common example, but this is the exact
	// same boot path grub's own `loopback`+`configfile` commands support
	// standalone, no particular tool required) boots by chainloading
	// grub against a .iso *file* sitting on an ordinary FAT/exFAT data
	// partition -- there is no device that *is* our ISO9660 filesystem
	// the way the dd'd case has, so it has to be located: scan every
	// real block device/partition (via /sys/block, skipping loop/ram/
	// optical devices already covered above), mount whichever ones
	// actually hold a vfat/exfat filesystem, and search each one
	// (bounded to 3 directory levels -- these tools keep ISOs at or near
	// the partition's root) for *any* .iso file to loop-mount in turn,
	// by name pattern alone -- nothing here is specific to one vendor's
	// directory layout or branding. Needs a second loop device (loop1)
	// live at the same time as the one the SQUASHFS.IMG-in-ISO mount
	// above uses (loop0): loop1 backs the outer .iso file, loop0 backs
	// SQUASHFS.IMG found inside it.
	b.WriteString("  if [ \"$ROOT_MOUNTED\" != 1 ]; then\n")
	b.WriteString("    for bd in /sys/block/*; do\n")
	b.WriteString("      dev=${bd##*/}\n")
	b.WriteString("      case \"$dev\" in loop*|ram*|sr*) continue;; esac\n")
	b.WriteString("      for part in \"$bd\" \"$bd\"/\"$dev\"*; do\n")
	b.WriteString("        [ -e \"$part\" ] || continue\n")
	b.WriteString("        pdev=${part##*/}\n")
	b.WriteString("        [ -e \"/dev/$pdev\" ] || continue\n")
	b.WriteString("        for fstype in vfat exfat; do\n")
	// AD-052: probe the exFAT signature before asking the kernel to mount
	// exfat. A failed vfat mount is silent, but the exFAT driver prints
	// three KERN_ERR lines ("invalid boot record signature", "failed to
	// read boot sector", "failed to recognize exfat type") for *every*
	// device and partition it is pointed at -- and since this scan walks
	// every block device on the machine, twice (the retry loop), a real
	// bare-metal boot with an NVMe system disk plus a USB stick filled two
	// screens with them, pushing the boot messages that actually matter
	// off the top of the display. The signature is a fixed 8-byte field
	// at offset 3 of an exFAT boot sector (Microsoft's exFAT spec calls it
	// FileSystemName), so this costs one 5-byte read per candidate and
	// leaves the mount attempt itself untouched wherever it might succeed.
	b.WriteString("          if [ \"$fstype\" = exfat ] && ! dd if=\"/dev/$pdev\" bs=1 skip=3 count=5 2>/dev/null | grep -q EXFAT; then continue; fi\n")
	b.WriteString("          mount -t \"$fstype\" -o ro \"/dev/$pdev\" /mnt/isoscan 2>/dev/null || continue\n")
	// Every *.iso match is tried in turn, not just the first: a
	// multiboot USB legitimately carries more than one ISO (that's the
	// whole point of the tool), and the first match alphabetically/by
	// directory order is not necessarily this project's own image --
	// confirmed by a real bare-metal boot where the first .iso found
	// loop-mounted fine but had no SQUASHFS.IMG inside it (a ~415MB
	// image, nothing close to this project's own ~36MB ISO), and the
	// old single-candidate logic gave up right there instead of trying
	// the next one. Redirected from a file, not piped into `while read`
	// -- a pipe's right-hand side runs in a subshell under BusyBox ash,
	// which would silently discard ROOT_MOUNTED=1 set inside the loop.
	b.WriteString("          find /mnt/isoscan -maxdepth 3 -iname '*.iso' 2>/dev/null > /isocandidates\n")
	b.WriteString("          while read -r iso; do\n")
	b.WriteString("            [ -n \"$iso\" ] || continue\n")
	b.WriteString("            /bin/sh /etc/cnimbus-say \"cnimbus: checking candidate image: $iso\"\n")
	b.WriteString("            [ -e /dev/loop1 ] || mknod /dev/loop1 b 7 1\n")
	b.WriteString("            if losetup /dev/loop1 \"$iso\" && mount -t iso9660 -o ro /dev/loop1 /mnt/boot 2>/dev/null; then\n")
	b.WriteString("              SQ=/mnt/boot/SQUASHFS.IMG\n")
	b.WriteString("              [ -e \"$SQ\" ] || SQ=/mnt/boot/squashfs.img\n")
	b.WriteString("              if [ -e \"$SQ\" ]; then\n")
	b.WriteString("                [ -e /dev/loop0 ] || mknod /dev/loop0 b 7 0\n")
	b.WriteString("                if losetup /dev/loop0 \"$SQ\" && mount -t squashfs -o ro /dev/loop0 /mnt/root; then\n")
	b.WriteString("                  ROOT_MOUNTED=1\n")
	writeIdentifyBootImage(&b, "/mnt/boot", "                  ")
	b.WriteString("                fi\n")
	b.WriteString("              fi\n")
	b.WriteString("            fi\n")
	b.WriteString("            [ \"$ROOT_MOUNTED\" = 1 ] && break\n")
	b.WriteString("            umount /mnt/boot 2>/dev/null\n")
	b.WriteString("            losetup -d /dev/loop1 2>/dev/null\n")
	b.WriteString("          done < /isocandidates\n")
	b.WriteString("          rm -f /isocandidates\n")
	b.WriteString("          [ \"$ROOT_MOUNTED\" = 1 ] || umount /mnt/isoscan 2>/dev/null\n")
	b.WriteString("        done\n")
	b.WriteString("        [ \"$ROOT_MOUNTED\" = 1 ] && break\n")
	b.WriteString("      done\n")
	b.WriteString("      [ \"$ROOT_MOUNTED\" = 1 ] && break\n")
	b.WriteString("    done\n")
	b.WriteString("  fi\n")

	b.WriteString("  [ \"$ROOT_MOUNTED\" = 1 ] && break\n")
	b.WriteString("  [ \"$attempt\" = 1 ] && /bin/sh /etc/cnimbus-say \"cnimbus: boot device not found yet -- retrying for up to 10s (real USB/SATA hardware can take a few seconds to enumerate)\"\n")
	b.WriteString("  sleep 1\n")
	b.WriteString("done\n")

	b.WriteString("if [ \"$ROOT_MOUNTED\" != 1 ]; then\n")
	b.WriteString(`  /bin/sh /etc/cnimbus-say "cnimbus: FATAL: could not find/mount the SquashFS root (tried the CD-ROM drive and common second-partition names, retried for 10s)"` + "\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	writeUptimeCheckpoint(&b, "root mounted, starting staging")

	for _, d := range []string{"bin", "sbin", "usr/bin", "usr/sbin"} {
		fmt.Fprintf(&b, "mkdir -p /mnt/root/%s\n", d)
		fmt.Fprintf(&b, "mount -t tmpfs -o mode=0755,nosuid,nodev,size=%s tmpfs /mnt/root/%s\n", tmpfsSize, d)
	}
	// T51/T52: every stage below this point writes into one of the four
	// tmpfs exec directories mounted above (size configurable via
	// TMPSIZE, defaultTmpfsSize otherwise). None of it was
	// previously checked for success -- a cp/chmod/ln-s failing (most
	// plausibly ENOSPC on the tmpfs, e.g. a COPY'd binary bigger than
	// the shadow budget) would print at most one line into a boot log
	// nobody reads and then fall straight through to switch_root
	// anyway, producing an image that boots cleanly with the
	// application silently absent -- the worst failure shape this
	// project can produce, because every visible signal says "it
	// worked". Every write from here on is therefore checked, and any
	// failure is fatal before switch_root ever runs.
	writeStagingCheck(&b, "cp /bin/busybox /mnt/root/bin/busybox", "/bin/busybox")
	writeStagingCheck(&b, "chmod 755 /mnt/root/bin/busybox", "/bin/busybox")

	// T53: a single `busybox --install -s` (no DIR argument) instead of
	// one `ln -s` per applet (~400 separate fork+exec calls before this
	// fix, plus ~400 lines of shell text inside the gzip'd initramfs) --
	// a permanent, measurable boot-time tax on every boot of every image
	// this project produces, on a project whose whole premise is
	// micro-VM boot latency.
	//
	// With no DIR argument, BusyBox's own install_links (verified against
	// this project's actual vendored BusyBox source, not assumed) uses
	// its *compiled-in* per-applet directory table
	// (APPLET_INSTALL_LOC(i) -> one of "/", "/bin/", "/sbin/",
	// "/usr/bin/", "/usr/sbin/") -- exactly this project's own four
	// tmpfs exec dirs (plus "/", for the couple of legacy manifest
	// entries like "linuxrc" that already lived outside all four and
	// were already skipped by the old loop). Run via `chroot /mnt/root`
	// so those hardcoded absolute paths resolve under the tmpfs mounts
	// just staged above, not stage 1's own initramfs root. CONFIG_CHROOT
	// and CONFIG_FEATURE_INSTALLER are both already on by default in
	// this project's BusyBox build (verified against the resolved
	// .config, not assumed) -- no kconfig change needed.
	//
	// install_links determines each symlink's *target* (what it points
	// at) via readlink("/proc/self/exe"), falling back to argv[0] when
	// that fails -- verified against BusyBox's own source, including the
	// comment noting this exact fallback exists *because* readlink
	// through /proc/self/exe usually fails inside a chroot (no /proc
	// mounted there). "/bin/busybox" below is deliberately the argv[0]
	// this depends on: an absolute path, resolved relative to the
	// chroot, producing the same "/bin/busybox" target the old `ln -s`
	// loop used explicitly.
	//
	// Per-applet failures (e.g. the "/" -> read-only SquashFS root case
	// for legacy entries) only print a warning to stderr and don't
	// affect busybox --install's own exit status (verified against the
	// source: main() unconditionally `return 0`s after calling
	// install_links) -- so this is checked by confirming a real,
	// essential applet symlink exists afterward instead of $?.
	writeStagingCheck(&b, "chroot /mnt/root /bin/busybox --install -s", "busybox applets")
	writeStagingCheck(&b, "[ -L /mnt/root/bin/sh ]", "busybox applets (verifying /bin/sh exists)")

	for _, f := range shadowed {
		rel := trimLeadingSlash(f.Path)
		dst := shQuote("/mnt/root/" + rel)
		writeStagingCheck(&b, fmt.Sprintf("cp %s %s", shQuote("/shadow/"+rel), dst), rel)
		writeStagingCheck(&b, fmt.Sprintf("chmod %o %s", f.Perm, dst), rel)
	}

	writeUptimeCheckpoint(&b, "staging complete, switching to real root")
	b.WriteString("exec switch_root /mnt/root /sbin/init\n")
	return b.String()
}

// writeUptimeCheckpoint (AD-051) emits an echo carrying /proc/uptime's
// integer-seconds value, labeled with whatever stage of boot just
// finished. Real-hardware boot finding: a boot can take far longer
// between two points than anything VM-tested ever suggested (see
// AD-048/049/050's own real-hardware-only findings), with no visible
// evidence of *where* the extra time actually went beyond "somewhere
// between these two console lines" -- these checkpoints turn that gap
// into a real, comparable number the next time this happens, cheap
// enough (one `cut` invocation) to leave in permanently rather than
// something to add back only after the fact.
func writeUptimeCheckpoint(b *strings.Builder, label string) {
	fmt.Fprintf(b, "/bin/sh /etc/cnimbus-say \"cnimbus: $(cut -d. -f1 /proc/uptime)s: %s\"\n", label)
}

// writeIdentifyBootImage (AD-050) emits a best-effort echo of
// CNIMBUS.CFG's content -- the plain-text identity manifest
// internal/isoimage.Write writes to the ISO9660 tree's top level --
// right after a boot-media candidate is committed to (ROOT_MOUNTED=1
// just set). isoDir is wherever that candidate's ISO9660 filesystem is
// mounted (/mnt/boot in every caller today). indent matches the
// surrounding block's own indentation purely for readable generated
// shell, no functional effect. Silent no-op if the file isn't present
// (an older cnimbus-built image, or a non-cnimbus ISO that happened to
// carry its own SQUASHFS.IMG-shaped file by coincidence) -- this is
// diagnostic output, not something boot depends on.
func writeIdentifyBootImage(b *strings.Builder, isoDir, indent string) {
	fmt.Fprintf(b, "%sCFG=%s/CNIMBUS.CFG\n", indent, isoDir)
	fmt.Fprintf(b, "%s[ -e \"$CFG\" ] || CFG=%s/cnimbus.cfg\n", indent, isoDir)
	// Each manifest line is fanned out individually rather than `cat`'d:
	// a bare cat writes to stdout, which is /dev/console, which is the
	// one console AD-052 exists to stop relying on -- the identity of the
	// image that actually booted is precisely the thing a bare-metal user
	// standing in front of the monitor needs to read.
	fmt.Fprintf(b, "%sif [ -e \"$CFG\" ]; then\n", indent)
	fmt.Fprintf(b, "%s  %s \"cnimbus: identified boot image:\"\n", indent, consoleSayCmd)
	fmt.Fprintf(b, "%s  while read -r cfgline; do %s \"cnimbus:   $cfgline\"; done < \"$CFG\"\n", indent, consoleSayCmd)
	fmt.Fprintf(b, "%sfi\n", indent)
}

// writeStagingCheck emits cmd followed by an explicit failure check --
// see the T51 comment in buildStage1Init for why bare, unchecked
// cp/chmod/ln-s lines are exactly the bug this exists to close. path is
// named in the FATAL message purely for diagnosis; it plays no role in
// the shell logic itself.
func writeStagingCheck(b *strings.Builder, cmd, path string) {
	fmt.Fprintf(b, "if ! %s; then\n", cmd)
	fmt.Fprintf(b, "  echo %s\n", shQuote(fmt.Sprintf(
		"cnimbus: FATAL: could not stage %s into its exec directory (tmpfs full? see TMPSIZE)", path)))
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
}
