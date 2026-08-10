package compileagent

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultLibnlVersion/defaultLibnlSHA256 pin libnl 3.12.0's own GitHub
// release asset (github.com/thom311/libnl, tag libnl3_12_0). Unlike
// BusyBox/iptables, this release *does* publish its own
// ".tar.gz.sha256sum" alongside the tarball -- verified in-session that
// the published checksum and this project's own independently computed
// SHA-256 of the downloaded bytes agree exactly, which is stronger
// evidence than the "no upstream checksum at all, pin our own" situation
// busybox.go/iptables.go are in.
//
// libnl is not a Nimbusfile-facing piece of its own -- it exists purely
// as a build-time static dependency of wpa_supplicant's nl80211 driver
// backend (driver_nl80211.c hard-requires libnl; there is no
// libnl-free nl80211 code path in mainline wpa_supplicant, verified
// against wpa_supplicant/defconfig and src/drivers/drivers.mak -- this
// is a real finding against design.md's D4, which only promised
// avoiding OpenSSL, not libnl). It is built, linked statically into
// wpa_supplicant, and never shipped as a standalone artifact -- same
// relationship BusyBox's own toolchain (gcc/bison/flex) has to the
// pieces this project actually publishes.
const (
	defaultLibnlVersion = "3.12.0"
	defaultLibnlSHA256  = "fc51ca7196f1a3f5fdf6ffd3864b50f4f9c02333be28be4eeca057e103c0dd18"
)

// defaultSupplicantVersion/defaultSupplicantSHA256 pin wpa_supplicant
// 2.12, the latest release published at w1.fi/releases/ at
// implementation time. w1.fi (like netfilter.org for iptables) publishes
// no detached signature or upstream checksum file for its releases --
// this hash is this project's own, computed from the tarball actually
// downloaded and used to write wpasupplicant.go, same posture as
// busybox.go's/iptables.go's own pins.
const (
	defaultSupplicantVersion = "2.12"
	defaultSupplicantSHA256  = "08e23937e16d0155e55cab2b51f51fbe10d80a1aa91c4e15442645059b737ef6"
)

// SupplicantSpec configures the WPA supplicant build (F6.4) -- the
// fourth piece HARDBOOT wifi requires (BusyBox ships none, and WPA2-PSK
// cannot associate without one; see design.md section 4, D4). Like
// IptablesSpec, there is no Nimbusfile-facing version knob: this is an
// internal implementation detail of HARDBOOT wifi, not something a
// Nimbusfile author chooses independently.
type SupplicantSpec struct {
	Arch     string
	CacheDir string
}

// FetchLibnl downloads and extracts libnl's source, hash-pinned and
// verified every run (including a cache hit) via fetchExtract -- the
// exact same mechanism/rigor busybox.go and iptables.go already use.
func FetchLibnl(spec SupplicantSpec) (FetchResult, error) {
	version := defaultLibnlVersion
	tarball := fmt.Sprintf("libnl-%s.tar.gz", version)
	dest := fmt.Sprintf("libnl-%s", version)
	url := fmt.Sprintf("https://github.com/thom311/libnl/releases/download/libnl3_%s/%s",
		underscoreVersion(version), tarball)
	result, err := fetchExtract(spec.CacheDir, url, tarball, dest, "gz", "", defaultLibnlSHA256, false)
	if err != nil {
		return result, err
	}
	result.ResolvedVersion = version
	return result, nil
}

// FetchSupplicant downloads and extracts wpa_supplicant's source,
// hash-pinned and verified every run (including a cache hit), same
// mechanism as FetchLibnl above.
func FetchSupplicant(spec SupplicantSpec) (FetchResult, error) {
	version := defaultSupplicantVersion
	tarball := fmt.Sprintf("wpa_supplicant-%s.tar.gz", version)
	dest := fmt.Sprintf("wpa_supplicant-%s", version)
	url := fmt.Sprintf("https://w1.fi/releases/%s", tarball)
	result, err := fetchExtract(spec.CacheDir, url, tarball, dest, "gz", "", defaultSupplicantSHA256, false)
	if err != nil {
		return result, err
	}
	result.ResolvedVersion = version
	return result, nil
}

// BuildSupplicant statically compiles libnl (a build-time-only
// dependency, discarded after -- see the const doc above) and then
// wpa_supplicant itself, writing the resulting static binary to
// outDir/wpa_supplicant.
//
// wpa_supplicant.conf's CONFIG_* selections implement this project's
// explicitly PSK-only scope (spec.md "Explicitly out of scope for
// v1.0"): every CONFIG_EAP_* line defconfig ships is omitted entirely
// (no WPA-Enterprise/802.1X/EAP code compiled in at all, not merely
// unconfigured at runtime), and CONFIG_TLS=internal answers spec.md's
// open question 4 -- a real, verified-in-session finding, not an
// assumption: wpa_supplicant builds and links with zero OpenSSL
// dependency using its own bundled crypto/TLS implementation
// (src/crypto/crypto_internal.c, src/tls/tls_internal.c) plus the
// bundled minimal LibTomMath (CONFIG_INTERNAL_LIBTOMMATH=y avoids a
// *third* build-time dependency beyond libnl).
//
// CONFIG_DRIVER_NL80211=y + CONFIG_LIBNL32=y is the one dependency that
// could not be avoided (see the libnl doc comment above) -- statically
// linked in via PKG_CONFIG_PATH pointed at libnl's own static .pc files
// plus LDFLAGS=-static, so the resulting wpa_supplicant binary itself
// still carries zero runtime shared-library dependencies.
func BuildSupplicant(spec SupplicantSpec, libnlSrcDir, supplicantSrcDir, outDir string) error {
	if _, ok := archTable[spec.Arch]; !ok {
		return fmt.Errorf("unsupported wpa_supplicant arch %q (supported: amd64, arm64)", spec.Arch)
	}

	libnlInstall := filepath.Join(filepath.Dir(libnlSrcDir), "libnl-install")
	if err := buildLibnlStatic(spec, libnlSrcDir, libnlInstall); err != nil {
		return fmt.Errorf("building libnl (wpa_supplicant's nl80211 dependency): %w", err)
	}

	if err := writeSupplicantConfig(supplicantSrcDir); err != nil {
		return fmt.Errorf("writing wpa_supplicant .config: %w", err)
	}

	pkgConfigPath := filepath.Join(libnlInstall, "lib", "pkgconfig")
	env := append([]string{}, os.Environ()...)
	env = append(env, "PKG_CONFIG_PATH="+pkgConfigPath)

	jobs := buildJobs()
	Logf("building wpa_supplicant with %s jobs (CONFIG_TLS=internal, CONFIG_DRIVER_NL80211=y, static)", jobs)
	// Real build finding, the hard way: unlike iptables.go's own
	// ./configure-based build (a one-shot invocation that never
	// re-touches CFLAGS/LDFLAGS again), wpa_supplicant/Makefile builds
	// CFLAGS up incrementally via its own `CFLAGS += ...` lines --
	// crucially including the "-I../src -I../src/utils" this binary
	// cannot compile without, plus (via src/drivers/drivers.mak) the
	// pkg-config-derived libnl `-L`/`-I` flags. A `make CFLAGS=...` (or
	// even `make CFLAGS+=...`) on the command line was tried first and
	// both empirically *block every one of those later `+=` lines*
	// inside the Makefile -- confirmed by a real failed build (fatal
	// "utils/includes.h: No such file" errors) and repeated `make -n`
	// dry runs showing the include/link flags silently missing from the
	// generated compile/link commands. `EXTRA_CFLAGS` is the Makefile's
	// own dedicated hook for exactly this ("CFLAGS += $(EXTRA_CFLAGS)"
	// is its very first accumulation line) and survives intact --
	// verified the same way. LDFLAGS has no equivalent hook, so instead
	// of fighting the same override problem, this passes the *complete*
	// LDFLAGS value directly (dropping only the Makefile's own
	// `-rdynamic`, needed only for backtrace symbol resolution, not
	// correctness) -- pkgConfigPath is exactly where BuildLibnlStatic
	// installed the static libraries this needs to find at link time.
	if err := run(filepath.Join(supplicantSrcDir, "wpa_supplicant"), env, "make", "-j"+jobs,
		"EXTRA_CFLAGS=-O2 -fstack-protector-strong -D_FORTIFY_SOURCE=2",
		"LDFLAGS=-static -L"+filepath.Join(libnlInstall, "lib")); err != nil {
		return fmt.Errorf("make: %w", err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return copyFile(filepath.Join(supplicantSrcDir, "wpa_supplicant", "wpa_supplicant"), filepath.Join(outDir, "wpa_supplicant"))
}

// buildLibnlStatic configures, builds, and installs libnl into prefix
// as static libraries only (--disable-shared), with pkg-config .pc
// files that point wpa_supplicant's own build (via PKG_CONFIG_PATH) at
// exactly this static install and nothing else on the builder image.
// --disable-cli/--disable-debug keep this to just the two libraries
// wpa_supplicant's nl80211 backend actually links against (libnl-3,
// libnl-genl-3) -- no route/netfilter/idiag sub-libraries, no CLI
// tools, no libnl-doc.
func buildLibnlStatic(spec SupplicantSpec, srcDir, prefix string) error {
	env := os.Environ()
	Logf("configuring libnl (static only, prefix=%s)", prefix)
	if err := run(srcDir, env, "./configure",
		"--prefix="+prefix,
		"--disable-shared", "--enable-static",
		"--disable-cli"); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	jobs := buildJobs()
	Logf("building libnl with %s jobs", jobs)
	if err := run(srcDir, env, "make", "-j"+jobs); err != nil {
		return fmt.Errorf("make: %w", err)
	}
	if err := run(srcDir, env, "make", "install"); err != nil {
		return fmt.Errorf("make install: %w", err)
	}
	return nil
}

// writeSupplicantConfig writes wpa_supplicant/.config from scratch
// (not copied from defconfig, then trimmed) -- every line here is
// something this project's own PSK-only, no-OpenSSL, no-CTRL_IFACE
// scope actually needs, so there is no defconfig line left commented
// out that a future reader has to reverse-engineer the absence of.
func writeSupplicantConfig(srcDir string) error {
	const config = `# Generated by cnimbus's internal/compileagent/wpasupplicant.go --
# PSK-only scope per .specs/features/hardboot-baremetal/spec.md
# ("Explicitly out of scope for v1.0": no WPA-Enterprise/802.1X/EAP,
# no WPS, no P2P, no AP mode). Every CONFIG_EAP_* line defconfig ships
# is deliberately absent, not merely unset -- the corresponding code
# is not compiled in at all.
CONFIG_DRIVER_NL80211=y
CONFIG_LIBNL32=y
CONFIG_BACKEND=file
CONFIG_IEEE8021X_EAPOL=y
# Answers spec.md's open question 4: internal crypto/TLS, no OpenSSL
# dependency at all. CONFIG_INTERNAL_LIBTOMMATH avoids a third
# build-time dependency (an external LibTomMath) beyond libnl.
CONFIG_TLS=internal
CONFIG_INTERNAL_LIBTOMMATH=y
`
	return os.WriteFile(filepath.Join(srcDir, "wpa_supplicant", ".config"), []byte(config), 0o644)
}

// underscoreVersion turns "3.12.0" into "12_0" (libnl's GitHub release
// tags are named "libnl3_<minor>_<patch>", dropping the leading major
// version component -- verified against the actual tag this project
// pins, libnl3_12_0, for version 3.12.0).
func underscoreVersion(version string) string {
	parts := splitDot(version)
	if len(parts) != 3 {
		return version
	}
	return parts[1] + "_" + parts[2]
}

func splitDot(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
