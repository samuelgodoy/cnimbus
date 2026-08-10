package compileagent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const defaultBusyboxVersion = "1.36.1"

// defaultBusyboxSHA256 pins busybox-1.36.1.tar.bz2's own published
// hash (verified against
// https://busybox.net/downloads/busybox-1.36.1.tar.bz2.sha256 at
// implementation time) -- busybox.net has no per-release PGP
// signatures the way kernel.org does (see FetchBusybox's own doc
// comment), so this is the only integrity check available for the
// *default* pinned version. Only checked when spec.Version is empty
// (i.e. this exact default is what's being built) or explicitly equals
// defaultBusyboxVersion -- an explicitly different --busybox version
// has no corresponding pinned hash to check against.
const defaultBusyboxSHA256 = "b8cc24c9574d809e7279c3be349795c5d5ceb6fdf19ca709f80cde50e47de314"

// BusyboxSpec configures the BusyBox build.
type BusyboxSpec struct {
	Version  string // defaults to defaultBusyboxVersion if empty
	Arch     string // "amd64" or "arm64"
	CacheDir string
}

// FetchBusybox downloads and extracts the BusyBox source, returning
// its directory (and provenance data for cmd/thunder's pieces.json --
// see FetchResult, notably ResolvedVersion since spec.Version may be
// empty).
func FetchBusybox(spec BusyboxSpec) (FetchResult, error) {
	version := spec.Version
	if version == "" {
		version = defaultBusyboxVersion
	}
	tarball := fmt.Sprintf("busybox-%s.tar.bz2", version)
	dest := fmt.Sprintf("busybox-%s", version)
	url := fmt.Sprintf("https://busybox.net/downloads/%s", tarball)
	// No sigURL: busybox.net doesn't publish per-release detached PGP
	// signatures the way kernel.org does (no discoverable WKD-published
	// signer key either) -- there is nothing to verify against. A pinned
	// SHA-256 is checked instead, but only for the exact default version
	// this constant matches -- an explicitly different --busybox version
	// has no corresponding pinned hash.
	expectedSHA256 := ""
	if version == defaultBusyboxVersion {
		expectedSHA256 = defaultBusyboxSHA256
	}
	result, err := fetchExtract(spec.CacheDir, url, tarball, dest, "bz2", "", expectedSHA256, false)
	if err != nil {
		return result, err
	}
	result.ResolvedVersion = version
	return result, nil
}

// BuildBusybox configures BusyBox for a fully static build with plain
// gcc (the container always runs natively as spec.Arch -- see
// archInfo's doc comment -- so no cross-compiler prefix is needed) and
// installs the applet tree into outDir/rootfs.
func BuildBusybox(spec BusyboxSpec, srcDir, outDir string) error {
	if _, ok := archTable[spec.Arch]; !ok {
		return fmt.Errorf("unsupported busybox arch %q (supported: amd64, arm64)", spec.Arch)
	}
	cc, ar := "gcc", "ar"
	makeArgs := []string{"CC=" + cc, "AR=" + ar}
	// SOURCE_DATE_EPOCH: BusyBox's own kconfig machinery
	// (scripts/kconfig/confdata.c's conf_write) honors this standard
	// reproducible-builds env var for the timestamp it bakes into
	// include/autoconf.h -- which is what ends up in the version banner
	// ("busybox --help") -- falling back to the wall-clock build time
	// otherwise. "0" (the Unix epoch), same reproducibility intent as
	// the kernel build's KBUILD_BUILD_TIMESTAMP=@0.
	env := append(os.Environ(), "SOURCE_DATE_EPOCH=0")

	cfgPath := filepath.Join(srcDir, ".config")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		Logf("configuring busybox (defconfig + CONFIG_STATIC=y)")
		if err := run(srcDir, env, "make", append(append([]string{}, makeArgs...), "-s", "defconfig")...); err != nil {
			return err
		}
		if err := setStaticConfig(cfgPath); err != nil {
			return err
		}
		if err := disableTCApplet(cfgPath); err != nil {
			return err
		}
		if err := setHardeningFlags(cfgPath); err != nil {
			return err
		}
		oldconfigArgs := append(append([]string{}, makeArgs...), "-s", "oldconfig")
		if err := runWithInfiniteNewlineStdin(srcDir, env, "make", oldconfigArgs...); err != nil {
			return fmt.Errorf("make oldconfig: %w", err)
		}
	}

	jobs := buildJobs()
	Logf("building busybox (static, %s) with %s jobs", cc, jobs)
	buildArgs := append(append([]string{}, makeArgs...), "-s", "-j"+jobs)
	if err := run(srcDir, env, "make", buildArgs...); err != nil {
		return err
	}

	rootfsDir := filepath.Join(outDir, "rootfs")
	if err := os.RemoveAll(rootfsDir); err != nil {
		return err
	}
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return err
	}
	Logf("installing busybox into %s", rootfsDir)
	installArgs := append(append([]string{}, makeArgs...), "-s", "CONFIG_PREFIX="+rootfsDir, "install")
	return run(srcDir, env, "make", installArgs...)
}

// ExportPieces reads the installed BusyBox tree at rootfsDir (a real
// Linux filesystem -- this must run where os.Lstat/Readlink correctly
// report symlinks, i.e. inside the build sandbox, never through a
// Windows-side Docker Desktop bind mount) and returns the binary plus
// a manifest of every applet symlink, in the format internal/pieces
// expects: relative path, a tab, the symlink target, one per line.
func ExportPieces(rootfsDir string) (busyboxBinary []byte, manifest []byte, err error) {
	busyboxBinary, err = os.ReadFile(filepath.Join(rootfsDir, "bin", "busybox"))
	if err != nil {
		return nil, nil, err
	}

	var buf bytes.Buffer
	walkErr := filepath.Walk(rootfsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(rootfsDir, p)
		if err != nil {
			return err
		}
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&buf, "%s\t%s\n", filepath.ToSlash(rel), target)
		return nil
	})
	if walkErr != nil {
		return nil, nil, fmt.Errorf("walking %s: %w", rootfsDir, walkErr)
	}
	return busyboxBinary, buf.Bytes(), nil
}

// setStaticConfig flips BusyBox's generated .config from dynamic to
// static linking. Equivalent to:
//
//	sed -i 's/# CONFIG_STATIC is not set/CONFIG_STATIC=y/' .config
func setStaticConfig(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	data = bytes.Replace(data,
		[]byte("# CONFIG_STATIC is not set"),
		[]byte("CONFIG_STATIC=y"),
		1,
	)
	return os.WriteFile(cfgPath, data, 0o644)
}

// disableTCApplet turns off BusyBox's "tc" (traffic control) applet.
// Its source (networking/tc.c) reads kernel uapi CBQ qdisc struct
// definitions (TCA_CBQ_RATE, struct tc_cbq_lssopt, ...) that newer
// distros' linux-libc-dev headers no longer ship -- CBQ was removed
// from the kernel itself years ago. cnimbus doesn't use tc, so disabling
// it is simpler and more honest than patching 15-year-old dead code.
func disableTCApplet(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	data = bytes.Replace(data, []byte("CONFIG_TC=y"), []byte("# CONFIG_TC is not set"), 1)
	return os.WriteFile(cfgPath, data, 0o644)
}

// setHardeningFlags (T103) patches BusyBox's own CONFIG_EXTRA_CFLAGS/
// CONFIG_EXTRA_LDFLAGS -- verified via BusyBox's own Makefile.flags
// (`ifneq ($(CONFIG_EXTRA_CFLAGS),) CFLAGS += ...`) that these are the
// real knobs, not a `make EXTRA_CFLAGS=...` command-line variable (a
// first attempt at that produced zero __stack_chk_fail references in
// the built binary -- caught only by inspecting the real output with
// readelf/nm, not by reading the code, since the build itself doesn't
// warn about an unused make variable).
//
// BusyBox is PID 1, every applet, and /bin/sh in every image this
// project ships -- the most privileged userspace binary here, and the
// one an attacker who breaches the app actually reaches (unlike the
// kernel, which T30 already hardened).
func setHardeningFlags(cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	data = bytes.Replace(data,
		[]byte(`CONFIG_EXTRA_CFLAGS=""`),
		[]byte(`CONFIG_EXTRA_CFLAGS="-fstack-protector-strong -D_FORTIFY_SOURCE=2"`),
		1,
	)
	data = bytes.Replace(data,
		[]byte(`CONFIG_EXTRA_LDFLAGS=""`),
		[]byte(`CONFIG_EXTRA_LDFLAGS="-Wl,-z,relro,-z,now"`),
		1,
	)
	return os.WriteFile(cfgPath, data, 0o644)
}

// runWithInfiniteNewlineStdin runs a command feeding it an unbounded
// stream of newlines on stdin -- equivalent to `yes "" | cmd`, which
// BusyBox's `make oldconfig` needs so every newly-visible Kconfig
// prompt (a side effect of flipping CONFIG_STATIC) accepts its default
// answer instead of blocking on input.
func runWithInfiniteNewlineStdin(dir string, env []string, name string, args ...string) error {
	pr, pw := io.Pipe()
	go func() {
		buf := bytes.Repeat([]byte("\n"), 4096)
		for {
			if _, err := pw.Write(buf); err != nil {
				return
			}
		}
	}()
	// pw.Close's error is never interesting here: it's what stops the
	// goroutine above (its next Write fails once the pipe is closed),
	// not a signal about anything this function's caller needs to know.
	defer func() { _ = pw.Close() }()

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = pr
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	_ = pw.Close()
	return err
}
