package rootfs

import (
	"strings"
	"testing"
)

func TestBuildStage1InitProbesEachDeviceForTheActualImage(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	// T76: FORMAT raw's root now lives on its own GPT partition, mounted
	// directly as squashfs -- second-partition candidate names, widened
	// to cover a *second* attached disk too (e.g. a VOLUME disk
	// enumerating before the boot disk).
	for _, dev := range []string{"/dev/sr0", "/dev/sr1", "/dev/vda2", "/dev/vdb2",
		"/dev/sda2", "/dev/sdb2", "/dev/xvda2", "/dev/xvdb2", "/dev/nvme0n1p2", "/dev/nvme1n1p2"} {
		if !strings.Contains(script, dev) {
			t.Errorf("expected candidate device %q in the boot-media probe: %q", dev, script)
		}
	}
	// A device that merely mounts must still be checked for the actual
	// image before being committed to -- not just "mount succeeded".
	// Written as an explicit "if ... ; then break; fi" (T54), not the
	// equivalent-but-fragile "cond && break" shape, so a future edit
	// can't silently detach the break from the wrong half of the test.
	if !strings.Contains(script, "if [ -e /mnt/boot/SQUASHFS.IMG ] || [ -e /mnt/boot/squashfs.img ]; then break; fi") {
		t.Errorf("expected each mounted candidate to be checked for the actual image before breaking: %q", script)
	}
	if !strings.Contains(script, "umount /mnt/boot 2>/dev/null") {
		t.Errorf("expected a miss to unmount before trying the next candidate: %q", script)
	}
}

// T105: confirmed via a real qemu-system-aarch64 -M virt boot that
// -cdrom on that machine type attaches the ISO as a whole virtio-blk
// disk with no partition table (/dev/vda) -- there is no legacy
// IDE/SATA/SCSI CD-ROM controller on -M virt at all, so /dev/sr0 never
// exists there. /dev/vda and /dev/vdb must be tried as ISO9660 (this
// loop), not only as the FORMAT-raw second-partition loop (T76), which
// is what a real FORMAT raw disk actually has.
func TestBuildStage1InitProbesWholeDiskDevicesForISO9660(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	iso9660Loop := script[:strings.Index(script, "if mountpoint")]
	for _, dev := range []string{"/dev/vda", "/dev/vdb"} {
		if !strings.Contains(iso9660Loop, dev) {
			t.Errorf("expected whole-disk device %q in the ISO9660 probe loop specifically: %q", dev, iso9660Loop)
		}
	}
	// /dev/vda (no partition suffix) must not appear only inside the
	// FORMAT-raw loop -- confirm it's mounted with -t iso9660 in this
	// earlier loop, not just present somewhere in the script.
	if !strings.Contains(iso9660Loop, `for dev in /dev/sr0 /dev/sr1 /dev/vda /dev/vdb /dev/sda /dev/sdb; do`) {
		t.Errorf("expected /dev/vda and /dev/vdb in the same for-loop as /dev/sr0/sr1/sda/sdb: %q", iso9660Loop)
	}
}

// Real bare-metal boot finding (2026-08-10): a FORMAT iso image dd'd
// onto a physical USB drive on real UEFI hardware enumerates as a
// plain SCSI/ATA disk (/dev/sda, no partition table of its own -- T80),
// which the vda/vdb-only list never covered -- confirmed via a real
// serial capture ending in "Can't lookup blockdev" for every candidate
// and a "kill init" panic. And unlike a hypervisor's emulated/virtio
// disk (attached instantly at guest boot), a real USB device's own
// SCSI negotiation can take a few seconds after the kernel's "new
// SuperSpeed USB device" line before its /dev/sdX node exists at all --
// so the whole scan must retry, bounded, rather than try once and fail.
func TestBuildStage1InitProbesRealDiskDevicesForISO9660WithRetry(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	iso9660Loop := script[:strings.Index(script, "if mountpoint")]
	for _, dev := range []string{"/dev/sda", "/dev/sdb"} {
		if !strings.Contains(iso9660Loop, dev) {
			t.Errorf("expected real-hardware whole-disk device %q in the ISO9660 probe loop: %q", dev, iso9660Loop)
		}
	}
	if !strings.Contains(script, "for attempt in 1 2 3 4 5 6 7 8 9 10; do") {
		t.Errorf("expected a bounded (10 attempt) retry loop around the whole boot-media scan: %q", script)
	}
	if !strings.Contains(script, "sleep 1") {
		t.Errorf("expected a wait between retry attempts: %q", script)
	}
	// The retry loop must wrap both the ISO9660 attempt and the
	// FORMAT-raw second-partition fallback -- not just one of the two --
	// since either FORMAT could legitimately hit the same slow-USB-
	// enumeration race on real hardware.
	retryIdx := strings.Index(script, "for attempt in")
	rawLoopIdx := strings.Index(script, "/dev/vda2")
	sleepIdx := strings.Index(script, "sleep 1")
	if retryIdx < 0 || rawLoopIdx < 0 || sleepIdx < 0 || retryIdx > rawLoopIdx || rawLoopIdx > sleepIdx {
		t.Errorf("expected the FORMAT-raw fallback loop inside the retry loop, before its per-attempt sleep: %q", script)
	}
}

// AD-057: a real bare-metal console capture showed "Can't lookup
// blockdev" for five of the six ISO9660 candidates (and again for most
// of the eight FORMAT-raw candidates) on every single attempt of the
// bounded retry loop -- a machine with NVMe system disks and one USB
// stick has at most one or two of these fourteen device names real at
// any given time. None of that noise carries any diagnostic value
// beyond "still missing", and a plain shell existence check avoids
// asking the kernel to look the device up at all.
func TestBuildStage1InitSkipsMountingNonexistentCandidateDevices(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	iso9660Loop := script[:strings.Index(script, "if mountpoint")]
	if !strings.Contains(iso9660Loop, `[ -e "$dev" ] || continue`) {
		t.Errorf("expected an existence check before mounting each ISO9660 candidate: %q", iso9660Loop)
	}
	existsIdx := strings.Index(iso9660Loop, `[ -e "$dev" ] || continue`)
	mountIdx := strings.Index(iso9660Loop, "mount -t iso9660")
	if existsIdx < 0 || mountIdx < 0 || existsIdx > mountIdx {
		t.Errorf("expected the existence check before the mount attempt: exists=%d mount=%d", existsIdx, mountIdx)
	}

	rawLoop := script[strings.Index(script, "/dev/vda2"):]
	if !strings.Contains(rawLoop, `[ -e "$dev" ] || continue`) {
		t.Errorf("expected an existence check before mounting each FORMAT-raw candidate: %q", rawLoop)
	}
	rawExistsIdx := strings.Index(rawLoop, `[ -e "$dev" ] || continue`)
	rawMountIdx := strings.Index(rawLoop, "mount -t squashfs")
	if rawExistsIdx < 0 || rawMountIdx < 0 || rawExistsIdx > rawMountIdx {
		t.Errorf("expected the existence check before the FORMAT-raw mount attempt: exists=%d mount=%d", rawExistsIdx, rawMountIdx)
	}
}

// AD-049: a grub-loopback-based multiboot USB (Ventoy is the common
// example, but this is the same mechanism grub's own loopback+configfile
// commands support standalone) boots by chainloading grub against a
// .iso *file* sitting on an ordinary FAT/exFAT partition -- there is no
// device that *is* our ISO9660 filesystem the way a plain dd'd stick
// has. This must be a generic "scan every real partition, mount
// vfat/exfat, search for any *.iso file" pass -- the same technique
// Ubuntu's casper/Fedora's dracut iso-scan already use, not anything
// tied to one vendor's tool or directory layout.
func TestBuildStage1InitScansForLoopbackISOOnFatPartitions(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	for _, want := range []string{
		"for bd in /sys/block/*",
		"for fstype in vfat exfat",
		"-iname '*.iso'",
		"mknod /dev/loop1 b 7 1",
		"losetup /dev/loop1",
		"mount -t iso9660 -o ro /dev/loop1 /mnt/boot",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected the generic loopback-ISO scan to contain %q: %q", want, script)
		}
	}
	// Must not hardcode any one vendor's name/path -- the mechanism is
	// generic (grub loopback boot), not tied to a specific tool.
	if strings.Contains(strings.ToLower(script), "ventoy") {
		t.Errorf("the loopback-ISO scan must stay generic, not name a specific vendor's tool: %q", script)
	}
	// Must be reachable inside the bounded retry loop, after the
	// FORMAT-raw fallback, before the per-attempt sleep.
	scanIdx := strings.Index(script, "for bd in /sys/block/*")
	rawLoopIdx := strings.Index(script, "/dev/vda2")
	sleepIdx := strings.Index(script, "sleep 1")
	if scanIdx < 0 || rawLoopIdx < 0 || sleepIdx < 0 || rawLoopIdx > scanIdx || scanIdx > sleepIdx {
		t.Errorf("expected the loopback-ISO scan after the FORMAT-raw fallback, before the per-attempt sleep: %q", script)
	}
}

// T76: the FORMAT-raw path must mount the second GPT partition directly
// as squashfs -- no losetup/mknod, no ESP lookup at all -- since the
// root filesystem is now the partition's own raw contents, not a file
// inside a filesystem.
func TestBuildStage1InitMountsRawPartitionDirectlyAsSquashfs(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	if !strings.Contains(script, `mount -t squashfs -o ro "$dev" /mnt/root`) {
		t.Errorf("expected a direct squashfs mount of the raw-disk candidate devices: %q", script)
	}
	rawLoopIdx := strings.Index(script, "/dev/vda2")
	switchIdx := strings.Index(script, "exec switch_root")
	if rawLoopIdx < 0 || switchIdx < 0 || rawLoopIdx > switchIdx {
		t.Errorf("expected the FORMAT-raw candidate loop to precede switch_root: %q", script)
	}
}

// AD-050: once a boot-media candidate is committed to (ROOT_MOUNTED=1),
// the script must try to echo CNIMBUS.CFG's content -- the identity
// manifest internal/isoimage.Write writes to the ISO9660 tree's top
// level -- so a multiboot USB stick with more than one candidate .iso
// leaves a trail of which one actually got picked, in both the direct
// ISO9660-device scan and the generic loopback-ISO scan.
func TestBuildStage1InitEchoesIdentityManifestOnSuccess(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	for _, want := range []string{
		"CFG=/mnt/boot/CNIMBUS.CFG",
		"[ -e \"$CFG\" ] || CFG=/mnt/boot/cnimbus.cfg",
		"cnimbus: identified boot image:",
	} {
		if strings.Count(script, want) < 2 {
			t.Errorf("expected %q in both the direct-device and loopback-ISO scans (>=2 occurrences), got %d: %q",
				want, strings.Count(script, want), script)
		}
	}
}

// AD-051: real bare-metal boot diagnosability -- a real boot can stall
// far longer between two console lines than any VM ever suggested
// (see AD-048/049/050's own real-hardware findings), with nothing
// showing *where* the time went. Checkpointed uptime echoes turn that
// into a comparable number rather than a guess.
func TestBuildStage1InitEmitsUptimeCheckpoints(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	for _, want := range []string{
		"root mounted, starting staging",
		"staging complete, switching to real root",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected an uptime checkpoint labeled %q: %q", want, script)
		}
	}
	if !strings.Contains(script, "cut -d. -f1 /proc/uptime") {
		t.Errorf("expected checkpoints to read /proc/uptime: %q", script)
	}
}

// T51: a cp/chmod/ln-s failure while staging the four exec directories
// (most plausibly ENOSPC on the size=32m tmpfs) must stop the boot with a
// FATAL message instead of falling through to switch_root with a
// knowingly incomplete root.
func TestBuildStage1InitStagingFailuresAreFatal(t *testing.T) {
	applets := []BusyboxApplet{{Path: "bin/sh", Target: "busybox"}}
	shadowed := []ExtraFile{{Path: "/usr/bin/myapp", Perm: 0o755, Data: []byte("x")}}
	script := buildStage1Init(applets, shadowed, "")

	for _, want := range []string{
		"if ! cp /bin/busybox /mnt/root/bin/busybox; then",
		"if ! chmod 755 /mnt/root/bin/busybox; then",
		"if ! chroot /mnt/root /bin/busybox --install -s; then",
		"if ! [ -L /mnt/root/bin/sh ]; then",
		"if ! cp '/shadow/usr/bin/myapp' '/mnt/root/usr/bin/myapp'; then",
		"if ! chmod 755 '/mnt/root/usr/bin/myapp'; then",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected staging command to be error-checked, missing %q in:\n%s", want, script)
		}
	}
	if strings.Count(script, "exit 1") < 2+3 {
		// at least the two boot-media FATAL exits already present, plus
		// one per newly-checked staging command above (busybox itself,
		// the --install call + its /bin/sh sanity check, and the shadowed
		// file's cp/chmod are separate checks)
		t.Errorf("expected a FATAL exit 1 per checked staging command, got only:\n%s", script)
	}
	if !strings.Contains(script, "tmpfs full? see TMPSIZE") {
		t.Errorf("expected the staging FATAL message to hint at the tmpfs-size cause: %q", script)
	}
	// The failure check must come strictly before switch_root -- a check
	// that ran after handoff would be pointless.
	lastCheck := strings.LastIndex(script, "tmpfs full? see TMPSIZE")
	switchIdx := strings.Index(script, "exec switch_root")
	if lastCheck < 0 || switchIdx < 0 || lastCheck > switchIdx {
		t.Errorf("expected every staging FATAL check to precede switch_root: %q", script)
	}
}

// T53: a single `busybox --install -s` (chrooted into /mnt/root so its
// own hardcoded per-applet directory table resolves under the tmpfs
// mounts staged above) replaces one `ln -s` line per applet -- roughly
// 400 separate fork+exec calls and lines of shell text before this fix,
// a permanent boot-time tax on every image this project produces.
func TestBuildStage1InitUsesBusyboxInstallInsteadOfPerAppletSymlinks(t *testing.T) {
	applets := []BusyboxApplet{
		{Path: "bin/sh", Target: "busybox"},
		{Path: "usr/bin/wget", Target: "/bin/busybox"},
	}
	script := buildStage1Init(applets, nil, "")

	if !strings.Contains(script, "chroot /mnt/root /bin/busybox --install -s") {
		t.Errorf("expected a single chrooted busybox --install call: %q", script)
	}
	if strings.Contains(script, "ln -s") {
		t.Errorf("expected no per-applet ln -s lines left at all: %q", script)
	}
	// The install call must come after busybox itself is staged (it's
	// the thing being chrooted into and exec'd) and before switch_root.
	installIdx := strings.Index(script, "chroot /mnt/root /bin/busybox --install")
	busyboxCopyIdx := strings.Index(script, "cp /bin/busybox /mnt/root/bin/busybox")
	switchIdx := strings.Index(script, "exec switch_root")
	if busyboxCopyIdx < 0 || installIdx < 0 || switchIdx < 0 || busyboxCopyIdx > installIdx || installIdx > switchIdx {
		t.Errorf("expected busybox copy, then --install, then switch_root, in that order: %q", script)
	}
}
