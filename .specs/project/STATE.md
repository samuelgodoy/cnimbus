# State

**Last Updated:** 2026-08-07

This is the architecture-decision (AD) log for cnimbus: a chronological
record of what was decided/built and, more importantly, *why* — so a
future engineer doesn't re-discover the same root cause twice. Each
entry is short by design: decision, reasoning, and any open caveat.
Full implementation detail (exact commands, test names, boot logs)
lives in git history and the code itself, not here.

**Current Work:** Section A (V1-V6, all boot-validation debt) and
almost all of Section B are closed. **Done:** V1-V6, F1, F2, F3, F4,
F7, F10. **Partial, real-hardware-only gaps left:**
- **F6** (HARDBOOT bare-metal) -- every sub-item (F6.1-F6.6) is
  code-complete and inspection/virtually-verified; only F6.1's physical
  USB boot and F6.3's WiFi radio association still need real hardware
  the dev machine doesn't have.
- **F8** (host-platform matrix) -- riscv64/cross-compile proven for
  real; Windows-on-ARM and both real macOS chip families still need
  physical/cloud hardware.
- **F9** (release readiness) -- the SemVer mechanism and a pre-flight
  checklist are done; only pushing the real `v0.1.0` tag remains, a
  deliberate, visible action left to the project owner.

No open item is blocked on missing permissions, elevation, or broken
host state as of AD-041 -- every real blocker found along the way was
resolved without shortcuts. See the AD log below for the full
chronological history.

---

## Recent Decisions (Last 60 days)

### AD-001: Backlog rebuilt, old ticket history archived (2026-08-06)

Old `Tasks.md` (T1-T105, all closed) renamed to an archive file; a
fresh `Tasks.md` rebuilt as the active backlog. The old file had grown
to 3421 lines of mostly-closed historical detail, making it useless as
an active backlog now that the tool is near production-ready. Git
history preserves every implementation detail regardless. `Tasks.md`
became the single source of truth for open work. (Later folded back:
the archive was merged into `Tasks.md`'s own compressed closed-items
list and removed as a separate file.)

### AD-002: Maximize guest-image compatibility over targeting one platform (2026-08-06)

The guiding principle for run-backend/boot-chain work is to maximize
compatibility across hypervisors/tools (QEMU/VirtualBox/VMware/Hyper-V/
bare-metal/Firecracker), not to optimize depth on any single platform.
User's explicit direction.

### AD-003: Distribution stays distroless, self-build-only (2026-08-06)

cNimbus will not publish or host reference "pieces" builds --
distribution stays distroless and self-build-only, per explicit user
direction. OCI as a purely local packaging format (not a hosted
channel) is unaffected either way.

### AD-004: SemVer versioning (2026-08-06)

Releases use SemVer, per user direction. Needs a real tagging process
on top of the already-wired `git describe`-based version stamping.

### AD-005: No compliance target -- unikernel-style extreme hardening instead (2026-08-06)

There is no FIPS/SOC2/FedRAMP/etc. compliance target. The philosophy
is extreme hardening + distroless + small, unikernel-shaped images
because that's the right shape for the artifact, not to satisfy an
auditor. Don't gate backlog prioritization on a named compliance
framework.

### AD-006: Host-platform priority matrix for the CLI (2026-08-06)

`cnimbus` (the CLI) must support Windows (amd64/arm64), Linux
(amd64/arm64/riscv64), and macOS (amd64/arm64) as *host* platforms --
confirmed this is about the host running the CLI, not guest kernel
arch. CI's cross-compile matrix needs a riscv64 target plus real
validation on the less-common host combos.

### AD-007: CI boot policy refined -- GitHub-hosted only, local runner ok (2026-08-06)

GitHub-hosted/cloud CI never boots an image (standing rule). A local,
self-hosted CI runner on the user's own machine doing real boots is
explicitly welcome if set up. Refines the earlier "CI stays build-only"
rule.

### AD-008: Firecracker tested via nested virtualization (2026-08-06)

Firecracker/micro-VM support is tested via nested virtualization inside
Docker, VirtualBox, or QEMU, since this Windows dev machine has no
native `/dev/kvm`.

### AD-009: v1.0 = Tasks.md at zero, then STOP and ask (2026-08-06)

The v1.0 milestone is driving the rebuilt `Tasks.md` to zero open
items; when that happens, stop and explicitly ask the user for
direction rather than self-directing into future work or declaring the
project done unilaterally. A hard checkpoint, per explicit user
instruction.

### AD-010: Bare-metal/USB boot in scope, conditioned on generic NIC drivers (2026-08-06)

Bare-metal/USB boot support is in scope, but only alongside a broader
NIC driver kconfig fragment (not virtio-only) -- real hardware can't
rely on the curated VM driver set.

### AD-011: Certification-grade rigor, no certification target (2026-08-06)

`.specs/` artifacts adopt certification-grade *practices* --
traceable numbered requirements, declared verification methods/evidence
levels (E0-E5), evidence gates by change class -- without adopting any
compliance framework's control set. Compatible with AD-005: rigor is
the goal, auditor-satisfaction is not. Motivated by a real, specific
weakness at the time: roughly 40 round-2 changes had merged with no
real-boot evidence; the evidence levels make that measurable instead of
implicit.

### AD-012: HARDBOOT is a prepare-time profile, following the VGA precedent (2026-08-06)

Bare-metal support is one `HARDBOOT none|eth|wifi` directive, resolved
at `prepare` time (it changes the kernel), recorded in `pieces.json` as
`boot_profile`, and enforced at `build-disk` via the existing
provenance-mismatch machinery -- reusing the pattern `ARCH`/`VGA`
already established rather than inventing a new mechanism. Default
`none` reproduces today's VM behavior byte-for-byte. Switching a
Nimbusfile to `HARDBOOT` requires re-running `prepare`.

### AD-013: WiFi requires a fourth built piece (2026-08-06)

`HARDBOOT wifi` builds a userspace WPA supplicant as a fourth piece
alongside kernel/BusyBox/iptables, following the iptables supply-chain
pattern. BusyBox ships no WPA supplicant, and WPA2-PSK association is
driven from userspace -- this makes WiFi a milestone, not a kconfig
edit.

### AD-014: HARDBOOT/WiFi parsing landed, cross-directive validation checked at end-of-parse (2026-08-06)

`HARDBOOT`/`WIFI`/`WIFIPSK`/`WIFICOUNTRY` directives landed in
`internal/nimbusfile` (parsing + validation only). Cross-directive
checks (WIFI* requires `HARDBOOT wifi`, and `wifi` requires all three)
run once at the end of `Parse` rather than inline per-directive,
matching this codebase's existing tolerance for directive-order
independence elsewhere. `BootProfile` defaults to `"none"`, so existing
Nimbusfiles are unaffected. SSID/PSK get the same shell-metacharacter
validation `FIREWALL` already has, since both get spliced into a
generated config file the same way.

### AD-015: `cnimbus prepare` refuses HARDBOOT eth/wifi outright (2026-08-06)

`--hardboot eth|wifi` (and the equivalent Nimbusfile directive) fails
immediately, before Docker is touched, naming the F6.1/F6.3 gap --
only `"none"` builds successfully. The kernel fragments these profiles
need don't exist yet and won't until hardware spikes validate the
design's assumptions; silently building a `"none"` kernel instead would
violate the project's own honesty rule against undocumented gaps.
`boot_profile` plumbing (pieces.json, build-disk mismatch check) is
otherwise fully wired and tested via fixtures, independent of the real
fragments.

### AD-016: SemVer release mechanism added, no tag cut yet (2026-08-06)

`.github/workflows/release.yml` (tag-triggered) and `RELEASING.md` land
now; pushing the actual `v0.1.0` tag does not. Pushing a tag is a
visible, hard-to-reverse, shared-state action requiring explicit user
confirmation, not a side effect of implementing F9. Tasks.md's F9 stays
marked partial.

### AD-017: F1 closed via real build verification, not a real boot (2026-08-06)

arm64 CPU-mitigation Kconfig symbols (F1) closed on the strength of a
real `cnimbus prepare --arch arm64` build and inspection of the
resolved `.config`, without a real VM boot -- these symbols aren't
boot-observable (they affect CPU exception/branch behavior, not
anything a boot log can distinguish), same precedent as amd64's
`CONFIG_MITIGATION_RETPOLINE`. Found a real, currently-true upstream
constraint: `CONFIG_ARM64_BTI_KERNEL` unconditionally depends on
`!CC_IS_GCC` (GCC bug 106671) and is unreachable with this project's
GCC-based builder -- confirmed absent from the real resolved `.config`.

### AD-018: Reunified a 19-commit branch divergence from master (2026-08-06)

Merged `master` into this branch after discovering a 19-commit
divergence: this branch's fork point predated real fixes (T44-T105-era
work, several boot-tested) that never made it here, which would have
made further boot-validation claims unreliable if left unmerged. One
merge conflict turned out to mask a pre-existing missing closing brace
from this session's own work, unrelated to the merge -- found and fixed
during resolution. This branch's rebuilt `Tasks.md` was kept over
master's old-format version (the ticket-history archive was confirmed
byte-identical either way). Tasks.md's V1-V6 boot-validation section
needed a fresh audit against the merged code (see AD-019).

### AD-019: Re-audited V1-V6 against master's own evidence markers (2026-08-06)

Cross-referenced every ticket in Tasks.md's V1-V6 against the
ticket-history archive's own `OK` (unit-verified) vs `OK-boot`
(real-boot-validated) markers per-ticket, rather than assuming the
merge closed or left open whole groups. Some tickets (T82, T76, T78)
were already boot-validated and excluded from V1/V4's remaining scope;
others (T68-71) were validated at the build level only, narrowing V6's
remaining scope to just the runtime claim; V2/V3/V5 had no
boot-validated markers at all. Tasks.md V1/V4/V6 rewritten accordingly,
each sub-item tagged with its own ticket and status.

### AD-020: F10 NFR baselines measured for real; surfaced a real WHPX failure (2026-08-06)

F10's NFR baselines (image/vmlinuz size, `build-disk` peak RSS/wall
time, boot-to-entrypoint latency) were measured against a real
`prepare`+`build-disk` run rather than left as pre-1.0 estimates. All
four measurable NFRs landed comfortably inside budget (e.g. 19.11 MiB
vs a 32 MiB budget, 0.53s vs a 30s budget); NFR-003/004 stay unmeasured
pending `HARDBOOT eth`/`wifi` kernel wiring. Surfaced a real,
undiagnosed finding rather than a clean pass: `--accel whpx` fails
outright on this dev machine (`WHPX: Unexpected VP exit code 4`), while
`--accel tcg` boots the same image successfully in 6.79s -- folded into
Tasks.md V3 rather than fixed here, since it's a hypervisor-interface
bug, not an NFR-measurement task.

### AD-021: V1's static filesystem checks (T85/T87/T88) closed via real boot (2026-08-06)

With a working boot path proven by AD-020's `-accel tcg`, closed V1's
cheapest static filesystem checks via real boots with debug entrypoints
dumping `/etc/hosts`, `/etc/passwd`, `/etc/group`, `/proc/mounts`. All
three (`/proc`+`/sys` hardening, `/etc/hosts` content, `/etc/passwd`+
`/etc/group` content) confirmed correct. The same boot showed a
positive signal for syslog capture but not enough to confirm
persistence, so that item stayed partial. Two remaining items
(healthcheck escalation, restart backoff) needed purpose-built fixtures
and were deferred to a separate pass.

### AD-022: T83/T84 closed by real boot; T89 closed after fixing a real VOLUME-mount bug (2026-08-06)

Closed V1's remaining items via real boots. HEALTHCHECK-wedge SIGKILL
escalation and restart-backoff-reset both confirmed working via
purpose-built entrypoints. The syslog-persistence item (T89) initially
failed: a real `VOLUME /dev/vda /mnt/data ext4` mount never happened,
root-caused via a debug entrypoint calling `mount(2)` directly
(bypassing a hidden `2>/dev/null` that had been hiding the real ENOENT).
`buildRCScript`'s `mkdir -p <mountpoint>` ran against the read-only
SquashFS root with no error checking, so a mountpoint not already baked
into the image silently no-op'd -- this exact pattern was what the
project's own shipped `examples/volume-persistent-disk/Nimbusfile`
used, undetected until now because every prior VOLUME test happened to
target an already-tmpfs path. Fixed by baking every VOLUME's mountpoint
(and intermediate path components) into the SquashFS image at build
time. Also added `CONFIG_CRYPTO_CRC32C`/`CONFIG_CRYPTO` to
`minimal.fragment` since a real `mkfs.ext4`'s default `metadata_csum`
feature needs `crc32c` to mount -- independently correct, though not
proven to be the specific cause of the bug above. V1 is now fully
closed.

### AD-023: V2 closed by real boot; T93's fix found not to actually halt boot, fixed for real (2026-08-06)

Real-boot-validated the firewall/injection group (T90-T93). Shell-
metacharacter injection via a Nimbusfile variable was confirmed to fail
the build before any image exists; iptables execution-time failures
were confirmed to respect the `FIREWALL_ON_ERROR open/closed` fallback
modes; a missing DHCP responder was confirmed to hit a bounded retry
then continue. T93's earlier fix turned out not to actually work:
`rcS`'s required-volume failure branch used `exit 1`, but `rcS` runs as
a BusyBox `::sysinit:` action -- BusyBox init proceeds to
`::respawn:`/`::once:` regardless of a sysinit action's exit status, so
the declared entrypoint started anyway right after the FATAL line
printed. Fixed by replacing `exit 1` with an infinite `sleep` loop,
since a sysinit action that never returns really does block every
subsequent entry. V2 is now fully closed.

### AD-024: V4 (storage/boot-chain group) closed by real Windows-host build + real boot (2026-08-06)

Validated V4's remaining items using whichever proof was strongest per
item. The permissions item (T73) mattered most: a real ISO built and
booted *on this Windows machine* showed `600` on generated scripts, not
the `0666` a Windows `os.Chmod`/`os.Stat` would otherwise bake into a
SquashFS-routed file -- proof the fix holds specifically on the
platform where the original bug was invisible to a Linux-only
developer. Large-block-size SquashFS mounting was confirmed stable
across restart cycles in the same boot. The El Torito size ceiling and
`-tmpdir` validation were confirmed via real `build-disk` invocations
rather than unit tests. The isohybrid-limitation doc claim was
re-checked against source and confirmed accurate. One item was excluded
as a pure internal refactor with no boot-observable behavior. V4 is now
fully closed with no source bugs found.

### AD-025: V5 (AGENT/network/hardening batch) closed by real boot; V4/V5 run in parallel worktrees (2026-08-06)

V2/V4/V5/V6 were dispatched as four parallel background agents in
isolated worktrees, following the same real-boot methodology, then
merged back by hand. V5 (AGENT/network/hardening) closed with no source
bugs found -- a live TLS `kv-serve` server, hostfwd-bind vs loopback
behavior, `hidepid=invisible` tmpfs mounts, and `--chmod` validation
were all confirmed for real. Also caught and corrected a stale
cross-reference in Tasks.md. Worth recording: the V5/V6 worktrees were
seeded from a stale branch state predating this session's own
Tasks.md/STATE.md rebuild -- their real findings were still sound, but
the doc formatting had to be manually reapplied rather than merged
directly; future parallel dispatches should diff against the exact
branch tip before starting.

### AD-026: V6 QEMU half closed; found and fixed a real virtio-rng gap (2026-08-06)

Closed V6's QEMU/virtio-rng half (the VMware half needed a real VMware
install and stayed open). Found a real gap: `/dev/hwrng` existed in the
guest but every read failed with "no such device" -- `qemuArgv` never
attached a virtio-rng device for the guest driver to bind to. Fixed by
always appending a `rng-builtin`-backed virtio-rng-pci device;
`rng-builtin` was chosen over the more common `rng-random` because
`rng-random` doesn't exist on the official Windows QEMU build, while
`rng-builtin` works identically across every host OS cnimbus targets.
Re-verified with real, different-each-time entropy reads across three
separate boots.

### AD-027: V3's QEMU-only slice closed (T94/T96/T97/T98); found and fixed a real cross-arch build bug (2026-08-06)

Split V3 into a QEMU-exercisable slice and a VirtualBox/VMware/
structural-refactor slice needing the B-001 permission pass; closed the
former. The `qemuArgv`/`describeQEMUCommand` unification had never been
boot-tested -- booted real amd64-UEFI and arm64 images to confirm.
Found and fixed a real cross-arch build bug getting there: an earlier
fix's builder-Dockerfile pin hardcoded a single `bc=` version verified
only against the amd64 Debian archive; the arm64 archive rebuilt the
same upstream source as a different binNMU version, so every arm64
`cnimbus prepare` on *any* host was broken by that earlier fix. Fixed by
branching the `bc` pin on `ARG TARGETARCH`.

### AD-028: B-001 was smaller than assumed; V3's T95 closed with real VirtualBox + VMware boots (2026-08-07)

Before accepting several tickets as blocked on B-001 (hypervisor
access), re-checked the premise instead of trusting it: `VBoxManage`/
`vmrun` (VirtualBox/VMware Player) both worked without any elevation --
only Hyper-V genuinely needed it. So B-001 was never really "need
hypervisor access," just "nobody had actually tried VirtualBox/VMware
yet." The user offered a plaintext account password mid-session for
elevation purposes; this was declined and never used, read, or stored
-- entering credentials into any field is a hard rule regardless of
user authorization. Hyper-V access was instead resolved through the
user adding the account to the (PT-BR-named) Hyper-V Administrators
group and approving per-invocation UAC prompts directly -- no shared
secret ever crossed into the session. Real VirtualBox (UEFI firmware
confirmed actually set, not just accepted) and VMware Player boots
closed V3's UEFI item.

### AD-029: F4 closed with a real Hyper-V boot; found and fixed a real cleanup bug (2026-08-07)

Closed F4 with a real Hyper-V boot (Generation 2, Secure Boot off,
static IP since the Internal switch has no DHCP) run from a
UAC-elevated session. Found and fixed a real cleanup bug:
`runViaHyperV`'s raw-image temp-VHD cleanup unconditionally deleted its
workDir on return, but a successful `Start-VM` leaves the VHD attached
to a still-running VM that holds the file open -- so cleanup silently
failed on every single successful raw-image Hyper-V run. Fixed by
tracking whether the VM actually started (mirroring the other
backends' own patterns) and printing a manual-cleanup reminder instead
of attempting a doomed delete.

### AD-030: V6 fully closed with a real VMware guest boot of AGENT vmware (2026-08-07)

Closed V6 fully with a real VMware guest boot of `AGENT vmware` --
distinct from AD-028's host-side vmrun automation, this proves the
guest-side backdoor protocol (`info-get guestinfo.<key>`) actually
round-trips a value set host-side via `vmrun writeVariable`. A second,
independent diagnostic entrypoint ruled out two plausible-sounding
alternate RPC namespaces, both rejected outright by VMware itself,
confirming the existing implementation needed no fix. All of Section A
(V1-V6) is now closed -- every boot-validation-debt item proven for
real against every hypervisor this machine has (QEMU, VirtualBox,
VMware Player, Hyper-V).

### AD-031: F8 partially closed (real cross-compile + CI matrix + Docker-riscv64 proof); found a real --arch scope gap (2026-08-07)

F8 (host-platform matrix) partially closed: `GOOS=linux
GOARCH=riscv64` builds cleanly (confirmed via `go tool dist list` that
Windows/darwin riscv64 aren't real Go ports at all), `ci.yml`'s build
matrix now includes riscv64, and Docker Desktop's riscv64 QEMU-user
emulation was independently re-verified in the orchestrating session.
Found a real scope gap: `cmd/cnimbus/prepare.go` hard-rejects any
`--arch` other than amd64/arm64, so `cnimbus prepare --arch riscv64` is
unreachable via the CLI today -- making riscv64 a real guest
architecture is a materially bigger change than F8 as scoped implies,
left as a named, undecided gap rather than silently expanded. Still
blocked on hardware this session doesn't have: a real Windows-on-ARM
host and both real macOS chip families.

### AD-032: F3 closed -- run backends unified behind a shared interface, validated against all four real hypervisors (2026-08-07)

Closed F3: run backends unified behind a shared `backend` interface
(`name()`/`available()`/`launch(spec)`) and a `launchSpec` struct
capturing every `cnimbus run` flag, replacing `runRun`'s four-armed
switch with a map lookup. Shared logic (FORMAT-raw-forces-UEFI,
hostfwd parsing, cleanup-on-success policy) moved into helpers; each
backend's genuinely different cleanup mechanism stayed backend-specific
code, per the ticket's own explicit warning against forcing a fake
shared abstraction. Real-boot-validated against all four backends
(QEMU/VBox/VMware/Hyper-V) with no bugs found in any of them -- the
drift the ticket named had already been fixed by earlier, since-closed
tickets.

### AD-033: F2 unblocked on both Hyper-V and VirtualBox; VirtualBox TPM needed the Extension Pack (2026-08-07)

Re-checked F2's "needs a real Secure Boot/TPM environment" premise
instead of accepting it: this dev machine has a real, active TPM 2.0
chip. Hyper-V Generation-2 Secure Boot + vTPM (via
`Set-VMKeyProtector`, no Host Guardian Service needed) and VirtualBox
Secure Boot both worked live. VirtualBox's `--tpm-type 2.0` was
silently accepted but did nothing until the user installed the Oracle
VirtualBox Extension Pack (TPM emulation is an Extension Pack feature)
-- confirmed working afterward via `VBox.log`, though it doesn't
surface in `showvminfo`'s summary listing (a display quirk, not a
functional gap). VMware Player genuinely can't do vTPM: it requires VM
encryption, a Workstation Pro/vSphere-only feature this Player install
doesn't expose. F2 is unblocked via either backend -- genuinely just
unimplemented, not blocked.

### AD-034: F7 closed with a real Firecracker/KVM boot; found and fixed two real non-root-Linux prepare bugs (2026-08-07)

Closed F7 with a real Firecracker/KVM boot inside WSL2, which this
Windows 11 host exposes nested virtualization into (confirmed a real,
usable `/dev/kvm`). Cross-compiled `cnimbus` for linux/amd64 and ran it
natively inside WSL2; extracted a real ELF `vmlinux` from the bzImage
(Firecracker's loader rejects bzImage) and booted a full stack to a
real entrypoint marker, with `Hypervisor detected: KVM` in dmesg
confirming real hardware virtualization. Found and fixed two real
Linux/non-root-invoking-user-only bugs, invisible on Windows/macOS
Docker Desktop (where the invoking user is uid 0 from the container's
view): (1) a cache-volume chown-fixup `docker run` call was missing
`--entrypoint sh`, so the chown command landed as raw argv on the
builder image's own exec-form entrypoint instead of running as a shell
command, breaking `prepare` on every real non-root Linux/CI host; (2)
the builder Dockerfile never created `/build` (Thunder's scratch dir),
so once bug 1 was fixed and privileges dropped for the real build, it
failed with a permission error. Both fixed and re-verified with a
second successful `prepare` run end to end.

### AD-035: F9 release-readiness prep work; found and fixed real release.yml/ci.yml matrix drift (2026-08-07)

Did the safe, non-publishing prep work for F9 so cutting `v0.1.0` for
real is a 30-second action whenever the project owner decides --
explicitly not pushing a tag, which stays their own call. Found and
fixed real drift: `release.yml`'s build matrix had gone stale against
`ci.yml`'s matrix, which AD-031 extended to riscv64 the next day, so a
`v0.1.0` tag pushed as-is would have silently shipped without a
linux-riscv64 archive despite that target being treated as real and
supported elsewhere. Fixed the matrix and corrected `RELEASING.md`'s
hardcoded target-count claim to point at AD-006/AD-031 instead of a
number that can drift again. Added a copy-pasteable pre-flight
checklist to `RELEASING.md` with the exact commands to run when cutting
the tag. `CHANGELOG.md`/`LICENSE`/`NOTICE`/`README.md` were all checked
and found to need no changes.

### AD-036: F6.1 virtually pre-checked (real QEMU e1000/e1000e + VirtualBox boots); F6.2 closed; physical USB boot still pending (2026-08-07)

Lifted `cnimbus prepare --hardboot`'s refusal for `"eth"` specifically
(per the design's own sequencing -- spikes before general mechanism),
adding `baremetal-eth.fragment` (`CONFIG_E1000E=y`) wired the same way
every other fragment already is. Verified the symbol's exact Kconfig
dependency chain against real upstream source before writing the
fragment; a real `prepare --hardboot eth` build succeeded and passed
`verifyFragmentsApplied`. Per the user's own suggestion, pre-checked
with real QEMU (e1000 and e1000e chips) and VirtualBox boots before
spending the one physical-USB attempt -- both showed driver probe,
link-up, and a DHCP lease. F6.2 (fragments wired into prepare) is
closed; F6.1 is explicitly not -- the pre-check proves the
kconfig/plumbing half but not the isohybrid-MBR/USB enumeration path or
any chipset beyond Intel e1000/e1000e, which needs the physical
hardware boot the project owner still needs to run.

### AD-037: F2 EFI-stub signing + UKI implemented and proven with real sbsign/objcopy evidence; live hypervisor cert-enrollment boot blocked by host state (2026-08-07)

Implemented F2 (EFI-stub kernel signing, UKI assembly, opt-in
`--secureboot`/`--uki`) as a Docker-based step inside `cnimbus
build-disk` (a new `internal/secureboot` package: RSA-3072 self-signed
keygen, a small `sbsign`/`objcopy`-based signer image kept separate
from the main builder image), since the initramfs a UKI needs doesn't
exist until `build-disk` assembles it. Found and fixed two real bugs
via actual `objcopy`/`objdump` runs against a real prepared kernel: (1)
`objcopy --add-section` with no `--change-section-vma` defaults the new
section's VMA to 0, colliding with vmlinuz's own `.setup` section --
fixed with explicit fixed VMAs well past any real bzImage's own
footprint; (2) a zero-length `--add-section` input (the always-empty
`.cmdline`, inert since `CONFIG_CMDLINE_OVERRIDE=y`) is a silent objcopy
no-op, compounded by reusing one file as both objcopy input and output
-- fixed by skipping the step when cmdline is empty and using distinct
temp files. Verified via `sbverify` positive/negative controls against
real signed and unsigned output. The live hypervisor cert-enrollment +
Secure-Boot-ON boot proof (step 3) was not completed this round:
VirtualBox's cert-enrollment attempt left the shared `VBoxSDS` COM
server in a broken state, and this session's account had no Hyper-V
access at the time (no credential-based elevation, only human-approved
UAC, unavailable non-interactively) -- a genuine, named gap, later
fully closed in AD-041.

### AD-038: F6.3/F6.4 implemented (WiFi firmware packaging + a static WPA supplicant); D2's real-hardware proof deliberately left open (2026-08-07)

Implemented F6.3 (WiFi firmware-in-initramfs) and F6.4 (a static
PSK-only WPA supplicant) per the design's D2 assumption, deliberately
not fabricating the one thing this session cannot produce: a real WiFi
radio associating with a real access point (no WiFi hardware or 802.11
chipset emulation exists in this environment). Chose the Atheros AR9271
(`ath9k_htc`) as the spike chipset specifically because it can't
silently "work anyway" without D2 being true -- no firmware, no
association. `baremetal-wifi.fragment` was verified symbol-by-symbol
against real upstream Kconfig; 6 real firmware blobs (~190 KB total,
Atheros/MediaTek/Realtek plus the regulatory database) were downloaded
and hash-pinned; firmware was placed directly in stage 1's own
initramfs tree since `request_firmware()` fires during kernel init.
`wpa_supplicant` 2.12 was built statically with `CONFIG_TLS=internal`
(zero OpenSSL) and no EAP support (PSK-only per spec); found
`driver_nl80211.c` hard-requires `libnl` regardless of TLS backend,
resolved by statically building `libnl` as a build-time-only
dependency. Found and fixed a real build bug: a plain
`CFLAGS=`/`LDFLAGS=` assignment on the make command line silently
blocks wpa_supplicant's own later `CFLAGS +=` lines -- fixed via
`EXTRA_CFLAGS` and a manually-constructed complete `LDFLAGS`. F6.4 is
fully closed; F6.3 stays partial -- the real-hardware association proof
and on-failure diagnostics remain open, the same physical-hardware gap
as F6.1's own.

---

## Active Blockers

### B-001: Need user-run commands to grant autonomous hypervisor access

**Discovered:** 2026-08-06 · **Resolved:** 2026-08-07 (fully as of AD-041)

Needed either the user running boot tests by hand each time, or a
one-time setup pass granting this session autonomous VirtualBox/VMware/
Hyper-V/QEMU/Docker access, to unblock V3 and backend unification.
Turned out smaller than assumed (AD-028): VirtualBox and VMware Player
never needed elevation at all, just someone to actually try them; only
Hyper-V did, resolved by the user adding the account to the Hyper-V
Administrators group and approving per-invocation UAC prompts -- no
shared secret, no password, ever. Fully closed as of AD-041, once a
later re-check found the earlier "no Hyper-V access" read had itself
been checking an unelevated token.

### B-002: HARDBOOT/WiFi design rests on six unverified assumptions

**Discovered:** 2026-08-06 · **Resolved:** 2026-08-07 (F6.3/F6.4/F6.5/F6.6)

`.specs/features/hardboot-baremetal/design.md` carried six open
`[ASSUMPTION]`s, two of which could have changed the design rather than
just the schedule: whether a built-in driver can load firmware from the
stage-1 initramfs, and whether every required WiFi driver can be
built-in (`=y`) in this module-less kernel. F6.1 and F6.3 were
sequenced as cheap spikes specifically to invalidate these before any
general mechanism was built. All six were resolved with real build/
inspection evidence -- driver build-ability, the firmware-packaging
mechanism, real firmware sizes (~190 KB, nowhere near budget), the
supplicant's crypto isolation, the regulatory-database requirement, and
Kconfig-gate correctness -- except the real-hardware half of firmware
actually loading on real silicon, which needs a physical WiFi radio
this session doesn't have (the same class of gap as F6.1's own physical
Ethernet/USB requirement).

### AD-039: real bug found merging F6.3/F6.4 -- `prepare` silently ignored any Nimbusfile parse error (2026-08-07)

Found while merging the F6.1/F6.2 and F6.3/F6.4 workstreams:
`cmd/cnimbus/prepare.go`'s Nimbusfile-reading code silently swallowed
*any* Parse error (a malformed directive, an invalid `HARDBOOT` value,
anything `internal/nimbusfile` rejects) and fell back to
CLI-flag-only defaults as if no Nimbusfile existed. Caught by a new
test that unexpectedly ran a full Docker kernel build instead of
failing fast. Fixed by distinguishing "no Nimbusfile present" (fine to
ignore) from "a Nimbusfile exists but failed to parse" (now a real,
immediate error). This bug predates both F6 workstreams and affects
every Nimbusfile directive, not just `HARDBOOT` -- never previously
caught because no test exercised a failing Nimbusfile read end-to-end
through `runPrepare`.

### AD-040: F6.5 (WIFI/WIFIPSK/WIFICOUNTRY wired into a real image) + F6.6 (Realtek R8169 added) (2026-08-07)

Implemented F6.5 (wpa_supplicant.conf generation, `rcS` wifi bringup,
credential handling) and F6.6 (Realtek R8169/8168/8101/8125 Ethernet
support), closing F6's remaining sub-items. Verified by mounting a real
built ISO and inspecting its extracted initramfs/SquashFS directly: the
generated wpa_supplicant.conf lands at mode 0600, and grepping the
entire extracted root for the literal PSK string found zero matches
anywhere except that one file. Found a real, previously-undetected doc
claim that was false: the project's docs claimed an AGENT-at-boot
credential-fetch layer existed for WiFi, but `wlan0` bring-up runs
synchronously inside `rcS`'s `::sysinit:` stage, which always finishes
before any `::respawn:`/`::once:` AGENT process even starts -- so no
such layer could exist; the doc comment was corrected rather than left
claiming something false. R8169 support is Kconfig-only (no firmware,
matching an existing precedent, since the driver treats missing
firmware as normal for common older revisions) and was not boot-tested
since neither QEMU nor VirtualBox emulates that chip family.

### AD-041: F2 fully closed -- real Secure Boot cert enrollment + boot proven on VirtualBox; Hyper-V's platform limit confirmed, not assumed (2026-08-07)

Finished F2 step 3 (live hypervisor cert-enrollment + boot proof),
which AD-037 had left open -- that earlier conclusion turned out to be
checking the wrong thing. VBoxSDS's broken state from AD-037's stuck
enrollment attempt was fixed with a plain `Restart-Service VBoxSDS
-Force` under UAC elevation. Hyper-V access was never actually blocked
-- AD-037's check had used an unelevated token; a UAC-elevated session
reaches Hyper-V fine on a real local-admin account. The real, confirmed
Hyper-V gap: no PowerShell cmdlet anywhere enrolls a caller-supplied
certificate for Secure Boot (`-SecureBootTemplate` only accepts fixed
built-in values) -- a genuine platform surface limitation, not a
permissions issue. On VirtualBox, avoided the risky `enrollorclpk`
entirely: `enrollpk --platform-key=... --owner-uuid=...` (a mandatory
flag not shown in the command's own usage banner) enrolled cnimbus's
cert as Platform Key, and Secure Boot correctly then rejected the
cnimbus-signed image (proving `enrollpk`/`enrollmok` don't populate the
`db` variable firmware actually checks) -- then writing the cert
directly into `db` via `efitools`' `cert-to-efi-sig-list` produced a
real, clean Secure-Boot-verified boot of a cnimbus-signed kernel, no
vendor demo key anywhere in the chain. F2 is now fully closed.

---

### AD-042: HARDBOOT gains a fourth value, `eth+wifi` (2026-08-07)

Added `"eth+wifi"` as a fourth `HARDBOOT` value so a Nimbusfile can
request both driver families explicitly. Found before implementing
that `HARDBOOT wifi` had *always* silently built both driver families
(`cmd/thunder/main.go` merged the eth fragment for both `eth` and
`wifi`, and spec.md already said "wifi implies eth") -- so `eth+wifi`
was deliberately not a new kernel-level combination, just the explicit
spelling of what `wifi` alone already produced, letting a Nimbusfile
author and `pieces.json`'s provenance say so directly. `BootProfile`
stays a single string; call sites across package boundaries each got a
small local `hasWifiDriver` helper rather than a shared import,
matching this codebase's existing duplication convention across those
boundaries. A real build confirmed both fragments' symbols land in the
same merged `.config` with no false conflict.

### AD-043: F2's own signing/UKI implementation was itself violating `build-disk`'s "never touches Docker" invariant -- replaced with pure-Go Authenticode signing (2026-08-08)

F2's own signing/UKI implementation (AD-037) was itself violating
`build-disk`'s long-standing documented invariant of never touching
Docker or a compiler: `--secureboot`/`--uki` shelled out to a throwaway
Docker image to run `sbsign`/`objcopy`. Replaced with pure-Go
Authenticode signing and hand-rolled PE section appending, using only
Go stdlib crypto/ASN.1 primitives (an external pure-Go library,
`go-uefi`, was investigated and explicitly rejected by the project
owner as unnecessary, since the stdlib already has every primitive
needed). Found and fixed four real bugs via cross-checks against actual
`sbsign`/`sbverify`/`objdump` output on a real kernel: (1) a PE header
offset computed 4 bytes short (the CheckSum field's own width was
omitted), corrupting every later field; (2) the new-section-append logic
originally grew the byte slice in place, shifting every subsequent
section's data without updating its own pointer -- fixed by overwriting
existing header padding instead; (3) the Authenticode messageDigest
attribute must hash the inner content octets, not the full wrapped
value including outer tag+length -- found by diffing against real
`sbsign` output, since both readings were internally self-consistent
but only one interoperated; (4) one ASN.1 field needed a fixed
placeholder value matching real `sbsign` convention rather than a
spec-derived reading. All verified via `sbverify` positive/negative
controls plus a permanent from-scratch ASN.1/RSA cross-verification
test. `build-disk --secureboot`/`--uki` now has zero runtime dependency
on Docker or any external tool; the old Dockerfile and its embed were
deleted. Not repeated this round: a full hypervisor boot proof against
the pure-Go-signed output specifically -- the `sbverify` evidence and
cross-verification test are the basis for this closure instead.

### AD-044: HARDBOOT's "wifi implies eth" coupling reversed (2026-08-08)

Reversed HARDBOOT's "wifi implies eth" coupling (documented, not
created, by AD-042): per direct product decision, `"eth"` now builds
only Ethernet drivers, `"wifi"` only the 802.11 stack, and `"eth+wifi"`
is the only value building both -- making `eth+wifi` genuinely distinct
from `wifi` alone for the first time. Verified via a real build that
Ethernet driver symbols are now absent from a `wifi`-only `.config`
while WiFi symbols remain present; no existing test had asserted the
old coupling directly, so this real build was the only evidence the
reversal actually took effect.

### AD-045: three real kernel-build bugs found by actually testing the manual's pinned-version example (2026-08-07)

Testing a manual example pinned to `KERNEL 6.9.4`/`BUSYBOX 1.36.1`
(never previously built by this project, since prior sessions reused
cached kernel versions) surfaced three real, previously-undiscovered,
latent-since-inception bugs, none a regression from this session's own
work: (1) duplicate `CONFIG_PCI=y` set in two fragments since the
project's earliest commits, caught by the existing merge-conflict
checker; (2) `CONFIG_NETFILTER_XTABLES_LEGACY=y` in `minimal.fragment`
is not a real Kconfig symbol for this kernel version at all -- silently
dropped by `olddefconfig` until a later check started hard-failing on
dropped symbols; (3) the builder Dockerfile's `gcc:16.1.0` base
defaults to the C23 dialect, under which `bool` becomes a keyword
colliding with the kernel's own `typedef _Bool bool` in the EFI stub
subtree (the one place that doesn't force `-std=gnu11`) -- Linux only
tolerates C23 well past 6.9.4, so repinned to `gcc:14-trixie`. All three
were latent because the build-cache means a real `merge_config.sh`/
fresh-compiler run essentially never happens once a kernel version is
cached, which is nearly always true for routine `latest`-floating
builds. Verified via a full real build-and-boot of the pinned example
after each fix.

---

## Deferred Ideas

- [ ] OCI as a *local* packaging format (not a hosted distribution
  channel) -- not requested, not ruled out; revisit only if a concrete
  need appears. Captured during: AD-003.
- [ ] Local self-hosted CI runner for real boot tests -- user said it's
  welcome but didn't ask for it directly. Captured during: AD-007.

---

## Todos

- [ ] When the user preps a real macOS session, validate `cnimbus` on
  both real chip families (F8c, ROADMAP.md M4) -- the one remaining
  host-platform gap that isn't a "no hardware, not planned" item.
- [ ] Whenever the project owner is ready: push the real `v0.1.0` tag
  (see RELEASING.md's pre-flight checklist) -- the last step of F9/M7,
  deliberately left for them to trigger.

### AD-046: VGA console prints the guest's IPv4/IPv6 addresses (2026-08-10)

Direct product request: with `VGA true`, once networking comes up, the
console prints every interface's assigned address so someone watching a
physical monitor (or a hypervisor console window) can learn it without
a shell or a serial capture. Implemented via a boot-time loop over
BusyBox's `ip addr show scope global` for both address families; an
absent family simply prints nothing, giving the desired "only if
present" behavior with no extra plumbing. Verified via a real QEMU
boot showing the correct banner after DHCP.

### AD-047: real IPv6 support -- FIREWALL6, ip6tables, kernel enablement (2026-08-10)

Added full IPv6 support (`FIREWALL6`, ip6tables, kernel enablement),
reversing an earlier decision to instead disable IPv6 at boot -- driven
by a real example needing dual-stack reachability. Removed
`ipv6.disable=1` from the kernel cmdline (the only thing actually
disabling IPv6 despite `CONFIG_IPV6=y` always being compiled in) and
mirrored the IPv4 netfilter chain for IPv6. Found a real version-skew
Kconfig bug along the way: `NETFILTER_XTABLES_LEGACY` (removed from
`minimal.fragment` by AD-045 as nonexistent in kernel 6.9.4) is a real
symbol that later kernel releases add and then require -- a static
fragment can't express "only on some versions," so `cmd/thunder` now
detects its presence directly against the fetched kernel source and
merges a synthetic fragment only when needed. `FIREWALL6` reuses the
existing static multi-call iptables binary (it dispatches on its first
argument) rather than needing a second binary. Verified via a real boot
showing IPv6 registered and both firewalls applying cleanly; full
external IPv6 reachability against a real network was not independently
verified in this session (a QEMU user-mode-networking DHCP timing
quirk, not a cnimbus bug).

### AD-048: real bare-metal boot bug -- stage 1's device probe never tried /dev/sda, and never retried (2026-08-10)

A real physical UEFI hardware boot of a bare-metal image hit a kernel
panic after failing to find any boot device -- every hypervisor this
project had tested against attaches the boot media as `/dev/sr0` or
`/dev/vda`, but real hardware booting from a `dd`'d USB/SATA drive
enumerates as plain `/dev/sda`, which stage 1's device candidate list
never included. A second, compounding issue: USB mass-storage
enumeration can take a few seconds after the kernel detects the device,
a race no hypervisor's instantly-attached emulated disk had ever
exercised. Fixed by adding `/dev/sda`/`/dev/sdb` to the candidate list
and wrapping the whole boot-media scan in a bounded 10-attempt,
1-second retry loop. Verified not to regress QEMU boots; not yet
re-confirmed against the original physical hardware that surfaced it --
that's on the project owner.

### AD-049: real bare-metal boot bug #2 -- no USB mass-storage driver at all, and multiboot USB tools (Ventoy) need a generic loopback-ISO scan (2026-08-10)

A second real hardware attempt, this time via Ventoy (a grub-based
multiboot USB tool), still failed the same way even with AD-048's fix.
Root-caused to two gaps: (1) no `CONFIG_USB_STORAGE`/`CONFIG_USB_UAS`
existed in any kernel fragment at all -- the running kernel needs its
own mass-storage driver to rediscover a USB stick as a block device,
separate from UEFI firmware's own USB stack; (2) Ventoy boots by
chainloading grub's `loopback` command against an `.iso` *file* on an
ordinary FAT/exFAT partition, so there's no discoverable ISO9660 device
for AD-048's scan to ever find, no matter how long it retries. Fixed
with a new `baremetal-usb.fragment` (USB core + mass-storage support,
merged for any real-hardware HARDBOOT profile) and a third boot-media
scan tier that mounts every vfat/exfat partition and loop-mounts any
`*.iso` file found -- implemented as a vendor-neutral generic scan
(corrected from initial Ventoy-specific naming per product feedback),
matching the same technique Ubuntu's casper and Fedora's dracut already
use. Verified locally via a QEMU USB mass-storage device (not the
ISO9660 CD-ROM path) booting the real kernel/initramfs end to end,
without needing a second physical round trip.

### AD-050: CNIMBUS.CFG -- an in-image identity manifest, not a boot-tool-specific selection mechanism (2026-08-10)

AD-049's generic loopback-ISO scan picks the first `.iso` file with
valid SquashFS contents, which is genuinely ambiguous with more than
one cnimbus image on the same multiboot stick. Investigated and
rejected replicating Ventoy's own distro-signature-based selection --
it depends on Ventoy recognizing specific known distro layouts, and the
generic opt-in path is Ventoy-specific and would require reversing
`CONFIG_CMDLINE_OVERRIDE` (added deliberately after an earlier
cmdline-drift incident), a real architecture change for a feature this
project can't fully verify without physical Ventoy hardware.
Implemented instead, per direct product correction: `CNIMBUS.CFG`, a
plain-text identity manifest written to the ISO9660 tree's top level
(readable before mounting SquashFS), echoed to console the moment a
boot candidate is committed to. Doesn't resolve automatic-selection
ambiguity (still "first one found") but makes whichever image boots
identifiable by its own name rather than an arbitrary filename.

### AD-051: uptime checkpoints -- a real boot froze for good with no visible evidence of where (2026-08-10)

A real hardware boot with AD-049/050's fixes applied got through USB/
network bring-up but then genuinely froze forever with no further
console output -- the one piece of evidence (a link flap ~190s after
boot) didn't cleanly localize the freeze. Rather than guess further,
instrumented the boot sequence with uptime-checkpoint prints (reading
`/proc/uptime`) at six points bracketing every phase with no prior
visibility, cheap enough to leave in permanently. Verified all six
print in order with real elapsed times under QEMU. The actual
real-hardware freeze remained undiagnosed at this point -- this only
prepared the next attempt to localize it (see AD-052).

### AD-052: the "frozen" bare-metal boot was never frozen -- userspace was printing to the wrong console (2026-08-10)

The "frozen" boot from AD-051 was never frozen: the kernel cmdline sets
multiple `console=` devices, and while the kernel prints to all of
them, userspace inherits only the *last* one as `/dev/console` -- so
every message this image prints itself (the checkpoints, the VGA IP
banner, etc.) went to serial alone, while the monitor showed only
kernel dmesg stopping dead at its last line, indistinguishable from a
hang. Every QEMU test this project runs reads the serial port, so no VM
testing in the existing style could have caught this. Rather than
reorder the cmdline (which would just move the blind spot onto serial
users), added a console helper that fans every message out to every
console the kernel actually registered, read from
`/sys/class/tty/console/active`. Also fixed a related readability issue
found in the same investigation: stage 1 was attempting an exFAT mount
against every block device on the machine without first checking the
filesystem signature, and the exFAT driver logs several error lines per
failed attempt -- enough to push real boot messages off-screen
entirely. Verified via a QEMU harness capturing both VGA framebuffer and
serial from the same boot: before the fix, VGA showed nothing past
kernel dmesg while serial had the full sequence; after, VGA showed
everything.

### AD-053: eth0 driver/carrier/DHCP-outcome debug lines, for the next real "link up, no IP" report (2026-08-10)

A subsequent real report was "link comes up, no IP ever obtained" --
the image had no way to distinguish no-carrier, carrier-fine-but-
no-DHCP-response, and lease-obtained-but-something-else-failed from the
console alone, since all three look identical. Added three debug lines
(driver/MAC, carrier/operstate/speed, udhcpc exit code/address) routed
through AD-052's console helper. Found and fixed a real race while
verifying: an unpolled single carrier read reported "no carrier" on a
link that got a DHCP lease moments later, since autonegotiation isn't
instantaneous -- fixed by polling briefly before printing, avoiding a
diagnostic line that would lie part of the time even on this project's
fast VM path. Also ruled out the r8169 driver's own missing-firmware
warning as a cause of no-traffic, by reading the driver source directly
(that firmware is for MAC identification only, not required for basic
operation).

### AD-055: FIREWALL6's DROP policy silently blocked ICMPv6 Neighbor Discovery, breaking IPv6 reachability entirely (2026-08-10)

Real bare-metal hardware and a real Proxmox VM both reported working
IPv4 but unreachable IPv6, despite correct SLAAC address assignment
(confirmed via a debug script against the real Proxmox VM). Root cause:
IPv6 has no ARP -- Neighbor Discovery Protocol runs entirely over
ICMPv6, which is ordinary IP traffic and is filtered by ip6tables like
anything else, unlike IPv4's ARP which never touches iptables at all.
The project's own example `FIREWALL6` (a DROP policy plus one ACCEPT
rule) silently dropped every Neighbor Solicitation aimed at the guest,
so no other host could ever resolve its MAC -- confirmed via a real
Windows host on the same LAN marking the address unreachable. Fixed by
auto-injecting RFC 4890's recommended minimum ICMPv6 profile
(unreachable/too-big/time-exceeded/parameter-problem, echo, and the NDP
message types) before any user rule, for `FIREWALL6` specifically,
verified against the project's own vendored iptables source for exact
type names. Verified end to end against the real Proxmox VM: `curl -6`
against the guest's global address returned 200 OK after the fix.

### AD-056: VGA banner and IPv6 diagnostic printed before SLAAC's global address had actually landed (2026-08-10)

After AD-055 fixed real IPv6 reachability, the VGA console still only
showed the link-local address, not the global SLAAC one -- both the VGA
banner and AD-053's diagnostic checked for a global IPv6 address exactly
once with no wait, the same class of race AD-053's own carrier-poll fix
addressed but hadn't been applied to IPv6. Fixed by polling briefly
before printing, bounded shorter than AD-048's device-scan bound --
confirmed via QEMU that a network offering no IPv6 at all (still the
common case) always runs the full bound, so a longer timeout would cost
real boot latency for the common case.

### AD-057: console refinement on a real, fully-working bare-metal boot -- device-existence checks and labeled firewall lines (2026-08-10)

With IPv4 and IPv6 both confirmed working on real bare-metal hardware,
reviewed a full boot log for refinements rather than chasing new
failures. Two cosmetic fixes: skip mount attempts against boot-media
candidate devices that don't exist yet (most of a real console's
visible output was "Can't lookup blockdev" spam from candidates that
were never going to exist on that machine), and label the two identical
"firewall fallback-on-error mode" lines from the IPv4/IPv6 firewall
scripts as (IPv4)/(IPv6) so they don't read like a duplicate bug. Left
the r8169 missing-firmware line unaddressed at this point (real,
harmless per driver source, but bundling the firmware is a larger
change) -- done next, in AD-058.

### AD-058: bundle the real rtl8168h-2.fw firmware, removing the r8169 console error (2026-08-10)

Bundled the real `rtl8168h-2.fw` Ethernet firmware (deferred in
AD-057), mirroring the existing WiFi firmware pipeline end to end --
same fetch/verify/embed mechanism via a new shared helper, threaded
through `pieces.json`/`PiecesSpec`/stage-1 initramfs embedding. Verified
via a real build confirming the firmware's SHA-256 and its presence in
the extracted stage-1 initramfs; the driver actually loading it
couldn't be verified via QEMU/VirtualBox since neither emulates any
R8169-family chip. Re-tested on the user's real physical hardware and
the console error was gone -- though on that specific machine because
its NIC isn't the exact RTL8168h revision needing this file, not
because of anything this fix changed; the bundled firmware stays
dormant and harmless on hardware that doesn't need it, and will apply
automatically on hardware that does.

### AD-059: acpid never started successfully -- ACPI Signal Shutdown timed out on a real Proxmox VM (2026-08-10)

A real ACPI shutdown request against a Proxmox VM (`qm shutdown`) timed
out. Two independent gaps: (1) `CONFIG_INPUT_EVDEV` was never enabled,
so the power button was registered as a kernel input device but never
exposed to userspace at all; (2) even after fixing that, BusyBox
1.36.1's real `acpid` doesn't read the classic `/etc/acpi/events/*`
config this project shipped -- it reads `/etc/acpi.map`/`/etc/acpid.conf`
or falls back to compiled-in tables routing the power button to a fixed
path, so the old config was simply never consulted. A third, subtler
gap surfaced while fixing the others: a bare `acpid &` never started
successfully because `/var/log` doesn't exist in this image, and only
the `-d` flag (not just `-f`) skips acpid's default log-file open.
Fixed all three; verified against the real Proxmox VM with `qm shutdown`
completing successfully after the fix (previously timed out).

### AD-060: Ctrl+Alt+Del re-enables the PS/2 keyboard driver, re-verified against real Hyper-V (2026-08-10)

Proxmox's "Send Key: Ctrl+Alt+Del" had nowhere to land because
`CONFIG_SERIO_I8042`/`CONFIG_KEYBOARD_ATKBD` had been disabled for a
previously-documented Hyper-V Generation 1 hang. Confirmed the current
symptom for real (a Proxmox VM's uptime kept climbing after the
send-key command) before re-enabling the keyboard driver (mouse support
stays off -- nothing in this image reads pointer input) and re-testing
the original Hyper-V hang rather than trusting the old comment. The
original hang did not reproduce on a real local Hyper-V Generation 1
VM -- most likely a Windows-side Hyper-V update to its own i8042
emulation since the original finding, though this can't be pinned down
further without a second machine still showing the old behavior. A real
QEMU boot confirmed the mechanism works end to end (a reboot request
triggers the full graceful shutdown sequence); the same command against
the real Proxmox VM had no visible effect via the API token used, but
the user separately confirmed Ctrl+Alt+Del works via the Proxmox web
console's own full session -- consistent with the API token's own
restricted permission scope, not a guest-side gap.

---

## Preferences

**Model Guidance Shown:** never
