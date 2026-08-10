package compileagent

import "path/filepath"

// EthernetFirmwareBlob is one curated firmware file HARDBOOT eth/eth+wifi
// bundles into the stage-1 initramfs (see internal/rootfs/stage1.go),
// landing at exactly /lib/firmware/<Path> in the booted image -- same
// convention and same request_firmware() consumer as WifiFirmwareBlob,
// just for the wired-Ethernet driver family baremetal-eth.fragment
// enables (see that fragment's own comments on CONFIG_R8169).
type EthernetFirmwareBlob struct {
	Path      string `json:"path"`
	SourceURL string `json:"source_url"`
	Version   string `json:"version,omitempty"`
	SHA256    string `json:"sha256"`
}

// ethernetFirmwareBlobs (AD-057): baremetal-eth.fragment's own comment
// on CONFIG_R8169 documents this precisely -- r8169_main.c's
// MODULE_FIRMWARE table lists a distinct rtl_nic/rtl<rev>-<n>.fw per
// chip revision, and the driver's own rtl_request_firmware() treats "no
// firmware available" as a normal, non-fatal case (most revisions need
// none at all). This is the one revision a real bare-metal boot this
// project was tested against actually carries
// ("r8169 0000:02:00.0: Unable to load firmware rtl_nic/rtl8168h-2.fw
// (-2)") -- confirmed harmless by reading r8169_main.c itself
// (r8169_apply_firmware is a no-op without it; the surrounding comment
// calls it "firmware is for MAC only", not a networking prerequisite)
// before bundling it, rather than assuming the missing-firmware message
// meant anything was actually broken. Bundling it removes the console
// line and applies the same MAC-only correctness patch upstream ships
// for this exact revision -- not required for networking to work, but
// no reason to run without it once it's known to be needed and free to
// carry (976 bytes).
//
// Verified by actually downloading this file (not copied from memory)
// from git.kernel.org/.../firmware/linux-firmware.git, the kernel's own
// canonical firmware distribution -- same source WifiFirmwareBlob's own
// entries already use.
var ethernetFirmwareBlobs = []EthernetFirmwareBlob{
	{
		Path:      "rtl_nic/rtl8168h-2.fw",
		SourceURL: "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/rtl_nic/rtl8168h-2.fw?h=main",
		Version:   "rtl8168h-2_0.0.2",
		SHA256:    "0b4beab008e308f28296c13188ee23edfeff7d865e398a7ade170495c8cec7e6",
	},
}

// FetchEthernetFirmware downloads every entry of ethernetFirmwareBlobs
// into cacheDir and copies the verified bytes into outDir/firmware/<Path>
// for cmd/thunder to publish alongside vmlinuz/busybox/iptables/wifi
// firmware -- see fetchOneFirmwareFile (shared with FetchWifiFirmware)
// for the actual download/verify/install steps.
func FetchEthernetFirmware(cacheDir, outDir string) ([]EthernetFirmwareBlob, error) {
	srcDir := filepath.Join(cacheDir, "ethernet-firmware-src")
	fwOutDir := filepath.Join(outDir, "firmware")

	result := make([]EthernetFirmwareBlob, 0, len(ethernetFirmwareBlobs))
	for _, blob := range ethernetFirmwareBlobs {
		if err := fetchOneFirmwareFile(srcDir, fwOutDir, "ethernet", blob.Path, blob.SourceURL, blob.SHA256); err != nil {
			return nil, err
		}
		result = append(result, blob)
	}
	return result, nil
}
