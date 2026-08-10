package rootfs

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

// AD-052: the whole point of the helper is that it does NOT write to
// stdout (which is /dev/console, which is only ever the last console= on
// the cmdline). It has to enumerate the kernel's own list instead, and
// still degrade to a plain echo where that list is unavailable.
func TestConsoleSayScriptFansOutToEveryActiveConsole(t *testing.T) {
	script := buildConsoleSayScript()
	if !strings.Contains(script, "/sys/class/tty/console/active") {
		t.Errorf("console helper must enumerate the kernel's active console list, got:\n%s", script)
	}
	if !strings.Contains(script, `echo "$*" > "/dev/$c"`) {
		t.Errorf("console helper must write to each console device by name, got:\n%s", script)
	}
	if !strings.Contains(script, `[ -n "$sent" ] || echo "$*"`) {
		t.Errorf("console helper must fall back to a plain echo when no console list exists, got:\n%s", script)
	}
}

// A bare `echo` in any generated boot script is the AD-052 bug itself:
// it reaches serial only, so a bare-metal user watching a monitor sees
// the kernel's dmesg stop dead and reads it as a hang. Both real reports
// of a "frozen" boot were this.
func TestGeneratedBootScriptsPrintThroughTheConsoleHelper(t *testing.T) {
	spec := PiecesSpec{
		Hostname: "myvm",
		DHCP:     true,
		VGA:      true,
		Services: []Service{{Name: "entrypoint", Argv: []string{"/usr/bin/app"}}},
		Firewall: []string{"-A INPUT -p tcp --dport 8080 -j ACCEPT"},
	}
	scripts := map[string]string{
		"stage1 init": buildStage1Init([]BusyboxApplet{{Path: "bin/sh", Target: "busybox"}}, nil, ""),
		"rcS":         buildRCScript(spec),
		"supervisor":  buildSupervisorScript(spec.Services[0], nil, "", "", nil),
		"shutdown.sh": buildShutdownScript(spec.Services, 5),
		"firewall.sh": buildFirewallScript(spec.Firewall, "halt", false),
	}
	for name, script := range scripts {
		if strings.Contains(script, `echo "cnimbus:`) {
			t.Errorf("%s still prints a cnimbus message with a bare echo (serial-only -- see AD-052):\n%s", name, script)
		}
		if !strings.Contains(script, consoleSayCmd+` "cnimbus:`) {
			t.Errorf("%s prints no cnimbus message through %s at all", name, consoleSayCmd)
		}
	}
}

// The helper is called by absolute path from both stages, so it has to
// actually exist at that path in both -- a missing one turns every boot
// message into a "not found" instead.
func TestConsoleHelperInstalledInBothStages(t *testing.T) {
	spec := PiecesSpec{
		Hostname:      "x",
		BusyboxBinary: []byte("fake-busybox-bytes"),
		Services:      []Service{{Name: "entrypoint", Argv: []string{"/usr/bin/app"}}},
	}
	images, err := BuildImages(spec)
	if err != nil {
		t.Fatalf("BuildImages: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(images.Stage1))
	if err != nil {
		t.Fatalf("gzip.NewReader on Stage1: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompressing Stage1: %v", err)
	}
	want := trimLeadingSlash(consoleSayPath)
	var found bool
	for _, e := range parseCPIO(t, raw) {
		if e.name != want {
			continue
		}
		found = true
		if perm := e.mode & 0o7777; perm != 0o755 {
			t.Errorf("%s in stage 1: mode = %o, want 755 (it is exec'd, not sourced)", want, perm)
		}
		if !strings.Contains(string(e.data), "/sys/class/tty/console/active") {
			t.Errorf("%s in stage 1 is not the console helper", want)
		}
	}
	if !found {
		t.Errorf("%s missing from stage 1's initramfs", want)
	}
	// Stage 2's copy goes into buildSquashfsRoot's file list, which is
	// built from a function-local type and so isn't reachable from a unit
	// test. It is covered end-to-end instead: a real QEMU boot with a VGA
	// display attached prints nothing at all if the SquashFS-side helper
	// is missing, since rcS calls it by absolute path (see AD-052's real
	// verification in .specs/project/STATE.md).
}

// AD-052: mounting exfat on a device that is not exFAT costs three
// KERN_ERR lines on the console per attempt, and this scan walks every
// block device on the machine twice -- enough on real hardware to push
// the boot messages that matter off the top of the screen.
func TestStage1ProbesExfatSignatureBeforeMounting(t *testing.T) {
	script := buildStage1Init([]BusyboxApplet{{Path: "bin/sh", Target: "busybox"}}, nil, "")
	probe := `if [ "$fstype" = exfat ] && ! dd if="/dev/$pdev" bs=1 skip=3 count=5 2>/dev/null | grep -q EXFAT; then continue; fi`
	if !strings.Contains(script, probe) {
		t.Errorf("stage 1 must probe the exFAT signature before attempting an exfat mount, got:\n%s", script)
	}
	probeIdx := strings.Index(script, probe)
	mountIdx := strings.Index(script, `mount -t "$fstype" -o ro "/dev/$pdev"`)
	if mountIdx < 0 || probeIdx > mountIdx {
		t.Errorf("the exFAT probe must come before the mount attempt (probe at %d, mount at %d)", probeIdx, mountIdx)
	}
}
