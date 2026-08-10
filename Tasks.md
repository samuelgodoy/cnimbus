# Tasks — active backlog

This is the single task-tracking file for cNimbus. It merges what used
to be split across an active `Tasks.md` and an archived
`Tasks-history-2026-07.md` (T1–T105, all closed) — the archive is gone;
its full implementation detail (exact Kconfig chains, flags that didn't
work as documented, real boot-log excerpts) lives in git history, and
architectural decisions specifically are logged in
[`.specs/project/STATE.md`](.specs/project/STATE.md)'s AD log.

This is the granular layer behind
[`.specs/project/ROADMAP.md`](.specs/project/ROADMAP.md)'s milestone-level
v1.0 roadmap (M1–M7) and the product-direction decisions in
`.specs/project/STATE.md`. **v1.0 is defined as this file reaching zero
open items** — when that happens, stop and ask the user what's next
rather than self-directing further.

Severity: 🔴 critical · 🟠 high · 🟡 medium · ⚪ low/info.
Effort: S (<1h) · M (a session) · L (multi-session) · XL (own milestone,
see [ROADMAP.md](.specs/project/ROADMAP.md)).

## Status snapshot

- **All boot-validation debt (V1–V6) and almost everything else is
  closed** — 105 archived tickets (T1–T105) plus V1–V6, F1–F5, F7, F10
  and most of F6/F8/F9 below.
- **Everything still open is the same kind of gap: real physical
  hardware this dev machine doesn't have**, not unfinished code — a
  physical USB/BIOS boot (F6.1), a real WiFi radio associating (F6.3), a
  real macOS/Windows-on-Arm host (F8), or a deliberate human action
  (pushing the `v0.1.0` release tag, F9).
- GitHub-hosted CI stays build-only by standing product decision; a
  local self-hosted runner doing real boots is an optional future item
  (Section C), not required for v1.0.

## A. Open items

- [~] **F6** 🟠 L — HARDBOOT bare-metal profile (`HARDBOOT
  none|eth|wifi|eth+wifi`). Fully specified in
  [`.specs/features/hardboot-baremetal/spec.md`](.specs/features/hardboot-baremetal/spec.md)
  and [design.md](.specs/features/hardboot-baremetal/design.md). Every
  sub-item is code-complete and verified by real builds and direct image
  inspection; two claims remain pending real physical hardware:
  - [~] **F6.1** — *Physical USB/bare-metal boot.* QEMU/VirtualBox
    e1000/e1000e pre-checks (driver probe, DHCP lease) already passed for
    real. Concrete checklist to close this for real:
    1. `cnimbus prepare --hardboot eth` (amd64), then `cnimbus build-disk
       --pieces ./pieces --format raw -o hardboot-eth.img` — **`--format
       raw`, not `iso`**: a plain ISO has no MBR boot code and won't boot
       a physical BIOS/legacy USB stick; `FORMAT raw`'s GPT+ESP layout is
       UEFI-only by design, so the target machine's firmware must be able
       to boot UEFI from USB.
    2. Write the raw image to a USB stick: Rufus in "DD image" mode on
       Windows, or `sudo dd if=hardboot-eth.img of=/dev/sdX bs=4M
       status=progress conv=fsync` on Linux/macOS (confirm the target
       device with `lsblk`/`diskutil list` first — this overwrites the
       whole device with no confirmation prompt).
    3. Boot the physical machine from the USB stick (UEFI boot menu /
       one-time boot override) with a wired Ethernet cable connected to a
       network with a DHCP server.
    4. Watch the serial console (`ttyS0`); pass `--vga` to `prepare` and
       rebuild first if the target machine has no serial adapter and
       needs to be watched on its own screen.
    5. Confirm success: an `e1000`/`e1000e`-family driver probe line
       naming the actual chip, `NIC Link is Up`, and a real DHCP lease
       line. If no such line appears, the machine's NIC isn't in the
       curated family yet — record the chipset (`lspci -nn` from a
       rescue USB) as a candidate for F6.6, not a failure of this ticket.
    6. Report back the chipset/PCI ID and the exact console output
       around the driver-probe/link-up lines so this can close for real.
    - 2026-08-10 update: a real Proxmox pass bundled real RTL8168h
      Realtek firmware, but the user's actual physical NIC isn't the
      exact revision that file matches — a different chipset family from
      this spike's own Intel e1000/e1000e target, so it does **not**
      close this checklist.
  - [~] **F6.3** 🔴 M — *WiFi real-radio association.* Firmware-loading
    packaging (AR9271/`ath9k_htc`), the WPA supplicant piece, and the
    `WIFI`/`WIFIPSK`/`WIFICOUNTRY` wiring are all real and inspected (see
    closed items below); no QEMU/VirtualBox WiFi radio emulation exists
    to test with, so real-hardware association still needs a physical
    WiFi radio and access point. No code work pending — just the test.
    Real-radio association remains explicitly deferred by the project
    owner (no new evidence as of the 2026-08-10 Proxmox pass).
  - Also open, lower priority: Realtek R8169/8168/8101/8125 Ethernet
    support (F6.6) is Kconfig-verified but not boot-tested against real
    Realtek silicon (neither QEMU nor VirtualBox emulate that family).
- [~] **F8** 🟠 M — Host-platform matrix for the CLI itself.
  - `linux/riscv64` cross-compile and Docker-binfmt are proven for real.
  - **F8b — Windows-on-Arm validation: NOT PLANNED.** No such hardware
    is available to this project; deprioritized by product decision.
  - **F8c — macOS Intel + Apple Silicon validation: PLANNED**, waiting on
    a real macOS session being made available to this project.
  - **Real scope gap, flagged not fixed:** `cnimbus prepare --arch`
    hard-rejects anything other than `amd64`/`arm64` — making riscv64 a
    real *guest* architecture (kernel/BusyBox piece selection, new
    kconfig fragments) is a materially bigger change than F8 as scoped
    implies. Needs a project-owner decision before ticketing further.
- [~] **F9** 🟡 S — Release readiness. The SemVer mechanism
  (`release.yml`, `RELEASING.md`, `-ldflags -X main.version=...`) is done
  and dry-run-verified for all 7 targets. **Only pushing the actual
  `v0.1.0` tag remains** — a deliberate, visible, shared-state action
  left for the project owner to trigger (see `RELEASING.md`'s pre-flight
  checklist), not something to do unprompted.

## B. Closed items

Full evidence for every item below (exact commands, console output, test
names, real boot logs) lives in git history; architectural decisions are
indexed in `.specs/project/STATE.md`'s AD log.

**Security hygiene** — T1 tmpfs exec-dir mount hardening (nosuid,nodev,mode) ·
T2 `/var/run` tmpfs hardening · T3 KV-agent state write switched to
`O_EXCL` (no symlink-follow) · T4 pinned BusyBox/iptables tarball SHA-256 ·
T5 iptables hash-check inverted to hard-fail on an unlisted file · T6
`--require-verified-pieces` + genuine-404-vs-fatal-error distinction · T7
hardened `ISOLINUX.CFG` (no interactive boot prompt) · T8
`CONFIG_CMDLINE_OVERRIDE=y` on the correct (amd64) fragment · T9 moved
`CONFIG_CMDLINE_BOOL=y` to the fragment it actually gates · T10
`panic=10 oops=panic` + PID 1 `oom_score_adj=-1000` · T11 reproducible
kernel build banners (`KBUILD_BUILD_*` pinned).

**IPv6 / firewall parity** — T12 IPv6 parity decision + implementation ·
T13 auto-prepended loopback/ESTABLISHED accept rules before user FIREWALL
rules · T14 `buildFirewallScript` fail-safe trap (`set -e` + fallback
ruleset) · T15 removed `-q` from `udhcpc` so leases actually renew · T16
`udhcpc` script honors `$mtu`/`$staticroutes`/`$router` · T17 `ntpd` only
started when DNS can resolve, backgrounded after a bounded sync.

**AGENT / networking hardening** — T18 fixed `kv-serve --token` help text
+ docs · T19 TLS for `kv-serve` (`--tls-cert`/`--tls-key`) · T20
`--hostfwd`/`--hostfwd-bind` default to `127.0.0.1` · T21 explicit
VirtualBox NIC type + eth0-missing self-diagnostic · T22 supervisor/agent
scripts written `0600` · T23 `/proc hidepid=2` + AGENT header via
env/file, not argv.

**Root-privilege / confinement hardening** — T24 `HEALTHCHECK` command
wrapped in `setuidgid` like the service · T25 VOLUME/`\tmp` mounts get
`nosuid,nodev(,noexec,mode=1777)` · T26 `--chmod` rejects setuid/setgid/
sticky bits · T27 explicit tmpfs `size=` everywhere + `TMPSIZE` directive ·
T28 `CONFIG_SECCOMP` + `setpriv --no-new-privs` replacing `setuidgid` ·
T29 VMware IOPL narrowed from `Iopl(3)` to the exact port range via
`Ioperm`.

**Kernel hardening & correctness** — T30 standard kernel hardening symbol
set (`RANDOMIZE_BASE`, `STACKPROTECTOR_STRONG`, `FORTIFY_SOURCE`,
`VMAP_STACK`, `HARDENED_USERCOPY`, …) · T31 retpoline/mitigation symbol for
the resolved kernel version · T32 `CONFIG_SCSI_VIRTIO=y` (Proxmox default
controller) · T33 `CONFIG_VMWARE_PVSCSI=y` (ESXi default controller) · T34
`CONFIG_VIRTIO_BALLOON=y` (memory overcommit beyond Hyper-V) · T35
`CONFIG_PRINTK_TIME=y` · T36 stage-1 boot-media probe retries per-device
instead of committing to the first mountable one.

**Provenance & compliance** — T37 `pieces.json` records kernel/BusyBox/
iptables provenance · T38 pinned release-signer PGP fingerprints · T39
`BuildLock` records `IptablesSHA256`/`AgentSHA256` · T40 real version
injected via `git describe` instead of every binary reporting `"dev"` ·
T41 `PROVENANCE.md` + pinning test for syslinux binaries.

**Partition/disk-image compliance** — T42 raw-image disk size rounded to
the next whole MiB (Azure alignment requirement) · T43 considered (and
left as informational) raising `espMinSize` toward Microsoft's documented
ESP minimums.

**Build-engine lifecycle & host hygiene** — T44 `docker run` hardened
(`--security-opt no-new-privileges`, `--cap-drop`, `--pids-limit`,
`--memory`, `--cpus`) · T45 `context.Context` + `signal.Notify` so Ctrl-C
cleans up temp trees and the running container · T46 env vars sorted
before argv (deterministic invocations) · T47 `pieces.sha256` written via
temp+rename · T48 `docker run --user` fixes root-owned output on Linux
hosts · T49 pinned apt package versions in the builder Dockerfile · T50
distinct exit codes per failure class instead of a flat `1`.

**Stage-1 boot path** — T51 stage-1 `/init` gets `set -e` + exit-status
checks on the COPY-shadow replay · T52 made the hardcoded 32 MiB exec-dir
tmpfs size actionable (same overflow class as T51) · T53 applet-symlink
creation in stage-1 refactored off ~400 individual `ln -s` lines,
boot-tested · T54 hardened a (already-correct) `||`/`&&` boot-media probe
expression against ShellCheck SC2015.

**CI/test gaps** — T55 CI extended to actually exercise build output
(`go test`) instead of never producing an image · T56 an optional
real-boot CI target was left open in the archive; superseded by the
"local self-hosted CI runner" future item in Section C, never separately
built · T57 added test coverage for `cmd/cnimbus` (`build.go`,
`prepare.go`, `lockfile.go`, `validate.go`, `run*.go`).

**Memory & provenance** — T58 lockfile image-hash computation stopped
reading the whole artifact into one `[]byte` · T59 `PiecesProvenance`
records `ARCH`/`--vga`/other previously-unrecorded build inputs · T60
output image written to temp + renamed, so a failed build never leaves a
lockfile-less partial artifact at the final path.

**arm64 parity** — T61 `CONFIG_PCI_HOST_GENERIC` added (arm64 images had
no PCI host bridge — no disk, no network — boot-tested) · T62 `panic=10
oops=panic` added to arm64's cmdline · T63 `CONFIG_CMDLINE_FORCE=y` added
to arm64 · T64 arm64 CPU-mitigation Kconfig parity, folded into F1 below.

**Kconfig verification** — T65 `verifyFragmentsApplied` checks symbol
value, not just presence · T66 `merge_config.sh` output inspected for
silently-dropped/redefined symbols · T67 asserted kernel security
baseline instead of inheriting `olddefconfig` defaults, build-validated.

**Micro-VM kernel tuning** — T68 `virtio-rng` entropy source added,
build-validated (later found to need a QEMU-side backend too, fixed
under V6) · T69 tick/timer policy added (`NO_HZ_IDLE`, `HIGH_RES_TIMERS`,
`NR_CPUS` cap), build-validated · T70 `CONFIG_VIRTIO_MMIO` added
(unblocks Firecracker/micro-VM boot), build-validated · T71 documented
`CONFIG_X86_IOPL_IOPERM`'s only consumer (AGENT vmware), build-validated ·
T72 bounded kernel build parallelism (`-j`) instead of unbounded
`NumCPU()`.

**Storage: SquashFS/boot-chain** — T73 fixed host-OS-dependent SquashFS
file modes (Windows builds shipped `0600` scripts/secrets world-readable)
by routing them through stage-1's tmpfs shadow-replay · T74 SquashFS
block size raised to `mksquashfs`'s own 128 KiB default · T75 image
pipeline payload handling partially moved off in-memory `[]byte` toward
path-based streaming · T76 `FORMAT raw` redesigned as a real two-partition
GPT+ESP layout, boot-validated · T77 El Torito oversized-`COPY` error
names the actual offending destination · T78 removed redundant triplicate
`vmlinuz`/initramfs copies on the ISO, boot-tested · T79 `--tmpdir`
override added (free-space pre-check remains a documented gap) · T80
documented the isohybrid-MBR limitation (`FORMAT raw` is the real
USB/bare-metal path) rather than implementing isohybrid MBR · T81 chain-
of-trust beyond `pieces.sha256` became F2 below, done.

**PID 1 & service lifecycle** — T82 real graceful-shutdown window
(`STOPGRACE`), validated with a real execution · T83 SIGKILL escalation
after a wedged `HEALTHCHECK` ignores SIGTERM · T84 restart-backoff
counter now resets after healthy operation · T85 `/proc` mount hardening
flags/exit status actually checked · T87 added `/etc/hosts` (was entirely
absent, breaking `HEALTHCHECK`'s own documented localhost idiom) · T88
`/etc/passwd`/`/etc/group` always populated, not just when `USER` is
declared · T89 service stdout/stderr reaches syslog and, once
VOLUME-persisted, survives on disk · **V1** — full real-boot validation
of the whole group above; also found and fixed a real bug where nested
VOLUME mountpoints silently failed to `mkdir` (and a missing
`CONFIG_CRYPTO_CRC32C` kernel symbol modern `mkfs.ext4` needs to mount).

**Firewall & injection surface** — T90 fixed a real FIREWALL
shell-injection vulnerability (rule text spliced unquoted into a root
shell script) · T91 documented/hardened `FIREWALL_ON_ERROR` open/closed
fail-open behavior · T92 bounded the foreground `udhcpc` timeout on eth0 ·
T93 a `required` VOLUME now actually halts boot on mount failure ·
**V2** — full real-boot validation of the group above; also found and
fixed T93's fix not actually halting boot (BusyBox init ignores sysinit
exit status; switched to a real blocking loop).

**Hypervisor interface** — T94 added `--accel`/acceleration flags to the
generated QEMU argv · T95 plumbed UEFI firmware selection into the
VirtualBox/VMware backends · T96 validated `--hostfwd`'s host:guest
parsing · T97 documented/validated the reused synthesized-UEFI-VARS-store
caching on Windows · **V3** — full real-boot validation of the group
above across QEMU/VirtualBox/VMware; also found and fixed an arm64 apt
package-pin bug (`bc`'s version string differs per architecture) and
documented a real WHPX-acceleration failure on this host.

**Duplication & structural quality** — T98 unified the QEMU-argv-building
implementation with the "copy this command" printed helper · T99 unified
four near-identical PATH/Windows-install-dir tool-lookup helpers · T100
unifying the four `run` backends became F3 below, done · T101 unified the
duplicated fetch/verify/build pipeline shared by `busybox.go`/
`iptables.go` · T102 fixed a missing `-trimpath` inconsistency between
Thunder's and `cnimbusagent`'s cross-compiles.

**Userspace hardening** — T103 added hardening compiler/linker flags
(stack-protector, `_FORTIFY_SOURCE`, PIE, RELRO) to the BusyBox/iptables/
`cnimbusagent` builds, build-validated via `readelf`/`objdump` · T104
added `io.LimitReader` bounds to `cnimbusagent`'s HTTP body reads.

**Round-3 real-boot finding** — T105 fixed the arm64 ISO9660 boot-media
probe (only tried `/dev/sr0`/`/dev/sr1`, which don't exist under QEMU's
`-M virt`; added whole-disk `/dev/vda`/`/dev/vdb` probing) — found via a
real arm64 boot, the single highest-value finding of the whole review.

**Remaining boot-validation debt** — **V4** storage/boot-chain group
(T73–T80) real-boot/real-build validated · **V5** AGENT/network/root-
hardening group (T18–T27) real-boot validated (TLS `kv-serve`,
`--hostfwd-bind`, `/proc hidepid=2`, `--chmod` rejection, tmpfs `size=`) ·
**V6** kernel entropy/timer/IOPL group real-boot validated; found and
fixed `virtio-rng` never being backed by a QEMU-side device (added
`rng-builtin`), and confirmed `agent-vmware.fragment`'s IOPL opt-in
against a real VMware host.

**Feature work** — **F1** arm64 CPU-mitigation Kconfig parity (KPTI,
Spectre-BHB, E0PD, PAC/BTI), real-build-verified · **F2** EFI-stub kernel
signing + UKI + Secure Boot, reimplemented pure-Go after a Docker-shell-
out violation was found and corrected, including a real Secure-Boot-
verified boot on VirtualBox with a cnimbus-generated cert (Hyper-V's cert-
enrollment gap is a documented real platform limitation) · **F3** unified
the four `run` backends behind one `backend`/`launchSpec` interface,
real-boot-validated on all four · **F4** Hyper-V raw-image support via a
pure-Go Fixed-VHD footer writer, plus `FORMAT vhd` on `build-disk` itself ·
**F6.2** `HARDBOOT` directive + `WIFI`/`WIFIPSK`/`WIFICOUNTRY` parsing,
`pieces.json` provenance, mismatch enforcement, byte-identical-default
guarantee · **F6.4** built `wpa_supplicant` (WPA2-PSK only, no OpenSSL/
EAP) as a fourth hash-pinned, statically-linked piece · **F6.5** wired
`WIFI`/`WIFIPSK`/`WIFICOUNTRY` into a real running image (0600 config,
`rcS` bring-up, credential never reaching argv/logs) · **F6.6** added
Realtek R8169/8168/8101/8125 Ethernet support (Kconfig-verified) ·
**F6.7** added `HARDBOOT eth+wifi` as an explicit combined profile, later
corrected so `"wifi"` alone no longer implicitly builds Ethernet drivers
too · **F7** Firecracker/micro-VM smoke test via a real KVM boot under
WSL2, found and fixed two Linux/WSL2-only bugs (chown-fixup never firing;
missing `/build` dir under a non-root user) · **F10** measured real NFR
baselines (image size, `vmlinuz` size, `build-disk` RSS/wall time)
against real builds, and found WHPX acceleration broken on this host
(folded into V3).

## C. Future milestones (post-v1.0, design pass needed before ticketing —
see [`.specs/project/ROADMAP.md`](.specs/project/ROADMAP.md) "Future
Considerations")

- **AF_VSOCK** as an `AGENT` transport for QEMU/KVM and Hyper-V.
- **BusyBox applet minimization** (~400 built → ~26 actually used).
- **UKI + fleet signing + dm-verity + measured boot**, strict order
  (overlaps F2 above; needs a real Secure Boot/TPM environment).
- **Kernel security-baseline fragment** that *asserts* posture (fails
  the build if a hardening symbol silently resolves wrong), not just
  requests symbols and hopes.
- **Further dm-verity-ready block-layout hardening** beyond the
  two-partition GPT raw-disk redesign already shipped.
- **Fully supervised build-engine process tree** — signal/context
  handling is done; resource bounds and cgroup-aware scheduling are
  still partial.
- **Local self-hosted CI runner** doing real boots — optional, only if
  it proves worth the setup cost. GitHub-hosted CI itself stays
  build-only regardless (standing decision).

**Explicitly not planned** (product decision, 2026-08-06): hosting or
publishing reference "pieces" builds / an OCI-based *distribution*
registry — cNimbus stays distroless and self-build-only.
