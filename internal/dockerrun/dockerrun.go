// Package dockerrun shells out to the docker CLI to build images and
// run build stages inside a Linux container. Shelling out (rather than
// talking to the daemon socket directly) is deliberate: it works
// unmodified against Docker Desktop on Windows/macOS and native Docker
// Engine on Linux, without juggling npipe vs unix-socket transports or
// vendoring the Docker SDK.
package dockerrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

// containerStopWaitDelay bounds how long Run/BuildImage wait for the
// docker CLI process to exit after a context cancellation (Ctrl-C)
// before force-killing it -- see cmd.WaitDelay's own doc comment for why
// a backstop like this is needed at all: without one, a client process
// that ignores its stdin/stdout pipes closing can hang the cancellation
// forever.
const containerStopWaitDelay = 5 * time.Second

// Mount describes a volume passed to `docker run -v`. Set IsVolume for
// a named Docker volume (e.g. "cnimbus-cache") rather than a host path --
// named volumes must NOT be resolved through filepath.Abs.
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
	IsVolume      bool
}

// RunOptions configures a single `docker run` invocation.
type RunOptions struct {
	Image    string
	Args     []string
	Env      map[string]string
	Mounts   []Mount
	Workdir  string
	Platform string // "linux/amd64" or "linux/arm64"
	// Name, if set, is passed as `docker run --name` -- giving the
	// container a deterministic identity so a context cancellation can
	// `docker rm -f` it directly (see Run's cmd.Cancel). Optional:
	// callers that never cancel (or don't care about orphaned
	// containers on a hard kill) can leave it empty.
	Name string
	// Memory and CPUs, if set, are passed as `docker run --memory`/
	// `--cpus` (T44) -- letting `prepare` bound a build that would
	// otherwise consume the whole host. Empty means Docker's own
	// unbounded default, unchanged from before these fields existed.
	Memory string
	CPUs   string
	// Entrypoint, if set, is passed as `docker run --entrypoint` --
	// the same override ensureVolumeOwnedByInvokingUser already applies
	// by hand (see its own doc comment for why: an image whose
	// ENTRYPOINT is a fixed exec-form binary, e.g. the builder image's
	// ["/opt/cnimbus/thunder"], otherwise takes Args as extra argv on
	// that binary rather than as a shell command). F2's secureboot
	// signer image (internal/secureboot) is the first RunOptions caller
	// that needs this, rather than shelling out to `docker run`
	// directly the way ensureVolumeOwnedByInvokingUser still does.
	Entrypoint string
}

// ErrUnavailable wraps every CheckAvailable failure (T50): missing
// docker CLI, unreachable daemon, or Windows-containers mode. A caller
// (cmd/cnimbus/main.go) can errors.Is against it to map this whole class
// of "the host isn't ready for a Docker-based build" failures to its own
// distinct exit code, separate from a usage error or a verification
// failure -- a CI pipeline retrying on transient failure needs to tell
// those apart.
var ErrUnavailable = errors.New("docker unavailable")

// CheckAvailable verifies the docker CLI is on PATH and the daemon is
// reachable and serving Linux containers. It returns an actionable
// error message when it is not (Docker Desktop not running, or set to
// Windows containers).
func CheckAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("%w: docker CLI not found on PATH — install Docker Desktop (Windows/macOS) or Docker Engine (Linux)", ErrUnavailable)
	}

	out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: docker daemon not reachable — start Docker Desktop and retry: %s", ErrUnavailable, firstLine(string(out)))
	}
	osType := firstLine(string(out))
	if osType != "linux" {
		return fmt.Errorf("%w: docker is running %q containers, cnimbus needs Linux containers — in Docker Desktop, switch to \"Switch to Linux containers\"", ErrUnavailable, osType)
	}
	return nil
}

// Platform builds a `docker --platform` value from cnimbus's own arch
// name ("amd64"/"arm64", matching Go/Docker's GOARCH convention).
func Platform(arch string) string {
	return "linux/" + arch
}

// BuildImage runs `docker build` against contextDir/Dockerfile for the
// given platform, tagging the result as tag. Build output streams live
// to stdout/stderr.
//
// The platform is never left to Docker's own default: cnimbus always
// wants the container running *as* the Nimbusfile's declared
// architecture (native compilation there, no cross-compiler needed),
// not the host's architecture -- on an ARM host building an amd64
// image, or an amd64 host building an arm64 image, Docker Desktop's
// emulation (Rosetta/QEMU) handles the rest transparently.
func BuildImage(ctx context.Context, contextDir, tag, platform string) error {
	absDir, err := filepath.Abs(contextDir)
	if err != nil {
		return fmt.Errorf("resolving build context path: %w", err)
	}
	args := []string{"build"}
	// Empty platform means "let Docker pick its own default" -- used by
	// internal/secureboot's signer image, which signs PE bytes rather
	// than executing native code, so it has no reason to force a
	// specific --platform the way the kernel/BusyBox builder image
	// (always forced to match the Nimbusfile's ARCH) does.
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, "-t", tag, absDir)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = containerStopWaitDelay
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("docker build canceled: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	return nil
}

// ImageDigest returns the local content-addressed ID (e.g.
// "sha256:<hex>") of a previously built/pulled image -- the same value
// `docker images --digests` derives its digest column from. Used to
// record exactly which builder image produced a given `prepare` run's
// pieces (see pieces.json), since the same Dockerfile can still resolve
// a different image if its own base image (e.g. "FROM gcc:16") gets
// updated upstream between two runs.
func ImageDigest(tag string) (string, error) {
	out, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", tag).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %s: %w", tag, firstLine(string(out)), err)
	}
	return firstLine(string(out)), nil
}

// Run executes `docker run --rm` with the given image, mounts, env and
// args, streaming output live to stdout/stderr. Honors ctx cancellation
// (see cmd.Cancel below) -- callers that want an orphaned container
// cleaned up on cancellation should set opts.Name.
func Run(ctx context.Context, opts RunOptions) error {
	platform := opts.Platform
	if platform == "" {
		platform = Platform("amd64")
	}

	// T48: see the --user doc comment below for the full history. uid 0
	// (the invoking user is already root -- true of most Linux CI images)
	// needs neither the fixup nor --user: there is no ownership mismatch
	// to create in the first place.
	uid, gid := os.Getuid(), os.Getgid()
	dropPrivileges := runtime.GOOS == "linux" && uid > 0
	if dropPrivileges {
		for _, m := range opts.Mounts {
			if !m.IsVolume {
				continue
			}
			if err := ensureVolumeOwnedByInvokingUser(ctx, opts.Image, platform, m.HostPath, m.ContainerPath, uid, gid); err != nil {
				return fmt.Errorf("fixing ownership of volume %q before dropping privileges: %w", m.HostPath, err)
			}
		}
	}

	args := []string{"run", "--rm", "--platform", platform}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.Entrypoint != "" {
		args = append(args, "--entrypoint", opts.Entrypoint)
	}
	// T44: previously just --rm --platform plus mounts/env/workdir --
	// full default capability set, no PID limit, no resource bound,
	// against a read-write bind mount of the host's own pieces
	// directory. A tampered-but-hash-matching tarball (T4 narrows which
	// tarballs can arrive, not what a legitimately-hashed build script
	// does once running -- Kbuild executes shell by design) got
	// full-cap root execution. --network=none is deliberately NOT added:
	// Thunder fetches sources itself over the network. The --cap-add
	// list is exactly what a kernel/BusyBox `make`+`make install`
	// actually needs (chown/chmod/setuid-setgid-bit handling during
	// install) -- narrower than this and `make install` breaks.
	args = append(args,
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		"--pids-limit=4096",
	)
	// T48: an unconditional --user=<uid>:<gid> on Linux was tried once
	// and reverted -- a real `cnimbus prepare` run from a genuine Linux
	// host with a non-root invoking user (WSL, uid 1000) failed outright
	// with "mkdir /cache/src: permission denied", because the named
	// Docker volume this mounts at /cache (cnimbus-cache-<arch>, see
	// prepare.go) is created fresh with root ownership by the Docker
	// daemon. Fixed for real this time with the volume-ownership-fixup
	// step that failed attempt's own doc comment proposed: on Linux,
	// with a non-root invoking user, ensureVolumeOwnedByInvokingUser runs
	// a disposable container (opts.Image itself -- already pulled/built,
	// no extra image to fetch) that chowns each named volume mount to
	// the invoking uid/gid *before* the real run, skipping the chown
	// entirely when the volume is already correctly owned (common case:
	// every run after the first). Only then is --user=<uid>:<gid> added
	// to the real run -- so the fix and the flag it depends on land
	// together, not the flag alone as last time.
	// One --cap-add per capability, not a comma-joined list (docker run
	// --cap-add takes exactly one capability name per flag occurrence --
	// verified empirically: a single comma-joined value is rejected
	// outright with "invalid CapAdd: unknown capability").
	for _, cap := range []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "SETGID", "SETUID"} {
		args = append(args, "--cap-add="+cap)
	}
	if dropPrivileges {
		args = append(args, "--user", fmt.Sprintf("%d:%d", uid, gid))
	}
	if opts.Memory != "" {
		args = append(args, "--memory", opts.Memory)
	}
	if opts.CPUs != "" {
		args = append(args, "--cpus", opts.CPUs)
	}

	for _, m := range opts.Mounts {
		hostSide := m.HostPath
		if !m.IsVolume {
			hostAbs, err := filepath.Abs(m.HostPath)
			if err != nil {
				return fmt.Errorf("resolving mount path %q: %w", m.HostPath, err)
			}
			hostSide = hostAbs
		}
		spec := hostSide + ":" + m.ContainerPath
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}

	// Sorted, not ranged directly over the map: two `prepare` runs of the
	// identical Nimbusfile previously produced two different `docker run`
	// command lines (Go's map iteration order is randomized), so neither
	// the printed command nor a future provenance record of it was
	// reproducible text (T46).
	envKeys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "-e", k+"="+opts.Env[k])
	}

	if opts.Workdir != "" {
		args = append(args, "-w", opts.Workdir)
	}

	args = append(args, opts.Image)
	args = append(args, opts.Args...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	// Killing the docker CLI *client* process on cancellation does not
	// stop the container: without -d, this is still a foreground client
	// attached to a container the daemon runs server-side, and the two
	// are not connected by anything a SIGKILL to the client propagates
	// through. Ctrl-C during a 20-minute kernel build previously left
	// the container running, still burning host CPU, with `cnimbus`
	// itself already gone (T45) -- explicitly asking the daemon to
	// remove the named container is what actually stops the work.
	cmd.Cancel = func() error {
		if opts.Name != "" {
			_ = exec.Command("docker", "rm", "-f", opts.Name).Run() // best-effort; nothing left to report the outcome to
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = containerStopWaitDelay
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("docker run canceled: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("docker run failed: %w", err)
	}
	return nil
}

// ensureVolumeOwnedByInvokingUser chowns a named Docker volume to uid:gid
// before Run's real, unprivileged (--user) invocation touches it (T48).
// Docker creates a fresh named volume with root ownership regardless of
// which user later mounts it, so an unprivileged --user cannot write
// into it at all on first use -- discovered via a real failure
// ("mkdir /cache/src: permission denied") from a genuine non-root Linux
// host. Runs a disposable container from image (whatever the real build
// already uses -- no extra image pull) that skips the chown entirely
// when the volume's top-level ownership already matches (every run
// after the volume's first use), so this doesn't cost a full recursive
// directory walk on every single invocation once the cache is warm.
func ensureVolumeOwnedByInvokingUser(ctx context.Context, image, platform, volume, containerPath string, uid, gid int) error {
	shCmd := fmt.Sprintf(
		`u=$(stat -c %%u %s 2>/dev/null); [ "$u" = "%d" ] || chown -R %d:%d %s`,
		containerPath, uid, uid, gid, containerPath)
	// --entrypoint sh is required, not optional: image's own ENTRYPOINT is
	// ["/opt/cnimbus/thunder"] (exec form), so without this override
	// "sh -c shCmd" would land as extra argv on thunder itself rather than
	// actually running as a shell command -- thunder would then fail on
	// its own required env vars (CNIMBUS_KERNEL_VERSION etc., none of
	// which this disposable chown container sets) instead of chowning
	// anything. Discovered via a real failure on a genuine non-root Linux
	// host (WSL2): every `prepare` run aborted here with "missing
	// required env var CNIMBUS_KERNEL_VERSION" before ever reaching the
	// real build.
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", platform,
		"-v", volume+":"+containerPath, "--entrypoint", "sh", image, "-c", shCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chown volume %s (via %s): %w", volume, image, err)
	}
	return nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' || c == '\r' {
			return s[:i]
		}
	}
	return s
}
