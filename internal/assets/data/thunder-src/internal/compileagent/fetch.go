// Package compileagent is the code behind Thunder, the program that
// actually runs inside the Linux build sandbox to compile the kernel
// and BusyBox. It is a plain Go program -- compiled fresh for the
// target architecture by `cnimbus prepare` and copied into the builder
// image -- not a shell script: the only shell-adjacent thing it
// invokes is Kbuild's own bundled merge_config.sh (part of the kernel
// source being built, not authored by cnimbus), because Kconfig's
// dependency resolution has no reasonable reimplementation.
package compileagent

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[thunder] "+format+"\n", args...)
}

// buildJobs returns the `-j` parallelism this build sandbox uses, as a
// string ready to append to a make -j flag (T101): kernel.go, busybox.go
// and iptables.go each independently computed
// strconv.Itoa(runtime.NumCPU()) -- one shared definition means a future
// change (like T72's memory bound below) only has to happen once.
//
// CNIMBUS_JOBS (T72), when set, wins outright -- it's threaded through
// from `cnimbus prepare --jobs`, an explicit user override.
//
// Otherwise the default is min(NumCPU, cgroup memory bound, cgroup CPU
// quota bound), each at least 1. Kbuild's own peak-per-job compile/link
// memory is roughly 0.5-1.5 GB, so a wide-CPU/low-memory container (a
// Docker Desktop VM with more vCPUs configured than RAM headroom, or a
// 32-core/16GB CI runner) building at full -j$(NumCPU) is a real,
// previously-undiagnosable OOM kill partway through a ten-minute build.
// runtime.NumCPU() honors --cpuset-cpus but NOT --cpus (CFS quota), so a
// user who limits CPU via --cpus alone still got a full-width -j before
// this: cpu.max is read explicitly to close that gap too.
func buildJobs() string {
	n := runtime.NumCPU()
	if v := os.Getenv("CNIMBUS_JOBS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			Logf("build parallelism: -j%d (CNIMBUS_JOBS override)", parsed)
			return strconv.Itoa(parsed)
		}
		Logf("CNIMBUS_JOBS=%q is not a positive integer, ignoring", v)
	}

	jobs := n
	if memGB := cgroupMemoryLimitGB(); memGB > 0 && memGB < jobs {
		jobs = memGB
	}
	if cpuQuota := cgroupCPUQuota(); cpuQuota > 0 && cpuQuota < jobs {
		jobs = cpuQuota
	}
	if jobs < 1 {
		jobs = 1
	}
	Logf("build parallelism: -j%d (of %d logical CPUs)", jobs, n)
	return strconv.Itoa(jobs)
}

// cgroupMemoryLimitGB returns the container's memory limit in whole GB
// (rounded down, minimum 1 once a limit is known), or 0 if no limit
// could be determined (unlimited, or the file couldn't be read -- e.g.
// not actually running under cgroups v2, or a host with no
// /sys/fs/cgroup at all). Falls back to /proc/meminfo's MemTotal only
// when the cgroup file itself says "max" (no limit set), matching what
// T72 asked for.
func cgroupMemoryLimitGB() int {
	data, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0
	}
	val := strings.TrimSpace(string(data))
	if val == "max" {
		return memInfoTotalGB()
	}
	bytes, err := strconv.ParseInt(val, 10, 64)
	if err != nil || bytes <= 0 {
		return 0
	}
	return gbFloor(bytes)
}

func memInfoTotalGB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return gbFloor(kb * 1024)
		}
	}
	return 0
}

// cgroupCPUQuota returns the whole-CPU count implied by cgroups v2's
// cpu.max ("$MAX $PERIOD", both in microseconds), rounded up, or 0 if
// unlimited ("max") or unreadable/unparsable.
func cgroupCPUQuota() int {
	data, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[0] == "max" {
		return 0
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || quota <= 0 {
		return 0
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period <= 0 {
		return 0
	}
	// Round up: a quota of 1.5 CPUs should still get 2 jobs' worth of
	// headroom rather than being truncated down to 1.
	return int((quota + period - 1) / period)
}

func gbFloor(bytes int64) int {
	gb := int(bytes / (1024 * 1024 * 1024))
	if gb < 1 {
		gb = 1
	}
	return gb
}

// FetchResult is what fetchExtract (and its FetchKernel/FetchBusybox/
// FetchIptables callers) return alongside the extracted source
// directory -- provenance data cmd/thunder records into pieces.json
// (see cmd/thunder/main.go), since none of it survives past this one
// process's lifetime otherwise.
type FetchResult struct {
	Dir string // the extracted source directory (same as fetchExtract's old sole return value)
	// ResolvedVersion/SourceURL matter because BusyBox/iptables specs
	// may leave Version empty ("use the built-in default") -- callers
	// need the version/URL actually used, not merely the input they
	// happened to pass in.
	ResolvedVersion string
	SourceURL       string
	TarballSHA256   string
	// PGPVerified/PGPSignedBy are the zero value unless sigURL was set
	// and verification actually ran (kernel.org only -- see
	// FetchKernel; BusyBox/iptables have no equivalent signatures to
	// check against).
	PGPVerified bool
	PGPSignedBy string
}

// fetchExtract downloads url into cacheDir/tarballName (skipping if
// already present) and extracts it into cacheDir/destDir (skipping if
// already extracted), stripping the tarball's single top-level
// directory component. kind selects the decompressor: "xz" or "bz2".
//
// sigURL, if non-empty, is a detached-PGP-signature URL (kernel.org's
// "<version>.tar.sign" convention) checked against known kernel.org
// signer keys (see VerifyKernelTarball) before extraction -- skipped
// entirely (with a loud warning, not silently) if insecureSkipVerify is
// set, which is the escape hatch for offline mirrors that don't carry a
// matching .sign file, or a release kernel.org itself never signed
// (mainline/rc tarballs often have no published signature at all --
// kernelinfo.Resolve leaves Resolved.PGP empty in that case, which
// callers should treat the same as insecureSkipVerify for that release).
//
// expectedSHA256, if non-empty, is checked against the tarball's own
// hash (a pinned constant for BusyBox/iptables -- see busybox.go/
// iptables.go -- neither of which has anything like kernel.org's PGP
// signatures to verify against instead) -- checked every time, not
// only right after a fresh download, so a corrupted or tampered
// *cached* tarball from a previous run is caught too, not silently
// reused forever. PGP verification (when sigURL is set) runs every
// time for the same reason, not only on the first extraction -- a
// FetchResult's PGPVerified/PGPSignedBy must be trustworthy on a cache
// hit too, since cmd/thunder writes it into pieces.json regardless of
// whether this run extracted anything or reused an existing directory.
func fetchExtract(cacheDir, url, tarballName, destDir, kind, sigURL, expectedSHA256 string, insecureSkipVerify bool) (FetchResult, error) {
	if err := os.MkdirAll(filepath.Join(cacheDir, "src"), 0o755); err != nil {
		return FetchResult{}, err
	}
	tarballPath := filepath.Join(cacheDir, "src", tarballName)
	destPath := filepath.Join(cacheDir, "src", destDir)

	if _, err := os.Stat(tarballPath); os.IsNotExist(err) {
		Logf("downloading %s", url)
		if err := downloadFile(url, tarballPath); err != nil {
			return FetchResult{}, err
		}
	} else {
		Logf("using cached download %s", tarballPath)
	}

	if expectedSHA256 != "" {
		if err := verifyTarballSHA256(tarballPath, expectedSHA256); err != nil {
			_ = os.Remove(tarballPath) // best-effort: don't leave a bad download around to be silently reused next run
			return FetchResult{}, err
		}
	}

	tarballSHA256, err := sha256File(tarballPath)
	if err != nil {
		return FetchResult{}, err
	}
	result := FetchResult{Dir: destPath, SourceURL: url, TarballSHA256: tarballSHA256}

	if sigURL != "" && !insecureSkipVerify {
		Logf("verifying PGP signature against %s", sigURL)
		signedBy, err := VerifyKernelTarball(tarballPath, sigURL)
		if err != nil {
			_ = os.Remove(tarballPath) // best-effort: don't leave an unverified download around to be silently reused next run
			return FetchResult{}, fmt.Errorf("%w (pass --insecure-skip-kernel-verify to bypass -- NOT recommended for anything but a trusted offline mirror)", err)
		}
		Logf("signature verified, signed by: %s", signedBy)
		result.PGPVerified = true
		result.PGPSignedBy = signedBy
	} else if sigURL == "" {
		Logf("WARNING: no PGP signature URL known for this release -- kernel tarball is unverified")
	} else {
		Logf("WARNING: --insecure-skip-kernel-verify set -- skipping PGP signature check")
	}

	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		Logf("extracting %s", tarballPath)
		tmp := destPath + ".tmp"
		_ = os.RemoveAll(tmp) // best-effort: clear any stale partial extraction from a previous failed run
		if err := extractTarball(tarballPath, tmp, kind); err != nil {
			_ = os.RemoveAll(tmp) // best-effort cleanup; the extraction error above is what's returned
			return FetchResult{}, err
		}
		if err := os.Rename(tmp, destPath); err != nil {
			return FetchResult{}, err
		}
	}
	return result, nil
}

// sha256File hashes the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyTarballSHA256 hashes path and compares it (case-insensitively)
// against want, a lowercase-hex-encoded SHA-256.
func verifyTarballSHA256(path, want string) error {
	got, err := sha256File(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA-256 mismatch for %s: expected %s, got %s -- the file does not match the pinned "+
			"hash (corrupted download, or the source was tampered with)", path, want, got)
	}
	return nil
}

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %s", url, resp.Status)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp) // best-effort: the download error above is what's returned
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// extractTarball extracts a .tar.xz, .tar.bz2, or .tar.gz archive into
// destDir, stripping the single top-level directory every
// kernel.org/busybox.net/w1.fi/libnl release tarball is packed with
// (mirrors "tar --strip-components=1").
func extractTarball(tarballPath, destDir, kind string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader
	switch kind {
	case "xz":
		xr, err := xz.NewReader(f)
		if err != nil {
			return fmt.Errorf("xz: %w", err)
		}
		r = xr
	case "bz2":
		r = bzip2.NewReader(f)
	case "gz":
		// wpa_supplicant (w1.fi) and libnl (github.com/thom311/libnl)
		// release tarballs are both plain .tar.gz -- gzip.NewReader is
		// the stdlib's own decoder, same tier of trust as compress/bzip2
		// above (both already vendored via the standard library, no
		// extra dependency).
		gr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = gr.Close() }()
		r = gr
	default:
		return fmt.Errorf("unknown archive kind %q", kind)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	tr := tar.NewReader(r)
	// Directory mtimes are restored only after every file is extracted
	// (below) -- creating a file inside a directory bumps that
	// directory's own mtime right back up, so setting it any earlier
	// would just get overwritten by the extraction that follows.
	type dirTime struct {
		path  string
		mtime time.Time
	}
	var dirTimes []dirTime

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		relPath := stripFirstComponent(hdr.Name)
		if relPath == "" {
			continue
		}
		target, err := safeJoin(destDir, filepath.FromSlash(relPath))
		if err != nil {
			return fmt.Errorf("tar entry %q: %w", hdr.Name, err)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			dirTimes = append(dirTimes, dirTime{target, hdr.ModTime})
		case tar.TypeReg:
			// A single kernel/BusyBox source file is never remotely
			// this large; declining a header that claims otherwise
			// before extracting caps how much a maliciously (or just
			// corruptly) huge declared size -- a compressed-in-flight
			// decompression bomb -- can inflate onto disk.
			if hdr.Size > maxExtractedFileSize {
				return fmt.Errorf("tar entry %q declares a size of %d bytes, exceeding the %d-byte per-file limit",
					hdr.Name, hdr.Size, maxExtractedFileSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode&0o777))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { // #nosec G110 -- tr is tar.Reader, which itself never hands back more than hdr.Size bytes for this entry, and hdr.Size was already bounds-checked above
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("closing %s: %w", target, err)
			}
			// Preserve the tarball's own mtime instead of leaving
			// whatever moment extraction happened to run at: autotools
			// build systems (e.g. iptables' ./configure && make --
			// internal/compileagent's BuildIptables) compare
			// Makefile.am/configure.ac against their generated
			// Makefile.in/configure by mtime, and every file landing at
			// essentially "now" (in whatever order tar.Reader happened
			// to yield them) can make a generated file look older than
			// its source, triggering an automake/autoconf re-run this
			// container has no reason to carry (verified empirically --
			// this is exactly what broke iptables' build before this
			// fix). The kernel and BusyBox don't use autotools, so this
			// was never visible there.
			if err := os.Chtimes(target, hdr.ModTime, hdr.ModTime); err != nil {
				return fmt.Errorf("restoring mtime on %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target) // best-effort: fine if it didn't already exist
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}

	for _, d := range dirTimes {
		if err := os.Chtimes(d.path, d.mtime, d.mtime); err != nil {
			return fmt.Errorf("restoring mtime on %s: %w", d.path, err)
		}
	}
	return nil
}

// maxExtractedFileSize bounds a single tar entry's declared size during
// extraction (see the TypeReg case above) -- generous enough for any
// real kernel or BusyBox source file, small enough to bound how much a
// maliciously (or corruptly) inflated size claim can write to disk.
const maxExtractedFileSize = 1 << 30 // 1 GiB

// safeJoin joins base and rel, rejecting any rel (e.g. containing "..")
// whose cleaned, resolved result would land outside base -- a tarball
// entry named "../../../etc/passwd" (or an absolute path) must not be
// able to write outside destDir, whether that tarball came from a
// compromised mirror or a corrupted download.
func safeJoin(base, rel string) (string, error) {
	target := filepath.Join(base, rel)
	baseClean := filepath.Clean(base) + string(filepath.Separator)
	if target != filepath.Clean(base) && !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), baseClean) {
		return "", fmt.Errorf("path escapes extraction directory: %q", rel)
	}
	return target, nil
}

func stripFirstComponent(name string) string {
	for i, c := range name {
		if c == '/' {
			return name[i+1:]
		}
	}
	return ""
}
