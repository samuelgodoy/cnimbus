package compileagent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// WifiFirmwareBlob is one curated firmware file HARDBOOT wifi bundles
// into the stage-1 initramfs (see internal/rootfs/stage1.go), landing at
// exactly /lib/firmware/<Path> in the booted image -- the standard
// location Linux's in-kernel firmware_class loader (request_firmware())
// searches, and the same path convention every mainstream distribution
// uses for the same files. Path doubles as this blob's own filename
// under the pieces output's firmware/ subdirectory (see
// FetchWifiFirmware) and, on the read side, under pieces.sha256 --
// deliberately identical so no separate path-mapping ever needs to
// travel through pieces.json.
type WifiFirmwareBlob struct {
	Path      string `json:"path"`
	SourceURL string `json:"source_url"`
	Version   string `json:"version,omitempty"`
	SHA256    string `json:"sha256"`
}

// wifiFirmwareBlobs is the curated set D3 requires (design.md section
// 3): never "ship everything", an explicit reviewed list tied to
// baremetal-wifi.fragment's driver set, each entry with a real upstream
// source and hash -- verified in-session by actually downloading every
// one of these (not copied from memory) from
// git.kernel.org/.../firmware/linux-firmware.git (the kernel's own
// canonical firmware distribution) and
// git.kernel.org/.../sforshee/wireless-regdb.git (the regulatory
// database project net/wireless/Kconfig's own CFG80211_USE_KERNEL_REGDB_KEYS
// help text names).
//
// This is deliberately a small subset of what baremetal-wifi.fragment's
// Kconfig breadth actually supports -- see that fragment's own comments
// and Tasks.md's F6.3/F6.4 entry for exactly which driver families got a
// Kconfig symbol but no bundled firmware in this round (Broadcom,
// Marvell, Intel), and why.
var wifiFirmwareBlobs = []WifiFirmwareBlob{
	// F6.3's spike family (Atheros AR9271, ath9k_htc, USB): the driver
	// requests "ath9k_htc/htc_9271-1.4.0.fw" before ever falling back to
	// the legacy top-level "htc_9271.fw" name (verified against
	// drivers/net/wireless/ath/ath9k/hif_usb.{c,h} at kernel.org v7.1.7 --
	// see HTC_FW_PATH/MAJOR_VERSION_REQ/FIRMWARE_MINOR_IDX_MAX in
	// hif_usb.h and ath9k_hif_request_firmware in hif_usb.c). This is the
	// modern, actively-maintained "open-ath9k-htc-firmware" build (real,
	// free-software source, not merely a redistributable binary blob --
	// see linux-firmware's WHENCE entry for this exact file).
	{
		Path:      "ath9k_htc/htc_9271-1.4.0.fw",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/ath9k_htc/htc_9271-1.4.0.fw?h=main",
		Version:   "1.4.0",
		SHA256:    "78f7d592a95b419a02fde7440f30c606fc31a871cf0ce150ced40d4857173eb0",
	},
	// AR7010 variant of the same htc driver family (dual-band AR7010 +
	// AR9280 combo devices) -- same driver, same request_firmware()
	// mechanism, bundled alongside the AR9271 file above since it costs
	// under 75 KB.
	{
		Path:      "ath9k_htc/htc_7010-1.4.0.fw",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/ath9k_htc/htc_7010-1.4.0.fw?h=main",
		Version:   "1.4.0",
		SHA256:    "737850aaf66ca13f82e0f5ca5e758f606f840c2f23c291350ab916bbf88e6f75",
	},
	// Regulatory database + its detached PKCS#7 signature: required by
	// CFG80211_REQUIRE_SIGNED_REGDB (baremetal-wifi.fragment) for
	// WIFICOUNTRY to mean anything at all -- cfg80211 loads these via
	// the exact same request_firmware() mechanism as any chipset's own
	// blob, at kernel-init time, which makes this D2-relevant too (see
	// wifi.go's package doc and Tasks.md's F6.3 entry). Sourced from the
	// wireless-regdb project itself (net/wireless/Kconfig's help text
	// names its maintainer, Seth Forshee, as the key CFG80211_USE_KERNEL_REGDB_KEYS
	// trusts) -- NOT linux-firmware, which does not carry this file.
	{
		Path:      "regulatory.db",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/sforshee/wireless-regdb.git/plain/regulatory.db",
		Version:   "",
		SHA256:    "0a4abd7ae20d07bb70642937ccb2293a72a6504730eea45a698882599f586368",
	},
	{
		Path:      "regulatory.db.p7s",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/sforshee/wireless-regdb.git/plain/regulatory.db.p7s",
		Version:   "",
		SHA256:    "bcd81aed039ea6b9b6f3726fbf26911a0caf4a5d894210e0fa2effb384d6b326",
	},
	// MediaTek MT7601U: one of the cheapest, most common USB dongle
	// chipsets, single file, no per-device variants -- verified against
	// drivers/net/wireless/mediatek/mt7601u/mcu.c's mt7601u_fw_paths
	// (requests "mediatek/mt7601u.bin" first).
	{
		Path:      "mediatek/mt7601u.bin",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/mediatek/mt7601u.bin?h=main",
		Version:   "34",
		SHA256:    "4511b1d840e22aea2bf5fdca419c91c0d94cbfb291b9ac4f8be6d9100d1a7046",
	},
	// Realtek RTL8188EU: the single most common cheap Realtek USB
	// dongle chipset covered by CONFIG_RTL8XXXU -- verified against
	// drivers/net/wireless/realtek/rtl8xxxu/core.c's
	// MODULE_FIRMWARE("rtlwifi/rtl8188eufw.bin") declaration.
	{
		Path:      "rtlwifi/rtl8188eufw.bin",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/rtlwifi/rtl8188eufw.bin?h=main",
		Version:   "28.0",
		SHA256:    "2ff74315287529dec2e50eb57d6e0c97d2116f28ae166773ccdf93b6360000c4",
	},
}

// FetchWifiFirmware downloads every entry of wifiFirmwareBlobs into
// cacheDir (skipping a download whose file already exists), verifies
// each one's SHA-256 unconditionally -- on every run, including a cache
// hit, exactly like fetchExtract already does for the kernel/BusyBox/
// iptables tarballs (HB-P-004's "verified on every run" rule extended to
// firmware) -- and copies the verified bytes into outDir/firmware/<Path>
// for cmd/thunder to publish alongside vmlinuz/busybox/iptables.
func FetchWifiFirmware(cacheDir, outDir string) ([]WifiFirmwareBlob, error) {
	srcDir := filepath.Join(cacheDir, "wifi-firmware-src")
	fwOutDir := filepath.Join(outDir, "firmware")

	result := make([]WifiFirmwareBlob, 0, len(wifiFirmwareBlobs))
	for _, blob := range wifiFirmwareBlobs {
		if err := fetchOneFirmwareFile(srcDir, fwOutDir, "wifi", blob.Path, blob.SourceURL, blob.SHA256); err != nil {
			return nil, err
		}
		result = append(result, blob)
	}
	return result, nil
}

// fetchOneFirmwareFile is FetchWifiFirmware/FetchEthernetFirmware's
// shared download-verify-install step: downloads sourceURL into
// srcDir/<flattened path> (skipping a download whose file already
// exists), verifies its SHA-256 unconditionally -- on every run,
// including a cache hit, exactly like fetchExtract already does for the
// kernel/BusyBox/iptables tarballs (HB-P-004's "verified on every run"
// rule) -- and copies the verified bytes into fwOutDir/<path>. kind is
// only for the two log lines below ("wifi"/"ethernet"), distinguishing
// which curated set a download belongs to in build output.
func fetchOneFirmwareFile(srcDir, fwOutDir, kind, path, sourceURL, wantSHA256 string) error {
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}
	cachedPath := filepath.Join(srcDir, flattenPath(path))
	if _, err := os.Stat(cachedPath); os.IsNotExist(err) {
		Logf("downloading %s firmware %s", kind, path)
		if err := downloadFile(sourceURL, cachedPath); err != nil {
			return fmt.Errorf("fetching firmware %s: %w", path, err)
		}
	} else {
		Logf("using cached %s firmware %s", kind, path)
	}
	data, err := os.ReadFile(cachedPath)
	if err != nil {
		return fmt.Errorf("reading cached firmware %s: %w", path, err)
	}
	got := sha256Hex(data)
	if got != wantSHA256 {
		_ = os.Remove(cachedPath) // best-effort: don't leave a bad/stale download around to be silently reused next run
		return fmt.Errorf("SHA-256 mismatch for firmware %s: expected %s, got %s -- the file does not "+
			"match the pinned hash (corrupted download, or the source was tampered with)", path, wantSHA256, got)
	}
	dst := filepath.Join(fwOutDir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// flattenPath turns a firmware blob's "/"-separated Path into a single
// filesystem-safe filename for the flat local cache directory (which has
// no reason to mirror /lib/firmware's own subdirectory structure).
func flattenPath(p string) string {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, '_')
		} else {
			out = append(out, p[i])
		}
	}
	return string(out)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
