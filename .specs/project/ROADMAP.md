# Roadmap — path to v1.0

**Current Milestone:** M5 — HARDBOOT bare-metal boot (only remaining
open item; see below)
**Status:** M1-M3, M6, M7's mechanism all DONE. M4 and M5 are the only
milestones with anything still open, and in both cases it's the same
kind of gap: real physical hardware this dev machine doesn't have,
not unfinished code. See each milestone below for specifics, and
[`.specs/project/STATE.md`](STATE.md)'s "Current Work" line for the
always-current one-paragraph summary.

This roadmap sequences the granular backlog in the repo-root
[Tasks.md](../../Tasks.md) into shippable milestones. Each milestone's
"Features" map 1:1 to Tasks.md items (IDs in parentheses). When every
milestone below is COMPLETE, Tasks.md is empty and v1.0 is done — at
that point, stop and ask the user what's next (see STATE.md AD-009).

---

## M1 — Boot-validate the existing hardening

**Goal:** every round-2 change (T44-T105 era) that landed as code + unit
tests only is proven against a real boot before this project calls
itself production-ready.
**Target:** all six groups below verified on real hardware/hypervisors.
**Status: DONE** (2026-08-06/07) — every group below is real-boot
validated; see Tasks.md's section A and STATE.md AD-019 through AD-030
for the full evidence trail, including four real bugs found and fixed
along the way.

### Features

**PID1/service-lifecycle validation (V1)** - DONE
- Graceful shutdown/SIGKILL escalation, restart-backoff reset,
  `/proc`+`/sys` mount-then-harden, `/etc/hosts`, default
  `/etc/passwd`/`/etc/group`, syslog capture — real QEMU boot with a
  SIGTERM-trapping service, a non-trapping one, and a wedged
  HEALTHCHECK.

**Firewall/injection validation (V2)** - DONE
- `FIREWALL` injection fix, `FIREWALL_ON_ERROR`, bounded `udhcpc`,
  `required` VOLUME — real Nimbusfile exercising `FIREWALL` + `VOLUME`
  together.

**Hypervisor-interface validation (V3)** - DONE
- `--accel`, UEFI on vbox/vmware, `--hostfwd` validation, per-VM OVMF
  VARS, `qemuArgv`/`findtool` refactors — real VirtualBox and VMware are
  now available on this machine (see M2's permission setup).

**Storage/boot-chain validation (V4)** - DONE
- SquashFS file-mode fix (build on Windows, confirm no world-readable
  secrets), block size, El Torito error message, ISO dedup, `--tmpdir`.

**Agent/network/root-hardening validation (V5)** - DONE
- TLS `kvserve`, `--hostfwd-bind`, `/proc hidepid=2`, tmpfs `size=`
  limits, `--chmod` rejection.

**Kernel entropy/timer/IOPL validation (V6)** - DONE
- `virtio-rng` visible to userspace; `agent-vmware.fragment`'s IOPL
  opt-in against a real VMware host with `AGENT vmware` declared.

---

## M2 — Real-hypervisor unblock & backend unification

**Goal:** stop being blocked by "no real hypervisor to test against" —
the dev machine already has VirtualBox, VMware, Hyper-V, QEMU, and
Docker installed; get permission to drive them autonomously, then do
the backend refactor for real.
**Status: DONE** (2026-08-07) — see STATE.md's B-001 (fully resolved,
including a later correction: plain UAC elevation reaches Hyper-V
regardless of local-admin-group membership, AD-041) and AD-032 for F3's
closure.

### Features

**Grant autonomous execution permissions** - DONE
- User runs a short list of setup commands (elevate/allowlist
  VBoxManage, vmrun, Hyper-V PowerShell module, qemu-system-*, docker)
  so future sessions don't need a human in the loop per hypervisor call.

**Unify the four run backends (F3 / old T100)** - DONE
- `backend`/`launchSpec` interface across qemu/vbox/vmware/hyperv,
  validated against all four real tools this time — the exact
  validation the earlier attempt was blocked on.

---

## M3 — arm64 parity + pieces signing (step 2)

**Goal:** close the amd64/arm64 hardening gap, and extend supply-chain
signing one step past `pieces.sha256`.
**Status: DONE** (2026-08-07) — including UKI + measured boot, which
turned out achievable this session rather than staying a post-v1.0
item; see STATE.md AD-037/AD-041.

### Features

**arm64 CPU-mitigation Kconfig parity (F1 / old T64)** - DONE
- KPTI, E0PD, Spectre-BHB, PAC/BTI equivalents — same research-then-
  real-build treatment amd64's retpoline symbols got.

**EFI-stub kernel signing + UKI + Secure Boot (F2 / old T81 steps 2-3)** - DONE
- Pure-Go Authenticode signing of the kernel EFI stub and a full
  Unified Kernel Image (AD-043 -- no Docker, no `sbsign`/`objcopy`;
  the original implementation shelled out to both via a throwaway
  Docker image, a real violation of `build-disk`'s own "never touches
  Docker" invariant, fixed after the fact), building on the Ed25519
  `pieces.sha256` signing already shipped. Originally scoped to
  *exclude* UKI/measured boot as a post-v1.0 item (genuinely
  milestone-scale at the time this was written) — done anyway,
  including a real, live certificate enrollment into VirtualBox's UEFI
  `db` variable and a real
  Secure-Boot-verified boot of a cnimbus-signed kernel (AD-041).
  Hyper-V's own certificate-enrollment path remains a confirmed platform
  limitation (no cmdlet for a custom cert), not something left undone.

---

## M4 — Host-platform expansion

**Goal:** `cnimbus` itself runs natively on every platform in the
priority matrix, not just the 6 targets CI currently cross-compiles.
**Status: PARTIAL** — riscv64 cross-compile and Docker-binfmt genuinely
proven (F8a); Windows-on-ARM is out of scope by product decision (no
hardware available, not planned); macOS validation (F8c) is scheduled
once a real macOS session is prepared — see Tasks.md's F8 entry.

### Features

**Linux/riscv64 CLI support (F8a)** - DONE
- `riscv64` added to the CI cross-compile matrix; `prepare`/`run`'s
  real *guest*-architecture gap (as opposed to the CLI's own
  cross-compile, which works) found and flagged as a separate,
  bigger, not-yet-scoped change — a real, honest finding, not silently
  expanded.

**Windows arm64 validation (F8b)** - NOT PLANNED
- No Windows-on-Arm hardware is available to this project; deprioritized
  by product decision rather than left as an open unknown.

**macOS Intel + Apple Silicon validation (F8c)** - PLANNED
- Confirm `cnimbus` runs correctly on both real macOS chip families
  (darwin/amd64 and darwin/arm64), once a real macOS session is
  prepared for this project to use.

---

## M5 — HARDBOOT: bare-metal boot with Ethernet and WiFi

**Goal:** a produced ISO can be `dd`'d to a USB stick and boot real
hardware, reaching the network over either real Ethernet or WiFi — with
the entire capability **opt-in and inert by default**, so a VM image is
unchanged.

Fully specified in
[`.specs/features/hardboot-baremetal/spec.md`](../features/hardboot-baremetal/spec.md)
and [design.md](../features/hardboot-baremetal/design.md). This is the
largest milestone in v1.0: WiFi alone requires a fourth built piece (a
userspace supplicant — BusyBox has none) plus curated binary firmware,
which is why it is sequenced last and spike-first.

**Status: PARTIAL** — every sub-item (F6.1-F6.6) is code-complete and
either virtually pre-checked (Ethernet, under QEMU/VirtualBox's own
e1000-family emulation) or verified by direct inspection of a real
built image (WiFi firmware/config/supplicant). The two things left are
both the same kind of gap: a real physical boot this dev machine can't
produce on its own -- F6.1's USB/bare-metal Ethernet boot, and F6.3's
WiFi radio actually associating. See Tasks.md's F6 entry and STATE.md
AD-036/AD-038/AD-040 for the full evidence trail.

### Features

**Bare-metal Ethernet + isohybrid (F6.1, F6.2)** - PARTIAL
- `HARDBOOT none|eth|wifi` directive, resolved at `prepare`, recorded in
  `pieces.json`, enforced at `build-disk` -- **done**.
- Isohybrid MBR: not pursued -- `FORMAT raw`'s GPT+ESP layout is this
  project's actual USB/bare-metal path instead (see Tasks.md's T80
  entry); a plain ISO staying non-isohybrid is a documented, deliberate
  limitation, not a gap.
- Real Ethernet chipset drivers (Intel e1000/e1000e, Realtek
  R8169/8168/8101/8125) in an opt-in fragment -- **done**, kconfig
  verified against real kernel source, boot-tested under QEMU/VirtualBox
  e1000 emulation.
- Guarantee: `HARDBOOT none` output is byte-identical to today's --
  **done**.
- **Open:** the actual physical USB boot on real hardware -- a
  concrete, six-step checklist for the user to run this is in Tasks.md's
  F6.1 entry.

**WiFi stack (F6.3, F6.4, F6.5)** - PARTIAL
- Firmware-loading spike (D2): the *packaging mechanism* is proven by
  direct inspection of a real built image's initrd -- **done**. What
  remains open is D2's real-hardware half: whether the AR9271 chip's
  firmware-download protocol actually succeeds on real silicon, which
  needs a physical WiFi radio this session doesn't have.
- Userspace WPA supplicant built as a fourth piece, on the existing
  hash-pinned supply-chain pattern -- **done**.
- `WIFI`/`WIFIPSK`/`WIFICOUNTRY` directives, wired into a real running
  image (config generation, `0600` permissions, credential never
  reaching logs/`/proc/<pid>/cmdline`) -- **done**. Of the three
  credential delivery layers originally scoped (runtime AGENT →
  `--build-arg` → literal), the "AGENT at boot" layer was found not to
  actually work (`rcS`'s sysinit completes before any AGENT respawn
  entry starts) and the doc comment claiming it was corrected; literal
  and `--build-arg` are the two real, working layers.

**Curated chipset widening (F6.6)** - DONE
- Realtek R8169/8168/8101/8125 added for Ethernet; WiFi breadth
  re-checked and found already substantially complete from F6.3's own
  pass (every mainstream vendor has one practical representative
  firmware file bundled). A bounded, named supported-hardware list --
  not an unbounded claim, as originally scoped.

---

## M6 — Firecracker/micro-VM smoke test

**Goal:** confirm the micro-VM kernel path (CONFIG_VIRTIO_MMIO, already
shipped per old T70) actually boots under Firecracker.
**Status: DONE** (2026-08-07) — WSL2 turned out to expose a genuine,
usable `/dev/kvm` on this Windows host, so no VirtualBox/QEMU
nested-virtualization fallback was needed. See STATE.md AD-034.

### Features

**Firecracker boot via nested virtualization (F7)** - DONE
- Since this dev machine is Windows without native `/dev/kvm`, test
  inside one of Docker, VirtualBox, or QEMU with nested virtualization
  enabled (user's own suggested approach).

---

## M7 — Release readiness

**Goal:** cnimbus ships a real, versioned release instead of every
binary reporting `"dev"`.
**Status: PARTIAL** — the SemVer mechanism, CI-validated release
workflow, and a pre-flight checklist are all done (STATE.md AD-035).
Cutting the first real `v0.1.0` tag is a deliberate, visible,
shared-state action deliberately left for the project owner to trigger,
not something automated on their behalf.

### Features

**SemVer versioning (F9)** - PARTIAL
- Real version tags, `-ldflags -X main.version=...` already wired to
  `git describe`; the tagging/release process itself (`release.yml`,
  `RELEASING.md`) is done and verified against a real dry-run build of
  every target it publishes -- **only pushing the actual `v0.1.0` tag
  remains**, by design.

---

## Future Considerations (explicitly post-v1.0)

- **Fleet signing + dm-verity** on top of the UKI/Secure Boot work done
  in M3 (old M6/T81 step 3) — UKI assembly, EFI-stub signing, and a
  real live certificate-enrollment + Secure-Boot-verified boot are done
  (see M3 above and STATE.md AD-041); fleet-wide key management and
  dm-verity remain genuinely future.
- AF_VSOCK as an `AGENT` transport for QEMU/KVM and Hyper-V (old M3).
- BusyBox applet minimization, ~400 built → ~26 actually used (old M5).
- Kernel security-baseline fragment that *asserts* posture at build time
  (old M11).
- Further dm-verity-ready block-layout hardening beyond the two-
  partition GPT raw-disk redesign (old M12).
- A local self-hosted CI runner doing real boots — optional, only if it
  turns out to be worth the setup cost; GitHub-hosted CI itself stays
  build-only regardless.
- OCI-based pieces/image distribution, replacing the current URL-prefix-
  plus-hashfile protocol — real content-addressed storage and a
  standard place to attach provenance/SBOM data. Not planned before
  there's something meaningful to attach.

See root [`ROADMAP.md`](../../ROADMAP.md) for architectural wrinkles
still open and ideas explicitly rejected.
