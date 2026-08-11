package rootfs

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// ExtraFile is a caller-supplied file placed into the image on top of
// the base BusyBox tree (e.g. a statically-linked userland binary).
type ExtraFile struct {
	Path string // e.g. "usr/bin/helloserver"
	Perm uint32
	Data []byte
}

// EnvVar is one ENV directive; order is preserved so that a later
// entry with the same Key overrides an earlier one (last export wins,
// standard shell semantics).
type EnvVar struct {
	Key   string
	Value string
}

// StaticIP mirrors nimbusfile.StaticIP without importing that package
// (same reasoning as BusyboxApplet below: keeps this package usable
// standalone).
type StaticIP struct {
	Address string
	Netmask string
	Gateway string
}

// Volume mirrors nimbusfile.Volume: a block device to mount at boot for
// persistent storage.
type Volume struct {
	Device     string
	Mountpoint string
	FSType     string // "vfat" or "ext4"
	// Required halts boot with a FATAL message when this volume fails to
	// mount, instead of logging and continuing (T93).
	Required bool
}

// Service is one respawned, supervised process: either the Nimbusfile's
// ENTRYPOINT/CMD (Name "entrypoint") or an additional SERVICE
// directive.
type Service struct {
	Name string
	Argv []string
	// Restart is "always" (default -- unconditional respawn, capped-
	// linear backoff), "on-failure" (respawn only on a non-zero exit
	// code), or "no" (run once, never respawn). Empty is treated as
	// "always" so a zero-value Service keeps cnimbus's original behavior.
	Restart string
}

// AgentHeader mirrors nimbusfile.AgentHeader: one extra header the
// plain-HTTP AGENT kind's wget adds to every poll.
type AgentHeader struct {
	Name  string
	Value string
}

// Agent mirrors nimbusfile.Agent. AgentBinary is cmd/cnimbusagent,
// prebuilt for the Nimbusfile's own ARCH (see
// assets.CnimbusAgentAmd64/Arm64) -- populated by cmd/cnimbus, which is
// the layer that knows which ARCH is in play; rootfs stays arch-agnostic
// otherwise. Every kind uses the same binary now (see buildInittab), so
// AgentBinary is always set whenever Agent is non-nil.
type Agent struct {
	Kind        string // "http", "vboxguest", "virtio-serial", "vmware", "aws-imds", or "ibm-imds"
	URL         string
	Interval    string
	Headers     []AgentHeader // http kind only
	AgentBinary []byte
}

// buildInittab wires up busybox-init. There is deliberately no
// respawned shell here (see PiecesSpec.User's doc comment): the only
// entry points into a running cnimbus image are whatever ENTRYPOINT/CMD
// and SERVICE directives declare, each running under its own
// supervisor script (see buildSupervisorScript). No shell means no
// interactive access point at all unless a Nimbusfile explicitly builds
// one in.
func buildInittab(services []Service, agent *Agent) string {
	var b strings.Builder
	// "/bin/sh /etc/init.d/rcS", not a bare path: rcS lives on the
	// read-only SquashFS root, built on the host running `cnimbus` itself
	// -- which, on Windows, cannot preserve a real execute bit at all
	// (NTFS has no such concept, so whatever os.Stat reports for the
	// build workspace is what ends up in the image's inode, verified
	// empirically as 0666 regardless of what this code requests).
	// Naming the interpreter explicitly means rcS only needs to be
	// readable, sidestepping the whole problem -- /bin/sh itself is a
	// tmpfs symlink to BusyBox, chmod'd for real at boot by stage 1,
	// running on an actual Linux system where chmod means something.
	b.WriteString("::sysinit:/bin/sh /etc/init.d/rcS\n")
	b.WriteString("::ctrlaltdel:/sbin/reboot\n")
	// T82: the graceful-shutdown script runs *before* umount -a -r (which
	// it invokes itself, at the end -- see buildShutdownScript) instead
	// of umount -a -r being the ::shutdown: action directly. BusyBox
	// init's own shutdown sequence only applies its blanket "SIGTERM
	// everyone, wait 1s, SIGKILL everyone" *after* this action returns,
	// so blocking here for up to STOPGRACE seconds turns that 1s window
	// into a backstop rather than the actual constraint.
	b.WriteString("::shutdown:/bin/sh /etc/init.d/shutdown.sh\n")
	for _, svc := range services {
		// "always" (the default): busybox-init's own ::respawn: does the
		// restarting, same as before RESTART existed. "on-failure"/"no":
		// the supervisor script itself now decides when to stop looping
		// and exit for real (see buildSupervisorScript) -- ::respawn:
		// would undo that by relaunching the script anyway, so those use
		// ::once: instead, which runs it exactly one time and leaves it
		// exited once it exits.
		action := "respawn"
		if svc.Restart == "on-failure" || svc.Restart == "no" {
			action = "once"
		}
		b.WriteString("::" + action + ":/bin/sh " + supervisorScriptPath(svc.Name) + "\n")
	}
	if agent != nil {
		// Its own respawn entry, not routed through buildSupervisorScript:
		// cnimbusagent is already an infinite loop with its own sleep, so
		// busybox-init's respawn here is purely a "restart if this
		// somehow dies" backstop, not the normal exit-and-retry path
		// every Service goes through. A real ELF binary, not a script --
		// no interpreter to hide an execute-bit problem behind, so it's
		// placed in stage 1's tmpfs (see BuildImages) where chmod is
		// real, and exec'd directly here (except "http", which needs a
		// shell wrapper for its optional headers -- see buildAgentScript).
		if agent.Kind == "http" {
			b.WriteString("::respawn:/bin/sh " + agentScriptPath + "\n")
		} else {
			// Not shell-quoted: busybox-init's inittab respawn line is
			// tokenized directly (no shell involved), so a quote
			// character here would just become a literal argument byte.
			// Values with spaces aren't supported as a result -- a
			// reasonable trade given guest property names, device
			// paths, metadata paths, and guestinfo keys are all
			// conventionally simple slash-separated tokens.
			fmt.Fprintf(&b, "::respawn:/usr/bin/cnimbusagent %s %s %s\n", agent.Kind, agent.URL, agent.Interval)
		}
	}
	return b.String()
}

// supervisorScriptPath and agentScriptPath deliberately point into
// usr/sbin/, not etc/init.d/ -- one of stage 1's four tmpfs-shadowed
// exec directories (see stage1.go), where chmod happens for real inside
// the booted guest kernel. T73: these scripts are generated with 0600
// (they carry every ENV value and, for the agent script, an AGENT
// bearer token as literal shell text -- see buildSupervisorScript/
// buildAgentScript), but go-diskfs's SquashFS writer takes each file's
// mode from the *build host's* filesystem (finalize.go's e.Mode()) --
// on Windows, os.Stat synthesizes 0666 for every regular file
// regardless of what was requested, verified empirically. A script
// placed in the read-only SquashFS root would therefore ship
// world-readable, silently, only on a Windows-built image. Routing them
// through stage 1's shadow-replay mechanism instead (see BuildImages)
// means the guest's own `chmod 600` (writeStagingCheck, stage1.go) is
// what actually sets the mode, on a real Linux filesystem where chmod
// means something -- the same fix rcS's own doc comment already
// describes for the execute-bit half of this problem, generalized to
// the confidentiality half.
func supervisorScriptPath(name string) string {
	return "/usr/sbin/cnimbus-svc-" + name + ".sh"
}

const agentScriptPath = "/usr/sbin/cnimbus-agent.sh"

// pidFilePath (T82) is where buildSupervisorScript records a healthcheck-
// tracked service's real child PID, for buildShutdownScript to signal
// directly at shutdown. /var/run is tmpfs (build.go's buildRCScript),
// writable at boot.
func pidFilePath(name string) string {
	return "/var/run/cnimbus-" + name + ".pid"
}

// buildShutdownScript is the ::shutdown: action (T82): it gives every
// healthcheck-tracked service (in practice, the entrypoint -- HEALTHCHECK
// only ever applies there) a real chance to shut down cleanly before the
// guest halts, instead of BusyBox init's own ~1-second SIGTERM-then-
// SIGKILL-to-everyone default.
//
// Only healthcheck-tracked services get a pidfile at all: that's the one
// path in buildSupervisorScript that already backgrounds the real
// workload command and captures its own PID directly (`cmd & ; pid=$!`).
// A plain service with no HEALTHCHECK runs through "cmd 2>&1 | logger
// -s -t name" instead (see runOnce's non-hc branch) -- backgrounding
// *that* would make "$!" name logger's PID, not the workload's, so
// there is no real PID to target precisely without the same mkfifo-based
// redirection T89 already declined as a larger, riskier change than its
// own pass's budget allowed. Those services still benefit from the
// overall wait below (nothing hurries their shutdown along), just
// without a signal aimed at them specifically.
func buildShutdownScript(services []Service, stopGrace int) string {
	if stopGrace <= 0 {
		stopGrace = defaultStopGrace
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "/bin/sh /etc/cnimbus-say \"cnimbus: shutdown: signaling tracked services, up to %ds grace\"\n", stopGrace)

	// SIGTERM every tracked service up front, in parallel (not one at a
	// time with its own wait) -- the grace budget below is the total
	// shutdown time this project asks the guest owner to wait, not a
	// per-service allowance to be summed across however many SERVICE
	// directives a Nimbusfile happens to declare.
	for _, svc := range services {
		pf := shQuote(pidFilePath(svc.Name))
		fmt.Fprintf(&b, "[ -f %s ] && kill -TERM \"$(cat %s)\" 2>/dev/null\n", pf, pf)
	}
	b.WriteString("n=0\n")
	fmt.Fprintf(&b, "while [ \"$n\" -lt %d ]; do\n", stopGrace)
	b.WriteString("  alive=0\n")
	for _, svc := range services {
		pf := shQuote(pidFilePath(svc.Name))
		fmt.Fprintf(&b, "  [ -f %s ] && kill -0 \"$(cat %s)\" 2>/dev/null && alive=1\n", pf, pf)
	}
	b.WriteString("  [ \"$alive\" -eq 0 ] && break\n")
	b.WriteString("  sleep 1\n")
	b.WriteString("  n=$((n+1))\n")
	b.WriteString("done\n")
	for _, svc := range services {
		pf := shQuote(pidFilePath(svc.Name))
		fmt.Fprintf(&b, "[ -f %s ] && kill -0 \"$(cat %s)\" 2>/dev/null && "+
			"{ /bin/sh /etc/cnimbus-say \"cnimbus: shutdown: %s did not exit within grace, sending SIGKILL\"; kill -9 \"$(cat %s)\" 2>/dev/null; }\n",
			pf, pf, svc.Name, pf)
	}
	b.WriteString("/bin/umount -a -r\n")
	return b.String()
}

// buildSupervisorScript wraps one Service's argv with the things
// busybox-init's bare ::respawn:/::once: can't do on their own:
//   - WORKDIR: cd'd into once, before the (possible) restart loop
//   - ENV: exported before the command runs, so it's just an
//     environment variable from the process's point of view
//   - USER: dropped via BusyBox's setuidgid (no PAM/shadow, no shell
//     needed beyond this script itself -- the minimal building block
//     for "USER", not a login mechanism), wrapped in BusyBox's setpriv
//     --nnp when that applet is present on PATH (falls back to bare
//     setuidgid otherwise -- an older pieces set predating this, or a
//     custom BusyBox build with it disabled). BusyBox's setpriv is a
//     reduced reimplementation of util-linux's, not the real thing --
//     verified empirically it supports only --dump/--nnp(/--no-new-privs)
//     /--inh-caps/--ambient-caps, none of util-linux's --reuid/--regid/
//     --clear-groups/--bounding-set -- so it cannot replace setuidgid's
//     job of the actual uid/gid switch, only add PR_SET_NO_NEW_PRIVS on
//     top of it (--nnp is inherited across setuidgid's own subsequent
//     execve, verified empirically via /proc/self/status). That one
//     flag still closes a real gap on its own: it blocks a setuid/setgid
//     file bit or file capability from ever regaining privilege for
//     this process or anything it execs, on top of (not instead of)
//     the tmpfs-mount hardening that already closes the "planted setuid
//     binary on PATH" path itself (see stage1.go).
//   - restart policy (see Service.Restart): "always" loops forever with
//     capped-linear backoff (cnimbus's original and only behavior before
//     RESTART existed; busybox-init's own ::respawn: restarts this
//     script if it's ever killed, though it never exits on its own).
//     "on-failure" loops the same way but stops -- exits the script for
//     real -- the moment the command exits 0. "no" runs the command
//     exactly once and exits with its code, no loop at all.
//   - hc, if non-nil (only ever passed for the entrypoint service): runs
//     the command in the background and polls hc.Argv every hc.Interval
//     seconds, killing and letting the restart logic above take over
//     once hc.Retries consecutive checks fail in a row -- the same
//     "unhealthy is like crashed" model Docker's own HEALTHCHECK uses.
func buildSupervisorScript(svc Service, env []EnvVar, user, workdir string, hc *Healthcheck) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	// Needed by the hc == nil branch of runOnce below (T89): without it,
	// "cmd 2>&1 | logger ..." reports logger's own exit status, not the
	// service command's. BusyBox ash supports pipefail (CONFIG_ASH's own
	// bash-compat option); harmless when unsupported, since this script
	// would just silently keep bash/dash's non-pipefail default.
	b.WriteString("set -o pipefail 2>/dev/null\n")
	for _, e := range env {
		fmt.Fprintf(&b, "export %s=%s\n", e.Key, shQuote(e.Value))
	}
	if workdir != "" {
		fmt.Fprintf(&b, "cd %s || exit 1\n", shQuote(workdir))
	}

	cmd := shJoin(svc.Argv)
	hcCmd := ""
	if hc != nil {
		hcCmd = shJoin(hc.Argv)
	}
	if user != "" {
		// NNP_PREFIX expands to nothing (zero shell words) when setpriv
		// isn't on PATH, so "$NNP_PREFIX setuidgid ..." degrades to a
		// bare "setuidgid ..." automatically -- no separate branch
		// needed for the two cases.
		b.WriteString("NNP_PREFIX=\"\"\n")
		b.WriteString("command -v setpriv >/dev/null 2>&1 && NNP_PREFIX=\"setpriv --nnp --\"\n")
		cmd = "$NNP_PREFIX setuidgid " + shQuote(user) + " " + cmd
		if hcCmd != "" {
			hcCmd = "$NNP_PREFIX setuidgid " + shQuote(user) + " " + hcCmd
		}
	}

	runOnce := func(b *strings.Builder) {
		if hc == nil {
			// T89: the VOLUME-backed log-persistence mechanism
			// (buildRCScript's syslogd -O "$LOGFILE") only ever captures
			// what actually goes through syslog -- supervisor scripts
			// otherwise run with inherited file descriptors straight to
			// /dev/console, so a workload's own stdout/stderr (the
			// twelve-factor/container convention almost everything
			// follows) never reached the persisted log at all, despite
			// this project's own doc comment promising "logs survive a
			// reboot". Piped through BusyBox logger -s (which both
			// forwards to syslog *and* echoes to stderr, so serial-console
			// debugging still works unchanged). `set -o pipefail` below is
			// what makes `code=$?` reflect cmd's exit status rather than
			// logger's -- safe here because this path is a synchronous
			// pipeline, not backgrounded, unlike the hc != nil branch
			// below (left untouched: piping a backgrounded command would
			// make "$!" name logger's PID instead of the app's, breaking
			// the healthcheck kill/wait logic T24 already hardened).
			fmt.Fprintf(b, "  %s 2>&1 | logger -s -t %s\n", cmd, shQuote(svc.Name))
			b.WriteString("  code=$?\n")
			return
		}
		fmt.Fprintf(b, "  %s &\n", cmd)
		b.WriteString("  pid=$!\n")
		// T82: this is the one path that already backgrounds the real
		// workload command and captures its actual PID (not a pipe's --
		// see runOnce's own non-hc branch and its T89 comment on why that
		// one can't cheaply do the same) -- recorded so the generated
		// shutdown script (buildShutdownScript) can signal this exact
		// process directly, without going through this supervisor script
		// at all.
		fmt.Fprintf(b, "  echo \"$pid\" > %s\n", shQuote(pidFilePath(svc.Name)))
		b.WriteString("  failcount=0\n")
		b.WriteString("  killed=0\n")
		b.WriteString("  while kill -0 \"$pid\" 2>/dev/null; do\n")
		fmt.Fprintf(b, "    sleep %s\n", shQuote(hc.Interval))
		b.WriteString("    if kill -0 \"$pid\" 2>/dev/null; then\n") // still alive after the sleep -- worth checking
		fmt.Fprintf(b, "      if %s >/dev/null 2>&1; then\n", hcCmd)
		b.WriteString("        failcount=0\n")
		b.WriteString("      else\n")
		b.WriteString("        failcount=$((failcount+1))\n")
		fmt.Fprintf(b, "        /bin/sh /etc/cnimbus-say \"cnimbus: %s healthcheck failed ($failcount/%s)\"\n", svc.Name, hc.Retries)
		fmt.Fprintf(b, "        if [ \"$failcount\" -ge %s ]; then\n", shQuote(hc.Retries))
		// T83: SIGTERM alone never escalates. A workload that's unhealthy
		// *because* it's wedged (deadlocked, blocked in an uninterruptible
		// syscall, or a hung SIGTERM handler) ignores "kill $pid" forever --
		// "kill -0 $pid" keeps succeeding, this branch keeps re-firing every
		// hc.Interval (previously re-printing "killing" and re-sending
		// SIGTERM every tick with no escalation), and the outer ::respawn:
		// never sees the process exit. killed=1 makes the next tick past
		// the retry limit send SIGKILL instead of repeating SIGTERM.
		b.WriteString("          if [ \"$killed\" -eq 0 ]; then\n")
		fmt.Fprintf(b, "            /bin/sh /etc/cnimbus-say \"cnimbus: %s healthcheck exceeded retry limit, sending SIGTERM\"\n", svc.Name)
		b.WriteString("            kill \"$pid\" 2>/dev/null\n")
		b.WriteString("            killed=1\n")
		b.WriteString("          else\n")
		fmt.Fprintf(b, "            /bin/sh /etc/cnimbus-say \"cnimbus: %s did not exit after SIGTERM, sending SIGKILL\"\n", svc.Name)
		b.WriteString("            kill -9 \"$pid\" 2>/dev/null\n")
		b.WriteString("          fi\n")
		b.WriteString("        fi\n")
		b.WriteString("      fi\n")
		b.WriteString("    fi\n")
		b.WriteString("  done\n")
		b.WriteString("  wait \"$pid\"\n")
		b.WriteString("  code=$?\n")
	}

	switch svc.Restart {
	case "no":
		runOnce(&b)
		fmt.Fprintf(&b, "/bin/sh /etc/cnimbus-say \"cnimbus: %s exited (code $code)\"\n", svc.Name)
		b.WriteString("exit \"$code\"\n")
	case "on-failure":
		b.WriteString("n=0\n")
		b.WriteString("while true; do\n")
		b.WriteString("  start=$(cut -d. -f1 /proc/uptime)\n")
		runOnce(&b)
		fmt.Fprintf(&b, "  /bin/sh /etc/cnimbus-say \"cnimbus: %s exited (code $code)\"\n", svc.Name)
		b.WriteString("  [ \"$code\" -eq 0 ] && exit 0\n")
		writeBackoffResetAndSleep(&b, svc.Name)
		b.WriteString("done\n")
	default: // "always", or unset (Service zero value) -- cnimbus's original behavior
		b.WriteString("n=0\n")
		b.WriteString("while true; do\n")
		b.WriteString("  start=$(cut -d. -f1 /proc/uptime)\n")
		runOnce(&b)
		fmt.Fprintf(&b, "  /bin/sh /etc/cnimbus-say \"cnimbus: %s exited (code $code)\"\n", svc.Name)
		writeBackoffResetAndSleep(&b, svc.Name)
		b.WriteString("done\n")
	}
	return b.String()
}

// writeBackoffResetAndSleep is T84's fix: the capped-linear restart
// backoff (n=1,2,...,30, then held at 30s) previously only ever grew --
// a service that crashed 40 times in its first minute (backoff
// saturated at 30s) stayed at the worst-case 30s delay forever after,
// even following a week of healthy operation, because n never resets.
// The standard fix is "reset on sustained success": if the just-finished
// run survived longer than the backoff delay its *own* restart would
// have used (computed from n before this run's increment), the crash
// loop that built up that backoff is considered over and n resets to 0
// before counting this run. start is captured by the caller (before
// runOnce) using /proc/uptime, the same clock source already available
// in every stage of this init system with no extra binary needed.
func writeBackoffResetAndSleep(b *strings.Builder, name string) {
	b.WriteString("  end=$(cut -d. -f1 /proc/uptime)\n")
	b.WriteString("  elapsed=$((end-start))\n")
	b.WriteString("  d=$n; [ \"$d\" -gt 30 ] && d=30\n")
	b.WriteString("  [ \"$elapsed\" -gt \"$d\" ] && n=0\n")
	b.WriteString("  n=$((n+1))\n")
	fmt.Fprintf(b, "  /bin/sh /etc/cnimbus-say \"cnimbus: %s restart #$n\"\n", name)
	b.WriteString("  d=$n; [ \"$d\" -gt 30 ] && d=30\n")
	b.WriteString("  sleep \"$d\"\n")
}

// ntpCanResolve reports whether ntpd stands a real chance of resolving
// its own NTP server names. DHCP already writes /etc/resolv.conf from
// the lease (see udhcpcScript) well before this runs, and an explicit
// DNS directive does the same regardless of networking mode -- either
// makes resolution plausible. Otherwise (a StaticIP Nimbusfile with no
// DNS directive, e.g. examples/static-ip-firewall) resolv.conf is empty
// by construction, so this only still returns true if every NTP entry
// is already a literal IP address needing no resolution at all.
// Without this check, ntpd was unconditionally invoked against
// hostnames (e.g. "pool.ntp.org") with no resolver configured on
// exactly that Nimbusfile shape -- guaranteed to fail every single
// boot, silently (its own stderr is redirected away), for the entire
// length of its timeout.
func ntpCanResolve(spec PiecesSpec) bool {
	if len(spec.DNS) > 0 {
		return true
	}
	// spec.DHCP alone isn't enough: StaticIP wins over it whenever both
	// are set (see the switch below), the same precedence
	// examples/static-ip-firewall demonstrates -- DHCP never actually
	// runs in that case, so it never gets the chance to write
	// resolv.conf either.
	if spec.StaticIP == nil && spec.DHCP {
		return true
	}
	for _, server := range spec.NTP {
		if net.ParseIP(server) == nil {
			return false
		}
	}
	return true
}

// buildRCScript is the sysinit script: hostname, networking (static IP
// wins over DHCP if both are configured), NTP, the optional persistent
// VOLUME, log persistence, ACPI shutdown handling, and (if
// Nimbusfile-configured) firewall rules.
func buildRCScript(spec PiecesSpec) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	// BusyBox's switch_root, verified empirically, does not carry
	// stage 1's devtmpfs/proc/sysfs mounts across into the new root on
	// its own (a bare directory is what's actually there post
	// switch_root, not the moved mount) -- so /dev/null didn't exist,
	// breaking the very first "> /dev/null" redirect anything here
	// tried. Mounting all three again here is cheap and unconditionally
	// correct regardless of what switch_root does or doesn't move.
	// No "2>/dev/null" on this first one: /dev/null doesn't exist until
	// devtmpfs is actually mounted, so redirecting to it here would hit
	// the exact same chicken-and-egg failure this block exists to fix.
	b.WriteString("mount -t devtmpfs devtmpfs /dev\n")
	// T85: mount, then harden -- split from the previous single
	// "mount -t proc -o hidepid=2,... 2>/dev/null" (and the equivalent for
	// /sys), which silenced the *entire* mount behind one 2>/dev/null. A
	// kernel/config combination that rejects any one hardening option
	// (hidepid=2, an older pinned KERNEL, a future kernel changing hidepid
	// handling) failed the whole mount, leaving the image with no /proc at
	// all -- silently, since the very next line
	// (echo -1000 > /proc/1/oom_score_adj) is itself 2>/dev/null'd and so
	// swallowed the resulting failure too, undoing T10 with no visible
	// signal until a workload later hit ENOENT reading /proc/self/*. A
	// bare "mount -t proc proc /proc" cannot fail on a kernel with
	// CONFIG_PROC_FS=y (unconditionally selected -- see minimal.fragment),
	// so hardening is now a separate best-effort remount that only ever
	// costs the hidepid=2 privacy property, never /proc itself.
	b.WriteString("mount -t proc proc /proc\n")
	b.WriteString("mount -o remount,hidepid=2,nosuid,nodev,noexec /proc || /bin/sh /etc/cnimbus-say \"cnimbus: warning: could not apply hidepid=2 to /proc (kernel/config too old?), continuing without it\"\n")
	b.WriteString("mount -t sysfs sysfs /sys\n")
	writeUptimeCheckpoint(&b, "rcS starting (post switch_root)")
	b.WriteString("mount -o remount,nosuid,nodev,noexec /sys || /bin/sh /etc/cnimbus-say \"cnimbus: warning: could not apply hardening options to /sys, continuing without them\"\n")
	// PID 1 is BusyBox init with no respawn logic of its own -- if the
	// OOM killer ever picks it (the kernel's default heuristics have no
	// reason to spare it over any other process), the whole VM dies
	// instead of just the offending workload. -1000 is the same
	// "never kill this" value systemd/dockerd use for their own PID 1.
	b.WriteString("echo -1000 > /proc/1/oom_score_adj 2>/dev/null\n")
	// The root filesystem is a read-only SquashFS (see stage1.go); these
	// two paths are the only ones anything here expects to write to
	// beyond bin/sbin/usr/{bin,sbin} (already tmpfs -- see stage 1's
	// init script) and an optional VOLUME.
	b.WriteString("mount -t tmpfs -o nosuid,nodev,noexec,mode=1777,size=64m tmpfs /tmp\n")
	b.WriteString("mount -t tmpfs -o nosuid,nodev,mode=0755,size=8m tmpfs /var/run\n")
	// Classic tools (iptables-legacy's xtables.lock, some pidfile-writing
	// daemons) hardcode /run, not /var/run -- most distros paper over
	// this with a /run -> /var/run (or reverse) symlink baked into the
	// root filesystem, but go-diskfs's SquashFS writer can't create
	// symlinks (see ROADMAP.md). A bind mount at boot gets the same
	// "one writable dir, two paths" result without needing one.
	b.WriteString("mount --bind /var/run /run 2>/dev/null || mount -t tmpfs tmpfs /run 2>/dev/null\n")
	// /etc/resolv.conf lives on the read-only SquashFS root like
	// everything else under /etc, but DNS resolution (this image's own
	// NTP hostnames, and any ENTRYPOINT/SERVICE that resolves a name)
	// needs somewhere writable to put nameserver lines. A build-time
	// placeholder file at that exact path (see squashfsroot.go) gives a
	// bind-mount target that already exists, without needing a symlink
	// (go-diskfs's SquashFS writer can't create those -- see
	// ROADMAP.md) or shadowing all of /etc the way bin/sbin/usr/{bin,sbin}
	// are shadowed in stage 1: after this, /etc/resolv.conf is a real
	// tmpfs-backed file, writable by udhcpc.script and by the DNS
	// directive's own override below.
	b.WriteString(": > /var/run/resolv.conf\n")
	b.WriteString("mount --bind /var/run/resolv.conf /etc/resolv.conf 2>/dev/null || " +
		"/bin/sh /etc/cnimbus-say \"cnimbus: could not make /etc/resolv.conf writable -- DNS resolution may not work\"\n")
	fmt.Fprintf(&b, "hostname %s\n", shQuote(spec.Hostname))
	b.WriteString("ifconfig lo up\n")

	if spec.StaticIP != nil || spec.DHCP {
		// Self-diagnosing: a NIC model the kernel has no driver for
		// (verified empirically to happen with VirtualBox's own ostype
		// table silently defaulting to something other than virtio-net --
		// see runViaVBox's own --nictype1 fix) leaves eth0 simply absent,
		// and every step below (ifconfig, udhcpc, ...) fails with a
		// generic "no such device" that gives no hint why. This one line
		// names the actual cause instead.
		b.WriteString("[ -e /sys/class/net/eth0 ] || /bin/sh /etc/cnimbus-say \"cnimbus: no eth0 -- unsupported NIC model for this VM/hypervisor\"\n")
		// AD-053: a real bare-metal boot reported eth0 reaching "Link is
		// Up" (the driver's own log line) with no IP ever obtained --
		// with no way to tell, from that alone, whether the interface
		// even had the driver/carrier state a working boot would show.
		// Printed unconditionally (both StaticIP and DHCP take this same
		// path below) so a diff against a known-good boot's log is
		// possible either way. /sys/class/net/eth0/device/driver is a
		// symlink into /sys/bus/pci/drivers/<name> (or /sys/bus/usb/...)
		// -- basename of its target is the actual driver bound to the
		// device, independent of what Kconfig symbol enabled it.
		b.WriteString("if [ -e /sys/class/net/eth0 ]; then\n")
		b.WriteString("  ETH0DRV=$(readlink -f /sys/class/net/eth0/device/driver 2>/dev/null); ETH0DRV=${ETH0DRV##*/}\n")
		fmt.Fprintf(&b, "  %s \"cnimbus: eth0 driver=${ETH0DRV:-unknown} mac=$(cat /sys/class/net/eth0/address 2>/dev/null)\"\n", consoleSayCmd)
		b.WriteString("fi\n")
	}

	networked := false
	switch {
	case spec.StaticIP != nil:
		fmt.Fprintf(&b, "ifconfig eth0 %s netmask %s up\n", shQuote(spec.StaticIP.Address), shQuote(spec.StaticIP.Netmask))
		fmt.Fprintf(&b, "route add default gw %s dev eth0\n", shQuote(spec.StaticIP.Gateway))
		networked = true
	case spec.DHCP:
		b.WriteString("ifconfig eth0 up\n")
		// AD-053: carrier/operstate/speed straight from sysfs, no ethtool
		// needed (this image has none) -- carrier=1 with a real speed
		// means the PHY actually linked to the switch/router, not just
		// that the driver printed a "Link is Up" message; carrier=0 here
		// means udhcpc below is guaranteed to get no reply, which is a
		// different failure than "link up, but the DHCP server never
		// answered" and this project had no way to tell apart before.
		//
		// Polled up to 3 times, 1s apart, rather than read once: a real
		// QEMU boot showed carrier=0 read immediately after "ifconfig
		// eth0 up" even on a link that DHCP then obtained a lease on a
		// moment later -- autonegotiation is not instantaneous, so a
		// single read right after bringing the interface up is a
		// meaningless race, not a diagnosis, and would have misreported
		// a perfectly good link as "no carrier" on this project's own
		// fast VM path, let alone real hardware's slower negotiation.
		// Bounded the same way the boot-media scan is (AD-048): this is
		// a real-hardware diagnostic, not a redesign of DHCP's own
		// timeout budget.
		b.WriteString("for i in 1 2 3; do [ \"$(cat /sys/class/net/eth0/carrier 2>/dev/null)\" = 1 ] && break; sleep 1; done\n")
		fmt.Fprintf(&b, "%s \"cnimbus: eth0 carrier=$(cat /sys/class/net/eth0/carrier 2>/dev/null) operstate=$(cat /sys/class/net/eth0/operstate 2>/dev/null) speed=$(cat /sys/class/net/eth0/speed 2>/dev/null)Mbps\"\n", consoleSayCmd)
		// /sbin/udhcpc.script, not an /etc path: udhcpc execve()s this
		// script directly (no shell in between), so it needs a real
		// execute bit -- same reasoning as rcS above, except here there's
		// no interpreter argument to hide behind, so the file itself has
		// to live in tmpfs, chmod'd for real at boot (see stage1.go).
		// Also writes /etc/resolv.conf from whatever DNS servers the
		// DHCP lease itself carries -- see udhcpcScript.
		// No "-q": that flag exits udhcpc right after the first lease,
		// so the lease was never renewed and silently expired on any
		// DHCP server with a short lease time. Without it, udhcpc's
		// default behavior (absent "-f") is to background itself once
		// the first lease lands and keep running to renew -- "-n" is
		// kept so a boot with no DHCP server at all still fails fast
		// instead of retrying forever.
		// -t 3 -T 1 -A 2 (T92): this runs in the *foreground* during
		// sysinit, gating every service start behind it. BusyBox's own
		// defaults (undocumented here otherwise) are 3 discover attempts,
		// 3s apart, with a 20s wait if no lease is obtained -- tens of
		// seconds with no visible upper bound on a slow/rate-limited/
		// absent DHCP server, on a project whose whole thesis is
		// micro-VM boot latency. -t 3 -T 1 bounds the actual worst case
		// to a few seconds instead. -A 2 is included for documentation
		// completeness but has no effect here: BusyBox's own usage
		// synopsis lists "-A SEC/-n" as mutually exclusive (verified
		// empirically -- `busybox udhcpc --help`), and -n (already
		// present, kept from before this fix) is what actually governs
		// "no lease obtained" behavior -- exit immediately rather than
		// wait -A seconds and retry.
		b.WriteString("udhcpc -i eth0 -n -t 3 -T 1 -A 2 -s /sbin/udhcpc.script\n")
		// AD-053: udhcpc's own exit status (0 = lease obtained, nonzero =
		// no server answered within -t/-T/-A's bound) plus whatever
		// address actually landed on the interface afterward --
		// "eth0 carrier=1 ... but no lease" (server unreachable/absent)
		// and "no carrier at all" (cabling/PHY/driver problem) were
		// previously indistinguishable from the console: both just
		// looked like "no IP printed".
		b.WriteString("DHCPRC=$?\n")
		fmt.Fprintf(&b, "%s \"cnimbus: udhcpc exit=$DHCPRC addr=$(ip -o -f inet addr show eth0 2>/dev/null | awk '{print $4}')\"\n", consoleSayCmd)
		networked = true
	}
	if networked {
		// AD-054: real hardware and a real Proxmox VM both reported IPv4
		// working end to end but no IPv6 -- and the VGA banner
		// deliberately only ever shows "scope global" addresses (see
		// its own comment below), so it cannot distinguish "kernel got
		// nothing at all" from "got only the mandatory fe80:: link-local
		// via SLAAC, no Router Advertisement ever arrived" from "got a
		// real global address, something else (firewall, routing) is
		// what's actually broken". All three read identically as "no
		// IPv6 shown" from the console alone.
		//
		// This is independent of DHCP/StaticIP above on purpose: IPv6
		// address assignment here is the kernel's own SLAAC (CONFIG_IPV6,
		// no ipv6.disable on the cmdline -- see vm-amd64.fragment/
		// vm-arm64.fragment), which runs automatically once the
		// interface is up and needs no userspace client at all, unlike
		// IPv4's udhcpc. Polled the same bounded way as the carrier
		// check above (a global address arriving via Router
		// Advertisement is not necessarily instantaneous either), rather
		// than assumed present by the time this prints.
		// AD-056: bounded at 5 attempts, not 3 -- a real Proxmox VM
		// showed the global SLAAC address landing after this originally
		// polled for up to 3s, so a real boot's console (and the VGA
		// banner below, which had the same bound) printed only the
		// mandatory fe80:: link-local and never the address curl could
		// actually reach a moment later. Not bounded at 10 like AD-048's
		// boot-media retry: unlike a missing/slow boot device, "no
		// global IPv6 at all" is the common case on networks with no
		// IPv6 configured (still most of them), and this poll cannot
		// tell that case apart from "still waiting" -- confirmed by a
		// real QEMU boot under this same fix: an IPv6-less network made
		// this loop run its full bound on every single boot, and 10
		// attempts alone (before udhcpc's own DHCP-failure worst case)
		// pushed a normally ~8s boot to 29s, directly against this
		// project's own boot-latency thesis. 5s keeps meaningfully more
		// slack than the original 3s for a real router's RA cadence
		// without doubling the no-IPv6 cost a second time.
		b.WriteString("for i in 1 2 3 4 5; do ip -6 addr show dev eth0 2>/dev/null | grep -q 'scope global' && break; sleep 1; done\n")
		b.WriteString("IPV6ADDRS=$(ip -o -6 addr show dev eth0 2>/dev/null | awk '{print $4}' | tr '\\n' ' ')\n")
		fmt.Fprintf(&b, "%s \"cnimbus: eth0 ipv6 addrs: ${IPV6ADDRS:-(none)}\"\n", consoleSayCmd)
		b.WriteString("IPV6GW=$(ip -6 route show default 2>/dev/null | head -n1)\n")
		fmt.Fprintf(&b, "%s \"cnimbus: eth0 ipv6 default route: ${IPV6GW:-(none)}\"\n", consoleSayCmd)
	}
	writeUptimeCheckpoint(&b, "eth0 network setup done")

	if hasWifiDriver(spec.BootProfile) {
		// F6.5: wlan0 bring-up, association, then the same DHCP/static
		// logic eth0 just ran, mirrored for wlan0. Runs after eth0's own
		// block above (not before): HB-F-013's "Ethernet and WiFi
		// coexist" is satisfied by both interfaces getting the same
		// DHCP/static config independently, in this fixed order. Emitted
		// for "wifi" and "eth+wifi" alike.
		buildWifiBringupScript(&b, spec)
		networked = true
		writeUptimeCheckpoint(&b, "wifi bringup done")
	}

	// Additional NICs (eth1..eth3 -- a hypervisor can attach more than
	// one, e.g. a "management" + "data" network split): DHCP each one
	// found, backgrounded with shell "&" (not udhcpc's own -b, which
	// only backgrounds *after* first trying in the foreground for its
	// -A timeout -- still blocking boot exactly as long as eth0 would)
	// so a slow/absent lease on a secondary NIC never blocks boot at
	// all. "-n" gives up (rather than retrying forever) if no lease
	// ever arrives. Address+route only, via udhcpc-secondary.script --
	// intentionally not touching /etc/resolv.conf or the default route
	// eth0 already owns, since StaticIP/DNS/the default gateway are all
	// singular, eth0-scoped concepts today (see ROADMAP.md for genuine
	// multi-NIC static config).
	// Gated on networked (T86): this loop used to sit outside the
	// StaticIP/DHCP switch above, so a Nimbusfile that deliberately
	// declares "DHCP false" and no IP -- the documented way to build an
	// image with no networking at all -- still ran a DHCP client on any
	// additional NIC the hypervisor happened to attach, with no FIREWALL
	// applied (a no-networking Nimbusfile has no reason to declare one).
	if networked {
		b.WriteString("for i in 1 2 3; do\n")
		fmt.Fprintf(&b, "  [ -e /sys/class/net/eth$i ] && { ifconfig eth$i up; udhcpc -i eth$i -n -s /sbin/udhcpc-secondary.script & }\n")
		b.WriteString("done\n")
	}

	if spec.VGA {
		// Nothing here logs in interactively -- there is no shell in this
		// image at all (see buildInittab) -- so a VGA-console user has no
		// other way to learn the guest's assigned address than reading it
		// off the screen at boot. BusyBox's real `ip` applet (verified
		// against the actual busybox-1.36.1 build: `ip --help` lists
		// `-f[amily] inet|inet6|link`, not iproute2's `-4`/`-6`) prints one
		// line per address in `-o` mode, e.g. "2: eth0    inet
		// 10.0.2.15/24 brd ... scope global eth0\ ...". "scope global"
		// excludes loopback (scope host) and, for inet6, link-local
		// fe80::/64 addresses (scope link) -- exactly the ones nobody
		// needs to read off a screen. Both address families are attempted
		// unconditionally: if the guest has no IPv6 address at all (the
		// default -- see vm-amd64.fragment's ipv6.disable=1 cmdline),
		// `ip -f inet6` simply finds nothing and prints nothing, which is
		// the desired "only if there is one" behavior with no extra
		// plumbing. Runs after every foreground DHCP/static-IP step above,
		// but before the backgrounded additional-NIC loop's own leases can
		// possibly land -- eth1..eth3 addresses (if any) won't be in this
		// banner, same "best effort, not authoritative" caveat T86 already
		// accepts for those NICs elsewhere in this script.
		// AD-056: waits up to 5s for a global IPv6 address before
		// printing, same bound and same reason as the ipv6 diagnostic
		// above -- a real Proxmox VM's global SLAAC address consistently
		// arrived after this loop's original single, immediate check,
		// so the banner printed only IPv4 even though the machine was
		// fully reachable over IPv6 moments later (confirmed via a real
		// `curl -6` from another host on the same LAN). Costs nothing
		// when the address is already there (breaks out immediately);
		// on a network with no IPv6 at all -- confirmed via a real QEMU
		// boot -- it costs the full bound every time, which is why this
		// is 5s and not AD-048's 10s (see the ipv6 diagnostic's own
		// comment above). IPv4 was never affected -- DHCP already
		// completed by this point in the script, so the inet loop
		// iteration never waits.
		b.WriteString("for fam in inet inet6; do\n")
		b.WriteString("  if [ \"$fam\" = inet6 ]; then\n")
		b.WriteString("    for i in 1 2 3 4 5; do ip -o -f inet6 addr show scope global 2>/dev/null | grep -q . && break; sleep 1; done\n")
		b.WriteString("  fi\n")
		b.WriteString("  ip -o -f \"$fam\" addr show scope global 2>/dev/null | while read -r _ iface _ addrpfx _; do\n")
		b.WriteString("    addr=\"${addrpfx%%/*}\"\n")
		b.WriteString("    [ \"$fam\" = inet6 ] && label=IPv6 || label=IPv4\n")
		b.WriteString("    /bin/sh /etc/cnimbus-say \"cnimbus: $label address: $addr ($iface)\"\n")
		b.WriteString("  done\n")
		b.WriteString("done\n")
	}

	if len(spec.DNS) > 0 {
		// Explicit DNS directive wins over whatever DHCP itself
		// provided, the same precedence StaticIP already has over
		// DHCP -- written after the block above so it's the last thing
		// to touch /etc/resolv.conf regardless of which networking mode
		// ran. The only source of DNS at all when using a StaticIP,
		// since there's no DHCP lease to carry it in that case.
		b.WriteString(": > /etc/resolv.conf\n")
		for _, s := range spec.DNS {
			fmt.Fprintf(&b, "echo %s >> /etc/resolv.conf\n", shQuote("nameserver "+s))
		}
	}

	if networked && len(spec.NTP) > 0 && ntpCanResolve(spec) {
		// One ntpd invocation, one -p per server: it queries all of them
		// itself and picks the best answer, rather than us calling it
		// once per server and hoping the last call wins.
		var args strings.Builder
		for _, server := range spec.NTP {
			args.WriteString(" -p ")
			args.WriteString(shQuote(server))
		}
		// A short (3s) foreground attempt first, so anything that logs
		// timestamps early in boot has a shot at a synced clock; the
		// full-length (15s) attempt then runs backgrounded, so a boot
		// against a slow/unreachable NTP server no longer holds up every
		// later sysinit step behind it the way a single foreground
		// "timeout 15" call previously did on every boot.
		fmt.Fprintf(&b, "timeout 3 ntpd -q -n%s >/dev/null 2>&1\n", args.String())
		fmt.Fprintf(&b, "(timeout 15 ntpd -q -n%s >/dev/null 2>&1 &)\n", args.String())
	}

	for _, v := range spec.Volumes {
		dev, mnt := shQuote(v.Device), shQuote(v.Mountpoint)
		fstype := v.FSType
		if fstype == "" {
			fstype = "vfat"
		}
		// Mount only -- never format. An earlier version of this ran
		// mkfs.vfat automatically whenever the mount failed, which
		// silently destroys anything already on the device (a different
		// filesystem, a disk you pre-populated with files to consume, or
		// just a mount failure unrelated to the device being blank). The
		// device is expected to already be a real, pre-formatted disk
		// (matching fstype) you attached yourself in your hypervisor --
		// if it isn't, this just logs and boots on without it, nothing
		// is written to the device at all.
		// nosuid,nodev by default: a VOLUME is user-attached, potentially
		// pre-populated storage from outside this image's own build --
		// nothing on it should be trusted to plant a setuid binary or a
		// device node reachable from inside the guest. No "noexec": a
		// VOLUME is the one documented way to bring in more than
		// COPY/ADD can carry (see README), and some workloads legitimately
		// run binaries staged there.
		fmt.Fprintf(&b, "mkdir -p %s\n", mnt)
		fmt.Fprintf(&b, "mount -t %s -o nosuid,nodev %s %s 2>/dev/null\n", fstype, dev, mnt)
		if v.Required {
			// T93: a workload expecting real, persistent storage at
			// v.Mountpoint that silently gets the read-only SquashFS root
			// (or, worse, tmpfs under /tmp or /var/run -- lost on reboot)
			// instead is a worse outcome than refusing to boot.
			//
			// Real-boot finding (2026-08-06): a plain "exit 1" here does
			// NOT halt boot. rcS runs as a "::sysinit:" inittab action;
			// BusyBox init runs a sysinit action to completion and then
			// unconditionally proceeds to "::respawn:"/"::once:" entries
			// *regardless of that action's exit status* -- there is no
			// "abort boot if sysinit failed" concept in BusyBox init. A
			// real boot with a deliberately-missing required volume
			// confirmed this the hard way: the FATAL line printed, then
			// every declared service (ENTRYPOINT included) started anyway,
			// exactly the silent-continuation outcome this ticket exists
			// to prevent -- the fix never actually took effect. Blocking
			// sysinit forever instead of exiting it does work: BusyBox
			// init still waits for the "::sysinit:" action to *return*
			// before moving on, so an rcS that never returns means no
			// "::respawn:"/"::once:" entry -- ENTRYPOINT or SERVICE -- ever
			// starts. That's what actually halts boot.
			fmt.Fprintf(&b, "if ! mountpoint -q %s; then\n", mnt)
			fmt.Fprintf(&b, "  /bin/sh /etc/cnimbus-say \"cnimbus: FATAL: required volume %s at %s (%s) did not mount -- is it attached, and pre-formatted as %s?\"\n",
				v.Device, v.Mountpoint, fstype, fstype)
			b.WriteString("  /bin/sh /etc/cnimbus-say \"cnimbus: halting boot -- no declared service will start\"\n")
			b.WriteString("  while true; do sleep 3600; done\n")
			b.WriteString("fi\n")
			fmt.Fprintf(&b, "/bin/sh /etc/cnimbus-say \"cnimbus: volume %s mounted at %s (%s)\"\n", v.Device, v.Mountpoint, fstype)
		} else {
			fmt.Fprintf(&b, "mountpoint -q %s && /bin/sh /etc/cnimbus-say \"cnimbus: volume %s mounted at %s (%s)\" || /bin/sh /etc/cnimbus-say \"cnimbus: could not mount %s at %s -- is it attached, and pre-formatted as %s?\"\n",
				mnt, v.Device, v.Mountpoint, fstype, v.Device, v.Mountpoint, fstype)
		}
	}

	// Log persistence: if at least one VOLUME is mounted, write there
	// (the first one declared) so logs survive a reboot; otherwise fall
	// back to the console, which at least lets a hypervisor-side serial
	// capture persist them externally (see README's serial/VGA
	// debugging notes).
	b.WriteString("LOGFILE=/dev/console\n")
	if len(spec.Volumes) > 0 {
		v := spec.Volumes[0]
		fmt.Fprintf(&b, "mountpoint -q %s && LOGFILE=%s/cnimbus.log\n", shQuote(v.Mountpoint), shQuote(strings.TrimRight(v.Mountpoint, "/")))
	}
	// -s/-b: BusyBox syslogd already rotates by default (200KB, 1 kept
	// copy), but that default is really only sized for a tiny RAM-backed
	// console log -- a VOLUME-backed one (the case this actually matters
	// for: it's the one that survives a reboot and can genuinely
	// accumulate) gets a more generous cap instead of silently growing
	// unbounded up to that small default before BusyBox's own rotation
	// kicks in. "0=off" isn't used anywhere here -- rotation is always on.
	b.WriteString("syslogd -O \"$LOGFILE\" -s 1024 -b 5\n")
	b.WriteString("klogd\n")

	// ACPI power button -> a clean shutdown: acpid's handler just runs
	// `poweroff`, the BusyBox applet, which signals busybox-init (PID 1)
	// to run the ::shutdown: action above (umount -a -r) before actually
	// powering off -- the same mechanism as running `poweroff` by hand.
	// AD-059: bind-mounts the real, already-executable /sbin/powerbtn.sh
	// (staged with a genuine chmod by stage 1 -- see stage1.go -- so this
	// never inherits the SquashFS-on-Windows exec-bit loss T73 already
	// documents) over the empty placeholder file baked into the
	// SquashFS root at exactly the path acpid's own compiled-in fallback
	// action table expects (see acpiPowerScript's doc comment). Placing
	// the real script content directly at that squashfs path instead
	// would risk the exact same T73 exec-bit loss AD-052 already hit
	// once with /etc/cnimbus-say.
	fmt.Fprintf(&b, "mount --bind /sbin/powerbtn.sh /%s 2>/dev/null || %s \"cnimbus: could not wire up ACPI power button handler\"\n", acpiPowerHandlerPath, consoleSayCmd)
	// "-d" (AD-059): without it, acpid tries to open its default log
	// file, /var/log/acpid.log -- but this image has no /var/log
	// directory at all (only "var" itself is baked into the SquashFS
	// root; nothing here has ever needed /var/log before), and
	// BusyBox's xopen() dies outright on that ENOENT rather than
	// falling back or continuing. acpid was therefore dying before it
	// ever reached /dev/input/event0, let alone the placeholder handler
	// -- confirmed by a real QEMU repro of the exact symptom Proxmox's
	// "Signal Shutdown" hit (system_powerdown, then watching whether
	// the guest process exits): "-d" alone (BusyBox's own usage text:
	// "Log to stderr, not log file (implies -f)") fixed it in an
	// environment that otherwise had everything else already correct
	// (the placeholder bind-mounted with the right content and
	// permissions, verified directly), while a bare "acpid &" and even
	// "acpid -f &" alone (skips only the *daemonize* step, not the
	// logfile open) both still failed to shut down. Routes acpid's own
	// diagnostics through this script's own stderr instead of a
	// dedicated log file no other part of this project's log-persistence
	// story (see buildRCScript's own LOGFILE handling) watches anyway.
	b.WriteString("acpid -d &\n")

	if len(spec.Firewall) > 0 {
		// A real `iptables` on PATH (a user's own COPY'd binary --
		// unusual now, but still honored, and wins if present) takes
		// priority over the bundled one; the bundled binary (see
		// internal/compileagent/iptables.go) dispatches on its first
		// *argument*, not argv[0] the way BusyBox applets do, so it
		// needs no symlink and can live in the genuinely-immutable
		// SquashFS root rather than stage 1's tmpfs shadow.
		b.WriteString("if command -v iptables >/dev/null 2>&1; then\n")
		b.WriteString("  IPTABLES_CMD=iptables\n")
		b.WriteString("elif [ -x /usr/sbin/cnimbus-iptables ]; then\n")
		b.WriteString("  IPTABLES_CMD=\"/usr/sbin/cnimbus-iptables iptables\"\n")
		b.WriteString("fi\n")
		b.WriteString("if [ -n \"$IPTABLES_CMD\" ]; then\n")
		b.WriteString("  export IPTABLES_CMD\n")
		b.WriteString("  sh /etc/init.d/firewall.sh || " +
			"/bin/sh /etc/cnimbus-say \"cnimbus: FIREWALL script exited non-zero -- see the failure logged above; " +
			"the ruleset was flushed to accept-all, not left half-applied\"\n")
		b.WriteString("else\n")
		b.WriteString("  /bin/sh /etc/cnimbus-say \"cnimbus: FIREWALL rules configured but no iptables binary available -- COPY one in as a fallback\"\n")
		b.WriteString("fi\n")
	}

	if len(spec.Firewall6) > 0 {
		// AD-047: mirrors the FIREWALL block immediately above, IPv6
		// side. The bundled binary's ip6tables dispatch mode is real and
		// already built -- verified empirically against the actual
		// static xtables-legacy-multi output (`<binary> ip6tables -L`
		// lists real chains once the kernel has IP6_NF_IPTABLES compiled
		// in) -- so this needs no separate binary, just a different first
		// argument. IPTABLES_CMD is reused (not a distinct
		// IP6TABLES_CMD): this and the block above run sequentially in
		// the same script, each setting it fresh right before its own
		// `sh .../firewallN.sh` invocation, so there's no cross-talk.
		b.WriteString("if command -v ip6tables >/dev/null 2>&1; then\n")
		b.WriteString("  IPTABLES_CMD=ip6tables\n")
		b.WriteString("elif [ -x /usr/sbin/cnimbus-iptables ]; then\n")
		b.WriteString("  IPTABLES_CMD=\"/usr/sbin/cnimbus-iptables ip6tables\"\n")
		b.WriteString("fi\n")
		b.WriteString("if [ -n \"$IPTABLES_CMD\" ]; then\n")
		b.WriteString("  export IPTABLES_CMD\n")
		b.WriteString("  sh /etc/init.d/firewall6.sh || " +
			"/bin/sh /etc/cnimbus-say \"cnimbus: FIREWALL6 script exited non-zero -- see the failure logged above; " +
			"the ruleset was flushed to accept-all, not left half-applied\"\n")
		b.WriteString("else\n")
		b.WriteString("  /bin/sh /etc/cnimbus-say \"cnimbus: FIREWALL6 rules configured but no ip6tables binary available -- COPY one in as a fallback\"\n")
		b.WriteString("fi\n")
	}

	writeUptimeCheckpoint(&b, "rcS finished, services starting")
	return b.String()
}

const udhcpcScript = `#!/bin/sh
[ "$1" = "bound" ] || [ "$1" = "renew" ] || exit 0
ifconfig "$interface" "$ip" netmask "$subnet"
[ -n "$mtu" ] && ifconfig "$interface" mtu "$mtu"
if [ -n "$router" ]; then
  while route del default gw 0.0.0.0 dev "$interface" 2>/dev/null; do :; done
  for gw in $router; do route add default gw "$gw" dev "$interface"; done
fi
if [ -n "$staticroutes" ]; then
  set -- $staticroutes
  while [ $# -ge 2 ]; do
    route add -net "$1" gw "$2" dev "$interface"
    shift 2
  done
fi
if [ -n "$dns" ]; then
  : > /etc/resolv.conf
  for d in $dns; do echo "nameserver $d" >> /etc/resolv.conf; done
fi
`

// udhcpcScriptSecondary handles eth1..eth3 (see buildRCScript):
// address only, deliberately no default route and no /etc/resolv.conf
// write -- the default gateway and DNS are eth0-scoped concepts in
// cnimbus today, and a second NIC is far more often a private/data
// network than another path to the internet.
const udhcpcScriptSecondary = `#!/bin/sh
[ "$1" = "bound" ] || [ "$1" = "renew" ] || exit 0
ifconfig "$interface" "$ip" netmask "$subnet"
`

// wifiPSKHex64 mirrors internal/nimbusfile's own wifiPSKHexPattern
// (HB-F-010): a pre-derived 64-hex-char PMK, distinguished from a plain
// passphrase because wpa_supplicant.conf's own "psk=" line takes the two
// forms very differently -- a hex PMK is written bare/unquoted, while a
// passphrase must be double-quoted or wpa_supplicant treats it as a PMK
// itself and fails to parse. internal/nimbusfile already validated
// whichever form actually reached here; this package stays standalone
// (no import of internal/nimbusfile, same reasoning as StaticIP/
// BusyboxApplet above) and re-checks the same shape rather than
// trusting the caller blindly.
var wifiPSKHex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// buildWpaSupplicantConf (F6.5) generates the config wpa_supplicant
// reads by path (see buildRCScript's invocation) -- the PSK never
// appears on a command line anywhere (HB-S-003), only as a line inside
// this file, which BuildImages stages at 0600 via the same tmpfs-shadow
// path supervisor scripts already use (HB-S-001).
//
// ssid/psk have already passed internal/nimbusfile's own
// validateWiFiSSID/validateWiFiPSK (length limits, and a ban on quote/
// backslash/newline characters mirroring FIREWALL's own injection guard
// -- see nimbusfile.go's wifiMetaChars) before ever reaching this
// package. wpaConfQuote below still escapes defensively rather than
// trusting that unconditionally -- belt-and-suspenders, the same
// posture buildFirewallScript/shQuote already take even though their
// own callers are pre-validated too.
func buildWpaSupplicantConf(ssid, psk, country string) string {
	var b strings.Builder
	b.WriteString("# Generated by cnimbus (F6.5) -- do not edit by hand.\n")
	b.WriteString("# Carries the WIFIPSK secret in plain text (wpa_supplicant has no other\n")
	b.WriteString("# way to consume a PSK); this file must stay 0600 -- see BuildImages.\n")
	b.WriteString("ap_scan=1\n")
	if country != "" {
		// wpa_supplicant sets the regulatory domain from this line itself
		// (via nl80211) once associated -- no separate `iw reg set` tool
		// needed, and none is on PATH in this image anyway.
		fmt.Fprintf(&b, "country=%s\n", country)
	}
	b.WriteString("network={\n")
	fmt.Fprintf(&b, "  ssid=%s\n", wpaConfQuote(ssid))
	if wifiPSKHex64.MatchString(psk) {
		// A pre-derived PMK: written bare, no quotes -- quoting a 64-char
		// hex string would make wpa_supplicant treat it as a (far too
		// long) passphrase instead of the PMK it actually is.
		fmt.Fprintf(&b, "  psk=%s\n", psk)
	} else {
		fmt.Fprintf(&b, "  psk=%s\n", wpaConfQuote(psk))
	}
	b.WriteString("  key_mgmt=WPA-PSK\n")
	b.WriteString("}\n")
	return b.String()
}

// wpaConfQuote double-quotes s for wpa_supplicant.conf's own string
// syntax, escaping any embedded quote/backslash. wpa_supplicant.conf is
// not a shell script -- shQuote's single-quote convention doesn't apply
// here, hence a separate function rather than reusing shQuote.
func wpaConfQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// wifiAssociateTimeoutSeconds is HB-N-004's bound: WiFi association
// must complete within 20s of rcS reaching the network stage, or fail
// loudly with a distinct diagnostic (HB-F-012) rather than hang boot
// indefinitely.
const wifiAssociateTimeoutSeconds = 20

// wpaSupplicantLog is where the backgrounded wpa_supplicant's own event
// log lands -- this build has no CONFIG_CTRL_IFACE (see
// internal/compileagent/wpasupplicant.go's writeSupplicantConfig: PSK-
// only, no control-socket support compiled in at all), so there is no
// wpa_cli to poll instead; grepping this log for wpa_supplicant's own
// CTRL-EVENT-* lines is the only association-status signal available.
const wpaSupplicantLog = "/var/log/wpa_supplicant.log"

// buildWifiBringupScript (F6.5, design.md section 7 step 1) is emitted
// into buildRCScript's network stage when hasWifiDriver(BootProfile) is
// true ("wifi" or "eth+wifi"): bring wlan0 up if the curated chipset set
// actually produced one, start the
// supplicant against the generated config, wait bounded, then hand off
// to the same DHCP/static logic eth0 already uses (mirrored here for
// wlan0 rather than factored out, deliberately -- eth0's own block above
// is unconditional, build-time text with no runtime branch around it,
// while this block's DHCP/static half only runs if association actually
// succeeded, a distinction not worth entangling into one shared helper).
//
// HB-F-012's four distinct diagnostics, as far as they're actually
// distinguishable without a control interface: no device (wlan0 never
// appears -- an unsupported/undetected chipset), auth rejected (the
// supplicant's own CTRL-EVENT-SSID-TEMP-DISABLED/WRONG_KEY lines), and a
// catch-all bounded timeout covering both "no AP found" and "firmware
// missing" (the kernel's own request_firmware() failure lands in dmesg,
// not this log, so the timeout message points there explicitly instead
// of guessing).
func buildWifiBringupScript(b *strings.Builder, spec PiecesSpec) {
	b.WriteString("if [ -e /sys/class/net/wlan0 ]; then\n")
	b.WriteString("  ifconfig wlan0 up\n")
	fmt.Fprintf(b, "  /usr/sbin/wpa_supplicant -B -i wlan0 -c /usr/sbin/wpa_supplicant.conf -f %s 2>/dev/null\n", shQuote(wpaSupplicantLog))
	b.WriteString("  WIFI_N=0\n")
	b.WriteString("  WIFI_STATE=pending\n")
	fmt.Fprintf(b, "  while [ \"$WIFI_N\" -lt %d ]; do\n", wifiAssociateTimeoutSeconds)
	fmt.Fprintf(b, "    grep -q 'CTRL-EVENT-CONNECTED' %s 2>/dev/null && { WIFI_STATE=connected; break; }\n", shQuote(wpaSupplicantLog))
	fmt.Fprintf(b, "    grep -qE 'CTRL-EVENT-SSID-TEMP-DISABLED|WRONG_KEY' %s 2>/dev/null && { WIFI_STATE=rejected; break; }\n", shQuote(wpaSupplicantLog))
	b.WriteString("    sleep 1\n")
	b.WriteString("    WIFI_N=$((WIFI_N+1))\n")
	b.WriteString("  done\n")
	fmt.Fprintf(b, "  [ \"$WIFI_STATE\" = pending ] && WIFI_STATE=timeout\n")
	b.WriteString("  case \"$WIFI_STATE\" in\n")
	fmt.Fprintf(b, "    connected) echo %s;;\n", shQuote("cnimbus: wifi: associated with SSID "+spec.WifiSSID))
	fmt.Fprintf(b, "    rejected) echo %s;;\n", shQuote("cnimbus: wifi: authentication rejected for SSID "+spec.WifiSSID+" -- check WIFIPSK"))
	fmt.Fprintf(b, "    *) echo %s;;\n", shQuote("cnimbus: wifi: no association with SSID "+spec.WifiSSID+
		" within "+fmt.Sprint(wifiAssociateTimeoutSeconds)+"s (no matching AP in range, or firmware missing -- check dmesg for request_firmware failures)"))
	b.WriteString("  esac\n")
	b.WriteString("  if [ \"$WIFI_STATE\" = connected ]; then\n")
	switch {
	case spec.StaticIP != nil:
		fmt.Fprintf(b, "    ifconfig wlan0 %s netmask %s up\n", shQuote(spec.StaticIP.Address), shQuote(spec.StaticIP.Netmask))
		fmt.Fprintf(b, "    route add default gw %s dev wlan0\n", shQuote(spec.StaticIP.Gateway))
	case spec.DHCP:
		// Same udhcpc.script as eth0 (see buildRCScript) -- it reads
		// $interface from its own environment, not a hardcoded name, so
		// it's safe to reuse verbatim for wlan0. Runs after eth0's own
		// (foreground, blocking) DHCP attempt above, so on a Nimbusfile
		// where both interfaces link and lease successfully, wlan0's
		// lease is the one applied last and therefore the one that wins
		// the default route (HB-F-013's "deterministic" requirement,
		// satisfied by this fixed ordering rather than any priority
		// logic) -- a real, documented limitation, not a general
		// per-interface routing-precedence mechanism.
		b.WriteString("    udhcpc -i wlan0 -n -t 3 -T 1 -A 2 -s /sbin/udhcpc.script\n")
	}
	b.WriteString("  fi\n")
	b.WriteString("else\n")
	b.WriteString("  echo " + shQuote("cnimbus: no wlan0 -- unsupported WiFi chipset for this HARDBOOT wifi/eth+wifi image "+
		"(see F6.6's curated chipset list)") + "\n")
	b.WriteString("fi\n")
}

// acpiPowerHandlerPath (AD-059) is where BusyBox's acpid actually looks
// for the power-button handler -- see acpiPowerScript's own doc comment
// for how this was found. A bare, empty 0644 placeholder is baked into
// the SquashFS root at this path (see squashfsroot.go); buildRCScript
// bind-mounts the real, already-executable /sbin/powerbtn.sh over it at
// boot, the same "placeholder file gives a bind-mount target that
// already exists" idiom /etc/resolv.conf's own setup already uses.
const acpiPowerHandlerPath = "etc/acpi/PWRF/00000080"

// acpiPowerScript (AD-059 rewrite): BusyBox's acpid, run with no "-e"
// flag (see "acpid &" below), is NOT the classic acpid this project's
// previous /etc/acpi/events/power config (event=.../action=... syntax)
// was written for -- verified directly against BusyBox 1.36.1's real
// util-linux/acpid.c source, not assumed, after a real Proxmox VM's
// ACPI "Signal Shutdown" request timed out
// ("VM quit/powerdown failed - got timeout") despite the button press
// visibly reaching the kernel (dmesg showed "ACPI: button: Power Button
// [PWRF]"). A real QEMU repro of the same symptom (system_powerdown,
// then checking whether the guest process exits) confirmed
// CONFIG_INPUT_EVDEV missing was one real gap (no /dev/input/event0 at
// all -- see minimal.fragment's own comment for that fix). Even after
// fixing that,
// shutdown still didn't happen: BusyBox's acpid reads /etc/acpid.conf
// (a flat key/action list) and /etc/acpi.map (event-tuple -> action
// string), falling back to compiled-in tables when neither exists --
// it never reads /etc/acpi/events/* at all, so the previous config was
// simply never consulted. The compiled-in fallback tables already map
// the power button's KEY_POWER event to action string
// "button/power PWRF 00000080", then (via the compiled-in action
// table) to "PWRF/00000080" -- which acpid then stat()s *relative to
// /etc/acpi* (it xchdir()s there at startup) and execve()s directly if
// it's a regular file. No custom /etc/acpid.conf or /etc/acpi.map is
// needed at all: placing the handler at exactly that path is sufficient
// on its own.
const acpiPowerScript = "#!/bin/sh\npoweroff\n"

// buildAgentScript wraps the AGENT http kind's cnimbusagent invocation
// in a tiny shell script rather than exec'ing the binary directly from
// inittab (see buildInittab): header values (e.g. "Bearer <token>") can
// contain spaces, and busybox-init's inittab respawn line has no
// quoting mechanism at all. shQuote handles that here; "exec" (not a
// bare call) replaces this shell process with cnimbusagent rather than
// leaving it around as a needless parent.
//
// Headers travel via the CNIMBUS_AGENT_HEADERS environment variable,
// not argv: this script itself is 0600 (see squashfsroot.go) and only
// ever read by root, but a header value handed to cnimbusagent as a
// literal argument would sit in that process's argv/cmdline instead --
// visible to any process that can read /proc/<pid>/cmdline, which
// (unlike /proc/<pid>/environ) isn't gated behind ptrace access even
// with hidepid set (see buildRCScript's /proc mount).
func buildAgentScript(a *Agent) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if len(a.Headers) > 0 {
		lines := make([]string, len(a.Headers))
		for i, h := range a.Headers {
			lines[i] = h.Name + ": " + h.Value
		}
		fmt.Fprintf(&b, "export CNIMBUS_AGENT_HEADERS=%s\n", shQuote(strings.Join(lines, "\n")))
	}
	b.WriteString("exec /usr/bin/cnimbusagent http " + shQuote(a.URL) + " " + shQuote(a.Interval) + "\n")
	return b.String()
}

// buildFirewallScript applies every FIREWALL rule via $IPTABLES_CMD --
// set and exported by the hook in buildRCScript that invokes this
// script, so the choice between a user's own COPY'd "iptables" and the
// bundled cnimbus-iptables binary is made exactly once, not per rule.
//
// Two auto-injected rules always come first, before any user rule:
// loopback traffic is always accepted (breaks nothing that talks to
// itself, e.g. a healthcheck hitting localhost), and established/
// related connections are always accepted (without it, a DROP-default
// policy silently drops the *replies* to the guest's own outbound
// traffic too -- DNS, NTP, the AGENT http poller -- not just unwanted
// inbound connections, which is very likely not what a Nimbusfile
// author declaring FIREWALL rules actually intended).
//
// "set -e" plus an EXIT trap: a rule that fails partway through (a
// genuinely malformed FIREWALL line, or a kernel missing a match/target
// the Nimbusfile asked for) must never leave the ruleset in whatever
// partial state it happened to reach when it failed -- that could
// easily be "the DROP policy applied, but none of the ACCEPT exceptions
// that were supposed to come after it", silently locking the workload
// out of its own declared ports with no obvious symptom beyond "nothing
// works". On any failure, flush back to a known-safe (accept-all)
// fallback and log exactly which rule failed, rather than booting with
// a half-applied, unpredictable ruleset.
// buildFirewallScript's EXIT trap fires only when set -e kills the
// script partway through a rule (see the rules loop below) -- it never
// runs after a clean, fully-applied ruleset. onError ("open", the
// default preserving this project's original T14 behavior, or
// "closed", T91) picks what a half-applied ruleset falls back to:
// "open" flushes to accept-all so a boot never hangs behind a broken
// ruleset; "closed" instead drops everything except loopback and
// already-established connections, for a Nimbusfile whose FIREWALL
// rules chose a DROP-default policy specifically -- silently inverting
// that intent to accept-all on any rule failure is the opposite of
// what its author declared.
// icmpv6NDPAutoRules (AD-055) are the ICMPv6 control messages a FIREWALL6
// ruleset must always accept, injected before any user rule the same way
// loopback/established-related already are.
//
// IPv4's ARP never passes through iptables at all -- it isn't IP traffic,
// so a DROP-default INPUT policy can never break address resolution.
// IPv6 has no ARP: Neighbor Discovery Protocol (RFC 4861) runs entirely
// over ICMPv6, which *is* ordinary IP traffic and *is* filtered by
// ip6tables like anything else. A Nimbusfile with FIREWALL6 "-P INPUT
// DROP" plus a single "-A INPUT -p tcp --dport 8080 -j ACCEPT" -- exactly
// this project's own hello-http-baremetal-proxmox example -- silently
// drops every Neighbor Solicitation aimed at this host's address, so no
// other node on the link can ever resolve its MAC and no packet (the
// Nimbufile's own allowed TCP included) can physically arrive. This is
// invisible from SLAAC's own side: the kernel obtains the address just
// fine (SLAAC's Router Solicitation/Advertisement exchange happens
// before this ruleset is even applied), so the console shows a real
// global address and the machine still looks completely broken from
// every other host on the network -- the exact split (IPv4 works, IPv6
// address is right there on the console, nothing reaches it) reported
// against a real Proxmox VM and real bare-metal hardware alike.
//
// Names verified against this project's own vendored iptables 1.8.8
// source (extensions/libip6t_icmp6.c's icmpv6_codes table) rather than
// assumed -- "neighbour-solicitation"/"neighbour-advertisement" are the
// canonical spellings there (an American "neighbor-*" alias also
// exists, unused here for no particular reason beyond picking one).
// Types 1-4 (unreachable/too-big/time-exceeded/parameter-problem) are
// RFC 4443's own error messages IPv6 itself depends on for Path MTU
// Discovery and basic error reporting, not just NDP's four message
// types (133/134/135/136) plus redirect (137) -- RFC 4890's recommended
// minimum firewall profile for exactly this reason. No source/hop-limit
// restriction on the NDP types (RFC 4890 recommends hop-limit 255 to
// scope them to the local link) -- CONFIG_IP6_NF_MATCH_HL isn't enabled
// in this project's kernel and adding it purely for this would be new
// kernel-side risk for a security property this image's own threat
// model (a single-purpose micro-VM, not a router) doesn't need.
var icmpv6NDPAutoRules = []string{
	"-A INPUT -p icmpv6 --icmpv6-type destination-unreachable -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type packet-too-big -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type time-exceeded -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type parameter-problem -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type echo-request -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type echo-reply -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type router-solicitation -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type router-advertisement -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type neighbour-solicitation -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type neighbour-advertisement -j ACCEPT",
	"-A INPUT -p icmpv6 --icmpv6-type redirect -j ACCEPT",
}

func buildFirewallScript(rules []string, onError string, ipv6 bool) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -e\n")
	if onError == "closed" {
		b.WriteString("trap 'code=$?; if [ \"$code\" -ne 0 ]; then " +
			"/bin/sh /etc/cnimbus-say \"cnimbus: FIREWALL rule failed ($CNIMBUS_FAILED_RULE), flushing to a closed (reachable-by-nothing) fallback\"; " +
			"$IPTABLES_CMD -F; " +
			"$IPTABLES_CMD -P INPUT DROP; $IPTABLES_CMD -P FORWARD DROP; $IPTABLES_CMD -P OUTPUT ACCEPT; " +
			"$IPTABLES_CMD -A INPUT -i lo -j ACCEPT; " +
			"$IPTABLES_CMD -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT; " +
			"fi' EXIT\n")
	} else {
		b.WriteString("trap 'code=$?; if [ \"$code\" -ne 0 ]; then " +
			"/bin/sh /etc/cnimbus-say \"cnimbus: FIREWALL rule failed ($CNIMBUS_FAILED_RULE), flushing to a known-safe (accept-all) fallback\"; " +
			"$IPTABLES_CMD -F; " +
			"$IPTABLES_CMD -P INPUT ACCEPT; $IPTABLES_CMD -P FORWARD ACCEPT; $IPTABLES_CMD -P OUTPUT ACCEPT; " +
			"fi' EXIT\n")
	}
	// AD-057: labeled by protocol -- with both FIREWALL and FIREWALL6
	// declared, firewall.sh and firewall6.sh each print this line at
	// their own start, and an unlabeled "cnimbus: firewall
	// fallback-on-error mode: 'open'" appearing twice in a row on a real
	// console reads like a duplicate or a bug, not two independent
	// scripts each reporting correctly.
	family := "IPv4"
	if ipv6 {
		family = "IPv6"
	}
	fmt.Fprintf(&b, "/bin/sh /etc/cnimbus-say \"cnimbus: firewall (%s) fallback-on-error mode: %s\"\n", family, shQuote(effectiveFirewallOnError(onError)))
	autoRules := []string{
		"-A INPUT -i lo -j ACCEPT",
		"-A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT",
	}
	if ipv6 {
		autoRules = append(autoRules, icmpv6NDPAutoRules...)
	}
	for _, r := range append(autoRules, rules...) {
		fmt.Fprintf(&b, "CNIMBUS_FAILED_RULE=%s\n", shQuote(r))
		b.WriteString("$IPTABLES_CMD " + r + "\n")
	}
	return b.String()
}

// effectiveFirewallOnError normalizes onError's zero value ("", no
// FIREWALL_ON_ERROR directive) to "open" -- the documented default,
// matching every Nimbusfile written before this directive existed.
func effectiveFirewallOnError(onError string) string {
	if onError == "closed" {
		return "closed"
	}
	return "open"
}

// buildHostsFile is what T87 was written to add: without it, no file on
// the image resolves "localhost" at all, which silently breaks the exact
// HEALTHCHECK idiom (http://localhost:8080/) the shipped Nimbusfile
// documents -- BusyBox's own wget/nslookup have no fallback resolution
// for a name with no /etc/hosts entry and no working resolver, so the
// lookup simply fails and the supervisor kills an otherwise-healthy
// service forever. spec.Hostname is included too, mirroring the
// hostname-maps-to-127.0.1.1 convention most distros ship, so a service
// that resolves its own configured hostname also works.
func buildHostsFile(hostname string) string {
	b := "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n"
	if hostname != "" && hostname != "localhost" {
		b += "127.0.1.1\t" + hostname + "\n"
	}
	return b
}

// buildPasswd always includes the root entry (T88): a Go binary built
// with CGO_ENABLED=0 parses /etc/passwd directly for os/user.Current(),
// and anything calling getpwuid(0) through glibc gets NULL without it --
// an opaque startup error in third-party code with no obvious connection
// to "this image has no USER directive". user is appended only when set.
func buildPasswd(user string) string {
	s := "root:x:0:0:root:/:/bin/false\n"
	if user != "" {
		s += user + ":x:1000:1000:" + user + ":/:/bin/false\n"
	}
	return s
}

func buildGroup(user string) string {
	s := "root:x:0:\n"
	if user != "" {
		s += user + ":x:1000:\n"
	}
	return s
}

// shQuote wraps s in single quotes for safe use as one POSIX shell
// word, escaping any single quotes it contains.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shQuote(a)
	}
	return strings.Join(quoted, " ")
}
