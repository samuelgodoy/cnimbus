package rootfs

// consoleSayPath is where buildConsoleSayScript's output is installed, in
// both stage 1's initramfs and the SquashFS root, so every generated
// script in either stage calls it by the exact same absolute path. It is
// deliberately not under /sbin or /bin: stage 1 stacks a fresh tmpfs over
// bin/, sbin/, usr/bin/ and usr/sbin/ on every boot (see stage1.go), which
// would hide a SquashFS-side copy living in any of them. /etc is ordinary
// read-only SquashFS content and is always there.
const consoleSayPath = "/etc/cnimbus-say"

// consoleSayCmd is how generated scripts actually invoke it: through an
// explicit interpreter, never as a bare executable. go-diskfs's SquashFS
// writer takes each file's mode from the build host and loses the
// execute bit entirely on Windows (the same T73 constraint that put
// supervisor scripts through stage 1's shadow/ mechanism), so a
// Windows-built image turned every boot message into
// "/etc/cnimbus-say: Permission denied" -- caught by a real VGA boot of
// a real Windows-built ISO, not by review. /etc/init.d/rcS is launched
// the same way by inittab for the same reason, so this is the
// established idiom here rather than a special case.
const consoleSayCmd = "/bin/sh " + consoleSayPath

// buildConsoleSayScript writes a boot message to *every* console the
// kernel currently has registered, rather than to whichever single one
// /dev/console happens to alias.
//
// AD-052, and the reason this exists at all: the kernel prints its own
// messages to all `console=` devices on the cmdline, but userspace
// inherits only the LAST one as /dev/console. This project's cmdline is
// `console=tty0 console=ttyS0,115200n8` (vm-amd64.fragment), so /dev/console
// is the serial port and every boot message this image prints -- the
// uptime checkpoints, the VGA IP banner, service state, firewall
// failures -- went to serial only. On a machine with a monitor attached
// (real bare metal, or a Proxmox/VirtualBox VGA console) the screen shows
// the kernel's own dmesg and then simply stops at the last kernel line,
// which reads exactly like a hang. It was reported as one twice.
//
// Every QEMU boot test this project has ever run read the serial port,
// where all of this was present and correct, so no amount of VM testing
// could have surfaced it; it was reproduced for real by booting the same
// ISO with both a VGA display and a serial port and capturing the
// framebuffer via the QEMU monitor's `screendump` (see AD-052).
//
// The fix is deliberately not "reorder the cmdline so tty0 wins": that
// would only move the blind spot to serial-console users (and to this
// project's own test suite). /sys/class/tty/console/active is the
// kernel's own list of active consoles, space-separated -- the same file
// plymouth and dracut read for exactly this purpose -- so writing to each
// entry in turn reaches every console, in any order, with no cmdline
// change and no per-platform special casing.
func buildConsoleSayScript() string {
	return `#!/bin/sh
# Print "$*" on every console the kernel registered, not just the one
# /dev/console aliases (the last console= on the cmdline). See
# internal/rootfs/console.go for the full rationale.
sent=
for c in $(cat /sys/class/tty/console/active 2>/dev/null); do
	[ -c "/dev/$c" ] || continue
	echo "$*" > "/dev/$c" 2>/dev/null && sent=1
done
# Fallback: no sysfs (or no console class) means no list to fan out to,
# so behave exactly like the plain echo this replaced.
[ -n "$sent" ] || echo "$*"
`
}
