# cNimbus

**Vision:** A CLI that builds distroless, extremely hardened,
unikernel-shaped bootable microVM images from a from-scratch Linux
kernel + BusyBox + iptables, from a single declarative Nimbusfile, and
runs them across the widest practical range of virtualization tools and
host platforms.

**For:** platform/infra engineers who want a minimal, auditable,
disposable VM image for one workload, without adopting a full Linux
distro or a language-specific unikernel toolchain.

**Solves:** today's choices are either a full distro (large, mutable,
broad attack surface) or a unikernel framework tied to one language
runtime. cNimbus gives a generic, Nimbusfile-driven path to a small,
hardened, single-purpose VM image for *any* workload, buildable and
runnable across whatever virtualization tooling the user already has.

## Goals

- **Maximum guest-image compatibility**: the produced image should boot
  correctly on as many hypervisors/tools as practical (QEMU, VirtualBox,
  VMware, Hyper-V today; bare-metal/USB and Firecracker next) —
  measured by the backend support matrix in README.md staying complete.
- **Extreme hardening + minimal size, unikernel-style** — not driven by
  any named compliance framework. This is the north star for every
  size/surface trade-off in the backlog.
- **Cross-platform CLI**: `cnimbus` itself must build and run natively
  on Windows (amd64, arm64), Linux (amd64, arm64, riscv64), and macOS
  (amd64/Intel, arm64/Apple Silicon).
- **v1.0 = Tasks.md driven to zero.** See STATE.md's AD-009 — this is a
  hard checkpoint, not a soft target: once cleared, stop and ask the
  user what's next rather than self-directing further scope.

## Tech Stack

**Core:** Go 1.26.4, Docker (build-time only, for `prepare`), no
runtime dependency once `cnimbus` and a hypervisor are present. Full
detail in [`.specs/codebase/STACK.md`](../codebase/STACK.md).

**Key dependencies:** `go-diskfs` (disk image assembly),
`ProtonMail/go-crypto` (kernel.org PGP verification), `golang.org/x/sys`
(low-level guest-agent syscalls), `ulikunitz/xz` (pure-Go decompression).

## Scope

**v1.0 includes:**

- Boot-validation of all round-2 hardening work (Tasks.md Section A) —
  proven on real hypervisors, not just unit tests.
- arm64 CPU-mitigation Kconfig parity with amd64.
- The four `cnimbus run` backends unified behind a shared interface,
  validated against real VirtualBox/VMware/Hyper-V/QEMU (now available
  on the dev machine).
- Bare-metal/USB boot (isohybrid) with a broad, real-hardware-shaped NIC
  driver set.
- A Firecracker/micro-VM smoke test via nested virtualization.
- CLI cross-platform support extended to Linux/riscv64, with real
  validation on Windows arm64 and both macOS chip families.
- SemVer-based versioning and a real release process.

**Explicitly out of scope (v1.0 and beyond, per 2026-08-06 product
direction):**

- Any named compliance-framework target (FIPS, SOC2, FedRAMP, etc.).
- Hosting or publishing reference "pieces" builds (kernel/BusyBox/
  iptables binaries) — distribution stays distroless/self-build-only.
- GitHub-hosted CI booting an image under any hypervisor.

## Constraints

- **Dev environment:** Windows. `go test` must run inside a `golang`
  Docker container, not natively (antimalware deletes freshly-compiled
  test binaries).
- **CI:** GitHub-hosted CI stays build-only forever; a local
  self-hosted runner doing real boots is optional and welcome if set up.
- **Checkpoint discipline:** when Tasks.md reaches zero open items,
  stop and ask the user for direction — do not auto-continue into
  post-v1.0 milestones.
