package rootfs

import (
	"os"
	"strings"
	"testing"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
)

// AD-059: a real Proxmox VM's "Signal Shutdown" (ACPI power button)
// timed out ("VM quit/powerdown failed - got timeout") even after
// confirming the kernel itself delivered the button press (dmesg showed
// "ACPI: button: Power Button [PWRF]") and after fixing the separate
// CONFIG_INPUT_EVDEV gap. The real cause: BusyBox's acpid does not read
// the classic acpid.conf-style /etc/acpi/events/* config this project
// previously wrote at all -- it reads /etc/acpid.conf and /etc/acpi.map,
// falling back to compiled-in tables (verified against BusyBox 1.36.1's
// own util-linux/acpid.c) that route the power button straight to
// "PWRF/00000080", stat()d and execve()d relative to /etc/acpi with no
// custom config needed. These assert the fix places the real handler
// exactly there.
func TestBuildSquashfsRootHasACPIPowerButtonPlaceholder(t *testing.T) {
	path, err := buildSquashfsRoot(PiecesSpec{Hostname: "x"}, nil)
	if err != nil {
		t.Fatalf("buildSquashfsRoot: %v", err)
	}
	defer os.Remove(path)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening produced squashfs image: %v", err)
	}
	defer f.Close()

	fs, err := squashfs.Read(file.New(f, true), 0, 0, 0)
	if err != nil {
		t.Fatalf("squashfs.Read: %v", err)
	}
	if _, err := fs.OpenFile(acpiPowerHandlerPath, os.O_RDONLY); err != nil {
		t.Errorf("expected placeholder file %q in the built image, got: %v", acpiPowerHandlerPath, err)
	}
	// The classic acpid.conf-style config this replaced must actually
	// be gone -- BusyBox's acpid never reads it, so shipping it
	// alongside the real fix would just be a second, misleading source
	// of truth about how this mechanism works.
	if _, err := fs.OpenFile("etc/acpi/events/power", os.O_RDONLY); err == nil {
		t.Errorf("expected the old, never-read /etc/acpi/events/power config to be gone")
	}
}

// AD-059: buildRCScript must bind-mount the real, already-executable
// /sbin/powerbtn.sh over the placeholder before starting acpid --
// acpid stat()s and execve()s the target directly, so it needs a real
// execute bit, which placing script content straight into the SquashFS
// build (T73's Windows exec-bit loss, the exact bug AD-052 already hit
// once with /etc/cnimbus-say) cannot reliably provide.
func TestBuildRCScriptWiresUpACPIPowerButtonHandler(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm"})
	want := "mount --bind /sbin/powerbtn.sh /" + acpiPowerHandlerPath
	if !strings.Contains(script, want) {
		t.Errorf("expected %q in rcS: %q", want, script)
	}
	bindIdx := strings.Index(script, want)
	acpidIdx := strings.Index(script, "acpid -d &")
	if bindIdx < 0 || acpidIdx < 0 || bindIdx > acpidIdx {
		t.Errorf("expected the bind-mount before acpid -d starts: bind=%d acpid=%d", bindIdx, acpidIdx)
	}
}

// AD-059: a bare "acpid &" never completed a real ACPI shutdown request
// in a real QEMU repro of the same symptom Proxmox's "Signal Shutdown"
// hit -- acpid dies trying to open /var/log/acpid.log, which doesn't
// exist in this image, before ever reaching /dev/input/event0. "-f"
// alone (skipping only the daemonize step) was tried and still failed;
// only "-d" (which also skips the logfile open) fixed it, confirmed by
// watching the guest actually power off within a few seconds against
// the exact same otherwise-correct setup.
func TestBuildRCScriptStartsACPIDWithLogToStderr(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm"})
	if !strings.Contains(script, "acpid -d &") {
		t.Errorf("expected acpid started with -d (no /var/log/acpid.log to open): %q", script)
	}
	if strings.Contains(script, "\nacpid &\n") {
		t.Errorf("expected the bare \"acpid &\" (dies opening /var/log/acpid.log) to be gone: %q", script)
	}
}
