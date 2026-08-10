package compileagent

// PiecesProvenanceSchemaVersion is bumped whenever PiecesProvenance's
// shape changes in a way a consumer might care about (T59 promotes
// pieces.json from a purely informational file to one internal/pieces
// actually parses and checks a Nimbusfile against -- see
// SchemaVersion's own field doc).
const PiecesProvenanceSchemaVersion = 2

// PiecesProvenance is the pieces.json schema cmd/thunder writes into
// its output directory at the end of a successful `cnimbus prepare`
// run -- everything a later audit needs to know about exactly what got
// built (which upstream release, from where, verified how) that
// doesn't otherwise survive past this one build process's lifetime.
//
// As of T59, internal/pieces.Resolve *does* parse this file (when
// present) and returns it in the Set -- the doc comment this replaces
// ("an opaque file … never parsed by cnimbus itself") stopped being
// true the moment build-disk needed to compare a pieces set's actual
// ARCH/VGA against the Nimbusfile it's about to assemble. Absence is
// still tolerated (an older prepare's pieces predate this file, or any
// pieces.json at all) -- see pieces.ProvenanceVerified.
type PiecesProvenance struct {
	// SchemaVersion lets a future cnimbus binary tell an old pieces.json
	// (this field absent or zero) apart from a newer/differently-shaped
	// one it might not fully understand -- unknown JSON fields are
	// already ignored by encoding/json, so this is only needed if a
	// *meaning* changes, not merely a field addition.
	SchemaVersion int                 `json:"schema_version"`
	Kernel        ComponentProvenance `json:"kernel"`
	Busybox       ComponentProvenance `json:"busybox"`
	Iptables      ComponentProvenance `json:"iptables"`
	// Supplicant (F6.4) is the fourth piece HARDBOOT wifi requires (see
	// wpasupplicant.go) -- zero value when BootProfile isn't "wifi",
	// same optional-piece treatment as Iptables above.
	Supplicant ComponentProvenance `json:"supplicant,omitempty"`
	// WifiFirmware (F6.3) records every curated firmware blob bundled
	// into the stage-1 initramfs (see wifi.go's wifiFirmwareBlobs and
	// design.md's D3) -- HB-P-003's "source URL, version/commit, and
	// SHA-256 for every firmware blob shipped" requirement. Empty when
	// BootProfile isn't "wifi".
	WifiFirmware []WifiFirmwareBlob `json:"wifi_firmware,omitempty"`
	// EthernetFirmware (AD-057) mirrors WifiFirmware for the wired-NIC
	// driver family baremetal-eth.fragment enables -- see ethernet.go's
	// ethernetFirmwareBlobs. Empty when BootProfile doesn't include eth.
	EthernetFirmware []EthernetFirmwareBlob `json:"ethernet_firmware,omitempty"`
	// BuilderImageDigest is the local content-addressed ID of the
	// Docker image this build actually ran in (see
	// internal/dockerrun.ImageDigest) -- empty if the caller didn't
	// resolve or pass one through (e.g. an older cnimbus binary talking
	// to a newer thunder, or vice versa).
	BuilderImageDigest string `json:"builder_image_digest,omitempty"`
	// Arch and VGA (T59) are the two `prepare`-time settings that
	// silently change what the kernel *can* do at boot with no trace
	// anywhere else: a pieces set built with VGA=false against a
	// Nimbusfile declaring "VGA true" assembles an ISO whose cmdline
	// says console=tty0 against a kernel with no framebuffer console
	// compiled in -- a permanently black window in VirtualBox, with
	// nothing in the build output or the image saying why. build-disk
	// checks both against the Nimbusfile it's assembling and fails with
	// a specific message on mismatch, rather than assembling a broken
	// image silently.
	Arch string `json:"arch"`
	VGA  bool   `json:"vga"`
	// BootProfile is the HARDBOOT setting this pieces set was built for:
	// "none" (today's VM-only kernel, the default), "eth", or "wifi".
	// omitempty so pieces built by a `prepare` that predates this field
	// serialize identically to before it existed -- build-disk's mismatch
	// check treats an absent value as "none", matching what every
	// pre-existing pieces set actually is.
	BootProfile string `json:"boot_profile,omitempty"`
	// KconfigFragmentSHA256 maps each merged kconfig fragment's base
	// filename (e.g. "minimal.fragment") to its own SHA-256, and
	// KernelConfigSHA256 is the resolved .config's -- recorded for
	// audit (this is the precondition M4's SBOM milestone needs: an
	// SBOM that cannot state the kernel's own build configuration isn't
	// describing the artifact) rather than compared against anything
	// today; there is no second copy of "what the fragments should
	// have been" to compare against at build-disk time.
	KconfigFragmentSHA256 map[string]string `json:"kconfig_fragment_sha256,omitempty"`
	KernelConfigSHA256    string            `json:"kernel_config_sha256,omitempty"`
}

// ComponentProvenance is one upstream source (kernel, BusyBox, or
// iptables) actually built into this pieces set.
type ComponentProvenance struct {
	Version       string `json:"version"`
	SourceURL     string `json:"source_url"`
	TarballSHA256 string `json:"tarball_sha256"`
	// SigURL/Verified/SignedBy are the zero value for BusyBox/iptables,
	// neither of which publishes anything like kernel.org's detached
	// PGP signatures to check against (see FetchBusybox/FetchIptables).
	SigURL   string `json:"sig_url,omitempty"`
	Verified bool   `json:"verified"`
	SignedBy string `json:"signed_by,omitempty"`
	// InsecureSkipUsed is true only for a kernel build that explicitly
	// passed --insecure-skip-kernel-verify -- Verified is false in that
	// case too, but this field is what tells "no signature was
	// available at all" apart from "a signature was available and
	// deliberately not checked".
	InsecureSkipUsed bool `json:"insecure_skip_used,omitempty"`
}
