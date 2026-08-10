package compileagent

import (
	"fmt"
	"os"
	"path/filepath"
)

const defaultIptablesVersion = "1.8.8"

// defaultIptablesSHA256 pins iptables-1.8.8.tar.bz2's own published
// hash (verified against
// https://www.netfilter.org/pub/iptables/iptables-1.8.8.tar.bz2.sha256sum
// at implementation time) -- same reasoning as busybox.go's
// defaultBusyboxSHA256: netfilter.org has no per-release PGP
// signatures either, and unlike BusyBox there isn't even a
// Nimbusfile-facing version knob, so this always applies.
const defaultIptablesSHA256 = "71c75889dc710676631553eb1511da0177bbaaf1b551265b912d236c3f51859f"

// IptablesSpec configures the iptables build. Unlike kernel/BusyBox,
// there is no Nimbusfile-facing version knob for this yet -- it's an
// internal implementation detail of the FIREWALL directive, not
// something a Nimbusfile author chooses independently.
type IptablesSpec struct {
	Arch     string
	CacheDir string
}

// FetchIptables downloads and extracts the iptables source, returning
// its directory. Sourced from netfilter.org's own /pub/iptables/
// mirror -- note this is a *different* path than
// netfilter.org/projects/iptables/files/ (which 404s for most
// versions still commonly deployed; verified empirically while
// building this feature).
func FetchIptables(spec IptablesSpec) (FetchResult, error) {
	version := defaultIptablesVersion
	tarball := fmt.Sprintf("iptables-%s.tar.bz2", version)
	dest := fmt.Sprintf("iptables-%s", version)
	url := fmt.Sprintf("https://www.netfilter.org/pub/iptables/%s", tarball)
	result, err := fetchExtract(spec.CacheDir, url, tarball, dest, "bz2", "", defaultIptablesSHA256, false)
	if err != nil {
		return result, err
	}
	result.ResolvedVersion = version
	return result, nil
}

// BuildIptables configures and statically compiles iptables-legacy
// (the netfilter/xtables userspace, "legacy" i.e. non-nftables mode),
// copying the resulting multi-call binary to outDir/iptables.
//
// This needs nothing beyond gcc/bison/flex/pkg-config -- the same
// toolchain already installed for Kbuild itself (see
// internal/assets/data/Dockerfile) -- deliberately built in "legacy"
// mode specifically to avoid nftables' libmnl/libnftnl dependency,
// which does not build with just gcc the way the kernel and BusyBox
// do. --without-kernel skips iptables' own attempt to locate a Linux
// kernel source tree for header generation (unnecessary: the running
// kernel's own installed uapi headers, already present in this
// container for Kbuild's sake, are sufficient).
//
// The resulting binary dispatches on its *first argument* ("iptables",
// "iptables-save", "ip6tables", ...), not on how it was invoked (argv[0]
// via a symlink, BusyBox-style) -- verified empirically
// (`xtables-legacy-multi iptables --version` works with no symlink at
// all) -- which sidesteps this project's one real SquashFS limitation
// (go-diskfs's writer can't create symlinks) entirely: this binary can
// live in the ordinary, genuinely-immutable SquashFS root, not stage 1's
// tmpfs shadow.
func BuildIptables(spec IptablesSpec, srcDir, outDir string) error {
	if _, ok := archTable[spec.Arch]; !ok {
		return fmt.Errorf("unsupported iptables arch %q (supported: amd64, arm64)", spec.Arch)
	}
	env := os.Environ()

	Logf("configuring iptables (static, legacy/non-nftables)")
	configureArgs := []string{
		"--disable-shared", "--enable-static",
		"--disable-nftables", "--disable-bpf-compiler", "--without-kernel",
	}
	if err := run(srcDir, env, "./configure", configureArgs...); err != nil {
		return fmt.Errorf("configure: %w", err)
	}

	jobs := buildJobs()
	Logf("building iptables with %s jobs", jobs)
	// LDFLAGS=-all-static (not plain -static): libtool otherwise drops
	// the flag for this target -- verified empirically, plain -static
	// produced a dynamically-linked binary despite configure succeeding
	// with it; -all-static is libtool's own flag for forcing every
	// linked library (including its internal .la wrappers) static too.
	// CFLAGS hardening (T103): iptables runs as root at boot parsing
	// Nimbusfile-supplied FIREWALL rule text (see T90) -- stack-protector
	// and FORTIFY_SOURCE cost nothing here and cover exactly the class of
	// bug a hostile rule string would try to exploit. RELRO/BIND_NOW
	// (read-only GOT) is deliberately *not* attempted here: libtool's
	// -all-static already fights this build once (see above), and
	// RELRO/BIND_NOW's value is against a *dynamically*-linked GOT being
	// overwritten post-load -- a fully static binary has already closed
	// that specific attack surface by construction, so the flag would be
	// a no-op dressed up as a fix, not a real hardening gap here.
	if err := run(srcDir, env, "make", "-j"+jobs,
		"CFLAGS=-O2 -fstack-protector-strong -D_FORTIFY_SOURCE=2",
		"LDFLAGS=-all-static"); err != nil {
		return fmt.Errorf("make: %w", err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return copyFile(filepath.Join(srcDir, "iptables", "xtables-legacy-multi"), filepath.Join(outDir, "iptables"))
}
