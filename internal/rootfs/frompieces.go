package rootfs

import (
	"fmt"
	"strconv"
	"strings"
)

// hasWifiDriver reports whether profile is a WiFi-driver boot profile:
// "wifi", or its explicit-both-drivers spelling "eth+wifi" (see
// internal/nimbusfile's HARDBOOT doc comment). Duplicated in
// internal/pieces rather than shared, same reasoning as BusyboxApplet
// below -- this package stays usable standalone with no dependency on
// internal/pieces.
func hasWifiDriver(profile string) bool {
	return profile == "wifi" || profile == "eth+wifi"
}

// BusyboxApplet mirrors pieces.Applet without importing that package
// here (internal/rootfs stays usable standalone / from cnimbus-agent,
// which has no reason to depend on internal/pieces).
type BusyboxApplet struct {
	Path   string
	Target string
}

// PiecesSpec configures the two images built from prebuilt pieces (a
// BusyBox binary + its applet manifest) rather than by walking a real
// install-tree directory. This is the path the end-user `cnimbus build`
// CLI uses -- no container, no directory of real symlinks to walk.
type PiecesSpec struct {
	BusyboxBinary  []byte
	BusyboxApplets []BusyboxApplet
	Hostname       string
	DHCP           bool
	StaticIP       *StaticIP // wins over DHCP when set
	DNS            []string  // explicit nameservers; wins over whatever DHCP itself provides
	NTP            []string  // NTP servers to sync against at boot; empty disables it
	Volumes        []Volume  // optional persistent mounts; repeatable
	Firewall       []string  // iptables (IPv4) rule lines
	// Firewall6 (AD-047) mirrors Firewall for FIREWALL6 -- ip6tables rule
	// lines, applied as their own separate ruleset via the same bundled
	// multi-call binary invoked in its ip6tables dispatch mode (see
	// internal/compileagent/iptables.go). Independent of Firewall: empty
	// means no FIREWALL6 directive, same as Firewall's own empty case.
	Firewall6 []string
	// FirewallOnError (T91) is "open" (default, "" means the same) or
	// "closed" -- what the generated firewall script's EXIT trap falls
	// back to when a FIREWALL rule fails to apply at boot. Ignored when
	// Firewall is empty. Shared by Firewall6 (AD-047): one policy for
	// both rulesets, since it's the same underlying "what should happen
	// on a broken rule" decision either way.
	FirewallOnError string
	// Iptables is the static iptables-legacy binary from the pieces
	// source (see internal/compileagent/iptables.go); nil falls back to
	// requiring a COPY'd one, exactly as before this field existed.
	Iptables []byte
	// Supplicant (F6.4) is the static wpa_supplicant binary from a
	// HARDBOOT wifi pieces set (see
	// internal/compileagent/wpasupplicant.go); nil for "none"/"eth".
	// Staged the same way Iptables is -- see splitShadowedFiles below --
	// since it's a real ELF binary that needs a genuine execute bit.
	Supplicant []byte
	// WifiFirmware (F6.3) is the curated firmware set from a HARDBOOT
	// wifi pieces set (see internal/compileagent/wifi.go's
	// wifiFirmwareBlobs), keyed by each blob's /lib/firmware-relative
	// path. Unlike every other *Files field on this struct, these land
	// directly in stage 1's own initramfs tree at "lib/firmware/"+path,
	// not through the tmpfs-shadow/SquashFS split splitShadowedFiles
	// implements -- see stage1.go's doc comment on why: a built-in
	// driver's request_firmware() call happens during kernel init,
	// before switch_root ever runs, so the firmware must already be
	// part of stage 1's own root, not merely staged into it by /init.
	WifiFirmware map[string][]byte
	// EthernetFirmware (AD-057) mirrors WifiFirmware above for the
	// wired-NIC driver family (see
	// internal/compileagent/ethernet.go's ethernetFirmwareBlobs); same
	// staging path and same reasoning, nil for "none"/"wifi".
	EthernetFirmware map[string][]byte
	// BootProfile (F6.5) mirrors nimbusfile.Nimbusfile.BootProfile --
	// "none" (default)/"eth"/"wifi"/"eth+wifi". Only a WiFi-driver profile
	// ("wifi" or "eth+wifi", see hasWifiDriver) changes anything here: it
	// gates whether buildRCScript emits the wpa_supplicant bring-up block
	// and whether BuildImages generates wpa_supplicant.conf at all. "" is
	// treated the same as "none" (an older caller that never set this
	// field gets the exact pre-F6.5 behavior).
	BootProfile string
	// WifiSSID/WifiPSK/WifiCountry (F6.5) mirror nimbusfile.Nimbusfile's
	// WiFiSSID/WiFiPSK/WiFiCountry -- only meaningful when BootProfile is
	// "wifi" or "eth+wifi", already validated by internal/nimbusfile
	// (SSID/PSK length + injection-metachar guards, ISO 3166-1 alpha-2
	// country) before ever reaching this package. WifiPSK is never
	// logged, never placed in an ExtraFile outside the 0600 shadow path
	// below, and never becomes a shell command-line argument anywhere
	// in the generated scripts (HB-S-002/003) -- it only ever appears as
	// a line inside wpa_supplicant.conf, which wpa_supplicant reads by
	// path, not by argv.
	WifiSSID    string
	WifiPSK     string
	WifiCountry string
	Env         []EnvVar // exported into every Service's environment

	// User is the unprivileged account every Service is dropped to via
	// BusyBox's setuidgid (uid/gid 1000). Empty means root -- the
	// default, unchanged from before this field existed.
	//
	// There is no respawned shell anywhere in this image (see
	// buildInittab): "distroless"-style, the only way in is whatever
	// Services a Nimbusfile declares. USER only controls what those
	// services run as; it does not add or gate any interactive access,
	// because there isn't any to gate.
	User string

	ExtraFiles []ExtraFile
	// Services are every respawned, supervised process: index 0 is
	// conventionally the Nimbusfile's ENTRYPOINT/CMD (name "entrypoint"),
	// followed by one per SERVICE directive. Empty means no respawned
	// process at all -- a valid, if unusual, image that just boots.
	Services []Service
	// Agent is set by the AGENT directive; nil means no runtime
	// key-value polling at all (no extra process, no extra file).
	Agent *Agent
	// Workdir is set by WORKDIR; "" means every Service runs in "/"
	// (unchanged from before this directive existed).
	Workdir string
	// Healthcheck is set by HEALTHCHECK; applies only to the Service
	// named "entrypoint" (see buildSupervisorScript).
	Healthcheck *Healthcheck
	// TmpfsSize is set by TMPSIZE (T52); "" means defaultTmpfsSize
	// ("32m", T27's original hardcoded value -- existing images are
	// unaffected by TMPSIZE's introduction). Applies to all four of
	// stage 1's exec-dir tmpfs mounts (bin/sbin/usr/bin/usr/sbin)
	// uniformly; there is no per-directory override.
	TmpfsSize string
	// TmpDir (T79) is the directory buildSquashfsRoot's workspace file is
	// created under; "" means the OS default ($TMPDIR/%TEMP%), unchanged
	// from before this field existed.
	TmpDir string
	// StopGrace is set by STOPGRACE (T82); 0 means defaultStopGrace (10s).
	// The entrypoint service's real child PID (tracked whenever it has a
	// HEALTHCHECK, since that's the only path that already backgrounds
	// the command and captures its actual PID rather than a pipe's) is
	// signaled directly by the generated shutdown script and given up to
	// this many seconds to exit before being escalated to SIGKILL, before
	// `umount -a -r` runs. Services without a HEALTHCHECK still benefit
	// from the same overall window (the shutdown script blocks for it
	// regardless), just without a precisely-targeted signal of their own
	// -- see buildShutdownScript's doc comment for why.
	StopGrace int
	// VGA mirrors nimbusfile.Nimbusfile.VGA. When true, buildRCScript
	// prints every interface's global-scope IPv4 (and IPv6, if the guest
	// actually has one) address to the console once networking comes up
	// -- the one way to learn an image's assigned address when there's no
	// shell to log into and no serial capture, only a hypervisor's VGA
	// console window. Skipped when false: a serial-only/headless boot has
	// no screen for this to be read from, and printing it there too would
	// just be console noise nobody asked for.
	VGA bool
}

// defaultStopGrace (T82): BusyBox init's own shutdown sequence (after
// the ::shutdown: action returns) sends SIGTERM to every process, waits
// only 1 second, then SIGKILLs everything -- far too short for a
// workload with any in-flight work (buffered writes, an open
// transaction, in-flight HTTP requests) to finish cleanly. 10s is a
// deliberately conservative default budget, well under typical
// hypervisor shutdown timeouts (Proxmox's own qm shutdown default is
// 180s) while still being enough to matter.
const defaultStopGrace = 10

// Healthcheck mirrors nimbusfile.Healthcheck.
type Healthcheck struct {
	Argv     []string
	Interval string
	Retries  string
}

// Images is the two-stage boot's output. Stage1 is what the kernel
// itself unpacks (a small BusyBox + its applet symlinks + an /init
// that finds the boot media, loop-mounts SquashfsRoot from it, and
// switch_root's in). SquashfsRoot is everything else: a genuinely
// read-only filesystem holding /etc, supervisor scripts, and whatever
// COPY/ADD placed outside bin/sbin/usr/{bin,sbin} -- see stage1.go's
// doc comment for why those four directories can't live here too.
type Images struct {
	Stage1 []byte
	// SquashfsRootPath (T75) is a path to the finished SquashFS image on
	// disk, not its bytes: this file can legitimately be gigabytes (a
	// large COPY/VOLUME payload), and go-diskfs already streams it to a
	// real temp file as it writes it -- returning it as a []byte here
	// just to have isoimage/rawimage write it right back out to another
	// file was a needless, unbounded round-trip through the heap. The
	// caller owns this path and must remove it once the caller's own
	// downstream Write (isoimage.Write/rawimage.Write) has consumed it.
	SquashfsRootPath string
}

// BuildImages assembles both images from a PiecesSpec.
func BuildImages(spec PiecesSpec) (Images, error) {
	if spec.Hostname == "" {
		spec.Hostname = "cnimbus"
	}

	// Supervisor scripts and the HTTP AGENT script are 0600-generated
	// (they carry ENV values and, for the agent script, a bearer token,
	// as literal shell text) and therefore must go through
	// splitShadowedFiles/stage 1's real-chmod path rather than the
	// SquashFS writer, whose mode fidelity depends on the build host's
	// own filesystem (T73) -- appended to spec.ExtraFiles before the
	// split below so they're routed exactly like any other
	// usr/sbin/-destined file.
	extraFiles := append([]ExtraFile{}, spec.ExtraFiles...)
	for _, svc := range spec.Services {
		var hc *Healthcheck
		if svc.Name == "entrypoint" { // HEALTHCHECK only ever applies to the entrypoint service, mirroring Docker's own single-HEALTHCHECK-per-container model
			hc = spec.Healthcheck
		}
		script := buildSupervisorScript(svc, spec.Env, spec.User, spec.Workdir, hc)
		extraFiles = append(extraFiles, ExtraFile{Path: supervisorScriptPath(svc.Name), Perm: 0o600, Data: []byte(script)})
	}
	if spec.Agent != nil && spec.Agent.Kind == "http" {
		extraFiles = append(extraFiles, ExtraFile{Path: agentScriptPath, Perm: 0o600, Data: []byte(buildAgentScript(spec.Agent))})
	}
	shadowed, normal := splitShadowedFiles(extraFiles)

	// udhcpc.script and powerbtn.sh are execve()'d directly by udhcpc
	// and acpid respectively -- no interpreter in between to hide behind
	// the way rcS and the supervisor scripts do -- so they need a real
	// execute bit. Generated internally rather than through
	// splitShadowedFiles because they aren't Nimbusfile ExtraFiles; they
	// still land in stage 1's tmpfs for exactly the same reason (see
	// stage1.go's doc comment on why the SquashFS root can't carry one).
	if spec.DHCP && spec.StaticIP == nil {
		shadowed = append(shadowed, ExtraFile{Path: "sbin/udhcpc.script", Perm: 0o755, Data: []byte(udhcpcScript)})
	}
	// Unconditional, regardless of the primary NIC's own DHCP/StaticIP
	// setting: a secondary NIC (eth1..eth3) is only ever detected at
	// boot (see buildRCScript), never known at build time, so this has
	// to always be present -- harmless if no secondary NIC ever shows up.
	shadowed = append(shadowed, ExtraFile{Path: "sbin/udhcpc-secondary.script", Perm: 0o755, Data: []byte(udhcpcScriptSecondary)})
	if len(spec.Iptables) > 0 {
		// usr/sbin/, like bin/sbin/usr/bin/, is unconditionally
		// tmpfs-shadowed by stage 1 (see stage1.go) -- a copy placed in
		// the normal SquashFS files list below would simply be invisible
		// under that mount, exactly like a COPY destined for one of
		// those four directories. Same reasoning, same fix: travel
		// through stage 1's tmpfs instead, same as cmd/cnimbusagent above.
		shadowed = append(shadowed, ExtraFile{Path: "usr/sbin/cnimbus-iptables", Perm: 0o755, Data: spec.Iptables})
	}
	shadowed = append(shadowed, ExtraFile{Path: "sbin/powerbtn.sh", Perm: 0o755, Data: []byte(acpiPowerScript)})
	if len(spec.Supplicant) > 0 {
		// F6.4: same tmpfs-shadow reasoning as Iptables above -- a real
		// static ELF binary, needs a real execute bit, destined for
		// usr/sbin/ like iptables and cnimbusagent. F6.5 (a separate,
		// later workstream) is what will actually invoke this from the
		// rcS network stage against a generated wpa_supplicant.conf; this
		// only stages the binary itself.
		shadowed = append(shadowed, ExtraFile{Path: "usr/sbin/wpa_supplicant", Perm: 0o755, Data: spec.Supplicant})
	}
	if hasWifiDriver(spec.BootProfile) && len(spec.Supplicant) > 0 {
		// F6.5: the config wpa_supplicant reads by path (never by argv --
		// see buildRCScript's invocation), carrying the PSK as plain text.
		// Same tmpfs-shadow/real-chmod reasoning as the supervisor
		// scripts (T73): go-diskfs's SquashFS writer can't be trusted for
		// mode fidelity on a Windows build host, and this file's whole
		// point is confidentiality (HB-S-001), not an execute bit -- 0600,
		// verified by reading a real built image, not the generator.
		conf := buildWpaSupplicantConf(spec.WifiSSID, spec.WifiPSK, spec.WifiCountry)
		shadowed = append(shadowed, ExtraFile{Path: "usr/sbin/wpa_supplicant.conf", Perm: 0o600, Data: []byte(conf)})
	}
	if spec.Agent != nil {
		// A real ELF binary (see cmd/cnimbusagent), not a generated
		// script -- needs a real execute bit, so it travels the same
		// tmpfs path as any other execve()'d-directly file. Every AGENT
		// kind uses this one binary now, including "http" (whose
		// generated shell script -- see buildAgentScript -- just execs
		// this in turn).
		shadowed = append(shadowed, ExtraFile{Path: "usr/bin/cnimbusagent", Perm: 0o755, Data: spec.Agent.AgentBinary})
	}

	// T52's build-time half of T51's fix: an over-budget COPY previously
	// only failed at *boot* (ENOSPC on the tmpfs, caught by T51's
	// writeStagingCheck) -- long after `build-disk` had already reported
	// success. Checked per exec directory since each of the four is its
	// own independent tmpfs mount; a COPY that overflows usr/bin doesn't
	// care how much headroom bin/ happens to have.
	if err := checkShadowedFilesFitTmpfs(shadowed, spec.BusyboxBinary, spec.TmpfsSize); err != nil {
		return Images{}, err
	}

	// AD-057: WiFi and Ethernet firmware share one stage-1 embedding
	// mechanism (both just land at "lib/firmware/"+path -- see stage1.go)
	// -- merged here so buildStage1Initramfs's own signature doesn't need
	// a second, near-identical map parameter for what is, from its own
	// perspective, the exact same operation done twice.
	firmware := mergeFirmwareMaps(spec.WifiFirmware, spec.EthernetFirmware)
	stage1, err := buildStage1Initramfs(spec.BusyboxBinary, spec.BusyboxApplets, shadowed, firmware, spec.TmpfsSize)
	if err != nil {
		return Images{}, err
	}

	rootPath, err := buildSquashfsRoot(spec, normal)
	if err != nil {
		return Images{}, err
	}

	return Images{Stage1: stage1, SquashfsRootPath: rootPath}, nil
}

// splitShadowedFiles separates ExtraFiles whose destination falls
// under bin/, sbin/, usr/bin/, or usr/sbin/ -- those four directories
// are tmpfs, populated by stage 1 before switch_root (see stage1.go),
// so anything placed there by the read-only SquashFS root would
// simply be invisible underneath that mount. Shadowed files are
// instead staged into stage 1's own initramfs and copied into place by
// its /init script, before switch_root hands off.
func splitShadowedFiles(files []ExtraFile) (shadowed, normal []ExtraFile) {
	for _, f := range files {
		if isShadowedPath(f.Path) {
			shadowed = append(shadowed, f)
		} else {
			normal = append(normal, f)
		}
	}
	return shadowed, normal
}

// IsShadowedPath reports whether p (an ExtraFile destination) lands in
// one of stage 1's four exec-dir tmpfs mounts rather than the ordinary
// SquashFS root -- exported (T77) so cmd/cnimbus/build.go can identify
// which COPY/ADD lines actually grew stage 1's initramfs when it needs
// to explain an El Torito size-ceiling failure back to the Nimbufile
// author, without duplicating this prefix list a second time.
func IsShadowedPath(p string) bool {
	return isShadowedPath(p)
}

func isShadowedPath(p string) bool {
	p = trimLeadingSlash(p)
	for _, prefix := range []string{"bin/", "sbin/", "usr/bin/", "usr/sbin/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func trimLeadingSlash(p string) string {
	return strings.TrimPrefix(p, "/")
}

// execDirOf returns which of stage 1's four independent tmpfs exec
// mounts (see stage1.go) p's shadowed destination falls under, or ""
// if p isn't a shadowed path at all (mirrors isShadowedPath's own
// prefix list -- kept as two functions rather than one returning
// (dir, bool) so isShadowedPath's existing simple bool callers are
// undisturbed).
func execDirOf(p string) string {
	p = trimLeadingSlash(p)
	for _, dir := range []string{"bin", "sbin", "usr/bin", "usr/sbin"} {
		if strings.HasPrefix(p, dir+"/") {
			return dir
		}
	}
	return ""
}

// parseTmpfsSizeBytes parses a TMPSIZE value (or defaultTmpfsSize when
// size is "") the same way the Linux tmpfs "size=" mount option itself
// does: a plain byte count, or one suffixed with k/K, m/M or g/G (binary
// multiples, matching tmpfs's own interpretation -- verified against
// Documentation/filesystems/tmpfs.rst).
func parseTmpfsSizeBytes(size string) (int64, error) {
	if size == "" {
		size = defaultTmpfsSize
	}
	mult := int64(1)
	numPart := size
	if n := len(size); n > 0 {
		switch size[n-1] {
		case 'k', 'K':
			mult, numPart = 1024, size[:n-1]
		case 'm', 'M':
			mult, numPart = 1024*1024, size[:n-1]
		case 'g', 'G':
			mult, numPart = 1024*1024*1024, size[:n-1]
		}
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid TMPSIZE value %q", size)
	}
	return n * mult, nil
}

// checkShadowedFilesFitTmpfs is T52's build-time half of T51's fix
// (see BuildImages' call site): sums the bytes destined for each of the
// four exec-dir tmpfs mounts -- busyboxBinary always lands in bin/ (see
// buildStage1Init's unconditional "cp /bin/busybox ..."), plus whatever
// COPY/ADD/generated ExtraFiles (iptables, cnimbusagent, supervisor
// scripts, ...) are shadowed into any of the four -- and fails
// `build-disk` up front with a clear message instead of letting an
// over-budget image "succeed" and only fail at boot.
func checkShadowedFilesFitTmpfs(shadowed []ExtraFile, busyboxBinary []byte, tmpfsSize string) error {
	limit, err := parseTmpfsSizeBytes(tmpfsSize)
	if err != nil {
		return err
	}
	totals := map[string]int64{"bin": int64(len(busyboxBinary))}
	for _, f := range shadowed {
		dir := execDirOf(f.Path)
		if dir == "" {
			continue
		}
		totals[dir] += int64(len(f.Data))
	}
	for _, dir := range []string{"bin", "sbin", "usr/bin", "usr/sbin"} {
		if totals[dir] > limit {
			effective := tmpfsSize
			if effective == "" {
				effective = defaultTmpfsSize
			}
			return fmt.Errorf(
				"TMPSIZE too small: /%s needs at least %d bytes but its tmpfs is only %s (%d bytes) -- "+
					"raise it with a Nimbusfile \"TMPSIZE <size>\" directive (e.g. \"TMPSIZE 128m\"), "+
					"or move large COPY'd files outside bin/sbin/usr/bin/usr/sbin",
				dir, totals[dir], effective, limit)
		}
	}
	return nil
}

// mergeFirmwareMaps combines WiFi and Ethernet firmware into the single
// map buildStage1Initramfs actually embeds -- see BuildImages's own
// comment on why one map rather than two parameters. nil is returned
// (not an empty map) when both inputs are nil, matching every other
// optional-piece field's "absent means exactly what it did before this
// existed" tolerance elsewhere in this package.
func mergeFirmwareMaps(wifi, ethernet map[string][]byte) map[string][]byte {
	if len(wifi) == 0 {
		return ethernet
	}
	if len(ethernet) == 0 {
		return wifi
	}
	merged := make(map[string][]byte, len(wifi)+len(ethernet))
	for path, data := range wifi {
		merged[path] = data
	}
	for path, data := range ethernet {
		merged[path] = data
	}
	return merged
}
