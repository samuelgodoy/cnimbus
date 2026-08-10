# Requirements

**Baseline:** 2026-08-06 · **Target release:** v1.0

Numbered, atomic, testable requirements. Every requirement has an ID, a
verification method, and an owner artifact. IDs are permanent — never
renumber; supersede instead (`REQ-X-nnn superseded by REQ-X-mmm`).

> **On "certification-grade":** cNimbus targets **no compliance
> framework** (see STATE.md AD-005). This document applies the *rigor* of
> infrastructure certification practice — traceable requirements,
> declared verification methods, evidence gates — without adopting any
> certification's control set. The goal is that a reviewer can answer
> "how do you know this works?" for every claim the project makes.

**Verification methods:** `T` Test (automated, repeatable) · `D`
Demonstration (real execution, evidence captured) · `A` Analysis
(reasoning/calculation from design) · `I` Inspection (code/doc review).

**Priority:** `M` Mandatory for v1.0 · `S` Should · `C` Could (post-1.0).

---

## REQ-IMG — Image artifact

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-IMG-001 | `build-disk` produces a bootable ISO9660+El Torito image for `FORMAT iso` | T,D | M |
| REQ-IMG-002 | `build-disk` produces a bootable GPT raw disk image for `FORMAT raw`, with a UEFI ESP and a separate SquashFS root partition | T,D | M |
| REQ-IMG-003 | The output image is written atomically — an interrupted build never leaves a partial file at the final path | T | M |
| REQ-IMG-004 | The image contains no interactive shell, login, or getty reachable by a console user | I,D | M |
| REQ-IMG-005 | File modes inside the SquashFS root are independent of the build host OS — an image built on Windows has identical modes to one built on Linux | T,D | M |
| REQ-IMG-006 | For `HARDBOOT`, the ISO carries an isohybrid MBR so `dd`-to-USB boots on legacy BIOS hardware | T,D | M |
| REQ-IMG-007 | Every image is accompanied by a `.lock` manifest recording the exact inputs that produced it | T | M |

## REQ-BOOT — Boot behavior

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-BOOT-001 | Stage 1 locates the boot medium by probing candidate devices and verifying the expected payload is present, not by accepting the first device that mounts | T,D | M |
| REQ-BOOT-002 | Any stage-1 failure halts the boot with a diagnostic on the console — it never proceeds to a silently-incomplete userspace | T,D | M |
| REQ-BOOT-003 | Boot succeeds on QEMU (BIOS and UEFI), VirtualBox, VMware, and Hyper-V, on amd64 | D | M |
| REQ-BOOT-004 | Boot succeeds on QEMU `-M virt` with UEFI on arm64 | D | M |
| REQ-BOOT-005 | A kernel panic or oops reboots the guest rather than hanging indefinitely | A,D | M |
| REQ-BOOT-006 | PID 1 is never selected by the OOM killer | I,D | M |
| REQ-BOOT-007 | For `HARDBOOT`, boot succeeds on at least one real physical machine from USB | D | M |
| REQ-BOOT-008 | Boot succeeds under Firecracker via the virtio-mmio path | D | S |

## REQ-NET — Networking

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-NET-001 | DHCP acquires an address and **renews the lease** for the lifetime of the guest | T,D | M |
| REQ-NET-002 | A static IP configuration takes precedence over DHCP when both are declared | T | M |
| REQ-NET-003 | DHCP on the primary interface is bounded in time — it cannot stall boot indefinitely | T,D | M |
| REQ-NET-004 | `localhost` resolves without external DNS | T,D | M |
| REQ-NET-005 | `FIREWALL` rules are applied before any declared service can accept a connection | A,D | M |
| REQ-NET-006 | `FIREWALL` rule text cannot inject shell commands, including via `ARG`/`--build-arg` substitution | T | M |
| REQ-NET-007 | Firewall rule failure behavior is explicitly selectable (`FIREWALL_ON_ERROR open\|closed`) and defaults are documented | T,D | M |
| REQ-NET-008 | superseded by REQ-NET-013 |  |  |
| REQ-NET-009 | For `HARDBOOT`, the image drives common physical Ethernet chipsets without additional configuration | D | M |
| REQ-NET-010 | For `HARDBOOT wifi`, the image associates with a WPA2-PSK network declared in the Nimbusfile and obtains an address over it | D | M |
| REQ-NET-011 | For `HARDBOOT wifi`, the regulatory domain is explicitly declared and applied | T,D | M |
| REQ-NET-012 | WiFi association failure is diagnosable from console output — it does not present as a generic "no network" | D | M |
| REQ-NET-013 | IPv6 is enabled by default; `FIREWALL6` auto-injects the minimal RFC 4890 ICMPv6 (Neighbor Discovery, MTU errors) before any user rule, so a `-P INPUT DROP` policy cannot silently break IPv6 address resolution the way it would for a plain `FIREWALL` (AD-047, AD-055) | T,D | M |

## REQ-SEC — Security posture

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-SEC-001 | No filesystem in the running image is simultaneously writable and executable by an unprivileged process | I,D | M |
| REQ-SEC-002 | Every tmpfs mount declares an explicit size bound | T,I | M |
| REQ-SEC-003 | A declared `USER` service runs with `no_new_privs` set | T,D | M |
| REQ-SEC-004 | `COPY --chmod` refuses setuid/setgid/sticky bits | T | M |
| REQ-SEC-005 | The AGENT bearer token, if any, is never world-readable on disk and never visible in `/proc/<pid>/cmdline` | T,D | M |
| REQ-SEC-006 | The WiFi pre-shared key, if any, is never world-readable in the image and is excluded from build logs | T,D | M |
| REQ-SEC-007 | The kernel is built with the declared hardening symbol set, and the build **fails** if any requested symbol did not survive `olddefconfig` | T,D | M |
| REQ-SEC-008 | BusyBox and iptables are built with stack-protector and FORTIFY_SOURCE | T,D | M |
| REQ-SEC-009 | The build container runs with dropped capabilities, `no-new-privileges`, and a PID limit | I,D | M |
| REQ-SEC-010 | Any network response consumed by the in-guest agent is size-bounded | T | M |

## REQ-SUP — Supply chain

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-SUP-001 | The kernel tarball's PGP signature is verified against a **pinned fingerprint**, and a fingerprint mismatch fails the build | T,D | M |
| REQ-SUP-002 | BusyBox and iptables tarballs are verified against pinned SHA-256 constants on every run, including cache hits | T,D | M |
| REQ-SUP-003 | `prepare` emits a `pieces.json` provenance record naming every source, version, hash, and the builder image digest | T,D | M |
| REQ-SUP-004 | `build-disk` refuses a pieces set whose provenance contradicts the Nimbusfile (arch, VGA, and — new — boot profile) | T | M |
| REQ-SUP-005 | `pieces.sha256` can be Ed25519-signed at `prepare` and verified at `build-disk` | T | M |
| REQ-SUP-006 | Plain-HTTP pieces sources are refused unless explicitly opted into | T | M |
| REQ-SUP-007 | Two `prepare` runs of the same inputs produce byte-identical `docker run` command lines | T | M |
| REQ-SUP-008 | Any binary firmware shipped in a `HARDBOOT` image is recorded in `pieces.json` with its upstream source and hash | T | M |
| REQ-SUP-009 | Distributed binaries report a real SemVer version, never `"dev"` | T | M |

## REQ-CLI — Host platform & interface

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-CLI-001 | `cnimbus` builds and runs on Windows amd64 and arm64 | T,D | M |
| REQ-CLI-002 | `cnimbus` builds and runs on Linux amd64, arm64, and riscv64 | T,D | M |
| REQ-CLI-003 | `cnimbus` builds and runs on macOS amd64 (Intel) and arm64 (Apple Silicon) | T,D | M |
| REQ-CLI-004 | Distinct failure classes exit with distinct, documented exit codes | T | M |
| REQ-CLI-005 | An explicitly-passed CLI flag always overrides the corresponding Nimbusfile directive | T | M |
| REQ-CLI-006 | Interrupting a long-running `prepare` removes the build container and temp trees | D | M |
| REQ-CLI-007 | Every Nimbusfile directive is documented in README and rejected with an actionable message when malformed | T,I | M |

## REQ-OPS — Runtime lifecycle

| ID | Requirement | Method | Pri |
|---|---|---|---|
| REQ-OPS-001 | Declared services are supervised and restarted with bounded backoff | T,D | M |
| REQ-OPS-002 | Restart backoff resets after a sustained healthy period | T,D | M |
| REQ-OPS-003 | `HEALTHCHECK` failure escalates SIGTERM → SIGKILL for a wedged process | T,D | M |
| REQ-OPS-004 | Shutdown honors a declared `STOPGRACE` window before SIGKILL | T,D | M |
| REQ-OPS-005 | A failed `required` VOLUME mount halts boot rather than silently writing to tmpfs | T,D | M |
| REQ-OPS-006 | Service stdout/stderr is captured to the declared log destination | T,D | M |
| REQ-OPS-007 | Live configuration can be delivered without rebuild or reboot via a declared `AGENT` transport | T,D | M |

## NFR — Non-functional budgets

Budgets are **declared targets with measurement obligations**, not
aspirations. A change that breaches a budget must either be reverted or
the budget consciously renegotiated and recorded in STATE.md.

| ID | Budget | Method | Pri | Measured actual (2026-08-06) |
|---|---|---|---|---|
| NFR-001 | VM-profile image (no COPY payload) ≤ **32 MiB** | T | M | **19.11 MiB** (20,039,680 B) — real `cnimbus build-disk` on amd64, kernel 7.1.7/BusyBox 1.36.1/iptables 1.8.8, `HARDBOOT none`. Comfortable margin. |
| NFR-002 | VM-profile kernel (`vmlinuz`) ≤ **12 MiB** | T | M | **3.31 MiB** (3,474,432 B) — same real `prepare` run. Comfortable margin. |
| NFR-003 | `HARDBOOT eth` image ≤ **64 MiB** | T | M | **Still unmeasured** — `HARDBOOT eth` kernel fragments aren't wired yet (F6.2's last sub-step, gated on F6.1). |
| NFR-004 | `HARDBOOT wifi` image ≤ **192 MiB** — dominated by firmware blobs; see RISKS.md R-004 | T | S | **Still unmeasured** — gated on F6.3/F6.4. |
| NFR-005 | Guest boot to entrypoint ≤ **3 s** on QEMU with hardware acceleration | D | S | **6.79 s measured, but under `-accel tcg` (software emulation), not hardware accel** — `-accel whpx` (this host's hardware backend, per `resolveAccel`'s Windows branch) fails outright with `qemu: WHPX: Unexpected VP exit code 4` on this dev machine. Real boot proven end-to-end (BIOS→kernel→initramfs→SquashFS root→ENTRYPOINT, real stdout captured) via a disposable one-off Nimbusfile with a static Go entrypoint printing a marker, timed from QEMU process launch to the marker line. The 3 s budget assumed hardware acceleration works, which it does not on this real host — **budget not yet evaluable as stated; WHPX failure needs its own investigation** (folded into Tasks.md V3, which already covers `--accel` boot-validation). |
| NFR-006 | `build-disk` peak RSS ≤ **512 MiB** regardless of image size | A,T | S | **36.03 MiB** (37,777,408 B) peak working set — real `build-disk` run against cached pieces, measured via `Process.PeakWorkingSet64`. Wide margin (~14x under budget). |
| NFR-007 | `build-disk` completes in ≤ **30 s** for a VM-profile image with cached pieces | T | C | **0.53 s** — real timed run against cached amd64 pieces. Wide margin. |

> **Baseline obligation — NFR-001, 002, 006, 007 now have real measured
> baselines (2026-08-06)**, all comfortably inside budget; no
> renegotiation needed. NFR-003/004 remain unmeasured (gated on
> unimplemented `HARDBOOT eth`/`wifi` kernel wiring). NFR-005 surfaced a
> real, unrelated finding instead of a clean baseline: the Windows
> hardware-acceleration path (WHPX) that the budget assumes doesn't
> currently work on this dev machine, so the 3 s figure can't yet be
> validated as written — see Tasks.md V3.

---

## Traceability

Requirements map to backlog items in [Tasks.md](../../Tasks.md) and to
evidence in [VERIFICATION.md](VERIFICATION.md). A requirement with no
backlog item and no evidence is **unverified by default** — that is the
honest current state for most `D`-method rows.
