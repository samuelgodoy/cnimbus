# Design: HARDBOOT — bare-metal boot profile

**Status:** Implemented as designed below, with two corrections noted
inline (credential delivery in §6, and `wifi`'s relationship to `eth` in
§2). Real-hardware WiFi association remains the one open gap — see
Tasks.md's F6 entry. Kept as architecture reference, not just a
historical proposal: this is still an accurate description of how
`HARDBOOT` works.

> **Confidence marking.** Claims below are marked `[verified]` (checked
> against this codebase in-session), `[known]` (well-established Linux
> behavior, but not re-verified here), or `[ASSUMPTION]` (must be proven
> before implementation). Per the project's own discipline, an
> `[ASSUMPTION]` that reaches code without being closed is a defect.

## 1. The central insight: HARDBOOT is a `prepare`-time decision

`[verified]` The kernel is produced by `prepare` and consumed by
`build-disk`. Driver selection is a kernel-config change. Therefore
`HARDBOOT` cannot be a `build-disk`-only concern — it changes which
*pieces* are valid.

This is exactly the situation `ARCH` and `VGA` are already in, and the
project already built the machinery for it: `pieces.json` records what
the pieces were built for, and `build-disk` refuses a mismatch with an
actionable message.

**Design decision D1:** `HARDBOOT` follows the `VGA` precedent exactly —
read at `prepare`, recorded in `pieces.json` as a `boot_profile` field,
and compared at `build-disk`. No new mechanism is invented. This gets
HB-P-001/002 nearly for free and keeps one consistent mental model for
"directives that change the kernel."

**Consequence:** a user switching a Nimbusfile from VM to `HARDBOOT wifi`
must re-run `prepare`. The mismatch error must say that explicitly,
rather than reporting an abstract provenance conflict.

## 2. Profile layering in kernel fragments

`[verified]` Fragments live in `internal/assets/data/kconfig/` and are
merged, then every requested symbol is asserted to have survived
`olddefconfig` — a build **fails** if a symbol was silently dropped.

Proposed additions, layered rather than forked so the VM profile is
untouched:

| Fragment | Applied when | Contents |
|---|---|---|
| `baremetal-eth.fragment` | `eth`, `wifi`, `eth+wifi` | Real Ethernet chipset drivers + their vendor gating symbols |
| `baremetal-wifi.fragment` | `wifi`, `eth+wifi` | 802.11 core stack, chipset drivers, regulatory support, firmware loader config |

**`eth+wifi` (added post-baseline, AD-042):** exactly the union of the two
rows above — no new fragment, no new symbol. It exists as its own
`HARDBOOT` value distinct from `wifi` purely so a Nimbusfile can say "I
want both drivers" explicitly, since `wifi` alone already silently builds
both (undocumented at the directive level until this addition). Both
fragments merge into the *same* `merge_config.sh` pass, in the same order
`wifi` alone already uses (`baremetal-eth.fragment` before
`baremetal-wifi.fragment`, both before `security-baseline.fragment`) —
verified by a real build showing both fragments' own asserted symbols
(`CONFIG_E1000`/`CONFIG_E1000E`/`CONFIG_R8169` from the eth side,
`CONFIG_CFG80211`/`CONFIG_MAC80211`/the curated chipset drivers from the
wifi side) landing `=y` in the one resolved `.config`, with T66's
`checkMergeConfigConflicts` confirming neither fragment silently
overrides a symbol the other one set.

`[verified]` The existing `agent-vmware.fragment` is already an opt-in,
conditionally-applied fragment — the mechanism for conditional
application exists and should be reused, not rebuilt.

**Open — must resolve before coding (spec.md Q1, Q6):** every symbol
added needs its dependency chain traced *including enclosing
`menuconfig`/`if` blocks*, and needs confirmation it can be `=y` in a
module-less kernel. Budget for at least one failed build per driver
family. This project's history says the first attempt is usually wrong,
and the failure mode is a silently-absent driver, not a build error —
which is precisely what the symbol-assertion check exists to catch.

## 3. The firmware problem — the hardest part of this feature

`[known]` Most WiFi chipsets, and some Ethernet chipsets, require binary
firmware loaded from userspace-visible storage at driver probe time. A
few (notably some Atheros parts) carry firmware on-chip and need none.

`[known]` A **built-in** driver probes during kernel initialization —
before the stage-2 SquashFS root is mounted. Firmware placed in the
stage-2 root is therefore plausibly requested *before it exists*.

Three candidate placements:

| Option | Mechanism | Kernel size | Ordering risk |
|---|---|---|---|
| **A** | Blobs compiled into `vmlinuz` at kernel build time | Grows `vmlinuz` by the full blob set | None — always present |
| **B** | Blobs in the stage-2 SquashFS root | Unchanged | **High** — likely too late for built-in drivers |
| **C** | Blobs in the stage-1 initramfs | Unchanged | Low — initramfs is the root filesystem during early boot |

**Design decision D2 (proposed): Option C.** It matches how mainstream
Linux distributions solve exactly this problem, keeps `vmlinuz` small
enough to stay bootable by the existing loaders, and fits this project's
existing two-stage architecture with no structural change — stage 1
already exists and already carries files that must be present early.

`[ASSUMPTION]` That the kernel's firmware loader will find blobs in the
stage-1 initramfs for a **built-in** driver, at the point that driver
probes. This is the single highest-risk assumption in the feature.
**Verification obligation:** prove it with one driver family on real
hardware before building the general mechanism. If it fails, fall back
to Option A and accept the `vmlinuz` growth — and re-check that the
oversized kernel still boots through both the BIOS and UEFI paths,
including the El Torito size ceiling this project has already hit once.

**Design decision D3:** blobs are a **curated set**, never the whole
upstream firmware collection. The set is an explicit, reviewed list tied
to the declared supported-chipset families, each entry recorded in
`pieces.json` with source and hash (HB-P-003). "Ship everything and let
the kernel pick" is incompatible with both the size budget and the
provenance requirement.

## 4. The userspace supplicant — a fourth piece

`[known]` Associating with a WPA2-PSK network requires a **userspace
supplicant**. The kernel's 802.11 stack handles the radio and the
cipher, but the authentication handshake is driven from userspace.

`[known]` BusyBox does **not** include a WPA supplicant. `[verified]`
This project ships exactly three built pieces today — kernel, BusyBox,
iptables — plus the embedded agent.

**Therefore `HARDBOOT wifi` requires building a fourth piece.** This is
the largest hidden cost in the feature and the reason WiFi is a
milestone rather than a fragment edit.

**Design decision D4:** the supplicant is built by `prepare` following
the **exact existing pattern** established by iptables `[verified]`:
hash-pinned source tarball, verified on every run including cache hits,
built statically with the same hardening flags, installed into the
pieces set, recorded in `pieces.json`, covered by `pieces.sha256`.

No new supply-chain path is introduced — this is deliberate. The
existing pattern is the project's strongest area (evidence level E3
throughout) and a new binary entering every bare-metal image must not be
the one thing that bypasses it.

`[ASSUMPTION]` That the supplicant can be built with internal crypto
(no OpenSSL) and PSK-only feature flags at acceptable size. If it cannot,
the size budget NFR-004 needs renegotiation before implementation, not
after.

## 5. Nimbusfile surface

`[verified]` Directives are one `case` arm in the parser's `apply()`
switch: validate, error with a message naming the directive, assign.
Repeatable directives append. Every directive gets a line in the package
doc comment that doubles as reference documentation.

```
HARDBOOT wifi
WIFI      MyNetwork
WIFIPSK   ${WIFI_PSK}
WIFICOUNTRY BR
```

Validation rules, all fail-closed and all unit-testable:

- `HARDBOOT` ∈ {`none`,`eth`,`wifi`,`eth+wifi`}; anything else lists the
  valid set
- `WIFI*` without `HARDBOOT wifi` or `HARDBOOT eth+wifi` → error (HB-F-006)
- `HARDBOOT wifi` or `HARDBOOT eth+wifi` without `WIFI` → error (HB-F-007)
- `WIFICOUNTRY` mandatory under `wifi`/`eth+wifi`, ISO 3166-1 alpha-2
  (HB-F-008)
- `WIFIPSK` accepts a passphrase **or** a 64-hex-char derived key
  (HB-F-010)

`[verified]` The `FIREWALL` directive already has a validator that
rejects shell metacharacters specifically because directive text is
spliced into a generated root shell script and `ARG` substitution can
carry attacker-controlled input. **The SSID and PSK reach a generated
config file by the same route and need the same treatment** — an SSID is
arbitrary user-controlled text and must never be able to break out of
its quoting.

## 6. Credential handling

The PSK is a real secret entering a file people commit to version
control. Three layers, in order of preference:

1. **Build-time injection (recommended).** `WIFIPSK ${WIFI_PSK}` via
   `--build-arg`, so the Nimbusfile is committable and the secret is
   not (HB-F-011).
2. **Literal in Nimbusfile.** Supported for a lab, documented as
   unsuitable for anything else.

**Correction found during implementation:** the originally-planned
third layer, runtime delivery via `AGENT` (so the image would carry no
baked-in credential at all), does not actually work — `wlan0` bring-up
runs synchronously inside `rcS`'s `::sysinit:` stage, which BusyBox init
always finishes *before* starting any `::respawn:` entry, including
`AGENT`'s own polling loop. There is no credential yet by the time
`wpa_supplicant` needs one. Making this work would mean a real
architecture change (a one-shot fetch mode invoked from `rcS` itself)
and is not currently planned.

Regardless of layer, in the produced image:
- `[verified]` The project already routes mode-sensitive files through
  the stage-1 tmpfs shadow path with an explicit `chmod`, precisely
  because SquashFS file modes otherwise inherit from the build host and
  a Windows-built image would ship them world-readable. **The supplicant
  config must use that same path** — this is not optional, and HB-S-001
  requires verifying it by reading the produced image.
- The PSK must never reach a process command line `[verified]` — the
  project already solved this for the AGENT bearer token after finding
  it visible via `/proc`, and the same reasoning applies.

**Design decision D5:** prefer a pre-derived PSK over a passphrase where
possible. It does not make the value non-secret, but it avoids baking a
passphrase — which users reuse across systems — into an artifact.

## 7. Boot-time behavior

Sequenced into the existing `rcS` network stage:

1. If profile is `wifi` and a WiFi device is present → bring up the
   interface, start the supplicant against the generated config,
   wait bounded (HB-N-004), then run the existing DHCP/static logic
   against `wlan0`.
2. Ethernet, if linked, follows today's path unchanged.
3. Each failure mode emits a **distinct** diagnostic (HB-F-012): no
   device, firmware missing, no AP found, auth rejected. A single
   "network failed" line is not acceptable — `[verified]` this project
   already learned that lesson when an arm64 boot failure presented as
   an opaque device-lookup panic and cost a full diagnostic cycle.
4. `[verified]` The firewall already applies before any service can
   listen, and that ordering must hold on the WiFi path too — an image
   that associates before its firewall is up is a real exposure, and the
   ordering guarantee is currently an `A`-method (analysis) claim that
   this feature makes worth re-demonstrating.

## 8. Risks this design carries

| Risk | Impact | Mitigation |
|---|---|---|
| Firmware-in-initramfs assumption wrong (D2) | Redesign mid-implementation | Prove with one driver family first, before general work |
| A required driver is module-only | Breaks the module-less kernel property | Verify per family, early; drop unsupportable families from the curated set |
| Firmware blobs blow the size budget | NFR-004 breach | Measure before committing; curate aggressively; consider per-family opt-in |
| Supplicant needs OpenSSL | Large new dependency in every bare-metal image | Verify internal-crypto build first |
| Bare-metal boot needs hardware we can't reproduce | Cannot verify to E4 | Accept a named, limited supported-hardware list rather than an unbounded claim |
| Attack surface grows substantially | Contradicts the minimal-image north star | Profile is opt-in and inert by default (HB-F-002, HB-N-003) |

## 9. Implementation sequencing

Ordered so the riskiest assumption is tested first and cheaply.

1. **Spike:** one Ethernet family, `HARDBOOT eth`, real build, real USB
   boot on one physical machine. Proves isohybrid + a real driver + the
   profile plumbing end to end.
2. **Plumbing:** the directive, validation, `pieces.json` profile field,
   mismatch enforcement, and the byte-identical-default guarantee.
3. **Firmware spike:** one WiFi family only. Closes D2 — the highest-risk
   assumption — before any general mechanism is built.
4. **Supplicant:** fourth piece, built to the existing supply-chain
   pattern.
5. **Credentials:** all three delivery layers, with image-level
   verification.
6. **Widen:** additional curated chipset families, each with its own
   evidence.

Steps 1 and 3 are deliberately spikes: their purpose is to **invalidate
assumptions cheaply**, and either one failing changes the design rather
than the schedule.
