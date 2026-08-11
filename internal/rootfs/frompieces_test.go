package rootfs

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"testing"
)

// T73: the per-Service supervisor script and the HTTP AGENT script are
// generated 0600 (they carry ENV values, and the agent script an AGENT
// bearer token, as literal shell text). They must travel through stage
// 1's tmpfs shadow-replay path -- where the mode is authored directly
// into the cpio header and re-asserted by an explicit `chmod` at boot,
// both independent of the build host's own filesystem -- rather than
// the SquashFS root, whose writer takes each file's mode from the build
// host (go-diskfs's Chmod/finalize.go), which loses real permission
// bits entirely on Windows.
func TestBuildImagesRoutesSupervisorAndAgentScriptsThroughStage1(t *testing.T) {
	spec := PiecesSpec{
		Hostname:      "x",
		BusyboxBinary: []byte("fake-busybox-bytes"),
		Services:      []Service{{Name: "entrypoint", Argv: []string{"/usr/bin/app"}}},
		Agent:         &Agent{Kind: "http", URL: "http://10.0.2.2:9999/kv", Interval: "5"},
	}
	images, err := BuildImages(spec)
	if err != nil {
		t.Fatalf("BuildImages: %v", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(images.Stage1))
	if err != nil {
		t.Fatalf("gzip.NewReader on Stage1: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompressing Stage1: %v", err)
	}
	entries := parseCPIO(t, raw)

	wantPerm := map[string]uint32{
		"shadow/" + trimLeadingSlash(supervisorScriptPath("entrypoint")): 0o600,
		"shadow/" + trimLeadingSlash(agentScriptPath):                    0o600,
	}
	found := map[string]bool{}
	var initScript string
	for _, e := range entries {
		if e.name == "init" {
			initScript = string(e.data)
		}
		if want, ok := wantPerm[e.name]; ok {
			found[e.name] = true
			if perm := e.mode & 0o7777; perm != want {
				t.Errorf("%s: cpio-encoded mode = %o, want %o", e.name, perm, want)
			}
		}
	}
	for name := range wantPerm {
		if !found[name] {
			t.Errorf("expected %s staged in stage 1's initramfs, not found among: %v", name, entryNames(entries))
		}
	}
	if initScript == "" {
		t.Fatal("no /init entry found in stage 1 cpio")
	}
	for path, perm := range map[string]uint32{
		trimLeadingSlash(supervisorScriptPath("entrypoint")): 0o600,
		trimLeadingSlash(agentScriptPath):                    0o600,
	} {
		want := fmt.Sprintf("chmod %o '/mnt/root/%s'", perm, path)
		if !strings.Contains(initScript, want) {
			t.Errorf("expected /init to explicitly chmod %s at boot, missing %q", path, want)
		}
	}
}

// F6.5/HB-S-001: wpa_supplicant.conf carries the PSK in plain text and
// must be 0600 in the produced image, via the same real-chmod stage-1
// shadow path as the supervisor/agent scripts above -- not the SquashFS
// root, whose mode fidelity depends on the build host's filesystem.
func TestBuildImagesRoutesWpaSupplicantConfThroughStage1At0600(t *testing.T) {
	spec := PiecesSpec{
		Hostname:      "x",
		BusyboxBinary: []byte("fake-busybox-bytes"),
		BootProfile:   "wifi",
		WifiSSID:      "MyNetwork",
		WifiPSK:       "supersecretpassphrase",
		WifiCountry:   "BR",
		Supplicant:    []byte("fake-wpa-supplicant-elf-bytes"),
	}
	images, err := BuildImages(spec)
	if err != nil {
		t.Fatalf("BuildImages: %v", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(images.Stage1))
	if err != nil {
		t.Fatalf("gzip.NewReader on Stage1: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompressing Stage1: %v", err)
	}
	entries := parseCPIO(t, raw)

	const shadowConf = "shadow/usr/sbin/wpa_supplicant.conf"
	var confData []byte
	var initScript string
	found := false
	for _, e := range entries {
		if e.name == "init" {
			initScript = string(e.data)
		}
		if e.name == shadowConf {
			found = true
			confData = e.data
			if perm := e.mode & 0o7777; perm != 0o600 {
				t.Errorf("%s: cpio-encoded mode = %o, want 0600", shadowConf, perm)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s staged in stage 1's initramfs, not found among: %v", shadowConf, entryNames(entries))
	}
	if !strings.Contains(string(confData), `ssid="MyNetwork"`) {
		t.Errorf("staged wpa_supplicant.conf missing expected ssid line: %q", confData)
	}
	if !strings.Contains(string(confData), "supersecretpassphrase") {
		t.Errorf("staged wpa_supplicant.conf missing the PSK -- this is the one place it's expected to appear: %q", confData)
	}

	wantChmod := "chmod 600 '/mnt/root/usr/sbin/wpa_supplicant.conf'"
	if !strings.Contains(initScript, wantChmod) {
		t.Errorf("expected /init to explicitly chmod wpa_supplicant.conf 600 at boot, missing %q in: %q", wantChmod, initScript)
	}
}

// TestBuildImagesRoutesWpaSupplicantConfThroughStage1ForEthPlusWifi
// mirrors TestBuildImagesRoutesWpaSupplicantConfThroughStage1At0600
// exactly, just for BootProfile "eth+wifi" -- confirming the same 0600
// staging happens for the combined profile, not just the exact string
// "wifi".
func TestBuildImagesRoutesWpaSupplicantConfThroughStage1ForEthPlusWifi(t *testing.T) {
	spec := PiecesSpec{
		Hostname:      "x",
		BusyboxBinary: []byte("fake-busybox-bytes"),
		BootProfile:   "eth+wifi",
		WifiSSID:      "MyNetwork",
		WifiPSK:       "supersecretpassphrase",
		WifiCountry:   "BR",
		Supplicant:    []byte("fake-wpa-supplicant-elf-bytes"),
	}
	images, err := BuildImages(spec)
	if err != nil {
		t.Fatalf("BuildImages: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(images.Stage1))
	if err != nil {
		t.Fatalf("gzip.NewReader on Stage1: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompressing Stage1: %v", err)
	}
	entries := parseCPIO(t, raw)
	const shadowConf = "shadow/usr/sbin/wpa_supplicant.conf"
	found := false
	for _, e := range entries {
		if e.name == shadowConf {
			found = true
			if perm := e.mode & 0o7777; perm != 0o600 {
				t.Errorf("%s: cpio-encoded mode = %o, want 0600", shadowConf, perm)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s staged in stage 1's initramfs for BootProfile=eth+wifi, not found among: %v", shadowConf, entryNames(entries))
	}
}

// A "none"/"eth" profile pieces set never carries wpa_supplicant.conf at
// all, even if Supplicant somehow ended up non-nil (shouldn't happen in
// practice -- pieces.Resolve only ever sets Supplicant for a "wifi"
// pieces set -- but this is the guard that actually enforces it here).
func TestBuildImagesNoWpaSupplicantConfWhenProfileNotWifi(t *testing.T) {
	spec := PiecesSpec{
		Hostname:      "x",
		BusyboxBinary: []byte("fake-busybox-bytes"),
		BootProfile:   "eth",
		Supplicant:    []byte("fake-wpa-supplicant-elf-bytes"),
	}
	images, err := BuildImages(spec)
	if err != nil {
		t.Fatalf("BuildImages: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(images.Stage1))
	if err != nil {
		t.Fatalf("gzip.NewReader on Stage1: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompressing Stage1: %v", err)
	}
	entries := parseCPIO(t, raw)
	for _, e := range entries {
		if e.name == "shadow/usr/sbin/wpa_supplicant.conf" {
			t.Fatalf("did not expect wpa_supplicant.conf staged for a non-wifi BootProfile")
		}
	}
}

func entryNames(entries []cpioEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name
	}
	return names
}

// AD-057: WiFi and Ethernet firmware share one stage-1 embedding
// mechanism -- see BuildImages's own comment on mergeFirmwareMaps.
func TestMergeFirmwareMaps(t *testing.T) {
	wifi := map[string][]byte{"ath9k_htc/htc_9271-1.4.0.fw": []byte("wifi-blob")}
	ethernet := map[string][]byte{"rtl_nic/rtl8168h-2.fw": []byte("eth-blob")}

	if got := mergeFirmwareMaps(nil, nil); got != nil {
		t.Errorf("mergeFirmwareMaps(nil, nil) = %v, want nil", got)
	}
	if got := mergeFirmwareMaps(wifi, nil); len(got) != 1 || string(got["ath9k_htc/htc_9271-1.4.0.fw"]) != "wifi-blob" {
		t.Errorf("mergeFirmwareMaps(wifi, nil) = %v, want just wifi's own entry", got)
	}
	if got := mergeFirmwareMaps(nil, ethernet); len(got) != 1 || string(got["rtl_nic/rtl8168h-2.fw"]) != "eth-blob" {
		t.Errorf("mergeFirmwareMaps(nil, ethernet) = %v, want just ethernet's own entry", got)
	}
	merged := mergeFirmwareMaps(wifi, ethernet)
	if len(merged) != 2 {
		t.Fatalf("mergeFirmwareMaps(wifi, ethernet): expected both entries, got %v", merged)
	}
	if string(merged["ath9k_htc/htc_9271-1.4.0.fw"]) != "wifi-blob" || string(merged["rtl_nic/rtl8168h-2.fw"]) != "eth-blob" {
		t.Errorf("mergeFirmwareMaps(wifi, ethernet) = %v, want both entries intact", merged)
	}
}

// AD-057: a HARDBOOT eth build's Ethernet firmware must reach stage 1's
// initramfs the same way WiFi firmware already does -- at exactly
// "lib/firmware/"+path, readable by request_firmware() before
// switch_root ever runs (see stage1.go's own doc comment on why WiFi
// firmware isn't staged through the tmpfs-shadow/SquashFS split like
// everything else).
func TestBuildImagesEmbedsEthernetFirmwareInStage1(t *testing.T) {
	spec := PiecesSpec{
		Hostname:         "x",
		BusyboxBinary:    []byte("fake-busybox-bytes"),
		EthernetFirmware: map[string][]byte{"rtl_nic/rtl8168h-2.fw": []byte("fake-firmware-bytes")},
	}
	images, err := BuildImages(spec)
	if err != nil {
		t.Fatalf("BuildImages: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(images.Stage1))
	if err != nil {
		t.Fatalf("gzip.NewReader on Stage1: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decompressing Stage1: %v", err)
	}
	entries := parseCPIO(t, raw)
	var found bool
	for _, e := range entries {
		if e.name == "lib/firmware/rtl_nic/rtl8168h-2.fw" {
			found = true
			if string(e.data) != "fake-firmware-bytes" {
				t.Errorf("firmware content mismatch: got %q", e.data)
			}
		}
	}
	if !found {
		t.Errorf("expected lib/firmware/rtl_nic/rtl8168h-2.fw staged in stage 1, not found among: %v", entryNames(entries))
	}
}
