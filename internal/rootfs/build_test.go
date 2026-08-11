package rootfs

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// shQuote/shJoin are the one thing standing between an ENV/CMD/ENTRYPOINT
// value and shell injection into a generated supervisor script -- every
// value they wrap is spliced directly into a "#!/bin/sh" script's text.
// These tests actually invoke /bin/sh (if available) to prove the quoting
// round-trips correctly, not just that it looks plausible.

func TestShQuoteBasic(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "''"},
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"it's", `'it'\''s'`},
		{"''", `''\'''\'''`},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"a;b", "'a;b'"},
	}
	for _, tt := range tests {
		if got := shQuote(tt.in); got != tt.want {
			t.Errorf("shQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestShQuoteRoundTripsThroughRealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		sh, err = exec.LookPath("bash")
	}
	if err != nil {
		t.Skip("no POSIX shell on PATH to verify against")
	}

	dangerous := []string{
		"",
		"plain",
		"has space",
		"it's got quotes",
		`"double quotes"`,
		"$(echo injected)",
		"`echo injected`",
		"a;echo injected;b",
		"a && echo injected",
		"a | echo injected",
		"newline\nafter",
		"$VAR${VAR}",
		"back\\slash",
	}
	for _, in := range dangerous {
		script := "printf '%s' " + shQuote(in)
		out, err := exec.Command(sh, "-c", script).Output()
		if err != nil {
			t.Errorf("shQuote(%q): shell rejected script: %v", in, err)
			continue
		}
		if string(out) != in {
			t.Errorf("shQuote(%q): shell echoed %q (injection or corruption)", in, string(out))
		}
	}
}

func TestShJoin(t *testing.T) {
	got := shJoin([]string{"/usr/bin/foo", "--flag", "value with spaces"})
	want := `'/usr/bin/foo' '--flag' 'value with spaces'`
	if got != want {
		t.Errorf("shJoin = %q, want %q", got, want)
	}
	if shJoin(nil) != "" {
		t.Errorf("shJoin(nil) = %q, want empty", shJoin(nil))
	}
}

func TestBuildPasswdGroup(t *testing.T) {
	passwd := buildPasswd("app")
	if !strings.Contains(passwd, "root:x:0:0:root:/:/bin/false") {
		t.Errorf("buildPasswd missing root entry: %q", passwd)
	}
	if !strings.Contains(passwd, "app:x:1000:1000:app:/:/bin/false") {
		t.Errorf("buildPasswd missing app entry: %q", passwd)
	}

	group := buildGroup("app")
	if !strings.Contains(group, "root:x:0:") || !strings.Contains(group, "app:x:1000:") {
		t.Errorf("buildGroup = %q", group)
	}
}

// T88: the default image (no USER directive) previously had no
// /etc/passwd or /etc/group at all -- both must still contain at least
// the root entry.
func TestBuildPasswdGroupNoUserStillHasRoot(t *testing.T) {
	passwd := buildPasswd("")
	if !strings.Contains(passwd, "root:x:0:0:root:/:/bin/false") {
		t.Errorf("buildPasswd(\"\") missing root entry: %q", passwd)
	}
	if strings.Count(passwd, "\n") != 1 {
		t.Errorf("buildPasswd(\"\") should contain only the root line: %q", passwd)
	}

	group := buildGroup("")
	if !strings.Contains(group, "root:x:0:") {
		t.Errorf("buildGroup(\"\") missing root entry: %q", group)
	}
	if strings.Count(group, "\n") != 1 {
		t.Errorf("buildGroup(\"\") should contain only the root line: %q", group)
	}
}

func TestBuildHostsFile(t *testing.T) {
	hosts := buildHostsFile("myvm")
	if !strings.Contains(hosts, "127.0.0.1\tlocalhost") {
		t.Errorf("buildHostsFile missing IPv4 localhost entry: %q", hosts)
	}
	if !strings.Contains(hosts, "::1\tlocalhost") {
		t.Errorf("buildHostsFile missing IPv6 localhost entry: %q", hosts)
	}
	if !strings.Contains(hosts, "127.0.1.1\tmyvm") {
		t.Errorf("buildHostsFile missing hostname entry: %q", hosts)
	}

	hosts = buildHostsFile("")
	if strings.Contains(hosts, "127.0.1.1") {
		t.Errorf("buildHostsFile with empty hostname should not add a hostname entry: %q", hosts)
	}

	hosts = buildHostsFile("localhost")
	if strings.Contains(hosts, "127.0.1.1") {
		t.Errorf("buildHostsFile with hostname==localhost should not duplicate the localhost entry: %q", hosts)
	}
}

func TestBuildAgentScriptHTTP(t *testing.T) {
	script := buildAgentScript(&Agent{Kind: "http", URL: "http://10.0.2.2:9999/", Interval: "5"})
	if !strings.Contains(script, "exec /usr/bin/cnimbusagent http") {
		t.Errorf("expected an exec into cnimbusagent: %q", script)
	}
	if !strings.Contains(script, "'http://10.0.2.2:9999/'") {
		t.Errorf("URL not shell-quoted in script: %q", script)
	}
	if !strings.Contains(script, "'5'") {
		t.Errorf("interval not present: %q", script)
	}
}

func TestBuildAgentScriptWithHeaders(t *testing.T) {
	a := &Agent{
		Kind: "http", URL: "http://169.254.169.254/computeMetadata/v1/", Interval: "5",
		Headers: []AgentHeader{{Name: "Metadata-Flavor", Value: "Google"}},
	}
	script := buildAgentScript(a)
	if !strings.Contains(script, `export CNIMBUS_AGENT_HEADERS='Metadata-Flavor: Google'`) {
		t.Errorf("expected the header exported via env, not argv: %q", script)
	}
	if strings.Contains(script, "--header") {
		t.Errorf("header must not be passed as an argv flag (visible via /proc/<pid>/cmdline): %q", script)
	}
}

func TestBuildFirewallScript(t *testing.T) {
	script := buildFirewallScript([]string{"-P INPUT DROP", "-A INPUT -p tcp --dport 8080 -j ACCEPT"}, "", false)
	if !strings.Contains(script, "$IPTABLES_CMD -P INPUT DROP") {
		t.Errorf("missing first rule: %q", script)
	}
	if !strings.Contains(script, "$IPTABLES_CMD -A INPUT -p tcp --dport 8080 -j ACCEPT") {
		t.Errorf("missing second rule: %q", script)
	}
}

// AD-055: IPv4's ARP never touches iptables, so a v4 DROP-default
// policy can never break address resolution -- but IPv6's Neighbor
// Discovery Protocol runs entirely over ICMPv6, which ip6tables filters
// like any other IP traffic. A real Proxmox VM and real bare-metal
// hardware both got a correct global SLAAC address (proving DHCP/SLAAC
// itself worked) yet were unreachable from every other host on the
// network once FIREWALL6 declared "-P INPUT DROP" plus a single TCP
// ACCEPT rule -- because no Neighbor Solicitation aimed at the guest
// could get past that DROP. This is the fix: FIREWALL6 (ipv6=true) must
// carry RFC 4890's recommended minimum ICMPv6 auto-rules; FIREWALL
// (ipv6=false) must not, since IPv4 has no equivalent need.
func TestBuildFirewallScriptIPv6AutoInjectsICMPv6NDP(t *testing.T) {
	v6Script := buildFirewallScript([]string{"-P INPUT DROP"}, "", true)
	for _, icmpType := range []string{
		"destination-unreachable", "packet-too-big", "time-exceeded", "parameter-problem",
		"echo-request", "echo-reply",
		"router-solicitation", "router-advertisement",
		"neighbour-solicitation", "neighbour-advertisement", "redirect",
	} {
		want := "$IPTABLES_CMD -A INPUT -p icmpv6 --icmpv6-type " + icmpType + " -j ACCEPT"
		if !strings.Contains(v6Script, want) {
			t.Errorf("ipv6=true missing %q in: %q", want, v6Script)
		}
	}
	// These rules must come before the user's own rules (same ordering
	// discipline as loopback/established), and specifically before a
	// DROP policy that would otherwise apply first and never get
	// reached by anything after it if ip6tables evaluated top-down past
	// a matching DROP -- ip6tables does stop at the first match, so
	// order here is not cosmetic.
	ndpIdx := strings.Index(v6Script, "--icmpv6-type destination-unreachable")
	userRuleIdx := strings.Index(v6Script, "$IPTABLES_CMD -P INPUT DROP")
	if ndpIdx < 0 || ndpIdx > userRuleIdx {
		t.Errorf("expected ICMPv6 auto-rules before the user's own DROP policy: ndp=%d userRule=%d", ndpIdx, userRuleIdx)
	}

	v4Script := buildFirewallScript([]string{"-P INPUT DROP"}, "", false)
	if strings.Contains(v4Script, "icmpv6") {
		t.Errorf("ipv6=false must not inject any ICMPv6 rule -- IPv4 has no NDP-equivalent need for it: %q", v4Script)
	}
}

// AD-057: firewall.sh and firewall6.sh each print their own
// fallback-on-error line at their own start -- an unlabeled copy of the
// same sentence twice in a row on a real console (both declared, both
// "open") reads like a duplicate rather than two independent scripts.
func TestBuildFirewallScriptLabelsFallbackModeByProtocol(t *testing.T) {
	v4Script := buildFirewallScript([]string{"-P INPUT DROP"}, "", false)
	if !strings.Contains(v4Script, "cnimbus: firewall (IPv4) fallback-on-error mode:") {
		t.Errorf("expected ipv6=false to label its fallback-mode line IPv4: %q", v4Script)
	}
	v6Script := buildFirewallScript([]string{"-P INPUT DROP"}, "", true)
	if !strings.Contains(v6Script, "cnimbus: firewall (IPv6) fallback-on-error mode:") {
		t.Errorf("expected ipv6=true to label its fallback-mode line IPv6: %q", v6Script)
	}
}

func TestBuildFirewallScriptAutoInjectsLoopbackAndEstablished(t *testing.T) {
	script := buildFirewallScript([]string{"-P INPUT DROP"}, "", false)
	loopbackIdx := strings.Index(script, "$IPTABLES_CMD -A INPUT -i lo -j ACCEPT")
	establishedIdx := strings.Index(script, "$IPTABLES_CMD -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT")
	userRuleIdx := strings.Index(script, "$IPTABLES_CMD -P INPUT DROP")
	if loopbackIdx < 0 || establishedIdx < 0 {
		t.Fatalf("expected loopback and established/related rules to be auto-injected: %q", script)
	}
	if loopbackIdx >= userRuleIdx || establishedIdx >= userRuleIdx {
		t.Errorf("expected the auto-injected rules before the user's own rule: %q", script)
	}
}

func TestBuildFirewallScriptHasFailureTrap(t *testing.T) {
	script := buildFirewallScript([]string{"-P INPUT DROP"}, "", false)
	if !strings.Contains(script, "set -e") {
		t.Errorf("expected set -e so a failed rule stops the script immediately: %q", script)
	}
	if !strings.Contains(script, "trap ") || !strings.Contains(script, "EXIT") {
		t.Errorf("expected an EXIT trap to flush to a safe fallback on failure: %q", script)
	}
	if !strings.Contains(script, "$IPTABLES_CMD -P INPUT ACCEPT") {
		t.Errorf("expected the fallback trap to reset the INPUT policy to ACCEPT: %q", script)
	}
}

// T91: "open" (the default, including "" for no FIREWALL_ON_ERROR
// directive) must preserve the original T14 accept-all fallback --
// existing TestBuildFirewallScriptHasFailureTrap already covers this
// for the "" case; this asserts explicit "open" behaves identically.
func TestBuildFirewallScriptOnErrorOpenIsAcceptAll(t *testing.T) {
	script := buildFirewallScript([]string{"-P INPUT DROP"}, "open", false)
	if !strings.Contains(script, "$IPTABLES_CMD -P INPUT ACCEPT") ||
		!strings.Contains(script, "$IPTABLES_CMD -P FORWARD ACCEPT") ||
		!strings.Contains(script, "$IPTABLES_CMD -P OUTPUT ACCEPT") {
		t.Errorf("expected onError=open to flush to accept-all on all three policies: %q", script)
	}
	if !strings.Contains(script, "firewall (IPv4) fallback-on-error mode: 'open'") {
		t.Errorf("expected the boot log to state the open fallback mode: %q", script)
	}
}

// A Nimbusfile whose FIREWALL rules chose a DROP-default policy
// specifically must not have that intent silently inverted to
// accept-all on a rule failure.
func TestBuildFirewallScriptOnErrorClosedDropsByDefault(t *testing.T) {
	script := buildFirewallScript([]string{"-P INPUT DROP", "-A INPUT -p tcp --dport 443 -j ACCEPT"}, "closed", false)
	if !strings.Contains(script, "$IPTABLES_CMD -P INPUT DROP") || !strings.Contains(script, "$IPTABLES_CMD -P FORWARD DROP") {
		t.Errorf("expected onError=closed to set INPUT/FORWARD policies to DROP on failure: %q", script)
	}
	if strings.Contains(script, "$IPTABLES_CMD -P INPUT ACCEPT") {
		t.Errorf("expected onError=closed to never fall back to INPUT ACCEPT: %q", script)
	}
	if !strings.Contains(script, "firewall (IPv4) fallback-on-error mode: 'closed'") {
		t.Errorf("expected the boot log to state the closed fallback mode: %q", script)
	}
	// Still able to complete its own outbound connections and reach
	// itself, so the image can at least report its own failure.
	trapSection := script[strings.Index(script, "trap '"):strings.Index(script, "EXIT")]
	if !strings.Contains(trapSection, "-A INPUT -i lo -j ACCEPT") {
		t.Errorf("expected the closed fallback to still accept loopback: %q", trapSection)
	}
	if !strings.Contains(script, "$IPTABLES_CMD -P OUTPUT ACCEPT") {
		t.Errorf("expected the closed fallback to still allow outbound connections: %q", script)
	}
}

func TestBuildRCScriptFirewallPicksBundledIptablesAsFallback(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", Firewall: []string{"-P INPUT DROP"}})
	if !strings.Contains(script, "/usr/sbin/cnimbus-iptables") {
		t.Errorf("expected the bundled iptables fallback path to be referenced: %q", script)
	}
	if !strings.Contains(script, "command -v iptables") {
		t.Errorf("expected a user-COPY'd iptables to still take priority if present: %q", script)
	}
}

func TestBuildSupervisorScriptEnvAndUser(t *testing.T) {
	svc := Service{Name: "entrypoint", Argv: []string{"/usr/bin/helloserver", ":8080"}}
	env := []EnvVar{{Key: "PORT", Value: "8080"}, {Key: "MSG", Value: "it's here"}}
	script := buildSupervisorScript(svc, env, "app", "", nil)

	if !strings.Contains(script, "export PORT='8080'") {
		t.Errorf("missing PORT export: %q", script)
	}
	if !strings.Contains(script, `export MSG='it'\''s here'`) {
		t.Errorf("missing quoted MSG export: %q", script)
	}
	if !strings.Contains(script, "setuidgid 'app'") {
		t.Errorf("missing setuidgid wrapping for USER: %q", script)
	}
	if !strings.Contains(script, "'/usr/bin/helloserver' ':8080'") {
		t.Errorf("missing quoted argv: %q", script)
	}
	if !strings.Contains(script, "while true;") {
		t.Errorf("missing respawn loop: %q", script)
	}
	if !strings.Contains(script, `restart #$n`) {
		t.Errorf("missing restart counter message: %q", script)
	}
}

func TestBuildSupervisorScriptNoUserMeansRoot(t *testing.T) {
	svc := Service{Name: "entrypoint", Argv: []string{"/bin/true"}}
	script := buildSupervisorScript(svc, nil, "", "", nil)
	if strings.Contains(script, "setuidgid") {
		t.Errorf("expected no setuidgid wrapping when USER is unset: %q", script)
	}
}

func TestBuildSupervisorScriptBackoffCapsAt30(t *testing.T) {
	script := buildSupervisorScript(Service{Name: "x", Argv: []string{"/bin/true"}}, nil, "", "", nil)
	if !strings.Contains(script, `[ "$d" -gt 30 ] && d=30`) {
		t.Errorf("expected backoff cap at 30s: %q", script)
	}
}

func TestBuildSupervisorScriptWorkdir(t *testing.T) {
	script := buildSupervisorScript(Service{Name: "x", Argv: []string{"/bin/true"}}, nil, "", "/app", nil)
	if !strings.Contains(script, `cd '/app' || exit 1`) {
		t.Errorf("expected cd into WORKDIR: %q", script)
	}
}

// T89: the non-healthcheck path must pipe the service command's
// stdout+stderr through BusyBox logger (so log persistence -- syslogd
// writing to a VOLUME -- actually captures app output, not just
// inherited-fd console noise), report the *command's* exit code (not
// logger's, which is what pipefail is for), and still echo to the
// console (logger -s) so serial debugging is unaffected.
func TestBuildSupervisorScriptPipesOutputThroughLogger(t *testing.T) {
	svc := Service{Name: "myapp", Argv: []string{"/usr/bin/app"}}
	script := buildSupervisorScript(svc, nil, "", "", nil)
	if !strings.Contains(script, "set -o pipefail") {
		t.Errorf("expected pipefail so $? reflects the command's exit code through the logger pipe: %q", script)
	}
	if !strings.Contains(script, "'/usr/bin/app' 2>&1 | logger -s -t 'myapp'") {
		t.Errorf("expected the command piped through logger -s -t <name>: %q", script)
	}
}

func TestBuildSupervisorScriptRestartNo(t *testing.T) {
	svc := Service{Name: "x", Argv: []string{"/bin/true"}, Restart: "no"}
	script := buildSupervisorScript(svc, nil, "", "", nil)
	if strings.Contains(script, "while true;") {
		t.Errorf("RESTART no should not loop at all: %q", script)
	}
	if !strings.Contains(script, `exit "$code"`) {
		t.Errorf("RESTART no should exit with the command's own code: %q", script)
	}
}

func TestBuildSupervisorScriptRestartOnFailureExitsCleanlyOnSuccess(t *testing.T) {
	svc := Service{Name: "x", Argv: []string{"/bin/true"}, Restart: "on-failure"}
	script := buildSupervisorScript(svc, nil, "", "", nil)
	if !strings.Contains(script, `[ "$code" -eq 0 ] && exit 0`) {
		t.Errorf("RESTART on-failure should stop looping on a clean exit: %q", script)
	}
}

func TestBuildInittabRestartPolicyPicksOnceOverRespawn(t *testing.T) {
	always := buildInittab([]Service{{Name: "a", Restart: "always"}}, nil)
	if !strings.Contains(always, "::respawn:/bin/sh /usr/sbin/cnimbus-svc-a.sh") {
		t.Errorf("always should use ::respawn:: %q", always)
	}
	onFailure := buildInittab([]Service{{Name: "b", Restart: "on-failure"}}, nil)
	if !strings.Contains(onFailure, "::once:/bin/sh /usr/sbin/cnimbus-svc-b.sh") {
		t.Errorf("on-failure should use ::once:: %q", onFailure)
	}
	no := buildInittab([]Service{{Name: "c", Restart: "no"}}, nil)
	if !strings.Contains(no, "::once:/bin/sh /usr/sbin/cnimbus-svc-c.sh") {
		t.Errorf("no should use ::once:: %q", no)
	}
}

func TestBuildSupervisorScriptHealthcheck(t *testing.T) {
	svc := Service{Name: "entrypoint", Argv: []string{"/usr/bin/app"}}
	hc := &Healthcheck{Argv: []string{"/usr/bin/curl", "-f", "http://localhost/"}, Interval: "10", Retries: "3"}
	script := buildSupervisorScript(svc, nil, "", "", hc)
	if !strings.Contains(script, "'/usr/bin/app' &") {
		t.Errorf("expected the main command backgrounded when a healthcheck is set: %q", script)
	}
	if !strings.Contains(script, "'/usr/bin/curl' '-f' 'http://localhost/'") {
		t.Errorf("expected the healthcheck command to run: %q", script)
	}
	if !strings.Contains(script, `failcount" -ge '3'`) {
		t.Errorf("expected the retries threshold to be checked: %q", script)
	}
	if !strings.Contains(script, "sleep '10'") {
		t.Errorf("expected the healthcheck interval to be used: %q", script)
	}
}

func TestBuildSupervisorScriptUserGetsNoNewPrivsPrefix(t *testing.T) {
	svc := Service{Name: "entrypoint", Argv: []string{"/usr/bin/app"}}
	script := buildSupervisorScript(svc, nil, "app", "", nil)
	if !strings.Contains(script, `command -v setpriv >/dev/null 2>&1 && NNP_PREFIX="setpriv --nnp --"`) {
		t.Errorf("expected a runtime setpriv availability check: %q", script)
	}
	if !strings.Contains(script, "$NNP_PREFIX setuidgid 'app' '/usr/bin/app'") {
		t.Errorf("expected the command prefixed with $NNP_PREFIX ahead of setuidgid: %q", script)
	}
}

func TestBuildSupervisorScriptHealthcheckWithUserIsSetuidgidWrapped(t *testing.T) {
	svc := Service{Name: "entrypoint", Argv: []string{"/usr/bin/app"}}
	hc := &Healthcheck{Argv: []string{"/usr/bin/curl", "-f", "http://localhost/"}, Interval: "10", Retries: "3"}
	script := buildSupervisorScript(svc, nil, "nobody", "", hc)
	if !strings.Contains(script, "setuidgid 'nobody' '/usr/bin/app' &") {
		t.Errorf("expected the main command setuidgid-wrapped: %q", script)
	}
	if !strings.Contains(script, "setuidgid 'nobody' '/usr/bin/curl' '-f' 'http://localhost/'") {
		t.Errorf("expected the healthcheck command setuidgid-wrapped, not run as root: %q", script)
	}
}

func TestBuildInittabAlwaysHasCoreEntries(t *testing.T) {
	tab := buildInittab(nil, nil)
	for _, want := range []string{
		"::sysinit:/bin/sh /etc/init.d/rcS",
		"::ctrlaltdel:/sbin/reboot",
		// T82: umount -a -r is now the last line of the generated
		// shutdown.sh, invoked here instead of being the ::shutdown:
		// action directly -- see TestBuildShutdownScript*.
		"::shutdown:/bin/sh /etc/init.d/shutdown.sh",
	} {
		if !strings.Contains(tab, want) {
			t.Errorf("inittab missing %q: %q", want, tab)
		}
	}
}

func TestBuildInittabServices(t *testing.T) {
	tab := buildInittab([]Service{{Name: "entrypoint"}, {Name: "sidecar"}}, nil)
	if !strings.Contains(tab, "::respawn:/bin/sh /usr/sbin/cnimbus-svc-entrypoint.sh") {
		t.Errorf("missing entrypoint respawn line: %q", tab)
	}
	if !strings.Contains(tab, "::respawn:/bin/sh /usr/sbin/cnimbus-svc-sidecar.sh") {
		t.Errorf("missing sidecar respawn line: %q", tab)
	}
}

func TestBuildInittabAgentHTTP(t *testing.T) {
	tab := buildInittab(nil, &Agent{Kind: "http", URL: "http://x/kv", Interval: "5"})
	if !strings.Contains(tab, "::respawn:/bin/sh /usr/sbin/cnimbus-agent.sh") {
		t.Errorf("missing http agent respawn line: %q", tab)
	}
}

func TestBuildInittabAgentVirtioSerial(t *testing.T) {
	tab := buildInittab(nil, &Agent{Kind: "virtio-serial", URL: "/dev/vport0p1", Interval: "5"})
	if !strings.Contains(tab, "::respawn:/usr/bin/cnimbusagent virtio-serial /dev/vport0p1 5") {
		t.Errorf("missing virtio-serial agent respawn line: %q", tab)
	}
}

func TestBuildInittabAgentVMware(t *testing.T) {
	tab := buildInittab(nil, &Agent{Kind: "vmware", URL: "some-key", Interval: "5"})
	if !strings.Contains(tab, "::respawn:/usr/bin/cnimbusagent vmware some-key 5") {
		t.Errorf("missing vmware agent respawn line: %q", tab)
	}
}

func TestBuildInittabAgentVBoxGuest(t *testing.T) {
	tab := buildInittab(nil, &Agent{Kind: "vboxguest", URL: "/cnimbus/message", Interval: "3"})
	if !strings.Contains(tab, "::respawn:/usr/bin/cnimbusagent vboxguest /cnimbus/message 3") {
		t.Errorf("missing vboxguest agent respawn line: %q", tab)
	}
}

func TestBuildRCScriptStaticIPWinsOverDHCP(t *testing.T) {
	spec := PiecesSpec{
		Hostname: "myvm",
		DHCP:     true,
		StaticIP: &StaticIP{Address: "192.168.1.50", Netmask: "255.255.255.0", Gateway: "192.168.1.1"},
	}
	script := buildRCScript(spec)
	if strings.Contains(script, "udhcpc -i eth0") {
		t.Errorf("expected eth0 DHCP to be skipped when StaticIP is set: %q", script)
	}
	if !strings.Contains(script, "ifconfig eth0 '192.168.1.50' netmask '255.255.255.0' up") {
		t.Errorf("missing static IP ifconfig: %q", script)
	}
	if !strings.Contains(script, "route add default gw '192.168.1.1' dev eth0") {
		t.Errorf("missing static route: %q", script)
	}
}

func TestBuildRCScriptDHCPOnly(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", DHCP: true}
	script := buildRCScript(spec)
	if !strings.Contains(script, "udhcpc -i eth0 -n -t 3 -T 1 -A 2 -s /sbin/udhcpc.script") {
		t.Errorf("missing udhcpc invocation: %q", script)
	}
}

// T92: the foreground eth0 udhcpc gates every service start during
// sysinit, so its worst-case wait must be a small, bounded number of
// seconds, not BusyBox's own tens-of-seconds defaults.
func TestBuildRCScriptDHCPHasBoundedTimeout(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	for _, flag := range []string{"-t 3", "-T 1", "-A 2"} {
		if !strings.Contains(script, flag) {
			t.Errorf("expected eth0's udhcpc to pass %s, bounding its worst-case wait: %q", flag, script)
		}
	}
}

func TestBuildRCScriptDiagnosesMissingEth0WhenNetworked(t *testing.T) {
	dhcpScript := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	if !strings.Contains(dhcpScript, "[ -e /sys/class/net/eth0 ] || "+consoleSayCmd+" \"cnimbus: no eth0") {
		t.Errorf("expected a self-diagnosing eth0 check for DHCP: %q", dhcpScript)
	}
	staticScript := buildRCScript(PiecesSpec{Hostname: "myvm", StaticIP: &StaticIP{Address: "10.0.0.5", Netmask: "255.255.255.0", Gateway: "10.0.0.1"}})
	if !strings.Contains(staticScript, "[ -e /sys/class/net/eth0 ] || "+consoleSayCmd+" \"cnimbus: no eth0") {
		t.Errorf("expected a self-diagnosing eth0 check for StaticIP: %q", staticScript)
	}
	noNetScript := buildRCScript(PiecesSpec{Hostname: "myvm"})
	if strings.Contains(noNetScript, "no eth0") {
		t.Errorf("no networking configured -- the eth0 check is pointless noise: %q", noNetScript)
	}
}

// AD-053: a real bare-metal boot reported eth0 reaching "Link is Up"
// (the driver's own kernel-log line) with no IP ever obtained, and
// nothing in the image's own output could distinguish "no carrier at
// all" from "carrier is fine but no DHCP server answered" -- both just
// looked like "no IP printed" on the console. These assert the specific
// debug lines that close that gap all exist, and that they go through
// the console-fan-out helper (AD-052) rather than a bare echo, since a
// debug line that only reaches serial is exactly the bug AD-052 fixed.
func TestBuildRCScriptDiagnosesEthDriverAndCarrierState(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	for _, want := range []string{
		"ETH0DRV=$(readlink -f /sys/class/net/eth0/device/driver 2>/dev/null); ETH0DRV=${ETH0DRV##*/}",
		consoleSayCmd + " \"cnimbus: eth0 driver=${ETH0DRV:-unknown} mac=$(cat /sys/class/net/eth0/address 2>/dev/null)\"",
		`for i in 1 2 3; do [ "$(cat /sys/class/net/eth0/carrier 2>/dev/null)" = 1 ] && break; sleep 1; done`,
		consoleSayCmd + " \"cnimbus: eth0 carrier=$(cat /sys/class/net/eth0/carrier 2>/dev/null) operstate=$(cat /sys/class/net/eth0/operstate 2>/dev/null) speed=$(cat /sys/class/net/eth0/speed 2>/dev/null)Mbps\"",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing eth0 debug line %q in: %q", want, script)
		}
	}
	// The carrier poll must happen before the reading it feeds, and both
	// before udhcpc runs -- a carrier read after udhcpc would just be
	// too late to explain a lease failure.
	pollIdx := strings.Index(script, "for i in 1 2 3;")
	printIdx := strings.Index(script, "cnimbus: eth0 carrier=")
	udhcpcIdx := strings.Index(script, "udhcpc -i eth0")
	if pollIdx <= 0 || pollIdx >= printIdx || printIdx >= udhcpcIdx {
		t.Errorf("expected carrier poll, then carrier print, then udhcpc, in that order: poll=%d print=%d udhcpc=%d", pollIdx, printIdx, udhcpcIdx)
	}
}

// AD-053: udhcpc's own exit status plus whatever address actually landed
// on the interface afterward -- distinguishing "server never answered"
// from "a lease was obtained but something later on this project's own
// side went wrong" for the first time.
func TestBuildRCScriptDiagnosesDHCPOutcome(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	udhcpcIdx := strings.Index(script, "udhcpc -i eth0 -n -t 3 -T 1 -A 2 -s /sbin/udhcpc.script")
	if udhcpcIdx < 0 {
		t.Fatalf("missing udhcpc invocation: %q", script)
	}
	if !strings.Contains(script, "DHCPRC=$?") {
		t.Errorf("expected udhcpc's exit status to be captured immediately after it runs: %q", script)
	}
	rcIdx := strings.Index(script, "DHCPRC=$?")
	if rcIdx < udhcpcIdx {
		t.Errorf("DHCPRC=$? must come after udhcpc runs, not before: %q", script)
	}
	want := consoleSayCmd + ` "cnimbus: udhcpc exit=$DHCPRC addr=$(ip -o -f inet addr show eth0 2>/dev/null | awk '{print $4}')"`
	if !strings.Contains(script, want) {
		t.Errorf("missing DHCP outcome debug line %q in: %q", want, script)
	}
	// StaticIP never runs udhcpc, so none of this debug output applies.
	staticScript := buildRCScript(PiecesSpec{Hostname: "myvm", StaticIP: &StaticIP{Address: "10.0.0.5", Netmask: "255.255.255.0", Gateway: "10.0.0.1"}})
	if strings.Contains(staticScript, "DHCPRC") {
		t.Errorf("StaticIP never runs udhcpc -- DHCPRC debug output is meaningless noise here: %q", staticScript)
	}
}

// AD-054: real hardware and a real Proxmox VM both got IPv4 working but
// reported no IPv6, and the VGA banner's own "scope global" filter
// (T59/HB's IP banner) means it can never distinguish "no IPv6 at all"
// from "only the mandatory link-local address, no Router Advertisement
// ever arrived" from "got a global address, something else is broken" --
// all three read identically as "nothing shown". These lines print
// every address (any scope) plus the default route, independent of the
// VGA banner and independent of whether DHCP/StaticIP succeeded, since
// SLAAC is the kernel's own doing and needs neither.
func TestBuildRCScriptDiagnosesIPv6State(t *testing.T) {
	dhcpScript := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	for _, want := range []string{
		"for i in 1 2 3 4 5; do ip -6 addr show dev eth0 2>/dev/null | grep -q 'scope global' && break; sleep 1; done",
		`IPV6ADDRS=$(ip -o -6 addr show dev eth0 2>/dev/null | awk '{print $4}' | tr '\n' ' ')`,
		consoleSayCmd + ` "cnimbus: eth0 ipv6 addrs: ${IPV6ADDRS:-(none)}"`,
		`IPV6GW=$(ip -6 route show default 2>/dev/null | head -n1)`,
		consoleSayCmd + ` "cnimbus: eth0 ipv6 default route: ${IPV6GW:-(none)}"`,
	} {
		if !strings.Contains(dhcpScript, want) {
			t.Errorf("missing ipv6 debug line %q in: %q", want, dhcpScript)
		}
	}
	// Must run after udhcpc (order matters for a human reading the
	// console top to bottom) and before the "eth0 network setup done"
	// checkpoint that already brackets everything else about eth0.
	udhcpcIdx := strings.Index(dhcpScript, "udhcpc -i eth0")
	ipv6Idx := strings.Index(dhcpScript, "eth0 ipv6 addrs")
	checkpointIdx := strings.Index(dhcpScript, "eth0 network setup done")
	if udhcpcIdx <= 0 || udhcpcIdx >= ipv6Idx || ipv6Idx >= checkpointIdx {
		t.Errorf("expected udhcpc, then ipv6 diagnostics, then the checkpoint, in that order: udhcpc=%d ipv6=%d checkpoint=%d", udhcpcIdx, ipv6Idx, checkpointIdx)
	}

	staticScript := buildRCScript(PiecesSpec{Hostname: "myvm", StaticIP: &StaticIP{Address: "10.0.0.5", Netmask: "255.255.255.0", Gateway: "10.0.0.1"}})
	if !strings.Contains(staticScript, "eth0 ipv6 addrs") {
		t.Errorf("StaticIP still brings up eth0 -- SLAAC runs regardless of DHCP/StaticIP, so this diagnostic must run for StaticIP too: %q", staticScript)
	}

	noNetScript := buildRCScript(PiecesSpec{Hostname: "myvm"})
	if strings.Contains(noNetScript, "eth0 ipv6") {
		t.Errorf("no networking configured -- there is no eth0 to have an IPv6 state at all: %q", noNetScript)
	}
}

func TestBuildRCScriptPrintsIPv4AndIPv6WhenVGA(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true, VGA: true})
	for _, want := range []string{
		"ip -o -f \"$fam\" addr show scope global",
		"label=IPv6",
		"label=IPv4",
		"cnimbus: $label address:",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected VGA=true to print IPv4/IPv6 addresses via %q, got: %q", want, script)
		}
	}
}

func TestBuildRCScriptNoIPBannerWithoutVGA(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true, VGA: false})
	if strings.Contains(script, "addr show scope global") {
		t.Errorf("VGA=false has no screen to read an IP banner from -- expected it to be skipped: %q", script)
	}
}

// AD-056: a real Proxmox VM's console showed only IPv4 in the VGA
// banner even though `curl -6` reached the VM's global address a
// moment later -- the banner's own inet6 check ran once, immediately,
// with no wait for SLAAC's Router Advertisement to actually land. The
// fix polls up to 10s (matching the ipv6 diagnostic's own bound above)
// before the inet6 branch prints, but must not add any wait to inet:
// IPv4 has already completed DHCP by this point in the script.
func TestBuildRCScriptVGABannerWaitsForGlobalIPv6(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true, VGA: true})
	pollLine := "for i in 1 2 3 4 5; do ip -o -f inet6 addr show scope global 2>/dev/null | grep -q . && break; sleep 1; done"
	if !strings.Contains(script, pollLine) {
		t.Errorf("expected the VGA banner's inet6 branch to poll for a global address before printing: %q", script)
	}
	pollIdx := strings.Index(script, pollLine)
	printIdx := strings.Index(script, "ip -o -f \"$fam\" addr show scope global")
	if pollIdx < 0 || printIdx < 0 || pollIdx > printIdx {
		t.Errorf("expected the poll before the print: poll=%d print=%d", pollIdx, printIdx)
	}
	// The poll must be scoped to the inet6 branch only -- IPv4 already
	// has its address by this point (DHCP/StaticIP ran first), so
	// waiting on it here would be a pure latency regression for no
	// benefit.
	ifIdx := strings.Index(script, `if [ "$fam" = inet6 ]; then`)
	if ifIdx < 0 || ifIdx > pollIdx {
		t.Errorf("expected the poll gated behind an inet6-only check: if=%d poll=%d", ifIdx, pollIdx)
	}
}

func TestBuildRCScriptNoNetworkingNoNTP(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", DHCP: false, NTP: []string{"pool.ntp.org"}}
	script := buildRCScript(spec)
	if strings.Contains(script, "ntpd") {
		t.Errorf("NTP should be skipped with no networking configured: %q", script)
	}
}

func TestBuildRCScriptNTPWhenNetworked(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", DHCP: true, NTP: []string{"1.pool.ntp.org", "2.pool.ntp.org"}}
	script := buildRCScript(spec)
	if !strings.Contains(script, "-p '1.pool.ntp.org' -p '2.pool.ntp.org'") {
		t.Errorf("expected one ntpd call with -p per server: %q", script)
	}
}

func TestBuildRCScriptNTPBackgroundsAfterShortForegroundSync(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", DHCP: true, NTP: []string{"pool.ntp.org"}}
	script := buildRCScript(spec)
	if !strings.Contains(script, "timeout 3 ntpd -q -n -p 'pool.ntp.org'") {
		t.Errorf("expected a short (3s) foreground sync attempt: %q", script)
	}
	if !strings.Contains(script, "(timeout 15 ntpd -q -n -p 'pool.ntp.org' >/dev/null 2>&1 &)") {
		t.Errorf("expected the full-length attempt backgrounded, not blocking sysinit: %q", script)
	}
}

func TestBuildRCScriptNTPSkippedOnStaticIPWithNoDNSAndHostnameServers(t *testing.T) {
	// Same shape as examples/static-ip-firewall: a StaticIP Nimbusfile
	// with no DNS directive means resolv.conf is empty by construction
	// -- an NTP hostname can never resolve, so ntpd shouldn't even run.
	spec := PiecesSpec{
		Hostname: "myvm",
		StaticIP: &StaticIP{Address: "192.168.1.10", Netmask: "255.255.255.0", Gateway: "192.168.1.1"},
		NTP:      []string{"pool.ntp.org"},
	}
	script := buildRCScript(spec)
	if strings.Contains(script, "ntpd") {
		t.Errorf("NTP should be skipped: StaticIP with no DNS directive and a non-IP NTP server can never resolve: %q", script)
	}
}

func TestBuildRCScriptNTPRunsOnStaticIPWithLiteralIPServers(t *testing.T) {
	spec := PiecesSpec{
		Hostname: "myvm",
		StaticIP: &StaticIP{Address: "192.168.1.10", Netmask: "255.255.255.0", Gateway: "192.168.1.1"},
		NTP:      []string{"192.168.1.5"},
	}
	script := buildRCScript(spec)
	if !strings.Contains(script, "ntpd") {
		t.Errorf("NTP should still run: a literal IP NTP server needs no DNS resolution: %q", script)
	}
}

func TestBuildRCScriptNTPSkippedWhenDHCPIgnoredInFavorOfStaticIP(t *testing.T) {
	// examples/static-ip-firewall's own shape: DHCP true is set but
	// ignored because StaticIP wins -- DHCP never actually runs, so it
	// never gets the chance to write resolv.conf either.
	spec := PiecesSpec{
		Hostname: "myvm",
		DHCP:     true,
		StaticIP: &StaticIP{Address: "192.168.1.10", Netmask: "255.255.255.0", Gateway: "192.168.1.1"},
		NTP:      []string{"pool.ntp.org"},
	}
	script := buildRCScript(spec)
	if strings.Contains(script, "ntpd") {
		t.Errorf("NTP should be skipped: DHCP is set but never runs once StaticIP is also set: %q", script)
	}
}

func TestBuildRCScriptVolumeNeverFormats(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", Volumes: []Volume{{Device: "/dev/vda", Mountpoint: "/data"}}}
	script := buildRCScript(spec)
	if strings.Contains(script, "mkfs") {
		t.Errorf("VOLUME must never format the device: %q", script)
	}
	if !strings.Contains(script, "mount -t vfat -o nosuid,nodev '/dev/vda' '/data'") {
		t.Errorf("missing volume mount: %q", script)
	}
}

func TestBuildRCScriptMultipleVolumesAndExt4(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", Volumes: []Volume{
		{Device: "/dev/vda", Mountpoint: "/data"},
		{Device: "/dev/vdb", Mountpoint: "/backup", FSType: "ext4"},
	}}
	script := buildRCScript(spec)
	if !strings.Contains(script, "mount -t vfat -o nosuid,nodev '/dev/vda' '/data'") {
		t.Errorf("missing first (default fstype) volume mount: %q", script)
	}
	if !strings.Contains(script, "mount -t ext4 -o nosuid,nodev '/dev/vdb' '/backup'") {
		t.Errorf("missing second (ext4) volume mount: %q", script)
	}
}

// T93: a required VOLUME that fails to mount must halt boot with a
// FATAL message rather than letting boot continue with a workload
// writing into the read-only SquashFS root (or tmpfs) instead.
func TestBuildRCScriptRequiredVolumeHaltsOnMountFailure(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", Volumes: []Volume{
		{Device: "/dev/vdb", Mountpoint: "/var/lib/data", FSType: "ext4", Required: true},
	}}
	script := buildRCScript(spec)
	if !strings.Contains(script, "if ! mountpoint -q '/var/lib/data'; then") {
		t.Errorf("expected an explicit mountpoint check for the required volume: %q", script)
	}
	if !strings.Contains(script, "cnimbus: FATAL: required volume /dev/vdb at /var/lib/data") {
		t.Errorf("expected a FATAL message naming the device and mountpoint: %q", script)
	}
	// A plain "exit 1" here does NOT halt boot: rcS runs as a
	// "::sysinit:" inittab action, and BusyBox init proceeds to
	// "::respawn:"/"::once:" entries regardless of that action's exit
	// status -- confirmed by a real boot (see Tasks.md's V2 entry) where
	// the FATAL line printed and the entrypoint started anyway. Blocking
	// sysinit forever is what actually works, since BusyBox init does
	// wait for the "::sysinit:" action to *return* before starting any
	// other entry.
	if !strings.Contains(script, "while true; do sleep 3600; done") {
		t.Errorf("expected the required volume's failure branch to block sysinit forever (a real boot showed \"exit 1\" alone does not halt boot): %q", script)
	}
}

// A non-required VOLUME (the default, unchanged from before T93) must
// still log-and-continue rather than gaining an exit path.
func TestBuildRCScriptNonRequiredVolumeStillJustLogs(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", Volumes: []Volume{
		{Device: "/dev/vdb", Mountpoint: "/data"},
	}}
	script := buildRCScript(spec)
	if strings.Contains(script, "FATAL: required volume") {
		t.Errorf("a non-required volume must not gain a FATAL/halt branch: %q", script)
	}
	if !strings.Contains(script, "could not mount /dev/vdb at /data") {
		t.Errorf("expected the original log-and-continue message: %q", script)
	}
}

func TestBuildRCScriptFirewallHookIsConditional(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", Firewall: []string{"-P INPUT DROP"}}
	script := buildRCScript(spec)
	if !strings.Contains(script, "command -v iptables") {
		t.Errorf("firewall script invocation should be gated on iptables actually existing: %q", script)
	}
}

// AD-051: mirrors stage1's own uptime checkpoints, on the post-
// switch_root side -- a real boot can stall between "rcS starting" and
// "rcS finished" for far longer than any VM ever suggested, with no
// visible evidence of which step actually took the time.
func TestBuildRCScriptEmitsUptimeCheckpoints(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	for _, want := range []string{
		"rcS starting (post switch_root)",
		"eth0 network setup done",
		"rcS finished, services starting",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected an uptime checkpoint labeled %q: %q", want, script)
		}
	}
	// The eth0 checkpoint must come after network setup, before rcS
	// finishes -- catches a future edit accidentally moving it out of
	// order, which would make elapsed-time comparisons meaningless.
	ethIdx := strings.Index(script, "eth0 network setup done")
	finishIdx := strings.Index(script, "rcS finished, services starting")
	startIdx := strings.Index(script, "rcS starting (post switch_root)")
	if startIdx < 0 || ethIdx < 0 || finishIdx < 0 || startIdx > ethIdx || ethIdx > finishIdx {
		t.Errorf("expected checkpoints in order start < eth0 < finish: %q", script)
	}
}

func TestBuildRCScriptNoFirewallNoHook(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm"}
	script := buildRCScript(spec)
	if strings.Contains(script, "firewall.sh") {
		t.Errorf("no firewall.sh reference expected with no FIREWALL rules: %q", script)
	}
}

// AD-047: FIREWALL6 mirrors FIREWALL's own hook, gated on ip6tables
// (not iptables) actually being available, invoking a separate
// firewall6.sh so the two rulesets apply (and can fail) independently.
func TestBuildRCScriptFirewall6HookIsConditional(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", Firewall6: []string{"-P INPUT DROP"}}
	script := buildRCScript(spec)
	if !strings.Contains(script, "command -v ip6tables") {
		t.Errorf("firewall6 script invocation should be gated on ip6tables actually existing: %q", script)
	}
	if !strings.Contains(script, "firewall6.sh") {
		t.Errorf("expected a firewall6.sh reference: %q", script)
	}
}

func TestBuildRCScriptNoFirewall6NoHook(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm"}
	script := buildRCScript(spec)
	if strings.Contains(script, "firewall6.sh") {
		t.Errorf("no firewall6.sh reference expected with no FIREWALL6 rules: %q", script)
	}
}

// Firewall and Firewall6 are independent: one present without the other
// must not pull in the other family's hook.
func TestBuildRCScriptFirewallAndFirewall6AreIndependent(t *testing.T) {
	v4Only := buildRCScript(PiecesSpec{Hostname: "myvm", Firewall: []string{"-P INPUT DROP"}})
	if strings.Contains(v4Only, "firewall6.sh") || strings.Contains(v4Only, "ip6tables") {
		t.Errorf("FIREWALL alone should not pull in any ip6tables/firewall6 hook: %q", v4Only)
	}
	v6Only := buildRCScript(PiecesSpec{Hostname: "myvm", Firewall6: []string{"-P INPUT DROP"}})
	if strings.Contains(v6Only, "firewall.sh") || strings.Contains(v6Only, "command -v iptables") {
		t.Errorf("FIREWALL6 alone should not pull in the IPv4 firewall.sh hook: %q", v6Only)
	}
}

func TestBuildRCScriptLogDestination(t *testing.T) {
	noVol := buildRCScript(PiecesSpec{Hostname: "myvm"})
	if !strings.Contains(noVol, "LOGFILE=/dev/console") {
		t.Errorf("expected console log fallback with no volume: %q", noVol)
	}

	withVol := buildRCScript(PiecesSpec{Hostname: "myvm", Volumes: []Volume{{Device: "/dev/vda", Mountpoint: "/data"}}})
	if !strings.Contains(withVol, "LOGFILE='/data'/cnimbus.log") {
		t.Errorf("expected volume-backed log path when mounted: %q", withVol)
	}
}

func TestBuildRCScriptSecondaryNICLoop(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	if !strings.Contains(script, "for i in 1 2 3; do") {
		t.Errorf("expected a loop over secondary NICs: %q", script)
	}
	if !strings.Contains(script, "udhcpc -i eth$i -n -s /sbin/udhcpc-secondary.script &") {
		t.Errorf("expected secondary NICs to be backgrounded via shell &, not blocking boot: %q", script)
	}
}

// T86: a Nimbusfile with DHCP false and no IP -- the documented way to
// declare an image with no networking at all -- must not still run a
// DHCP client on any secondary NIC the hypervisor happens to attach.
func TestBuildRCScriptNoSecondaryNICLoopWithoutNetworking(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: false, StaticIP: nil})
	if strings.Contains(script, "for i in 1 2 3; do") {
		t.Errorf("expected no secondary-NIC loop when neither DHCP nor StaticIP is set: %q", script)
	}
	if strings.Contains(script, "udhcpc-secondary.script") {
		t.Errorf("expected no reference to udhcpc-secondary.script when networking is not declared: %q", script)
	}
}

func TestUDHCPCScriptSecondaryHasNoRouteOrDNS(t *testing.T) {
	if strings.Contains(udhcpcScriptSecondary, "route add default") {
		t.Errorf("secondary NIC script should not add a default route: %q", udhcpcScriptSecondary)
	}
	if strings.Contains(udhcpcScriptSecondary, "resolv.conf") {
		t.Errorf("secondary NIC script should not touch /etc/resolv.conf: %q", udhcpcScriptSecondary)
	}
}

func TestBuildRCScriptResolvConfBindMountAlwaysPresent(t *testing.T) {
	// Regression test: /etc/resolv.conf was never written at all before
	// this bind-mount existed, silently breaking DNS resolution
	// (including the default "NTP pool.ntp.org") on every image.
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	if !strings.Contains(script, "mount --bind /var/run/resolv.conf /etc/resolv.conf") {
		t.Errorf("expected the resolv.conf bind mount to always be set up: %q", script)
	}
}

func TestBuildRCScriptDNSDirectiveWritesResolvConf(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true, DNS: []string{"8.8.8.8", "1.1.1.1"}})
	if !strings.Contains(script, "echo 'nameserver 8.8.8.8' >> /etc/resolv.conf") {
		t.Errorf("missing first DNS server: %q", script)
	}
	if !strings.Contains(script, "echo 'nameserver 1.1.1.1' >> /etc/resolv.conf") {
		t.Errorf("missing second DNS server: %q", script)
	}
}

func TestBuildRCScriptNoDNSDirectiveMeansNoExplicitOverride(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	if strings.Contains(script, "nameserver") {
		t.Errorf("no DNS directive should mean no explicit nameserver lines written by rcS itself (DHCP's own udhcpc.script handles it dynamically): %q", script)
	}
}

func TestUDHCPCScriptWritesResolvConf(t *testing.T) {
	if !strings.Contains(udhcpcScript, `for d in $dns; do echo "nameserver $d" >> /etc/resolv.conf; done`) {
		t.Errorf("udhcpcScript should write /etc/resolv.conf from DHCP-provided $dns: %q", udhcpcScript)
	}
}

func TestUDHCPCScriptAppliesMTU(t *testing.T) {
	if !strings.Contains(udhcpcScript, `ifconfig "$interface" mtu "$mtu"`) {
		t.Errorf("udhcpcScript should apply DHCP-provided $mtu: %q", udhcpcScript)
	}
}

func TestUDHCPCScriptIteratesMultipleRouters(t *testing.T) {
	if !strings.Contains(udhcpcScript, "for gw in $router; do route add default gw \"$gw\" dev \"$interface\"; done") {
		t.Errorf("udhcpcScript should iterate every gateway in a multi-gateway $router lease: %q", udhcpcScript)
	}
}

func TestUDHCPCScriptAppliesStaticRoutes(t *testing.T) {
	if !strings.Contains(udhcpcScript, "route add -net \"$1\" gw \"$2\" dev \"$interface\"") {
		t.Errorf("udhcpcScript should apply DHCP option 121 $staticroutes: %q", udhcpcScript)
	}
}

func TestBuildRCScriptSyslogdRotationConfigured(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm"})
	if !strings.Contains(script, "syslogd -O \"$LOGFILE\" -s 1024 -b 5") {
		t.Errorf("expected syslogd to be given explicit rotation size/count flags: %q", script)
	}
}

func TestBuildRCScriptHostnameQuoted(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "it's a vm"})
	if !strings.Contains(script, `hostname 'it'\''s a vm'`) {
		t.Errorf("hostname not safely quoted: %q", script)
	}
}

// T85: /proc and /sys must mount unconditionally first (cannot fail on
// a kernel this project actually produces), with hidepid=2/hardening
// applied as a separate, non-fatal remount -- so a kernel/config that
// rejects a hardening option loses only that option, never /proc or
// /sys entirely.
func TestBuildRCScriptMountsProcAndSysBeforeHardening(t *testing.T) {
	script := buildRCScript(PiecesSpec{})
	if !strings.Contains(script, "mount -t proc proc /proc\n") {
		t.Errorf("expected an unconditional, unhardened /proc mount: %q", script)
	}
	if !strings.Contains(script, "mount -o remount,hidepid=2") {
		t.Errorf("expected hidepid=2 applied via a separate remount: %q", script)
	}
	if !strings.Contains(script, "mount -t sysfs sysfs /sys\n") {
		t.Errorf("expected an unconditional, unhardened /sys mount: %q", script)
	}
	if !strings.Contains(script, "mount -o remount,nosuid,nodev,noexec /sys") {
		t.Errorf("expected /sys hardening applied via a separate remount: %q", script)
	}
	procIdx := strings.Index(script, "mount -t proc proc /proc\n")
	procRemountIdx := strings.Index(script, "mount -o remount,hidepid=2")
	oomIdx := strings.Index(script, "oom_score_adj")
	if procIdx < 0 || procRemountIdx < 0 || oomIdx < 0 || procIdx > procRemountIdx || procRemountIdx > oomIdx {
		t.Errorf("expected /proc mount, then its hardening remount, then oom_score_adj, in that order: %q", script)
	}
}

// T84: the capped-linear restart backoff must reset after the service
// has run longer than its own current backoff delay -- otherwise a
// crash loop early in a VM's life (backoff saturated at 30s) leaves
// every future, unrelated restart waiting the full 30s forever, with no
// way to observe or reset the counter.
func TestBuildSupervisorScriptBackoffResetsAfterSustainedSuccess(t *testing.T) {
	script := buildSupervisorScript(Service{Name: "x", Argv: []string{"/bin/true"}}, nil, "", "", nil)
	if !strings.Contains(script, "start=$(cut -d. -f1 /proc/uptime)") {
		t.Errorf("expected the run's start time to be captured: %q", script)
	}
	if !strings.Contains(script, `[ "$elapsed" -gt "$d" ] && n=0`) {
		t.Errorf("expected the backoff counter to reset after a sufficiently long run: %q", script)
	}
}

// T83: a wedged workload (deadlocked, or a hung SIGTERM handler) must
// eventually be SIGKILL'd -- SIGTERM alone left the supervisor's
// healthcheck loop spinning forever, re-printing "killing" every
// interval with the process never actually exiting.
func TestBuildSupervisorScriptHealthcheckEscalatesToSigkill(t *testing.T) {
	hc := &Healthcheck{Argv: []string{"/bin/check"}, Interval: "5", Retries: "3"}
	script := buildSupervisorScript(Service{Name: "x", Argv: []string{"/bin/true"}}, nil, "", "", hc)
	if !strings.Contains(script, "killed=0") {
		t.Errorf("expected a killed-state flag initialized: %q", script)
	}
	if !strings.Contains(script, `kill -9 "$pid"`) {
		t.Errorf("expected a SIGKILL escalation path: %q", script)
	}
	sigtermIdx := strings.Index(script, `kill "$pid" 2>/dev/null`)
	sigkillIdx := strings.Index(script, `kill -9 "$pid"`)
	if sigtermIdx < 0 || sigkillIdx < 0 || sigtermIdx > sigkillIdx {
		t.Errorf("expected the SIGTERM attempt to precede the SIGKILL escalation: %q", script)
	}
}

// T82: a healthcheck-tracked service is the one path that already
// backgrounds the real workload command and captures its own PID (not
// a pipe's) -- buildShutdownScript needs that PID recorded to a real
// file so it can signal the process directly at shutdown.
func TestBuildSupervisorScriptWritesPidFileForHealthcheckedService(t *testing.T) {
	hc := &Healthcheck{Argv: []string{"/bin/check"}, Interval: "5", Retries: "3"}
	script := buildSupervisorScript(Service{Name: "entrypoint", Argv: []string{"/bin/true"}}, nil, "", "", hc)
	want := `echo "$pid" > '/var/run/cnimbus-entrypoint.pid'`
	if !strings.Contains(script, want) {
		t.Errorf("expected the healthcheck-tracked PID to be recorded: missing %q in %q", want, script)
	}
	pidIdx := strings.Index(script, `pid=$!`)
	writeIdx := strings.Index(script, want)
	if pidIdx < 0 || writeIdx < 0 || pidIdx > writeIdx {
		t.Errorf("expected the pidfile write to come after pid=$!: %q", script)
	}
}

func TestBuildShutdownScriptSignalsTrackedServicesThenUnmounts(t *testing.T) {
	services := []Service{{Name: "entrypoint"}, {Name: "sidecar"}}
	script := buildShutdownScript(services, 15)
	for _, name := range []string{"entrypoint", "sidecar"} {
		pf := "'/var/run/cnimbus-" + name + ".pid'"
		if !strings.Contains(script, `kill -TERM "$(cat `+pf) {
			t.Errorf("expected a SIGTERM against %s's pidfile: %q", name, script)
		}
		if !strings.Contains(script, `kill -9 "$(cat `+pf) {
			t.Errorf("expected a SIGKILL escalation against %s's pidfile: %q", name, script)
		}
	}
	if !strings.Contains(script, `-lt 15`) {
		t.Errorf("expected the grace loop bounded by the given STOPGRACE value: %q", script)
	}
	if !strings.HasSuffix(strings.TrimRight(script, "\n"), "/bin/umount -a -r") {
		t.Errorf("expected umount -a -r to be the final action: %q", script)
	}
	// SIGTERM (signaling) must precede the umount (which only runs after
	// the grace-period wait loop) -- a script that unmounted first would
	// pull the root out from under a still-shutting-down workload.
	if strings.Index(script, "kill -TERM") > strings.Index(script, "/bin/umount") {
		t.Errorf("expected SIGTERM to precede umount: %q", script)
	}
}

func TestBuildShutdownScriptDefaultsGraceWhenUnset(t *testing.T) {
	script := buildShutdownScript(nil, 0)
	if !strings.Contains(script, fmt.Sprintf("-lt %d", defaultStopGrace)) {
		t.Errorf("expected the default STOPGRACE (%ds) to be used when unset: %q", defaultStopGrace, script)
	}
}

// isValidCPIOHeaderMagic is a lightweight structural check for the
// buildStage1Initramfs codepath's headers -- full symlink/tmpfs
// integration is covered indirectly via internal/rootfs's other tests;
// this focuses on the "newc" cpio header format itself.
func TestCPIOHeaderMagicAndRoundTrip(t *testing.T) {
	tree := newFileTree()
	tree.addDir("etc")
	tree.addFile("etc/hostname", 0o644, []byte("myvm\n"))
	tree.addSymlink("bin/ls", "busybox")

	raw, err := buildCPIO(tree)
	if err != nil {
		t.Fatal(err)
	}

	entries := parseCPIO(t, raw)
	if len(entries) < 4 { // root dir implied via etc's parent(""), etc, etc/hostname, bin, bin/ls, TRAILER
		t.Fatalf("too few cpio entries: %d", len(entries))
	}

	var found struct{ dir, file, symlink, trailer bool }
	for _, e := range entries {
		switch e.name {
		case "etc":
			found.dir = e.mode&0o170000 == 0o040000
		case "etc/hostname":
			found.file = e.mode&0o170000 == 0o100000 && string(e.data) == "myvm\n"
		case "bin/ls":
			found.symlink = e.mode&0o170000 == 0o120000 && string(e.data) == "busybox"
		case "TRAILER!!!":
			found.trailer = true
		}
	}
	if !found.dir {
		t.Error("etc directory entry not found or wrong mode")
	}
	if !found.file {
		t.Error("etc/hostname file entry not found, wrong mode, or wrong content")
	}
	if !found.symlink {
		t.Error("bin/ls symlink entry not found, wrong mode, or wrong target")
	}
	if !found.trailer {
		t.Error("TRAILER!!! entry not found")
	}
}

type cpioEntry struct {
	name string
	mode uint32
	data []byte
}

// parseCPIO is a minimal "newc" cpio reader, independent of buildCPIO's
// own writer, so this test actually exercises the on-disk format rather
// than just calling the writer and trusting it.
func parseCPIO(t *testing.T, raw []byte) []cpioEntry {
	t.Helper()
	var entries []cpioEntry
	off := 0
	for {
		if off+110 > len(raw) {
			t.Fatalf("truncated cpio header at offset %d", off)
		}
		header := string(raw[off : off+110])
		if header[:6] != "070701" {
			t.Fatalf("bad cpio magic at offset %d: %q", off, header[:6])
		}
		field := func(i int) uint32 {
			v, err := strconv.ParseUint(header[6+8*i:6+8*(i+1)], 16, 32)
			if err != nil {
				t.Fatalf("bad hex field: %v", err)
			}
			return uint32(v)
		}
		mode := field(1)
		filesize := field(6)
		namesize := field(11)
		off += 110
		name := string(raw[off : off+int(namesize)-1]) // drop trailing NUL
		off += int(namesize)
		if rem := off % 4; rem != 0 {
			off += 4 - rem
		}
		data := raw[off : off+int(filesize)]
		off += int(filesize)
		if rem := off % 4; rem != 0 {
			off += 4 - rem
		}
		entries = append(entries, cpioEntry{name: name, mode: mode, data: data})
		if name == "TRAILER!!!" {
			break
		}
	}
	return entries
}

func TestFileTreeDedup(t *testing.T) {
	tree := newFileTree()
	tree.addFile("a/b/c", 0o644, []byte("1"))
	tree.addFile("a/b/c", 0o644, []byte("2")) // duplicate path, should be ignored
	count := 0
	for _, e := range tree.entries {
		if e.path == "a/b/c" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one a/b/c entry, got %d", count)
	}
}

func TestFileTreeAutoCreatesParentDirs(t *testing.T) {
	tree := newFileTree()
	tree.addFile("a/b/c", 0o644, []byte("x"))
	paths := map[string]entryKind{}
	for _, e := range tree.entries {
		paths[e.path] = e.kind
	}
	if paths["a"] != entryDir {
		t.Error("expected parent dir 'a' to be auto-created")
	}
	if paths["a/b"] != entryDir {
		t.Error("expected parent dir 'a/b' to be auto-created")
	}
	if paths["a/b/c"] != entryFile {
		t.Error("expected 'a/b/c' to be a file entry")
	}
}

// --- F6.5: WIFI/WIFIPSK/WIFICOUNTRY wiring -------------------------------

func TestBuildWpaSupplicantConfQuotesSSIDAndPassphrase(t *testing.T) {
	conf := buildWpaSupplicantConf("MyNetwork", "supersecret1", "BR")
	if !strings.Contains(conf, `ssid="MyNetwork"`) {
		t.Errorf("expected quoted ssid line: %q", conf)
	}
	if !strings.Contains(conf, `psk="supersecret1"`) {
		t.Errorf("expected quoted passphrase psk line: %q", conf)
	}
	if !strings.Contains(conf, "country=BR") {
		t.Errorf("expected country line: %q", conf)
	}
	if !strings.Contains(conf, "key_mgmt=WPA-PSK") {
		t.Errorf("expected key_mgmt line: %q", conf)
	}
}

func TestBuildWpaSupplicantConfWritesHexPSKBare(t *testing.T) {
	hexPSK := strings.Repeat("ab", 32) // 64 hex chars
	conf := buildWpaSupplicantConf("MyNetwork", hexPSK, "US")
	want := "psk=" + hexPSK + "\n"
	if !strings.Contains(conf, want) {
		t.Errorf("expected a 64-hex-char pre-derived PSK written bare (unquoted): %q", conf)
	}
	if strings.Contains(conf, `psk="`+hexPSK) {
		t.Errorf("a pre-derived hex PSK must not be quoted -- wpa_supplicant would treat it as an (invalid) passphrase: %q", conf)
	}
}

func TestBuildWpaSupplicantConfNoCountryLineWhenEmpty(t *testing.T) {
	conf := buildWpaSupplicantConf("MyNetwork", "supersecret1", "")
	if strings.Contains(conf, "country=") {
		t.Errorf("no country given should mean no country= line: %q", conf)
	}
}

func TestWpaConfQuoteEscapesQuotesAndBackslashes(t *testing.T) {
	// Defense in depth (see buildWpaSupplicantConf's doc comment):
	// internal/nimbusfile already rejects a quote/backslash/newline in
	// WIFI/WIFIPSK before this package ever sees the value, but this
	// function must not corrupt the generated file's syntax even if it
	// somehow received one.
	got := wpaConfQuote(`a"b\c`)
	want := `"a\"b\\c"`
	if got != want {
		t.Errorf("wpaConfQuote(%q) = %q, want %q", `a"b\c`, got, want)
	}
}

func TestBuildRCScriptWifiBringupGatedOnBootProfile(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", DHCP: true, BootProfile: "wifi", WifiSSID: "MyNet", WifiPSK: "supersecret1", WifiCountry: "BR"}
	script := buildRCScript(spec)
	if !strings.Contains(script, "/sys/class/net/wlan0") {
		t.Errorf("HARDBOOT wifi should probe for wlan0: %q", script)
	}
	if !strings.Contains(script, "/usr/sbin/wpa_supplicant -B -i wlan0 -c /usr/sbin/wpa_supplicant.conf") {
		t.Errorf("expected wpa_supplicant invoked by config path, not inline credentials: %q", script)
	}
	if strings.Contains(script, "supersecret1") {
		t.Errorf("the PSK itself must never appear in the generated rcS script (only inside the 0600 conf file): %q", script)
	}
	if !strings.Contains(script, "udhcpc -i wlan0") {
		t.Errorf("expected DHCP to also run against wlan0 once associated: %q", script)
	}
}

// TestBuildRCScriptWifiBringupGatedOnCombinedBootProfile confirms
// hasWifiDriver's gate covers "eth+wifi" too, not just the exact string
// "wifi" -- the combined profile's whole point is both eth0 (always
// emitted, unconditionally) and the wlan0 bring-up block coexisting in
// the same generated rcS script.
func TestBuildRCScriptWifiBringupGatedOnCombinedBootProfile(t *testing.T) {
	spec := PiecesSpec{Hostname: "myvm", DHCP: true, BootProfile: "eth+wifi", WifiSSID: "MyNet", WifiPSK: "supersecret1", WifiCountry: "BR"}
	script := buildRCScript(spec)
	if !strings.Contains(script, "udhcpc -i eth0") {
		t.Errorf("HARDBOOT eth+wifi should still bring up eth0: %q", script)
	}
	if !strings.Contains(script, "/sys/class/net/wlan0") {
		t.Errorf("HARDBOOT eth+wifi should probe for wlan0: %q", script)
	}
	if !strings.Contains(script, "/usr/sbin/wpa_supplicant -B -i wlan0 -c /usr/sbin/wpa_supplicant.conf") {
		t.Errorf("expected wpa_supplicant invoked by config path, not inline credentials: %q", script)
	}
	if strings.Contains(script, "supersecret1") {
		t.Errorf("the PSK itself must never appear in the generated rcS script (only inside the 0600 conf file): %q", script)
	}
}

func TestBuildRCScriptNoWifiBringupWhenProfileIsEth(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true, BootProfile: "eth"})
	if strings.Contains(script, "wlan0") {
		t.Errorf("HARDBOOT eth (no wifi) should emit no wlan0 logic at all: %q", script)
	}
}

func TestBuildRCScriptNoWifiBringupWhenProfileIsNone(t *testing.T) {
	script := buildRCScript(PiecesSpec{Hostname: "myvm", DHCP: true})
	if strings.Contains(script, "wlan0") || strings.Contains(script, "wpa_supplicant") {
		t.Errorf("default (no HARDBOOT) should emit no wifi logic at all -- byte-identical-default guarantee: %q", script)
	}
}

// wpa_supplicant's own PSK never travels as a command-line argument
// anywhere this project generates shell text -- see HB-S-003. This test
// greps every script buildRCScript can produce for the actual
// wpa_supplicant invocation line and asserts it only ever names a path,
// never a bare secret-shaped token.
func TestWifiBringupInvokesSupplicantByConfigPathNotArgv(t *testing.T) {
	var b strings.Builder
	buildWifiBringupScript(&b, PiecesSpec{DHCP: true, WifiSSID: "MyNet", WifiPSK: "topsecretpassphrase", WifiCountry: "US"})
	script := b.String()
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "wpa_supplicant -B") {
			if strings.Contains(line, "topsecretpassphrase") {
				t.Fatalf("PSK leaked into the wpa_supplicant invocation line itself: %q", line)
			}
			if !strings.Contains(line, "-c /usr/sbin/wpa_supplicant.conf") {
				t.Fatalf("expected wpa_supplicant invoked with -c <config path>: %q", line)
			}
		}
	}
}

func TestFileTreeLeadingSlashStripped(t *testing.T) {
	tree := newFileTree()
	tree.addFile("/etc/hostname", 0o644, []byte("x"))
	for _, e := range tree.entries {
		if strings.HasPrefix(e.path, "/") {
			t.Errorf("cpio path must not have a leading slash: %q", e.path)
		}
	}
}
