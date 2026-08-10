// Package assets embeds every binary blob cnimbus needs at runtime, so
// the tool is a single self-contained executable: no adjacent files,
// no separate download step for the bootloader stubs.
package assets

//go:generate go run ./gensync
//go:generate go run ./genagent

import "embed"

//go:embed data/syslinux/isolinux.bin
var IsolinuxBin []byte

//go:embed data/syslinux/ldlinux.c32
var LdlinuxC32 []byte

// CnimbusAgent{Amd64,Arm64} are cmd/cnimbusagent, prebuilt (like the
// syslinux stubs above -- used by `cnimbus build-disk`, needs no Docker,
// no kernel/BusyBox-version dependency to recompile against). It's the
// single runtime binary behind every real AGENT kind: http,
// virtio-serial, vboxguest (VirtualBox Guest Properties via the
// mainline-kernel VBoxGuest driver, CONFIG_VBOXGUEST, no Guest
// Additions installed), aws-imds/ibm-imds (cloud instance metadata),
// and vmware (VMware's backdoor I/O protocol) -- see cmd/cnimbusagent's
// own doc comment for each kind's implementation. Kept in sync with
// cmd/cnimbusagent's source by internal/assets/genagent (`go generate
// ./internal/assets`); see assets_sync_test.go for the drift check.
//
//go:embed data/cnimbusagent/cnimbusagent-amd64
var CnimbusAgentAmd64 []byte

//go:embed data/cnimbusagent/cnimbusagent-arm64
var CnimbusAgentArm64 []byte

// The following are only used by `cnimbus prepare` (the Docker-based
// pipeline in internal/compileagent that produces the prebuilt
// "pieces") -- never by `cnimbus build-disk`.

//go:embed data/Dockerfile
var ForgeDockerfile []byte

//go:embed data/kconfig/minimal.fragment
var KconfigMinimal []byte

//go:embed data/kconfig/vm-amd64.fragment
var KconfigVMAmd64 []byte

//go:embed data/kconfig/vm-arm64.fragment
var KconfigVMArm64 []byte

//go:embed data/kconfig/vga.fragment
var KconfigVGA []byte

//go:embed data/kconfig/agent-vmware.fragment
var KconfigAgentVMware []byte

//go:embed data/kconfig/security-baseline.fragment
var KconfigSecurityBaseline []byte

// KconfigBaremetalEth backs HARDBOOT eth/wifi (F6.1/F6.2): real,
// physical-hardware wired-Ethernet chipset support (CONFIG_E1000E),
// additive on top of vm-amd64.fragment/vm-arm64.fragment's own
// CONFIG_E1000 rather than replacing it. Only merged by thunder when
// CNIMBUS_HARDBOOT is "eth" or "wifi" -- see cmd/thunder/main.go.
//
//go:embed data/kconfig/baremetal-eth.fragment
var KconfigBaremetalEth []byte

// KconfigBaremetalWifi backs HARDBOOT wifi (F6.3/F6.4): the curated
// WiFi chipset driver set. Only merged by thunder when CNIMBUS_HARDBOOT
// is "wifi" -- see cmd/thunder/main.go.
//
//go:embed data/kconfig/baremetal-wifi.fragment
var KconfigBaremetalWifi []byte

// KconfigBaremetalUsb backs AD-049: USB core/host-controller +
// mass-storage support, merged for any real-hardware HARDBOOT profile
// at all ("eth", "wifi", "eth+wifi"), never for "none" -- see
// cmd/thunder/main.go.
//
//go:embed data/kconfig/baremetal-usb.fragment
var KconfigBaremetalUsb []byte

// ThunderSrc is Thunder's own source (plus a vendored copy of its one
// dependency, github.com/ulikunitz/xz) -- not a prebuilt binary.
// `prepare` compiles it fresh, inside a throwaway golang container, for
// whichever architecture the Nimbusfile declares: that's what lets one
// Nimbusfile with "ARCH arm64" produce an entirely arm64 pipeline (the
// compiler container, Thunder, and the kernel/BusyBox it builds) with
// no pre-built arm64 binary shipped inside `cnimbus` itself, and no local
// Go toolchain required on the machine running `cnimbus prepare` (Docker
// is the only dependency, for this exactly the same reason it's needed
// to run Kbuild).
//
//go:embed all:data/thunder-src
var ThunderSrc embed.FS
