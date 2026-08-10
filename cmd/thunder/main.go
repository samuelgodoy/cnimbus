// Thunder runs inside the Linux build sandbox (invoked by
// `cnimbus prepare`, the only cnimbus subcommand that touches Docker) and
// drives the actual kernel + BusyBox compilation. It replaces what
// used to be a set of shell scripts: this is a plain Go program,
// compiled fresh for the target architecture by `cnimbus prepare` and
// baked into the builder image, that shells out only to the build
// tools themselves (make, gcc) -- the same way the host CLI shells out
// to docker.
//
// Its output is the "ready pieces" (see internal/pieces): vmlinuz,
// the busybox binary, and its applet symlink manifest. It does not
// assemble a rootfs or an image -- that happens later, in pure Go, on
// whatever host runs `cnimbus build-disk`.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"cnimbus/internal/compileagent"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// hasEthDriver reports whether bootProfile builds real Ethernet chipset
// drivers: true for "eth" and "eth+wifi" only. Each single profile is
// exclusive to its own driver family -- "wifi" alone builds only the
// 802.11 stack, not Ethernet too (a prior version of this function
// folded eth into "wifi" unconditionally; that implicit coupling was
// removed per an explicit product correction -- "eth+wifi" is now the
// only way to request both, matching what its name says).
func hasEthDriver(bootProfile string) bool {
	return bootProfile == "eth" || bootProfile == "eth+wifi"
}

// hasWifiDriver reports whether bootProfile builds the 802.11 stack: true
// for "wifi" and "eth+wifi".
func hasWifiDriver(bootProfile string) bool {
	return bootProfile == "wifi" || bootProfile == "eth+wifi"
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "[thunder] missing required env var %s\n", key)
		os.Exit(1)
	}
	return v
}

func main() {
	const (
		cacheDir    = "/cache"
		outDir      = "/out"   // bind mount -> host: only single opaque files cross this boundary
		localDir    = "/build" // container-local: safe for the busybox tree's real symlinks
		userFragDir = "/work/fragments"
	)

	arch := env("CNIMBUS_KERNEL_ARCH", "amd64")

	kernelSpec := compileagent.KernelSpec{
		Version:            requireEnv("CNIMBUS_KERNEL_VERSION"),
		SourceURL:          requireEnv("CNIMBUS_KERNEL_SOURCE_URL"),
		PGPURL:             os.Getenv("CNIMBUS_KERNEL_PGP_URL"),
		Arch:               arch,
		CacheDir:           cacheDir,
		InsecureSkipVerify: env("CNIMBUS_INSECURE_SKIP_KERNEL_VERIFY", "") == "true",
	}
	if entries, err := os.ReadDir(userFragDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				kernelSpec.FragmentDirs = append(kernelSpec.FragmentDirs, filepath.Join(userFragDir, e.Name()))
			}
		}
	}

	compileagent.Logf("thunder starting: kernel %s (%s)", kernelSpec.Version, arch)

	vga := env("CNIMBUS_VGA", "") == "true"
	// bootProfile is written into provenance verbatim, and also drives
	// which kconfig fragments get merged below: "none", "eth", "wifi", or
	// "eth+wifi" (the explicit spelling of both driver families at once --
	// see internal/nimbusfile's HARDBOOT doc comment). Defaulting here just
	// keeps thunder correct standalone (e.g. run outside `cnimbus prepare`
	// for local Thunder development).
	bootProfile := env("CNIMBUS_HARDBOOT", "none")
	agentVMware := env("CNIMBUS_AGENT_VMWARE", "") == "true"
	nimbusFragments := []string{"/opt/cnimbus/kconfig/minimal.fragment"}
	switch arch {
	case "arm64":
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/vm-arm64.fragment")
	default:
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/vm-amd64.fragment")
	}
	if vga {
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/vga.fragment")
	}
	// baremetal-eth.fragment (F6.1/F6.2) is additive on top of the VM
	// profile's own CONFIG_E1000, never a replacement for it -- merged for
	// "eth", "wifi" (wifi needs wired Ethernet drivers too, same as any
	// bare-metal profile), and "eth+wifi" (which needs it by definition).
	if hasEthDriver(bootProfile) {
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/baremetal-eth.fragment")
	}
	// baremetal-usb.fragment (AD-049): USB core/host-controller +
	// mass-storage support, merged for any real-hardware HARDBOOT
	// profile at all ("eth", "wifi", "eth+wifi"), never for "none" (a
	// VM-only image has no reason to talk to USB). Ahead of
	// baremetal-wifi.fragment below, which depends on CONFIG_USB=y
	// already being set for its own USB-attached WiFi dongle drivers.
	if bootProfile != "none" {
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/baremetal-usb.fragment")
	}
	// agent-vmware.fragment is amd64-only (CONFIG_X86_IOPL_IOPERM has no
	// arm64 equivalent -- VMware's I/O-port backdoor is an x86-only
	// mechanism), so it's only merged for that arch even if AGENT vmware
	// were somehow declared for an arm64 image.
	if agentVMware && arch != "arm64" {
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/agent-vmware.fragment")
	}
	// baremetal-wifi.fragment (F6.3/F6.4) merges before
	// security-baseline.fragment for the same reason vga.fragment/
	// agent-vmware.fragment do: security-baseline must stay last so
	// checkMergeConfigConflicts (T66) can catch anything above it that
	// silently re-disables one of its assertions. Merged for "wifi" and
	// "eth+wifi" alike -- both need the 802.11 stack.
	if hasWifiDriver(bootProfile) {
		nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/baremetal-wifi.fragment")
	}
	kernelFetch, err := compileagent.FetchKernel(kernelSpec)
	fatalOn(err)

	// AD-047: NETFILTER_XTABLES_LEGACY is a real Kconfig gate that
	// IP_NF_IPTABLES_LEGACY/IP6_NF_IPTABLES_LEGACY (this project's whole
	// legacy iptables/ip6tables chain, see minimal.fragment) `depends on`
	// -- but only starting with some kernel release after 6.9.4, the
	// version AD-045 tested against. It doesn't exist at all in 6.9.4
	// (confirmed empirically: absent from net/netfilter/Kconfig there),
	// so setting it unconditionally in a static fragment fails
	// verifyFragmentsApplied on that version ("requested but dropped" --
	// there's no symbol to apply it to); *not* setting it fails the same
	// check on a newer kernel that does define it (its two dependents get
	// silently dropped instead). Detected directly against the real
	// fetched source, once per build, rather than hardcoding a version
	// number this project would have to keep updated by hand as
	// kernel.org ships new releases.
	if compileagent.KernelDefinesNetfilterXtablesLegacy(kernelFetch.Dir) {
		legacyFragPath := filepath.Join(cacheDir, "netfilter-xtables-legacy.fragment")
		fatalOn(os.WriteFile(legacyFragPath, []byte("CONFIG_NETFILTER_XTABLES_LEGACY=y\n"), 0o644))
		nimbusFragments = append(nimbusFragments, legacyFragPath)
	}

	// security-baseline.fragment (T67) is arch-agnostic and always
	// merged, last, so nothing above it can silently re-disable one of
	// its assertions without checkMergeConfigConflicts (T66) catching it.
	nimbusFragments = append(nimbusFragments, "/opt/cnimbus/kconfig/security-baseline.fragment")

	fatalOn(compileagent.BuildKernel(kernelSpec, kernelFetch.Dir, nimbusFragments, outDir))

	busyboxSpec := compileagent.BusyboxSpec{
		Version:  os.Getenv("CNIMBUS_BUSYBOX_VERSION"),
		Arch:     arch,
		CacheDir: cacheDir,
	}
	busyboxFetch, err := compileagent.FetchBusybox(busyboxSpec)
	fatalOn(err)
	// Installed under localDir, NOT outDir: BusyBox's install tree is
	// mostly symlinks (each applet -> the busybox binary), and a Docker
	// Desktop bind mount silently turns every one of those into a full
	// copy of the target when the host side is Windows. Exporting it as
	// (binary, manifest) below -- computed from inside the container,
	// where symlinks are real -- avoids that entirely.
	fatalOn(compileagent.BuildBusybox(busyboxSpec, busyboxFetch.Dir, localDir))

	compileagent.Logf("exporting busybox pieces")
	binary, manifest, err := compileagent.ExportPieces(filepath.Join(localDir, "rootfs"))
	fatalOn(err)
	fatalOn(os.WriteFile(filepath.Join(outDir, "busybox"), binary, 0o755))
	fatalOn(os.WriteFile(filepath.Join(outDir, "busybox-manifest.tsv"), manifest, 0o644))

	// Built unconditionally (cheap: a handful of seconds, no extra
	// toolchain beyond what Kbuild itself already needs -- see
	// internal/compileagent/iptables.go) rather than only when a
	// Nimbusfile's FIREWALL directive is present, since `prepare` has no
	// visibility into that (it only ever reads KERNEL/BUSYBOX/ARCH/VGA)
	// and a later Nimbusfile revision might add FIREWALL without
	// re-running prepare.
	iptablesSpec := compileagent.IptablesSpec{Arch: arch, CacheDir: cacheDir}
	iptablesFetch, err := compileagent.FetchIptables(iptablesSpec)
	fatalOn(err)
	fatalOn(compileagent.BuildIptables(iptablesSpec, iptablesFetch.Dir, outDir))

	// F6.3/F6.4: only for a WiFi-driver profile ("wifi" or "eth+wifi") --
	// the fourth piece (wpa_supplicant) and the curated firmware set (D3)
	// are both entirely inert/absent for "none"/"eth", same opt-in-and-
	// costs-nothing-otherwise property HARDBOOT itself has.
	var supplicantFetch compileagent.FetchResult
	var wifiFirmware []compileagent.WifiFirmwareBlob
	if hasWifiDriver(bootProfile) {
		supplicantSpec := compileagent.SupplicantSpec{Arch: arch, CacheDir: cacheDir}
		libnlFetch, err := compileagent.FetchLibnl(supplicantSpec)
		fatalOn(err)
		suppFetch, err := compileagent.FetchSupplicant(supplicantSpec)
		fatalOn(err)
		fatalOn(compileagent.BuildSupplicant(supplicantSpec, libnlFetch.Dir, suppFetch.Dir, outDir))
		supplicantFetch = suppFetch

		compileagent.Logf("fetching curated WiFi firmware set")
		fw, err := compileagent.FetchWifiFirmware(cacheDir, outDir)
		fatalOn(err)
		wifiFirmware = fw
	}

	// AD-057: only for a wired-Ethernet-driver profile ("eth" or
	// "eth+wifi") -- same opt-in-and-costs-nothing-otherwise property as
	// the WiFi firmware set above.
	var ethernetFirmware []compileagent.EthernetFirmwareBlob
	if hasEthDriver(bootProfile) {
		compileagent.Logf("fetching curated Ethernet firmware set")
		fw, err := compileagent.FetchEthernetFirmware(cacheDir, outDir)
		fatalOn(err)
		ethernetFirmware = fw
	}

	fatalOn(writeProvenance(outDir, arch, vga, bootProfile, nimbusFragments, kernelFetch.Dir, kernelSpec,
		kernelFetch, busyboxFetch, iptablesFetch, supplicantFetch, wifiFirmware, ethernetFirmware))

	compileagent.Logf("done: %s/vmlinuz, %s/busybox, %s/busybox-manifest.tsv, %s/iptables, %s/pieces.json",
		outDir, outDir, outDir, outDir, outDir)
}

// writeProvenance records pieces.json: everything a later audit needs
// to know about exactly what this run built, that doesn't otherwise
// survive past this one process's lifetime (fetchExtract's own cache
// dir is per-machine, not part of what `--pieces` publishes). Fatal on
// error, same as every other piece this program produces -- `cnimbus
// prepare` always includes it in pieces.sha256 (see
// writePiecesHashes), so a silently-missing pieces.json would just
// turn into a more confusing failure one step later instead.
func writeProvenance(outDir, arch string, vga bool, bootProfile string, nimbusFragments []string, kernelSrcDir string, kernelSpec compileagent.KernelSpec, kernelFetch, busyboxFetch, iptablesFetch, supplicantFetch compileagent.FetchResult, wifiFirmware []compileagent.WifiFirmwareBlob, ethernetFirmware []compileagent.EthernetFirmwareBlob) error {
	fragHashes, err := hashFragments(nimbusFragments)
	if err != nil {
		return fmt.Errorf("hashing kconfig fragments for provenance: %w", err)
	}
	configHash, err := hashFile(filepath.Join(kernelSrcDir, ".config"))
	if err != nil {
		return fmt.Errorf("hashing resolved .config for provenance: %w", err)
	}
	prov := compileagent.PiecesProvenance{
		SchemaVersion: compileagent.PiecesProvenanceSchemaVersion,
		Kernel: compileagent.ComponentProvenance{
			Version:          kernelFetch.ResolvedVersion,
			SourceURL:        kernelFetch.SourceURL,
			TarballSHA256:    kernelFetch.TarballSHA256,
			SigURL:           kernelSpec.PGPURL,
			Verified:         kernelFetch.PGPVerified,
			SignedBy:         kernelFetch.PGPSignedBy,
			InsecureSkipUsed: kernelSpec.InsecureSkipVerify,
		},
		Busybox: compileagent.ComponentProvenance{
			Version:       busyboxFetch.ResolvedVersion,
			SourceURL:     busyboxFetch.SourceURL,
			TarballSHA256: busyboxFetch.TarballSHA256,
		},
		Iptables: compileagent.ComponentProvenance{
			Version:       iptablesFetch.ResolvedVersion,
			SourceURL:     iptablesFetch.SourceURL,
			TarballSHA256: iptablesFetch.TarballSHA256,
		},
		BuilderImageDigest:    os.Getenv("CNIMBUS_BUILDER_IMAGE_DIGEST"),
		Arch:                  arch,
		VGA:                   vga,
		BootProfile:           bootProfile,
		KconfigFragmentSHA256: fragHashes,
		KernelConfigSHA256:    configHash,
		WifiFirmware:          wifiFirmware,
		EthernetFirmware:      ethernetFirmware,
	}
	// Supplicant is the zero ComponentProvenance for "none"/"eth"
	// (supplicantFetch is never populated for those profiles) -- omitempty
	// on the struct tag keeps pieces.json identical to before this field
	// existed in that case.
	if hasWifiDriver(bootProfile) {
		prov.Supplicant = compileagent.ComponentProvenance{
			Version:       supplicantFetch.ResolvedVersion,
			SourceURL:     supplicantFetch.SourceURL,
			TarballSHA256: supplicantFetch.TarballSHA256,
		}
	}
	data, err := json.MarshalIndent(prov, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pieces.json: %w", err)
	}
	return os.WriteFile(filepath.Join(outDir, "pieces.json"), append(data, '\n'), 0o644)
}

// hashFragments returns each fragment's own base filename (not its full
// path, which is a container-local detail no one outside this build can
// resolve) mapped to its SHA-256, for the kconfig fragment provenance
// T59 adds -- auditable evidence of which exact fragment contents
// produced this build's .config, alongside hashFile's whole-.config hash.
func hashFragments(paths []string) (map[string]string, error) {
	hashes := make(map[string]string, len(paths))
	for _, p := range paths {
		h, err := hashFile(p)
		if err != nil {
			return nil, err
		}
		hashes[filepath.Base(p)] = h
	}
	return hashes, nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func fatalOn(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "[thunder] fatal: %v\n", err)
		os.Exit(1)
	}
}
