package rootfs

import (
	"fmt"
	"os"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
)

// buildSquashfsRoot assembles the real, read-only root filesystem:
// everything from a PiecesSpec except the BusyBox binary and its
// applet symlinks (those live in stage 1's tmpfs -- see stage1.go),
// and except any ExtraFile shadowed by that tmpfs (already filtered
// out of normalFiles by splitShadowedFiles).
//
// Built with plain default (gzip) compression. An uncompressed build
// was tried first for speed, but go-diskfs's NoCompress* combination
// produces a superblock the Linux kernel's squashfs driver rejects
// with EINVAL -- verified against two unrelated kernels -- so this
// uses the one code path go-diskfs's own tests actually exercise.
//
// Returns the path to the finished SquashFS image on disk rather than
// its bytes (T75): this file can legitimately be gigabytes (a large
// COPY/VOLUME image), and go-diskfs already streams it to a real temp
// file as it writes -- reading the whole thing back into a []byte here
// just to have callers write it back out to another file downstream
// (isoimage/rawimage) was a needless double round-trip through the
// heap, and the project's only real scaling ceiling. The caller owns
// the returned path and must remove it when done.
func buildSquashfsRoot(spec PiecesSpec, normalFiles []ExtraFile) (string, error) {
	tmp, err := os.CreateTemp(spec.TmpDir, "cnimbus-squashfs-*")
	if err != nil {
		return "", fmt.Errorf("creating squashfs workspace file: %w", err)
	}
	tmpPath := tmp.Name()
	// Cleaned up by the caller on success; best-effort here only covers
	// the early-return error paths below, where the caller never learns
	// the path at all.
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(tmpPath)
		}
	}()

	backend := file.New(tmp, false)
	// T74: 131072 (128 KiB) is mksquashfs's own default block size, and
	// the largest go-diskfs's squashfs writer supports here -- 4 KiB (the
	// SquashFS format's minimum) was previously used, which compresses
	// far worse per byte (gzip over a smaller window) and multiplies the
	// block-index metadata roughly 32x for no benefit. Safely under the
	// kernel driver's own 1 MiB SQUASHFS_FILE_SIZE cap.
	const blocksize = 131072
	fs, err := squashfs.Create(backend, 0, 0, blocksize)
	if err != nil {
		_ = tmp.Close() // best-effort; the error above is what's returned
		return "", fmt.Errorf("squashfs.Create: %w", err)
	}

	dirs := []string{
		"bin", "sbin", "usr", "usr/bin", "usr/sbin", // tmpfs mountpoints -- see stage1.go
		"dev", "proc", "sys", "tmp", "var", "var/run", "run", "mnt",
		"etc", "etc/init.d", "etc/acpi", "etc/acpi/PWRF", // AD-059: PWRF, not events -- see acpiPowerScript's doc comment
	}
	// Every VOLUME's mountpoint needs to already exist as a real
	// directory *in the image*, not just get "mkdir -p"'d at boot --
	// buildRCScript's mkdir runs against this read-only SquashFS root,
	// so it silently no-ops (no error checking, consistent with this
	// init system's general style) for any path not already present
	// here. Confirmed by real boot: VOLUME /dev/vda /mnt/data (this
	// project's own examples/volume-persistent-disk/Nimbusfile) never
	// actually mounted -- "/mnt" existed (it's in the list above) but
	// "/mnt/data" did not, so mkdir -p silently failed and the
	// subsequent mount call got ENOENT. A mountpoint under a path that's
	// *already* tmpfs at boot (e.g. VOLUME .../ /tmp/data) worked by
	// accident, which is what let this go unnoticed. seen dedupes
	// against the dirs list above and across mountpoints that share a
	// parent (e.g. two VOLUMEs both under /data).
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		seen[d] = true
	}
	for _, v := range spec.Volumes {
		mnt := trimLeadingSlash(v.Mountpoint)
		if mnt == "" {
			continue
		}
		parts := strings.Split(mnt, "/")
		var built strings.Builder
		for i, part := range parts {
			if i > 0 {
				built.WriteByte('/')
			}
			built.WriteString(part)
			d := built.String()
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	for _, d := range dirs {
		if err := fs.Mkdir(d); err != nil {
			_ = tmp.Close() // best-effort; the error above is what's returned
			return "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	type namedFile struct {
		path string
		perm os.FileMode
		data []byte
	}
	// rcS itself needs no execute bit here: it's invoked as "/bin/sh
	// /etc/init.d/rcS" (see buildInittab), and 0o755 below is best-effort
	// documentation, not something that actually survives -- see
	// stage1.go's doc comment for why. udhcpc.script and powerbtn.sh are
	// execve()'d directly with no interpreter to hide behind, so they
	// live in stage 1's tmpfs instead (see BuildImages), not here.
	files := []namedFile{
		{"etc/inittab", 0o644, []byte(buildInittab(spec.Services, spec.Agent))},
		// AD-052: every generated script below calls this to print boot
		// messages, so that they land on a physical monitor as well as on
		// the serial port. Installed under the same absolute path in stage
		// 1's initramfs too (see buildStage1Initramfs).
		{trimLeadingSlash(consoleSayPath), 0o755, []byte(buildConsoleSayScript())},
		{"etc/init.d/rcS", 0o755, []byte(buildRCScript(spec))},
		{"etc/init.d/shutdown.sh", 0o755, []byte(buildShutdownScript(spec.Services, spec.StopGrace))},
		{"etc/hostname", 0o644, []byte(spec.Hostname + "\n")},
		{"etc/hosts", 0o644, []byte(buildHostsFile(spec.Hostname))},
		// AD-059: empty placeholder -- buildRCScript bind-mounts the real
		// /sbin/powerbtn.sh over this exact path (acpiPowerHandlerPath)
		// before starting acpid. See acpiPowerScript's doc comment for
		// why this specific path, not the classic acpid.conf-style
		// event/action config this replaced.
		{acpiPowerHandlerPath, 0o644, nil},
		// Empty placeholder: rcS bind-mounts a tmpfs file over this exact
		// path so DNS resolution has somewhere writable to put
		// nameserver lines, despite /etc otherwise being read-only
		// SquashFS -- see buildRCScript's doc comment on the bind mount.
		{"etc/resolv.conf", 0o644, []byte("")},
	}

	// Always emitted (T88), not gated on spec.User != "": a Go binary
	// built with CGO_ENABLED=0 parses /etc/passwd directly for
	// os/user.Current(), and getpwuid(0) through glibc returns NULL with
	// no /etc/passwd at all -- the default image (no USER directive) had
	// neither file before this fix. buildPasswd/buildGroup always include
	// the root entry and append the USER entry only when one is declared.
	files = append(files,
		namedFile{"etc/passwd", 0o644, []byte(buildPasswd(spec.User))},
		namedFile{"etc/group", 0o644, []byte(buildGroup(spec.User))},
	)
	if len(spec.Firewall) > 0 {
		files = append(files, namedFile{"etc/init.d/firewall.sh", 0o755, []byte(buildFirewallScript(spec.Firewall, spec.FirewallOnError, false))})
	}
	if len(spec.Firewall6) > 0 {
		// AD-047: same generator, same on-error policy, a separate file
		// so the two rulesets are applied (and can fail) independently.
		// AD-055: the `true` is the whole fix -- see icmpv6NDPAutoRules.
		files = append(files, namedFile{"etc/init.d/firewall6.sh", 0o755, []byte(buildFirewallScript(spec.Firewall6, spec.FirewallOnError, true))})
	}
	// The HTTP AGENT script and every per-Service supervisor script used
	// to be generated and written right here, at 0o600 -- but this
	// function's own writeSquashfsFile takes its mode from the *build
	// host's* filesystem (go-diskfs's Chmod/finalize.go), which loses
	// the execute bit and the group/other permission bits entirely on
	// Windows (verified empirically as 0666 regardless of what's
	// requested). Both scripts carry ENV values (and, for the agent
	// script, an AGENT bearer token) as literal shell text, so that
	// silently reopens a confidentiality hole on exactly the platform
	// this project is developed on. They're now generated in
	// BuildImages (frompieces.go) instead and travel through stage 1's
	// tmpfs shadow-replay path, where chmod happens for real inside the
	// booted guest kernel -- see T73 and supervisorScriptPath's doc
	// comment. Nothing to do here anymore; normalFiles below no longer
	// contains them either, since splitShadowedFiles already routed
	// them out before this function was called.
	for _, f := range normalFiles {
		files = append(files, namedFile{trimLeadingSlash(f.Path), os.FileMode(f.Perm), f.Data})
	}

	for _, nf := range files {
		if err := writeSquashfsFile(fs, nf.path, nf.perm, nf.data); err != nil {
			_ = tmp.Close() // best-effort; the error above is what's returned
			return "", err
		}
	}

	err = fs.Finalize(squashfs.FinalizeOptions{})
	if err != nil {
		_ = tmp.Close() // best-effort; the error above is what's returned
		return "", fmt.Errorf("finalizing squashfs root: %w", err)
	}

	// go-diskfs's Finalize() doesn't actually round the file up to a
	// sector boundary despite FinalizeOptions.NoPad defaulting to false
	// (pad on) -- verified empirically. Padding only to the nearest
	// 512-byte sector still wasn't enough on its own: the kernel driver
	// went on to fail with "attempt to access beyond end of device"
	// reading the id table, one metadata block (8KB) at a time,
	// regardless of how much of that block the superblock's own
	// bytes_used actually needs -- so the backing storage has to have
	// room for a full trailing metadata block past the last real byte,
	// not just up to the next sector. Rounding up to 64KB comfortably
	// covers that with a few KB of harmless slack, given these images
	// are themselves only ever a few KB to a handful of MB.
	info, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close() // best-effort; the error above is what's returned
		return "", fmt.Errorf("stat squashfs workspace file: %w", err)
	}
	const padTo = 64 * 1024
	if rem := info.Size() % padTo; rem != 0 {
		if err := tmp.Truncate(info.Size() + (padTo - rem)); err != nil {
			_ = tmp.Close() // best-effort; the error above is what's returned
			return "", fmt.Errorf("padding squashfs image: %w", err)
		}
	}

	if err := tmp.Close(); err != nil {
		return "", err
	}

	removeOnError = false // handing the path to the caller now; it owns cleanup
	return tmpPath, nil
}

func writeSquashfsFile(fs *squashfs.FileSystem, pathname string, perm os.FileMode, data []byte) error {
	f, err := fs.OpenFile(pathname, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("opening %s in squashfs workspace: %w", pathname, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing %s in squashfs workspace: %w", pathname, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s in squashfs workspace: %w", pathname, err)
	}
	if err := fs.Chmod(pathname, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", pathname, err)
	}
	return nil
}
