# Verification & Validation Plan

**Baseline:** 2026-08-06

This is the document that answers *"how do you know?"* for every claim
cNimbus makes. It exists because this project's single largest quality
risk is not bugs — it is **unverified belief**: roughly 40 hardening
changes are merged, unit-tested, and have never been executed on a real
boot.

## Evidence levels

Every requirement carries an evidence level. Levels are **ordered** —
a higher level subsumes the lower ones.

| Level | Meaning | What counts |
|---|---|---|
| **E0 — None** | Asserted only. Code exists, nothing proves it. | — |
| **E1 — Inspected** | A human or reviewer read the code/config and reasoned it correct. | Review note, doc comment with rationale |
| **E2 — Unit** | An automated test exercises the logic in isolation. | `go test` case name |
| **E3 — Artifact** | A real build ran and the produced artifact was inspected. | `prepare`/`build-disk` log + byte-level assertion |
| **E4 — Boot** | A real guest booted on a real hypervisor and behaved as required. | Captured serial log with the specific evidence line |
| **E5 — Matrix** | E4 reproduced across every platform the requirement claims. | One serial log per platform in the claim |

**Rule:** a requirement whose method includes `D` (Demonstration)
**cannot** be marked satisfied below E4. Unit tests do not close a `D`
requirement — they only reduce the chance E4 fails.

## Why E2 is not enough here — three worked examples

These are real findings from this project's own history. They are the
argument for the whole document.

1. **`docker run --cap-add`** — the comma-joined form (`--cap-add=A,B,C`)
   reads correctly, passes review, and is wrong. Docker accepts one
   capability per flag occurrence. Only a real `prepare` run surfaced it.
   *E1 and E2 both passed. E3 caught it.*
2. **`CONFIG_FUSION_SAS`** — the symbol's own `depends on` line is
   satisfied, yet the symbol silently vanished from the built config
   because an enclosing `menuconfig FUSION ... if FUSION ... endif`
   block adds an invisible dependency. *Only a real kernel build caught
   it.*
3. **`/dev/sr0` on arm64** — the ISO boot-media probe was correct on
   amd64 and could never work on QEMU `-M virt`, where the ISO attaches
   as a whole virtio-blk disk. *Only a real arm64 boot caught it — and
   it was a kernel panic, not a subtle degradation.*

The pattern is consistent: **the failure modes this project actually
suffers from are invisible below E3.**

## Verification methods, defined

- **T — Test.** Automated, repeatable, runs in `go test`. Must assert on
  behavior or artifact bytes, not on the shape of an internal call.
- **D — Demonstration.** A real execution: a real `prepare`, a real
  `build-disk`, or a real boot. Requires captured evidence (log excerpt
  with the specific line that proves the claim), not "I ran it and it
  looked fine."
- **A — Analysis.** A written argument from the design, valid only where
  execution is impossible or the property is structural (e.g. ordering
  guaranteed by `sysinit` completing before `respawn`).
- **I — Inspection.** Code or documentation review against a stated
  criterion.

## Evidence capture standard

An E4 claim is only valid with:
1. **The command** that produced it, verbatim and re-runnable.
2. **The serial log excerpt** containing the specific evidence line —
   not the whole log, and not a summary of it.
3. **The negative control** where one is meaningful: what the log looked
   like *before* the fix, or what a failing case produces.
4. **Platform and version**: hypervisor, firmware (BIOS/UEFI/OVMF build),
   guest arch, kernel version.

Evidence for closed work lives in git history and, for the decisions
behind it, in `.specs/project/STATE.md`'s AD log -- not duplicated here.

## Current status by requirement group

Most groups have reached E4 (a real boot on real hardware/hypervisors)
or E5 (reproduced across the whole platform matrix a requirement
claims) via the real physical-hardware, Proxmox, QEMU, VirtualBox,
VMware, Hyper-V, and Firecracker boots recorded in STATE.md. Genuinely
still open, by requirement group:

| Group | Still open |
|---|---|
| REQ-NET | WiFi association (REQ-NET-010..012) needs a real radio + AP -- no WiFi hardware available |
| REQ-BOOT | riscv64 as a guest `ARCH` (REQ-BOOT-008 covers Firecracker only, already E4) |
| REQ-CLI | macOS (Intel + Apple Silicon) and Windows-on-Arm host validation -- cross-compiles clean, not run on real hardware of either kind |

Every other group (REQ-IMG, REQ-SEC, REQ-SUP, REQ-OPS, NFR) is at its
target evidence level.

## Verification campaigns still open

| Campaign | Discharges | Depends on |
|---|---|---|
| **WiFi** | REQ-NET-010..012, REQ-SEC-006 | A physical machine with a supported WiFi chip + a test AP |
| **Host platform matrix (remainder)** | REQ-CLI-001..003 | A real macOS host (Intel + Apple Silicon); Windows-on-Arm is out of scope by product decision, no hardware planned |

## What is deliberately NOT verified in CI

GitHub-hosted CI never boots an image (standing decision, STATE.md
AD-007). CI's verification role is bounded to: compile, vet, lint, unit
and artifact-structure tests, cross-compile matrix, and embedded-source
drift. Every `D`-method requirement is discharged **manually and
locally**, with evidence recorded in the closing ticket.

This is a conscious trade: it accepts slower, human-paced verification
in exchange for not running hypervisors in cloud CI. The mitigation is
this document — if verification is manual, it must at least be
*tracked*, or it silently doesn't happen. Which is exactly what
occurred across round 2.
