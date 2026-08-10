package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// hypervNet* fix the private, cnimbus-owned network the Hyper-V backend
// puts a --hostfwd guest on. Design notes, all established empirically
// against a real Hyper-V host (see ROADMAP.md for the full trail):
//
//   - A "SwitchType Internal" switch puts the host's own vEthernet
//     adapter on the same L2 segment as the guest, so the host can open
//     connections *to* the guest directly. That's the same shape as
//     VirtualBox's host-only networking, and the reason this backend
//     doesn't try to reuse Windows' built-in "Default Switch": a
//     Default-Switch guest gets its address from Windows' own DHCP with
//     no way to learn it back on the host, since this image runs no
//     KVP/Data-Exchange guest daemon (no shell, no userland beyond
//     ENTRYPOINT/CMD/SERVICE) and Get-VMNetworkAdapter's IPAddresses
//     property therefore stays {NoContact} even on a healthy boot.
//   - Guessing that address from the host's ARP table was tried first
//     and is genuinely unsafe: Hyper-V allocates guest MACs from a
//     shared 00-15-5D-* pool and reuses them, so an unscoped
//     Get-NetNeighbor MAC match can return a stale entry belonging to a
//     completely unrelated network. It did exactly that on the
//     development machine -- returning a VMware VMnet8 NAT address for
//     a Hyper-V guest -- and the bogus forwarding rule built on it then
//     squatted the host port, breaking the mapping in a way that looked
//     like a guest-side failure. Hence: a fixed subnet plus a static
//     guest IP, nothing inferred at runtime.
//
// The tradeoff of an Internal switch is that it has no DHCP server (and
// Windows client SKUs can't add the DHCP Server role), so a --hostfwd
// guest's Nimbusfile must set a static IP matching this convention.
// That mirrors the constraint examples/static-ip-firewall already
// documents for VirtualBox host-only networking. Plain DHCP still works
// under --backend hyperv without --hostfwd, via whatever switch the
// host already has.
const (
	hypervSwitch  = "cnimbus-nat"
	hypervHostIP  = "192.168.200.1"
	hypervGuestIP = "192.168.200.10"
	hypervPrefix  = "192.168.200.0/24"
)

// hypervBackend implements backend by scripting the Hyper-V PowerShell
// module (New-VM, Set-VMDvdDrive/Add-VMHardDiskDrive, Start-VM, ...) to
// boot an ISO or a FORMAT raw image headless, then -- for --hostfwd --
// relaying a host port to the guest and verifying the relay actually
// answers before returning.
type hypervBackend struct{}

func (hypervBackend) name() string { return "hyperv" }

// available checks for powershell.exe on PATH -- the one precondition
// this backend can check without attempting a real launch. Whether the
// Hyper-V module itself is installed, and whether this process is
// elevated, can only be determined by actually running the script (see
// launch's own elevation check), so available deliberately does not try
// to replicate that here.
func (hypervBackend) available() error {
	_, err := exec.LookPath("powershell.exe")
	if err != nil {
		return fmt.Errorf("powershell.exe not found on PATH: %w", err)
	}
	return nil
}

// launch is amd64 only (Hyper-V on this kind of host targets amd64
// guests). A FORMAT raw image is wrapped into a throwaway Fixed VHD first
// (see run_vhd.go's writeFixedVHD) -- Hyper-V's virtual disk formats
// (VHD/VHDX) are structured containers, not a place a raw disk image's
// bytes can simply be pointed at the way VMware's flat-VMDK trick (see
// run_vmdk.go) or QEMU's/VirtualBox's own raw-file support allow. A
// "Fixed" VHD *is* just raw bytes plus a 512-byte trailing footer, which
// is exactly what the wrapper produces.
func (hypervBackend) launch(spec launchSpec) error {
	imagePath, isISO, arch, uefi := spec.imagePath, spec.isISO, spec.arch, spec.uefi
	memMB := spec.memMB
	hostfwd, hostfwdBind, vmName, keep := spec.hostfwd, spec.hostfwdBind, spec.vmName, spec.keep

	if arch != "amd64" {
		return fmt.Errorf("--backend hyperv only supports --arch amd64")
	}

	generation := 1
	if uefi {
		generation = 2
	}

	// For a raw image, wrap it into a throwaway Fixed VHD in its own temp
	// dir -- mirroring runViaVMware's workDir/writeFlatVMDK pattern
	// exactly, down to the --vm-keep-controlled cleanup below. The VHD
	// path fed to Hyper-V is always this wrapper, never imagePath itself:
	// writeFixedVHD never modifies its source, and a raw image someone
	// might reuse via --backend qemu/vbox/vmware right after must stay
	// exactly as `cnimbus build-disk` produced it.
	diskPath := imagePath
	var workDir string
	// Set true only once Start-VM itself has actually succeeded (see the
	// runErr check far below) -- a real Hyper-V boot (2026-08-07) showed
	// the naive "always os.RemoveAll on return" version failing every
	// single time on this exact path: Start-VM leaves the VHD attached
	// to a live, still-running VM, so the OS keeps the file open and
	// RemoveAll always errors with "the file is being used by another
	// process" -- silently logged, never actually cleaned up, on every
	// successful run. Mirrors runViaVMware's own workDir handling, which
	// never attempts automatic cleanup after a successful vmrun start
	// for the same reason.
	vmStarted := false
	if !isISO {
		var err error
		workDir, err = os.MkdirTemp("", "cnimbus-hyperv-*")
		if err != nil {
			return fmt.Errorf("creating Hyper-V work directory: %w", err)
		}
		defer func() {
			if keep {
				fmt.Printf("leaving VHD wrapper in place (--vm-keep): %s\n", workDir)
				return
			}
			if vmStarted {
				fmt.Printf("VM still running with %s attached -- remove it yourself once stopped:\n  rmdir /s /q %q\n", workDir, workDir)
				return
			}
			if err := os.RemoveAll(workDir); err != nil {
				fmt.Printf("cleanup: could not remove %s: %v\n", workDir, err)
			}
		}()
		diskPath = filepath.Join(workDir, "disk.vhd")
		if err := writeFixedVHD(diskPath, imagePath); err != nil {
			return fmt.Errorf("wrapping %s into a Fixed VHD: %w", imagePath, err)
		}
	}

	hostPort, guestPort, forward := splitHostfwd(hostfwd)
	// A socket can't connect *to* 0.0.0.0 as a destination -- when that's
	// the configured bind (deliberately wide), verify against loopback
	// instead, which a 0.0.0.0-bound listener still always accepts.
	verifyAddr := hostfwdBind
	if verifyAddr == "0.0.0.0" {
		verifyAddr = "127.0.0.1"
	}

	var s strings.Builder
	fmt.Fprintf(&s, "$ErrorActionPreference = 'Stop'\n")
	// Every Hyper-V and netsh call below needs an elevated token; failing
	// here with one clear line beats a wall of individual access-denied
	// errors from each cmdlet in turn.
	fmt.Fprintf(&s, "if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {\n")
	fmt.Fprintf(&s, "  throw 'this PowerShell session is not elevated -- re-run from an Administrator prompt'\n")
	fmt.Fprintf(&s, "}\n")

	if forward {
		fmt.Fprintf(&s, "Write-Host 'cnimbus: preparing switch %s (%s)...'\n", hypervSwitch, hypervPrefix)
		fmt.Fprintf(&s, "if (-not (Get-VMSwitch -Name %s -ErrorAction SilentlyContinue)) {\n", psQuote(hypervSwitch))
		fmt.Fprintf(&s, "  New-VMSwitch -SwitchName %s -SwitchType Internal | Out-Null\n", psQuote(hypervSwitch))
		fmt.Fprintf(&s, "}\n")
		fmt.Fprintf(&s, "$ifIdx = (Get-NetAdapter -Name %s).ifIndex\n", psQuote("vEthernet ("+hypervSwitch+")"))
		fmt.Fprintf(&s, "if (-not (Get-NetIPAddress -InterfaceIndex $ifIdx -IPAddress %s -ErrorAction SilentlyContinue)) {\n", hypervHostIP)
		fmt.Fprintf(&s, "  New-NetIPAddress -InterfaceIndex $ifIdx -IPAddress %s -PrefixLength 24 | Out-Null\n", hypervHostIP)
		fmt.Fprintf(&s, "}\n")
		// NAT here is only so the guest can reach *outward* (its own NTP,
		// an AGENT http URL, ...). Inbound host->guest does not go through
		// it at all -- see the portproxy block below for why.
		fmt.Fprintf(&s, "if (-not (Get-NetNat -Name %s -ErrorAction SilentlyContinue)) {\n", psQuote(hypervSwitch))
		fmt.Fprintf(&s, "  New-NetNat -Name %s -InternalIPInterfaceAddressPrefix %s | Out-Null\n", psQuote(hypervSwitch), hypervPrefix)
		fmt.Fprintf(&s, "}\n")
	}

	fmt.Fprintf(&s, "Write-Host 'cnimbus: creating VM...'\n")
	fmt.Fprintf(&s, "New-VM -Name %s -MemoryStartupBytes %dMB -Generation %d -NoVHD | Out-Null\n",
		psQuote(vmName), memMB, generation)
	switch {
	case generation == 2 && isISO:
		// This kernel/bootloader isn't signed with a cert Hyper-V's
		// Generation 2 Secure Boot trusts by default -- the same reason
		// UEFI Secure Boot itself is out of scope for cnimbus.
		fmt.Fprintf(&s, "Set-VMFirmware -VMName %s -EnableSecureBoot Off\n", psQuote(vmName))
		fmt.Fprintf(&s, "Set-VMDvdDrive -VMName %s -Path %s\n", psQuote(vmName), psQuote(imagePath))
		fmt.Fprintf(&s, "Set-VMFirmware -VMName %s -FirstBootDevice (Get-VMDvdDrive -VMName %s)\n", psQuote(vmName), psQuote(vmName))
	case generation == 2 && !isISO:
		fmt.Fprintf(&s, "Set-VMFirmware -VMName %s -EnableSecureBoot Off\n", psQuote(vmName))
		fmt.Fprintf(&s, "Add-VMHardDiskDrive -VMName %s -Path %s\n", psQuote(vmName), psQuote(diskPath))
		fmt.Fprintf(&s, "Set-VMFirmware -VMName %s -FirstBootDevice (Get-VMHardDiskDrive -VMName %s)\n", psQuote(vmName), psQuote(vmName))
	case generation == 1 && isISO:
		fmt.Fprintf(&s, "Set-VMDvdDrive -VMName %s -ControllerNumber 1 -ControllerLocation 0 -Path %s\n",
			psQuote(vmName), psQuote(imagePath))
		fmt.Fprintf(&s, "Set-VMBios -VMName %s -StartupOrder @('CD','IDE','LegacyNetworkAdapter','Floppy')\n", psQuote(vmName))
	default: // generation == 1 && !isISO
		fmt.Fprintf(&s, "Add-VMHardDiskDrive -VMName %s -ControllerType IDE -ControllerNumber 0 -ControllerLocation 0 -Path %s\n",
			psQuote(vmName), psQuote(diskPath))
		fmt.Fprintf(&s, "Set-VMBios -VMName %s -StartupOrder @('IDE','CD','LegacyNetworkAdapter','Floppy')\n", psQuote(vmName))
	}
	if forward {
		fmt.Fprintf(&s, "Connect-VMNetworkAdapter -VMName %s -SwitchName %s\n", psQuote(vmName), psQuote(hypervSwitch))
	} else {
		fmt.Fprintf(&s, "Connect-VMNetworkAdapter -VMName %s -SwitchName (Get-VMSwitch | Select-Object -First 1 -ExpandProperty Name) -ErrorAction SilentlyContinue\n", psQuote(vmName))
	}
	fmt.Fprintf(&s, "Start-VM -Name %s\n", psQuote(vmName))
	fmt.Fprintf(&s, "Write-Host 'cnimbus: VM started.'\n")

	if forward {
		// netsh portproxy -- a userspace TCP relay -- not a NetNat static
		// mapping: WinNAT maps inbound traffic arriving on the host's
		// *external* addresses, which is not what "curl 127.0.0.1:PORT"
		// is. portproxy listens locally and dials the guest itself, so
		// loopback works, which is the whole point of --hostfwd matching
		// QEMU's and VirtualBox's behavior -- listenaddress is now
		// hostfwdBind (127.0.0.1 by default, matching that intent for
		// real; a previous revision hardcoded 0.0.0.0 here despite this
		// very comment already describing loopback as the goal).
		//
		// The delete-before-add is load-bearing, not defensive tidying: a
		// leftover rule on this port silently wins over the new one (it
		// accepts the TCP connection, then hangs forwarding to whatever
		// stale address it still holds), which is exactly how an earlier
		// bad rule made a healthy guest look dead. Both possible bind
		// addresses are cleared, regardless of which one is used this
		// time, so a rule left over from a previous --hostfwd-bind value
		// can never linger and win over the new one either.
		fmt.Fprintf(&s, "Write-Host 'cnimbus: forwarding %s:%s -> %s:%s...'\n", hostfwdBind, hostPort, hypervGuestIP, guestPort)
		fmt.Fprintf(&s, "if ((Get-Service iphlpsvc).Status -ne 'Running') { Start-Service iphlpsvc }\n")
		fmt.Fprintf(&s, "netsh interface portproxy delete v4tov4 listenport=%s listenaddress=0.0.0.0 2>$null | Out-Null\n", hostPort)
		fmt.Fprintf(&s, "netsh interface portproxy delete v4tov4 listenport=%s listenaddress=127.0.0.1 2>$null | Out-Null\n", hostPort)
		// Also drop the NAT static mappings an earlier revision of this
		// backend created on this port; they bind the same port and make
		// which one serves a connection ambiguous.
		fmt.Fprintf(&s, "Get-NetNatStaticMapping -ErrorAction SilentlyContinue | Where-Object { $_.ExternalPort -eq %s } | ForEach-Object { Remove-NetNatStaticMapping -NatName $_.NatName -StaticMappingID $_.StaticMappingID -Confirm:$false }\n", hostPort)
		fmt.Fprintf(&s, "netsh interface portproxy add v4tov4 listenport=%s listenaddress=%s connectport=%s connectaddress=%s | Out-Null\n",
			hostPort, hostfwdBind, guestPort, hypervGuestIP)

		// Self-verify. Without this the command's own success only ever
		// proved "Hyper-V accepted my configuration", never "the guest
		// answers" -- and the difference between those two is precisely
		// where every bug in this backend has lived.
		// Probes by sending a request and requiring bytes *back*, not by
		// connecting. A TCP connect proves nothing here: netsh portproxy
		// is itself the listener, so it completes the handshake locally
		// and only then tries to reach the guest -- a dead guest yields a
		// connect that succeeds and a read that hangs. An earlier
		// revision checked only the connect and duly reported success
		// against a guest with no network device at all.
		fmt.Fprintf(&s, "Write-Host 'cnimbus: waiting for the guest to answer on %s:%s (up to 90s)...'\n", verifyAddr, hostPort)
		fmt.Fprintf(&s, "$answered = $false\n")
		fmt.Fprintf(&s, "for ($i = 0; $i -lt 30; $i++) {\n")
		fmt.Fprintf(&s, "  try {\n")
		fmt.Fprintf(&s, "    $c = New-Object System.Net.Sockets.TcpClient\n")
		fmt.Fprintf(&s, "    $h = $c.BeginConnect('%s', %s, $null, $null)\n", verifyAddr, hostPort)
		fmt.Fprintf(&s, "    if ($h.AsyncWaitHandle.WaitOne(2000)) {\n")
		fmt.Fprintf(&s, "      $c.EndConnect($h)\n")
		fmt.Fprintf(&s, "      $c.ReceiveTimeout = 3000; $c.SendTimeout = 3000\n")
		fmt.Fprintf(&s, "      $st = $c.GetStream()\n")
		fmt.Fprintf(&s, "      $req = [Text.Encoding]::ASCII.GetBytes(\"GET / HTTP/1.0`r`nHost: %s`r`n`r`n\")\n", verifyAddr)
		fmt.Fprintf(&s, "      $st.Write($req, 0, $req.Length); $st.Flush()\n")
		fmt.Fprintf(&s, "      $buf = New-Object byte[] 64\n")
		fmt.Fprintf(&s, "      if ($st.Read($buf, 0, 64) -gt 0) { $answered = $true }\n")
		fmt.Fprintf(&s, "    }\n")
		fmt.Fprintf(&s, "    $c.Close()\n")
		fmt.Fprintf(&s, "  } catch { }\n")
		fmt.Fprintf(&s, "  if ($answered) { break }\n")
		fmt.Fprintf(&s, "  Start-Sleep -Seconds 1\n")
		fmt.Fprintf(&s, "}\n")
		fmt.Fprintf(&s, "if ($answered) { Write-Output 'CNIMBUS_FWD_OK' } else {\n")
		fmt.Fprintf(&s, "  Write-Output 'CNIMBUS_FWD_FAIL'\n")
		fmt.Fprintf(&s, "  Write-Output ('  guest ARP state: ' + ((Get-NetNeighbor -IPAddress %s -InterfaceIndex $ifIdx -ErrorAction SilentlyContinue).State -join ','))\n", hypervGuestIP)
		fmt.Fprintf(&s, "  Write-Output ('  portproxy: ' + ((netsh interface portproxy show all | Out-String).Trim()))\n")
		fmt.Fprintf(&s, "}\n")
	}

	fmt.Printf("creating and starting Hyper-V VM %q (Generation %d)...\n", vmName, generation)
	fmt.Println("(requires an elevated/Administrator PowerShell and the Hyper-V module)")

	// Streamed, not buffered: an earlier revision used CombinedOutput(),
	// which printed nothing until the whole script finished, so a healthy
	// multi-second run was indistinguishable from a hang. The timeout is
	// a backstop for a cmdlet blocking on a prompt -NonInteractive can
	// never answer.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// #nosec G204 -- script is built entirely from this function's own
	// fixed template plus psQuote'd flag-derived values.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", s.String())
	var captured strings.Builder
	cmd.Stdout = teeWriter{os.Stdout, &captured}
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	vmStarted = runErr == nil && ctx.Err() != context.DeadlineExceeded

	cleanupVM := func() {
		c := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("Stop-VM -Name %s -TurnOff -ErrorAction SilentlyContinue; Remove-VM -Name %s -Force -ErrorAction SilentlyContinue",
				psQuote(vmName), psQuote(vmName)))
		_ = c.Run()
	}
	if ctx.Err() == context.DeadlineExceeded {
		cleanupVM()
		return fmt.Errorf("hyper-v setup timed out after 3m -- VM cleaned up")
	}
	if runErr != nil {
		cleanupVM()
		return fmt.Errorf("hyper-v setup failed: %w", runErr)
	}

	if forward {
		switch {
		case strings.Contains(captured.String(), "CNIMBUS_FWD_OK"):
			fmt.Printf("\nforwarding verified: the guest answered on %s:%s\n", verifyAddr, hostPort)
			fmt.Printf("  curl http://%s:%s/\n", verifyAddr, hostPort)
		case strings.Contains(captured.String(), "CNIMBUS_FWD_FAIL"):
			fmt.Printf("\nthe guest never answered on %s:%s (diagnostics above).\n", verifyAddr, hostPort)
			fmt.Printf("check the guest's own console -- this needs %q in its Nimbusfile:\n  IP %s 255.255.255.0 %s\n",
				"IP "+hypervGuestIP, hypervGuestIP, hypervHostIP)
			fmt.Printf("  vmconnect.exe localhost %s\n", vmName)
		}
		fmt.Printf("remove the forwarding rule with:\n  netsh interface portproxy delete v4tov4 listenport=%s listenaddress=%s\n", hostPort, hostfwdBind)
	}

	fmt.Printf("VM running. Stop it with:\n  Stop-VM -Name '%s' -TurnOff\n", vmName)
	if keep {
		fmt.Println("VM left registered (--vm-keep)")
	} else {
		fmt.Printf("Remove it afterward with:\n  Remove-VM -Name '%s' -Force\n", vmName)
	}
	return nil
}

// teeWriter forwards writes to a live stream and a capture buffer at
// once, so the caller can both show progress as it happens and still
// inspect the output afterward for the script's own status markers.
type teeWriter struct {
	live    *os.File
	capture *strings.Builder
}

func (t teeWriter) Write(p []byte) (int, error) {
	t.capture.Write(p)
	return t.live.Write(p)
}

// psQuote wraps s in single quotes for safe use as one PowerShell
// string literal, doubling any single quote it contains (PowerShell's
// own escaping rule inside single-quoted strings).
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
