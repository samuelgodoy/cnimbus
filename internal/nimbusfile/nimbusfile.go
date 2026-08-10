// Package nimbusfile parses a Nimbusfile: a Dockerfile-style declarative
// manifest describing a cnimbus image. Same spirit as a Dockerfile, but
// the "layers" being assembled are kernel + BusyBox + your own files,
// not a container image.
//
//	KERNEL latest-stable
//	BUSYBOX 1.36.1
//	ARCH amd64
//	HOSTNAME cnimbus
//	DHCP true
//
//	COPY ./helloserver /usr/bin/helloserver
//	ENTRYPOINT /usr/bin/helloserver
//	CMD :8080
//
// Directives:
//
//	KERNEL <version>      "latest-stable", "latest-longterm", or explicit (e.g. "6.9.4")
//	BUSYBOX <version>     explicit version, or "latest"
//	ARCH <amd64|arm64>    target architecture (default "amd64")
//	VGA <true|false>      enable a real VGA/framebuffer console for console=tty0
//	                      (default false; only read by `cnimbus prepare` -- see its
//	                      --vga flag, which takes precedence over this)
//	HARDBOOT <none|eth|wifi|eth+wifi>  bare-metal boot profile (default "none" --
//	                      today's VM-only behavior, byte-identical output). Changes
//	                      which kernel drivers `cnimbus prepare` builds in, so --
//	                      like ARCH/VGA -- it's a prepare-time decision enforced at
//	                      build-disk via the pieces provenance check. "eth" adds
//	                      real Ethernet chipset drivers + isohybrid MBR so the
//	                      ISO can be dd'd to a USB stick and boot legacy BIOS.
//	                      "wifi" builds the 802.11 stack, a curated firmware
//	                      set, and a userspace WPA supplicant (see
//	                      WIFI/WIFIPSK/WIFICOUNTRY) instead of Ethernet --
//	                      BusyBox has no supplicant of its own, so WPA2-PSK
//	                      cannot associate without it. Each single value is
//	                      exclusive to its own driver family: "eth" alone
//	                      never builds the WiFi stack, and "wifi" alone
//	                      never builds Ethernet chipset drivers -- "eth+wifi"
//	                      is the only value that builds both, for a physical
//	                      machine with both a wired NIC and a WiFi radio.
//	                      Requires WIFI/WIFIPSK/WIFICOUNTRY exactly like
//	                      "wifi" does -- the WiFi driver stack with no
//	                      network to associate with is never what's meant.
//	WIFI <ssid>           network to associate with; requires HARDBOOT wifi or
//	                      HARDBOOT eth+wifi
//	WIFIPSK <passphrase>  WPA2-PSK passphrase (8-63 printable chars) or a
//	                      pre-derived 64-hex-char key; requires HARDBOOT wifi.
//	                      Prefer "WIFIPSK ${WIFI_PSK}" + "--build-arg
//	                      WIFI_PSK=<value>" (see ARG) at build time over a
//	                      literal in the Nimbusfile committed to version
//	                      control -- it is a real secret. F6.5 finding:
//	                      unlike the AGENT bearer token (baked into a
//	                      generated script at build time, same as this),
//	                      there is currently no working "deliver the real
//	                      PSK at boot via AGENT, bake nothing in" path --
//	                      wlan0 association happens synchronously during
//	                      rcS's own sysinit stage, which busybox-init always
//	                      runs to completion *before* any AGENT respawn
//	                      entry ever starts (see internal/rootfs's
//	                      buildInittab), so there is nothing yet running to
//	                      poll by the time wpa_supplicant needs a PSK.
//	                      WIFIPSK is therefore always baked into the image
//	                      today, via one of the two layers above.
//	WIFICOUNTRY <XX>      ISO 3166-1 alpha-2 regulatory domain (e.g. "BR", "US");
//	                      required whenever HARDBOOT wifi is set
//	HOSTNAME <name>
//	DHCP <true|false>
//	IP <addr> <netmask> <gw>  static IP instead of DHCP; wins over DHCP if both are set
//	NTP <server|false>    NTP server to sync the clock against at boot (default
//	                      "pool.ntp.org"); repeatable for multiple servers; "false"
//	                      disables time sync entirely (and clears any servers
//	                      already added by an earlier NTP line)
//	FORMAT <format>       the image format to produce: "iso", "raw" (GPT +
//	                      UEFI-only ESP; no BIOS path), or "vhd" (the same
//	                      raw layout wrapped in a Fixed VHD footer, ready
//	                      for Hyper-V without a separate `cnimbus run` step).
//	                      Not a path -- see `cnimbus build-disk -o` for that.
//	PIECESKEY <hex-pubkey>   pin the Ed25519 public key (see "cnimbus keygen")
//	                      that pieces.sha256 must be signed by (T81 step 1) --
//	                      build-disk then refuses to build unless the pieces
//	                      source published a pieces.sha256.sig verifying
//	                      against it. A --pieces-verify-key flag passed on
//	                      the command line overrides this, same rule as
//	                      every other Nimbusfile-vs-flag setting.
//	USER <name>           run ENTRYPOINT/CMD and every SERVICE as this
//	                      unprivileged user instead of root (default: root)
//	VOLUME <device> <mount> [fstype] [required]   mount <device> (e.g.
//	                      "/dev/vda") at <mount> at boot for persistent
//	                      storage. Never formats it -- the device must
//	                      already be a real, pre-formatted disk (fstype
//	                      "vfat", the default, or "ext4") you attached
//	                      yourself. Without "required": if it doesn't
//	                      mount, boot continues without it, nothing is
//	                      written to the device. With "required": a
//	                      failed mount halts boot with a FATAL message
//	                      instead, before any service starts against
//	                      what would otherwise be missing storage.
//	                      Optional -- with no VOLUME, the whole image is
//	                      RAM-only as before.
//	ENV <KEY>=<VALUE>     environment variable set on ENTRYPOINT/CMD and every
//	                      SERVICE; repeatable
//	FIREWALL <rule...>    one iptables (IPv4) rule line, applied at boot via a
//	                      static iptables-legacy binary `cnimbus prepare`
//	                      builds and bundles automatically (a COPY'd
//	                      "iptables" still wins if present); repeatable
//	FIREWALL6 <rule...>   the IPv6 counterpart of FIREWALL: same rule syntax
//	                      and the same bundled binary (which also dispatches
//	                      as ip6tables -- see internal/compileagent/
//	                      iptables.go), applied via a separate ruleset;
//	                      repeatable. Independent of FIREWALL -- neither
//	                      implies or affects the other's rules or policy.
//	COPY <src> <dest>     local file -> image path, verbatim (like Docker's COPY)
//	ADD <src> <dest>      like COPY, but src may be a URL, and a local
//	                      .tar/.tar.gz src is auto-extracted into dest
//	                      (matches Docker ADD's actual semantics exactly)
//	ENTRYPOINT <cmd...>   the main service respawned at boot; exec form
//	                      (["/usr/bin/foo","arg"]) or shell form (space-split)
//	CMD <args...>         default args appended after ENTRYPOINT, or (with
//	                      no ENTRYPOINT set) the whole respawned command
//	SERVICE <name> <cmd...>   an additional respawned service alongside
//	                      ENTRYPOINT/CMD, under its own supervisor; repeatable
//	AGENT <url> [interval]    poll <url> (any plain HTTP(S) server) every
//	                      [interval] seconds (default 5) and write the
//	                      response body to /var/run/cnimbus-kv.json; see
//	                      "cnimbus kv-serve" for a ready-made server to point
//	                      it at. Works on any hypervisor with guest
//	                      networking, at the cost of needing that network
//	                      path at all.
//	AGENT vboxguest <property> [interval]   VirtualBox's own real guest
//	                      integration channel instead of HTTP: reads
//	                      Guest Property <property> via the mainline
//	                      Linux VBoxGuest driver (CONFIG_VBOXGUEST) --
//	                      no Guest Additions installed. Set it from the
//	                      host with, e.g., "VBoxManage guestproperty set
//	                      <vm> <property> <value>". Same
//	                      /var/run/cnimbus-kv.json output as the HTTP form.
package nimbusfile

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type CopyOp struct {
	Src   string
	Dest  string
	IsURL bool
	IsAdd bool // ADD vs COPY: only ADD auto-extracts local tarballs or fetches URLs
	// Chmod is the permission bits an explicit "--chmod=<mode>" set, or 0
	// if none was given -- 0 is never a valid real Chmod value (every
	// file needs at least some permission bits), so it doubles as "unset,
	// use the caller's own default" with no separate bool needed.
	Chmod uint32
}

// EnvVar is one ENV directive; order is preserved so a later ENV with
// the same Key can be relied on to override an earlier one. Also reused
// for LABEL (same Key=Value shape, different purpose).
type EnvVar struct {
	Key   string
	Value string
}

// ExposedPort is one EXPOSE directive: purely informational (like
// Docker's own EXPOSE) -- documents which port(s) a hypervisor-side
// port-forward should target (see README's "Reaching a service running
// in the guest from your host"). cnimbus itself never opens or forwards
// anything based on this.
type ExposedPort struct {
	Port  int
	Proto string // "tcp" or "udp"
}

// Healthcheck is set by the HEALTHCHECK directive: a command run
// periodically against the ENTRYPOINT service specifically (mirroring
// Docker's own single-HEALTHCHECK-per-container model) -- if it fails
// Retries times in a row, the supervisor kills and respawns the main
// process, the same way a crash would.
type Healthcheck struct {
	Argv     []string
	Interval string // seconds, default "30"
	Retries  string // consecutive failures before restarting, default "3"
}

// StaticIP is set by the IP directive; when non-nil it wins over DHCP
// regardless of the DHCP directive's value.
type StaticIP struct {
	Address string
	Netmask string
	Gateway string
}

// Volume is one VOLUME directive: a pre-formatted block device mounted
// at boot for persistent storage. Never formatted by cnimbus itself --
// if it doesn't mount, it's logged and skipped by default (Required ==
// false); with Required, a failed mount halts boot instead (T93) -- see
// buildRCScript.
type Volume struct {
	Device     string
	Mountpoint string
	FSType     string // "vfat" (default) or "ext4"
	// Required, set via a trailing "required" token (e.g. "VOLUME
	// /dev/vdb /var/lib/data ext4 required"), halts boot with a FATAL
	// message when this volume fails to mount instead of silently
	// continuing. Off by default, matching every VOLUME line written
	// before this field existed. Named after the failed mountpoint, not
	// the directive's own position, since a workload that expects its
	// data directory to be real storage writing straight through to the
	// read-only SquashFS root (or, worse, tmpfs -- silently lost on
	// reboot) is a worse outcome than refusing to boot at all.
	Required bool
}

// Service is one SERVICE directive: an additional respawned process
// alongside ENTRYPOINT/CMD, under its own supervisor.
type Service struct {
	Name string
	Argv []string
	// Restart is this service's restart policy, set via a "RESTART
	// <name> <policy>" line that must come after this SERVICE's own
	// declaration: "always" (default -- respawn unconditionally, capped-
	// linear backoff, cnimbus's original and only behavior before this
	// directive existed), "on-failure" (respawn only on a non-zero exit
	// code), or "no" (run once, never respawn).
	Restart string
}

// AgentHeader is one "AGENT header <Name>: <Value>" line, added to the
// plain-HTTP AGENT kind's wget requests -- covers cloud metadata
// services requiring a fixed identifying header (GCE's
// "Metadata-Flavor: Google", OCI's "Authorization: Bearer Oracle").
type AgentHeader struct {
	Name  string
	Value string
}

// Agent is set by the AGENT directive: something that writes live
// config to /var/run/cnimbus-kv.json at boot, so a Nimbusfile's own
// ENTRYPOINT/SERVICE can read it without rebuilding the image or
// rebooting the VM. Every kind writes the fetched value verbatim, with
// no envelope of its own added. Kinds:
//   - "http": plain HTTP(S) polling, works on any hypervisor with guest
//     networking, no vendor guest tools. Optional custom headers via
//     "AGENT header <Name>: <Value>" lines (see AgentHeader) -- covers
//     GCE/OCI's metadata services outright.
//   - "vboxguest": VirtualBox's own real Guest Properties channel via
//     the mainline-kernel VBoxGuest driver -- no Guest Additions
//     installed, but VirtualBox-only (see cmd/cnimbusagent).
//   - "virtio-serial": QEMU/Proxmox's virtio-console channel -- no
//     qemu-guest-agent needed (URL holds the guest-side device path).
//   - "vmware": VMware's backdoor I/O port protocol (same one
//     open-vm-tools implements) -- linux/amd64 only, validated against
//     a real VMware Player VM (see ROADMAP.md).
//   - "aws-imds": AWS EC2 IMDSv2's two-step token-then-GET dance (see
//     cmd/cnimbusagent). URL holds the metadata path under
//     /latest/meta-data/.
//   - "ibm-imds": IBM Cloud VPC's equivalent two-step token dance (see
//     cmd/cnimbusagent). URL holds the metadata path under /metadata/v1/.
type Agent struct {
	Kind     string        // "http", "vboxguest", "virtio-serial", "vmware", "aws-imds", or "ibm-imds"
	URL      string        // http/virtio-serial/vmware: URL or device path; vboxguest: property name; aws-imds/ibm-imds: metadata path
	Interval string        // seconds, kept as the literal string typed in the Nimbusfile
	Headers  []AgentHeader // http kind only
}

// Nimbusfile is the fully-parsed, defaulted manifest.
type Nimbusfile struct {
	KernelVersion  string
	BusyboxVersion string
	Arch           string // "amd64" or "arm64"
	VGA            bool   // only read by `cnimbus prepare`; see its --vga flag
	// BootProfile is set by HARDBOOT: "none" (default, today's VM-only
	// behavior), "eth", "wifi", or "eth+wifi". Only read by `cnimbus
	// prepare` -- like Arch/VGA, it changes which kernel drivers get built
	// in, so it's enforced at build-disk via the pieces provenance check
	// (an exact-string compare against pieces.json's own recorded value),
	// not here. Kept as a single string, not two booleans, specifically so
	// that comparison stays exact-string-equality end to end -- see
	// cmd/cnimbus/build.go's mismatch check.
	BootProfile string
	// WiFiSSID/WiFiPSK/WiFiCountry are set by WIFI/WIFIPSK/WIFICOUNTRY.
	// Only meaningful (and only permitted) when BootProfile is "wifi" or
	// "eth+wifi" -- see hasWifiDriver and the cross-directive checks at
	// the end of Parse.
	WiFiSSID    string
	WiFiPSK     string
	WiFiCountry string
	Hostname    string
	DHCP        bool
	StaticIP    *StaticIP // set by IP; wins over DHCP when non-nil
	DNS         []string  // set by DNS; explicit nameservers, winning over whatever DHCP itself provides
	NTP         []string  // NTP servers; nil means disabled ("NTP false"); defaulted in Parse
	Format      string    // image format to produce; not a path (see cmd/cnimbus's -o flag)
	User        string    // unprivileged user to run as; "" means root (no change)
	Volumes     []Volume
	Env         []EnvVar
	Firewall    []string
	// Firewall6 (AD-047) is FIREWALL6's IPv4-sibling-turned-IPv6 rule
	// list -- same rule syntax, validated the same way, applied as its
	// own separate ip6tables ruleset. Reverses T12's original "disable
	// IPv6 at boot instead of building real coverage for it" choice: nil
	// means no FIREWALL6 directive at all, same "no firewall script"
	// behavior Firewall's own nil case already has.
	Firewall6 []string
	// FirewallOnError is set by FIREWALL_ON_ERROR ("open" or "closed"),
	// default "open" -- what buildFirewallScript's EXIT trap falls back
	// to if a FIREWALL rule fails to apply at boot (T91). "open"
	// preserves this project's original behavior (T14): flush to
	// accept-all, so a boot never hangs behind a broken ruleset. Ignored
	// entirely when Firewall is empty (no FIREWALL directive means no
	// firewall script at all). Shared by Firewall6: one fallback-on-error
	// policy applies uniformly to both rulesets rather than doubling the
	// directive surface for what is the same underlying decision either
	// way.
	FirewallOnError string
	// TmpfsSize is set by TMPSIZE (T52); "" means the rootfs package's
	// own default (32m, T27's original hardcoded value). Applies
	// uniformly to all four of stage 1's exec-dir tmpfs mounts
	// (bin/sbin/usr/bin/usr/sbin) -- there is no per-directory override.
	TmpfsSize string
	// StopGrace is set by STOPGRACE <seconds> (T82); 0 means "not
	// declared", and rootfs.PiecesSpec defaults it to a sane built-in
	// (10s) rather than BusyBox init's own ~1-second SIGTERM-then-SIGKILL
	// window, which is too short for a workload with any in-flight work
	// to save (a batch of buffered writes, an open transaction, in-flight
	// HTTP requests) to finish cleanly.
	StopGrace int
	// PiecesKey is set by PIECESKEY <hex-pubkey> (T81 step 1); "" means
	// "not declared" -- build-disk then performs no authenticity check at
	// all, exactly as if signing didn't exist, same tolerance every other
	// optional pieces feature (iptables, pieces.json) gets.
	PiecesKey   string
	Copies      []CopyOp
	Entrypoint  []string
	Cmd         []string
	Services    []Service
	Agent       *Agent
	Workdir     string        // set by WORKDIR; "" means "/" (unchanged from before this directive existed)
	Labels      []EnvVar      // set by LABEL; written to /etc/cnimbus-release
	Exposed     []ExposedPort // set by EXPOSE; informational only
	Healthcheck *Healthcheck  // set by HEALTHCHECK; applies to the entrypoint service only
	// EntrypointRestart is the entrypoint service's restart policy, set
	// via "RESTART entrypoint <policy>" -- see Service.Restart for the
	// policy values. Defaults to "always".
	EntrypointRestart string

	// BaseDir is the directory the Nimbusfile itself lives in; COPY/ADD
	// local sources are resolved relative to it.
	BaseDir string

	// ntpTouched tracks whether an explicit NTP directive has been seen
	// yet, so the first one replaces the default server instead of
	// piling on top of it, while a second (or later) one adds to
	// whatever's already there instead of replacing it. Unexported:
	// pure parse-time bookkeeping, not part of the parsed result.
	ntpTouched bool
}

// Parse reads and parses the Nimbusfile at path. buildArgs supplies
// values for ARG directives (from e.g. "cnimbus build-disk --build-arg
// NAME=VALUE", repeatable); an ARG with no matching entry here falls
// back to its own declared default, or is a parse error if it has
// neither. Pass an empty map for a Nimbusfile that doesn't use ARG.
func Parse(path string, buildArgs map[string]string) (*Nimbusfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	hf := &Nimbusfile{
		KernelVersion:     "latest-stable",
		BusyboxVersion:    "latest",
		EntrypointRestart: "always",
		Arch:              "amd64",
		BootProfile:       "none",
		Hostname:          "cnimbus",
		DHCP:              true,
		Format:            "iso",
		BaseDir:           dirOf(path),
	}

	// argValues accumulates every ARG's resolved value (CLI-supplied
	// override, or its own declared default) as ARG lines are seen --
	// referencing one before its ARG line, or one never declared at
	// all, is a parse error (see substituteArgs).
	argValues := map[string]string{}

	logical, lineNos := joinContinuations(string(data))
	for i, line := range logical {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directive, rest, _ := strings.Cut(line, " ")
		directive = strings.ToUpper(directive)
		rest = strings.TrimSpace(rest)

		if directive == "ARG" {
			name, def, hasDefault := strings.Cut(rest, "=")
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("line %d: ARG requires a name, e.g. \"ARG VERSION\" or \"ARG VERSION=1.0\"", lineNos[i])
			}
			if v, ok := buildArgs[name]; ok {
				argValues[name] = v
			} else if hasDefault {
				argValues[name] = def
			} else {
				return nil, fmt.Errorf("line %d: ARG %s has no default and was not supplied via --build-arg %s=<value>", lineNos[i], name, name)
			}
			continue
		}

		substituted, err := substituteArgs(rest, argValues)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNos[i], err)
		}
		rest = substituted

		if err := hf.apply(directive, rest); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNos[i], err)
		}
	}
	if !hf.ntpTouched {
		hf.NTP = []string{"pool.ntp.org"}
	}
	if err := hf.validateWiFiCrossRefs(); err != nil {
		return nil, err
	}
	return hf, nil
}

// hasWifiDriver reports whether profile builds the 802.11 stack -- true
// for "wifi" and its explicit-both-drivers spelling "eth+wifi" (see
// HARDBOOT's doc comment above), false for "none"/"eth". Centralized here
// rather than repeating "profile == \"wifi\" || profile == \"eth+wifi\""
// at every call site, since every one of those call sites means the exact
// same thing: "does this profile need the WiFi directives/pieces/fragment".
func hasWifiDriver(profile string) bool {
	return profile == "wifi" || profile == "eth+wifi"
}

// validateWiFiCrossRefs enforces HB-F-006/007/008: WIFI/WIFIPSK/WIFICOUNTRY
// only make sense (and are only permitted) under a WiFi-driver profile
// ("wifi" or "eth+wifi"), and those profiles require all three to be set.
// Checked once at the end of Parse, rather than inline in apply(), so it
// doesn't depend on directive order -- a Nimbusfile is free to declare
// HARDBOOT before or after WIFI.
func (hf *Nimbusfile) validateWiFiCrossRefs() error {
	wifiDirectivesSet := hf.WiFiSSID != "" || hf.WiFiPSK != "" || hf.WiFiCountry != ""
	if !hasWifiDriver(hf.BootProfile) {
		if wifiDirectivesSet {
			return fmt.Errorf("WIFI/WIFIPSK/WIFICOUNTRY require \"HARDBOOT wifi\" or \"HARDBOOT eth+wifi\" (got HARDBOOT %q)", hf.BootProfile)
		}
		return nil
	}
	var missing []string
	if hf.WiFiSSID == "" {
		missing = append(missing, "WIFI")
	}
	if hf.WiFiPSK == "" {
		missing = append(missing, "WIFIPSK")
	}
	if hf.WiFiCountry == "" {
		missing = append(missing, "WIFICOUNTRY")
	}
	if len(missing) > 0 {
		return fmt.Errorf("HARDBOOT %s requires %s to also be set", hf.BootProfile, strings.Join(missing, ", "))
	}
	return nil
}

func (hf *Nimbusfile) apply(directive, rest string) error {
	switch directive {
	case "KERNEL":
		if rest == "" {
			return fmt.Errorf("KERNEL requires a version")
		}
		hf.KernelVersion = rest
	case "BUSYBOX":
		if rest == "" {
			return fmt.Errorf("BUSYBOX requires a version")
		}
		hf.BusyboxVersion = rest
	case "ARCH":
		if rest != "amd64" && rest != "arm64" {
			return fmt.Errorf("ARCH must be \"amd64\" or \"arm64\", got %q", rest)
		}
		hf.Arch = rest
	case "VGA":
		v, err := strconv.ParseBool(rest)
		if err != nil {
			return fmt.Errorf("VGA: invalid boolean %q", rest)
		}
		hf.VGA = v
	case "HARDBOOT":
		if rest != "none" && rest != "eth" && rest != "wifi" && rest != "eth+wifi" {
			return fmt.Errorf(`HARDBOOT must be "none", "eth", "wifi", or "eth+wifi", got %q`, rest)
		}
		hf.BootProfile = rest
	case "WIFI":
		if rest == "" {
			return fmt.Errorf("WIFI requires an SSID")
		}
		if hf.WiFiSSID != "" {
			return fmt.Errorf("WIFI already set (only one WIFI directive is supported per Nimbusfile)")
		}
		if err := validateWiFiSSID(rest); err != nil {
			return fmt.Errorf("WIFI: %w", err)
		}
		hf.WiFiSSID = rest
	case "WIFIPSK":
		if rest == "" {
			return fmt.Errorf("WIFIPSK requires a passphrase or a 64-hex-char pre-derived key")
		}
		if err := validateWiFiPSK(rest); err != nil {
			return fmt.Errorf("WIFIPSK: %w", err)
		}
		hf.WiFiPSK = rest
	case "WIFICOUNTRY":
		if err := validateWiFiCountry(rest); err != nil {
			return fmt.Errorf("WIFICOUNTRY: %w", err)
		}
		hf.WiFiCountry = rest
	case "HOSTNAME":
		if rest == "" {
			return fmt.Errorf("HOSTNAME requires a value")
		}
		hf.Hostname = rest
	case "DHCP":
		v, err := strconv.ParseBool(rest)
		if err != nil {
			return fmt.Errorf("DHCP: invalid boolean %q", rest)
		}
		hf.DHCP = v
	case "IP":
		fields := strings.Fields(rest)
		if len(fields) != 3 {
			return fmt.Errorf("IP requires <address> <netmask> <gateway>, got %q", rest)
		}
		for i, name := range []string{"address", "netmask", "gateway"} {
			if net.ParseIP(fields[i]) == nil {
				return fmt.Errorf("IP: %s %q is not a valid IPv4/IPv6 address", name, fields[i])
			}
		}
		hf.StaticIP = &StaticIP{Address: fields[0], Netmask: fields[1], Gateway: fields[2]}
	case "DNS":
		if rest == "" {
			return fmt.Errorf("DNS requires a nameserver address")
		}
		if net.ParseIP(rest) == nil {
			return fmt.Errorf("DNS: %q is not a valid IPv4/IPv6 address", rest)
		}
		hf.DNS = append(hf.DNS, rest)
	case "NTP":
		if rest == "" {
			return fmt.Errorf("NTP requires a server hostname, or \"false\" to disable")
		}
		if rest == "false" {
			hf.NTP = nil
		} else {
			if !hf.ntpTouched {
				hf.NTP = nil // first explicit server replaces the eventual default, doesn't add to it
			}
			hf.NTP = append(hf.NTP, rest)
		}
		hf.ntpTouched = true
	case "FORMAT":
		if rest != "iso" && rest != "raw" && rest != "vhd" {
			return fmt.Errorf("FORMAT must be \"iso\", \"raw\", or \"vhd\", got %q", rest)
		}
		hf.Format = rest
	case "USER":
		if rest == "" {
			return fmt.Errorf("USER requires a name")
		}
		hf.User = rest
	case "WORKDIR":
		if rest == "" {
			return fmt.Errorf("WORKDIR requires a path")
		}
		hf.Workdir = rest
	case "LABEL":
		key, value, ok := strings.Cut(rest, "=")
		if !ok || key == "" {
			return fmt.Errorf("LABEL requires <key>=<value>")
		}
		hf.Labels = append(hf.Labels, EnvVar{Key: key, Value: value})
	case "EXPOSE":
		if rest == "" {
			return fmt.Errorf(`EXPOSE requires a port, e.g. "8080" or "8080/udp"`)
		}
		portStr, proto, hasProto := strings.Cut(rest, "/")
		if !hasProto {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			return fmt.Errorf(`EXPOSE: protocol must be "tcp" or "udp", got %q`, proto)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("EXPOSE: invalid port %q (must be 1-65535)", portStr)
		}
		hf.Exposed = append(hf.Exposed, ExposedPort{Port: port, Proto: proto})
	case "HEALTHCHECK":
		remaining, interval, retries, err := parseHealthcheckFlags(rest)
		if err != nil {
			return fmt.Errorf("HEALTHCHECK: %w", err)
		}
		argv, err := parseExecForm(remaining)
		if err != nil {
			return fmt.Errorf("HEALTHCHECK: %w", err)
		}
		hf.Healthcheck = &Healthcheck{Argv: argv, Interval: interval, Retries: retries}
	case "RESTART":
		fields := strings.Fields(rest)
		if len(fields) != 2 {
			return fmt.Errorf(`RESTART requires <target> <policy> (target: "entrypoint" or a SERVICE name already declared; policy: "always", "on-failure", or "no")`)
		}
		target, policy := fields[0], fields[1]
		if policy != "always" && policy != "on-failure" && policy != "no" {
			return fmt.Errorf(`RESTART: policy must be "always", "on-failure", or "no", got %q`, policy)
		}
		if target == "entrypoint" {
			hf.EntrypointRestart = policy
		} else {
			found := false
			for i := range hf.Services {
				if hf.Services[i].Name == target {
					hf.Services[i].Restart = policy
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("RESTART: no SERVICE named %q declared yet (RESTART must come after the SERVICE line it targets)", target)
			}
		}
	case "VOLUME":
		fields := strings.Fields(rest)
		// "required" (T93) is a trailing modifier, not a positional
		// argument -- stripped first so <device> <mountpoint> [fstype]
		// parsing below is unaffected by whether it's present.
		required := false
		if len(fields) > 0 && fields[len(fields)-1] == "required" {
			required = true
			fields = fields[:len(fields)-1]
		}
		if len(fields) < 2 || len(fields) > 3 {
			return fmt.Errorf("VOLUME requires <device> <mountpoint> [fstype] [required]")
		}
		fstype := "vfat"
		if len(fields) == 3 {
			fstype = fields[2]
			if fstype != "vfat" && fstype != "ext4" {
				return fmt.Errorf(`VOLUME: fstype must be "vfat" or "ext4", got %q`, fstype)
			}
		}
		hf.Volumes = append(hf.Volumes, Volume{Device: fields[0], Mountpoint: fields[1], FSType: fstype, Required: required})
	case "ENV":
		key, value, ok := strings.Cut(rest, "=")
		if !ok || key == "" {
			return fmt.Errorf("ENV requires <KEY>=<VALUE>")
		}
		hf.Env = append(hf.Env, EnvVar{Key: key, Value: value})
	case "FIREWALL":
		if rest == "" {
			return fmt.Errorf("FIREWALL requires an iptables rule")
		}
		if err := validateFirewallRule(rest); err != nil {
			return fmt.Errorf("FIREWALL: %w", err)
		}
		hf.Firewall = append(hf.Firewall, rest)
	case "FIREWALL6":
		if rest == "" {
			return fmt.Errorf("FIREWALL6 requires an ip6tables rule")
		}
		if err := validateFirewallRule(rest); err != nil {
			return fmt.Errorf("FIREWALL6: %w", err)
		}
		hf.Firewall6 = append(hf.Firewall6, rest)
	case "FIREWALL_ON_ERROR":
		if rest != "open" && rest != "closed" {
			return fmt.Errorf(`FIREWALL_ON_ERROR must be "open" or "closed", got %q`, rest)
		}
		hf.FirewallOnError = rest
	case "TMPSIZE":
		if err := validateTmpfsSize(rest); err != nil {
			return fmt.Errorf("TMPSIZE: %w", err)
		}
		hf.TmpfsSize = rest
	case "STOPGRACE":
		seconds, err := strconv.Atoi(rest)
		if err != nil || seconds <= 0 {
			return fmt.Errorf("STOPGRACE requires a positive integer number of seconds, got %q", rest)
		}
		hf.StopGrace = seconds
	case "PIECESKEY":
		raw, err := hex.DecodeString(rest)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return fmt.Errorf("PIECESKEY requires a %d-byte Ed25519 public key as %d hex characters "+
				"(see \"cnimbus keygen\"), got %q", ed25519.PublicKeySize, ed25519.PublicKeySize*2, rest)
		}
		hf.PiecesKey = strings.ToLower(rest)
	case "SERVICE":
		name, cmd, ok := strings.Cut(rest, " ")
		if !ok || name == "" {
			return fmt.Errorf("SERVICE requires <name> <cmd...>")
		}
		argv, err := parseExecForm(cmd)
		if err != nil {
			return fmt.Errorf("SERVICE %s: %w", name, err)
		}
		hf.Services = append(hf.Services, Service{Name: name, Argv: argv})
	case "AGENT":
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return fmt.Errorf("AGENT requires <url> [interval-seconds], or <kind> <value> [interval-seconds] " +
				"(kind: vboxguest, virtio-serial, vmware, aws-imds, ibm-imds), or \"header <Name>: <Value>\"")
		}
		keyword := strings.ToLower(fields[0])
		if keyword == "header" {
			if hf.Agent == nil || hf.Agent.Kind != "http" {
				return fmt.Errorf("AGENT header requires a preceding \"AGENT <url>\" line -- headers only apply to the http kind")
			}
			headerRest := strings.TrimSpace(rest[len(fields[0]):])
			name, value, ok := cutHeader(headerRest)
			if !ok {
				return fmt.Errorf("AGENT header requires \"<Name>: <Value>\" or \"<Name> <Value>\", got %q", headerRest)
			}
			hf.Agent.Headers = append(hf.Agent.Headers, AgentHeader{Name: name, Value: value})
			return nil
		}
		if hf.Agent != nil {
			return fmt.Errorf("AGENT already set (only one AGENT directive is supported per Nimbusfile)")
		}
		switch keyword {
		case "vboxguest", "virtio-serial", "vmware", "aws-imds", "ibm-imds":
			if len(fields) < 2 || len(fields) > 3 {
				return fmt.Errorf("AGENT %s requires <value> [interval-seconds]", keyword)
			}
			interval := "5"
			if len(fields) == 3 {
				if _, err := strconv.Atoi(fields[2]); err != nil {
					return fmt.Errorf("AGENT %s: invalid interval %q, must be a whole number of seconds", keyword, fields[2])
				}
				interval = fields[2]
			}
			hf.Agent = &Agent{Kind: keyword, URL: fields[1], Interval: interval}
		default:
			if len(fields) > 2 {
				return fmt.Errorf("AGENT requires <url> [interval-seconds]")
			}
			interval := "5"
			if len(fields) == 2 {
				if _, err := strconv.Atoi(fields[1]); err != nil {
					return fmt.Errorf("AGENT: invalid interval %q, must be a whole number of seconds", fields[1])
				}
				interval = fields[1]
			}
			hf.Agent = &Agent{Kind: "http", URL: fields[0], Interval: interval}
		}
	case "COPY", "ADD":
		var chmod uint32
		if strings.HasPrefix(rest, "--chmod=") {
			tok, remainder, _ := strings.Cut(rest, " ")
			val := strings.TrimPrefix(tok, "--chmod=")
			parsed, err := strconv.ParseUint(val, 8, 32)
			if err != nil {
				return fmt.Errorf("%s: invalid --chmod value %q (expected octal, e.g. 0644)", directive, val)
			}
			if parsed == 0 {
				return fmt.Errorf("%s: --chmod=0 is not a valid permission", directive)
			}
			// setuid/setgid/sticky (0o7000): honored for real on the
			// shadowed-tmpfs path (stage1.go copies COPY/ADD files into a
			// writable tmpfs, chmod'd for real at boot) but silently
			// dropped on the SquashFS path (go-diskfs's writer has no
			// concept of these bits) -- reject outright rather than let
			// the same Nimbusfile line behave differently depending on
			// which of the two paths a given destination happens to land
			// on.
			if parsed&0o7000 != 0 {
				return fmt.Errorf("%s: --chmod=%s: setuid/setgid/sticky bits (0o7000) are not supported", directive, val)
			}
			chmod = uint32(parsed)
			rest = strings.TrimSpace(remainder)
		}
		src, dest, ok := strings.Cut(rest, " ")
		dest = strings.TrimSpace(dest)
		if !ok || src == "" || dest == "" {
			return fmt.Errorf("%s requires <src> <dest>", directive)
		}
		hf.Copies = append(hf.Copies, CopyOp{
			Src:   src,
			Dest:  dest,
			IsURL: strings.Contains(src, "://"),
			IsAdd: directive == "ADD",
			Chmod: chmod,
		})
	case "ENTRYPOINT":
		args, err := parseExecForm(rest)
		if err != nil {
			return fmt.Errorf("ENTRYPOINT: %w", err)
		}
		hf.Entrypoint = args
	case "CMD":
		args, err := parseExecForm(rest)
		if err != nil {
			return fmt.Errorf("CMD: %w", err)
		}
		hf.Cmd = args
	default:
		return fmt.Errorf("unknown directive %q", directive)
	}
	return nil
}

// parseExecForm accepts either JSON-array exec form (["a","b"]) or
// plain shell form (naive whitespace split, no quoting) -- same two
// forms Dockerfile's ENTRYPOINT/CMD accept.
func parseExecForm(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("requires at least a command")
	}
	if strings.HasPrefix(s, "[") {
		var args []string
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return nil, fmt.Errorf("invalid exec-form array %q: %w", s, err)
		}
		return args, nil
	}
	return strings.Fields(s), nil
}

// substituteArgs replaces every "${NAME}" or bare "$NAME" reference in
// rest with its resolved ARG value, erroring on any name not present in
// values (never declared via an ARG directive at all, or referenced
// before its ARG line -- values only ever contains names seen so far).
// "$$" is a literal, unexpanded "$" -- the escape hatch for
// ENTRYPOINT/CMD lines that need a literal "$VAR" passed through to
// BusyBox's sh for runtime (not Nimbusfile-parse-time) expansion.
func substituteArgs(rest string, values map[string]string) (string, error) {
	if !strings.Contains(rest, "$") {
		return rest, nil
	}
	var out strings.Builder
	i := 0
	for i < len(rest) {
		c := rest[i]
		if c != '$' || i+1 >= len(rest) {
			out.WriteByte(c)
			i++
			continue
		}
		if rest[i+1] == '$' {
			// "$$" is the escape for a literal "$" -- same convention
			// Docker's own Dockerfile ARG/ENV expansion uses, needed
			// so ENTRYPOINT/CMD lines can pass a literal "$VAR" through
			// for BusyBox's sh to expand at runtime (from an ENV
			// directive) instead of at Nimbusfile-parse time.
			out.WriteByte('$')
			i += 2
			continue
		}
		if rest[i+1] == '{' {
			end := strings.IndexByte(rest[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated \"${\" in %q", rest)
			}
			name := rest[i+2 : i+2+end]
			val, ok := values[name]
			if !ok {
				return "", fmt.Errorf("undefined ARG %q (declare it with \"ARG %s\" before this line, or pass --build-arg %s=<value>)", name, name, name)
			}
			out.WriteString(val)
			i = i + 2 + end + 1
			continue
		}
		// A bare $NAME reference's first character must be a letter or
		// underscore, matching shell variable naming -- "$5" (a literal
		// dollar sign followed by a digit, e.g. a price) is not a
		// reference to a variable named "5", it's just "$5".
		if i+1 >= len(rest) || !isArgNameStartByte(rest[i+1]) {
			out.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		for j < len(rest) && isArgNameByte(rest[j]) {
			j++
		}
		name := rest[i+1 : j]
		val, ok := values[name]
		if !ok {
			return "", fmt.Errorf("undefined ARG %q (declare it with \"ARG %s\" before this line, or pass --build-arg %s=<value>)", name, name, name)
		}
		out.WriteString(val)
		i = j
	}
	return out.String(), nil
}

func isArgNameByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isArgNameStartByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// parseHealthcheckFlags strips leading "--interval=<secs>" and/or
// "--retries=<n>" tokens (either order, both optional) off the front of
// a HEALTHCHECK line, defaulting to 30s/3 retries, and returns whatever
// remains (the command itself, still in exec-form-or-shell-form,
// unparsed) for the caller to hand to parseExecForm.
func parseHealthcheckFlags(rest string) (remaining, interval, retries string, err error) {
	interval, retries = "30", "3"
	for {
		rest = strings.TrimSpace(rest)
		const intervalPrefix, retriesPrefix = "--interval=", "--retries="
		var prefix string
		switch {
		case strings.HasPrefix(rest, intervalPrefix):
			prefix = intervalPrefix
		case strings.HasPrefix(rest, retriesPrefix):
			prefix = retriesPrefix
		default:
			return rest, interval, retries, nil
		}
		tok, remainder, _ := strings.Cut(rest, " ")
		val := strings.TrimPrefix(tok, prefix)
		if _, e := strconv.Atoi(val); e != nil {
			return "", "", "", fmt.Errorf("invalid %s%q", prefix, val)
		}
		if prefix == intervalPrefix {
			interval = val
		} else {
			retries = val
		}
		rest = remainder
	}
}

// firewallMetaChars are shell metacharacters that must never appear in a
// FIREWALL rule. internal/rootfs's buildFirewallScript splices a rule's
// text unquoted into a root-run shell script during sysinit --
// deliberately, since a rule is legitimately many words -- so this check
// is the one boundary standing between a declared rule and arbitrary
// command execution. It matters specifically because FIREWALL rules can
// reference an ARG (substituted before this validation runs -- see
// Parse's substituteArgs call), and ARG values can come from
// --build-arg on the command line, i.e. from CI parameters, which are
// far more often attacker-influenced than a committed Nimbusfile (T90).
const firewallMetaChars = ";&|`$()<>\n"

// firewallOps is the set of real iptables-legacy operations (short and
// long form) a FIREWALL rule's first token may be. Deliberately not
// "anything iptables accepts" -- narrowing to the actual set of
// operations keeps the check simple and catches an injected rule whose
// first token isn't even a real iptables flag, on top of the
// metacharacter check above.
var firewallOps = map[string]bool{
	"-A": true, "--append": true,
	"-I": true, "--insert": true,
	"-D": true, "--delete": true,
	"-R": true, "--replace": true,
	"-L": true, "--list": true,
	"-F": true, "--flush": true,
	"-Z": true, "--zero": true,
	"-N": true, "--new-chain": true,
	"-X": true, "--delete-chain": true,
	"-P": true, "--policy": true,
	"-E": true, "--rename-chain": true,
}

func validateFirewallRule(rule string) error {
	if strings.ContainsAny(rule, firewallMetaChars) {
		return fmt.Errorf("rule %q contains a shell metacharacter (one of %q), which is never valid in a FIREWALL rule -- "+
			"if this came from an ARG substitution, the --build-arg value is the problem", rule, firewallMetaChars)
	}
	fields := strings.Fields(rule)
	if len(fields) == 0 || !firewallOps[fields[0]] {
		return fmt.Errorf("rule %q must start with a real iptables operation (-A/-I/-D/-R/-L/-F/-Z/-N/-X/-P/-E, "+
			"or its long form), got %q", rule, firstFieldOrEmpty(fields))
	}
	return nil
}

// validateTmpfsSize checks a TMPSIZE value against the same format the
// Linux tmpfs "size=" mount option itself accepts (T52): a plain byte
// count, or one suffixed with k/K, m/M or g/G. Rejects "0" and anything
// with a leading zero or sign, since those aren't meaningful tmpfs sizes
// and the mount option's own parser would silently misbehave on them.
var tmpfsSizePattern = regexp.MustCompile(`^[1-9][0-9]*[kKmMgG]?$`)

func validateTmpfsSize(size string) error {
	if !tmpfsSizePattern.MatchString(size) {
		return fmt.Errorf(`%q is not a valid tmpfs size -- expected a positive integer optionally suffixed with k/m/g, e.g. "128m"`, size)
	}
	return nil
}

// wifiMetaChars mirrors firewallMetaChars' reasoning (T90): the SSID and
// PSK both get spliced into a generated wpa_supplicant config file
// (internal/rootfs, HB-F-005/HB-S-002), quoted with plain double quotes and
// no escaping, so a value containing a quote, backslash, or newline could
// break out of its field -- and both can arrive via an ARG substituted from
// --build-arg, i.e. from CI parameters, same attacker-influence path T90
// closed for FIREWALL.
const wifiMetaChars = "\"\\\n\r"

// validateWiFiSSID enforces HB-F-009: an SSID is at most 32 bytes per the
// 802.11 spec, and must not carry a character that could break out of the
// generated config file's quoting.
func validateWiFiSSID(ssid string) error {
	if len(ssid) > 32 {
		return fmt.Errorf("SSID %q is %d bytes, exceeds the 802.11 maximum of 32", ssid, len(ssid))
	}
	if strings.ContainsAny(ssid, wifiMetaChars) {
		return fmt.Errorf("SSID %q contains a quote, backslash, or newline, which is never valid in an SSID -- "+
			"if this came from an ARG substitution, the --build-arg value is the problem", ssid)
	}
	return nil
}

// wifiPSKHexPattern matches a pre-derived 64-hex-char PMK (HB-F-010) --
// wpa_supplicant accepts this in place of a passphrase, and D5 in the
// design prefers it since it avoids baking a reused passphrase into the
// image.
var wifiPSKHexPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// validateWiFiPSK enforces HB-F-010: a WPA2-PSK passphrase is 8-63 printable
// ASCII characters per the 802.11i spec, or a 64-hex-char pre-derived key.
// Same quoting concern as the SSID applies, since both land in the same
// generated config file.
func validateWiFiPSK(psk string) error {
	if wifiPSKHexPattern.MatchString(psk) {
		return nil
	}
	if len(psk) < 8 || len(psk) > 63 {
		return fmt.Errorf("must be a passphrase of 8-63 characters, or a 64-hex-char pre-derived key (got %d chars)", len(psk))
	}
	if strings.ContainsAny(psk, wifiMetaChars) {
		return fmt.Errorf("contains a quote, backslash, or newline, which is never valid in a WIFIPSK passphrase -- " +
			"if this came from an ARG substitution, the --build-arg value is the problem")
	}
	for _, r := range psk {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("contains a non-printable-ASCII character, which is never valid in a WIFIPSK passphrase")
		}
	}
	return nil
}

// wifiCountryPattern matches an ISO 3166-1 alpha-2 country code -- the
// format the kernel's regulatory database (CONFIG_CFG80211) expects.
var wifiCountryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

func validateWiFiCountry(country string) error {
	if country == "" {
		return fmt.Errorf("requires an ISO 3166-1 alpha-2 country code, e.g. \"BR\" or \"US\"")
	}
	if !wifiCountryPattern.MatchString(country) {
		return fmt.Errorf("%q is not a valid ISO 3166-1 alpha-2 country code (two uppercase letters, e.g. \"BR\")", country)
	}
	return nil
}

func firstFieldOrEmpty(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// cutHeader splits an "AGENT header" line's remainder into a name and
// value: "Name: Value" (the natural way to write an HTTP header) if it
// contains a colon, else "Name Value" on the first space.
func cutHeader(s string) (name, value string, ok bool) {
	if n, v, found := strings.Cut(s, ":"); found {
		n, v = strings.TrimSpace(n), strings.TrimSpace(v)
		if n == "" || v == "" {
			return "", "", false
		}
		return n, v, true
	}
	n, v, found := strings.Cut(s, " ")
	n, v = strings.TrimSpace(n), strings.TrimSpace(v)
	if !found || n == "" || v == "" {
		return "", "", false
	}
	return n, v, true
}

// joinContinuations splits src into logical lines, joining any line
// ending in a bare trailing backslash with the next physical line
// (Dockerfile-style line continuation). lineNos[i] is the 1-indexed
// physical line number the i-th logical line *started* on, so a parse
// error inside (or after) a `\`-continued block still points at a real
// line in the file the user can find, rather than the post-join index.
func joinContinuations(src string) (logical []string, lineNos []int) {
	physical := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var acc string
	startLine := 0
	for i, l := range physical {
		if acc == "" {
			startLine = i + 1
		}
		trimmed := strings.TrimRight(l, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			acc += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		logical = append(logical, acc+l)
		lineNos = append(lineNos, startLine)
		acc = ""
	}
	if acc != "" {
		logical = append(logical, acc)
		lineNos = append(lineNos, startLine)
	}
	return logical, lineNos
}

func dirOf(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if i < 0 {
		return "."
	}
	return path[:i]
}
