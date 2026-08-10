# Feature: HARDBOOT — bare-metal boot profile with Ethernet and WiFi

**Status:** Implemented — see [design.md](design.md) and Tasks.md's F6
entry for what's built and the one open gap (real WiFi-radio
association; no hardware available to test it). **Scope:** Large/Complex

## Problem

cNimbus images target virtual machines. The kernel config carries
virtio drivers and nothing else — correct and minimal for a hypervisor
guest, and completely non-functional on physical hardware, which has no
virtio bus, real Ethernet chipsets, and often only WiFi for connectivity.

Booting real hardware requires three things the current image does not
have: a bootable-from-USB image layout, real device drivers, and — for
WiFi — an entire userspace association stack that does not exist in
BusyBox.

Adding all of that unconditionally would destroy the project's core
property. The whole point of cNimbus is a small, single-purpose,
minimal-surface image; a generic driver set plus WiFi firmware blobs is
plausibly **5–10× the size** of a current VM image. So this capability
must be **opt-in and inert by default**.

## Solution shape

One Nimbusfile directive selects a boot profile. The profile is resolved
at **`prepare` time** (it changes the kernel), recorded in provenance,
and enforced at `build-disk` time.

```
HARDBOOT <profile>        # none (default) | eth | wifi | eth+wifi
WIFI <ssid>
WIFIPSK <psk>
WIFICOUNTRY <cc>
```

Each single value is exclusive to its own driver family: `eth` builds
only real Ethernet chipset drivers, `wifi` builds only the 802.11 stack
(no Ethernet chipset drivers at all), and `eth+wifi` (added after this
spec's original baseline, see AD-042/AD-044 in STATE.md) is the **only**
value that builds both, for a physical machine with both a wired NIC and
a WiFi radio. (An earlier version of this spec had `wifi` silently imply
`eth` -- that coupling was removed per an explicit product correction;
`eth+wifi` is now a genuinely distinct kernel/piece combination from
`wifi` alone, not just a documented spelling of the same thing.) Requires
`WIFI`/`WIFIPSK`/`WIFICOUNTRY` exactly like `wifi` does (same
HB-F-006/007/008 rules) — the WiFi driver stack with no network to
associate with is never what's meant, regardless of which value
requested it. Default `none` reproduces today's behavior byte-for-byte —
an existing Nimbusfile is unaffected.

## Requirements

### Functional

| ID | Requirement | Method |
|---|---|---|
| HB-F-001 | `HARDBOOT` accepts exactly `none`, `eth`, `wifi`, `eth+wifi`; anything else is a parse error naming the directive and listing valid values | T |
| HB-F-002 | Absent `HARDBOOT`, the produced kernel and image are byte-identical to today's VM profile | T |
| HB-F-003 | `HARDBOOT eth` adds real Ethernet chipset drivers for the common Intel, Realtek, and Broadcom families | D |
| HB-F-004 | `HARDBOOT wifi` adds the 802.11 stack, chipset drivers, required firmware, and a userspace supplicant | D |
| HB-F-005 | `HARDBOOT` (any non-`none`) produces an isohybrid ISO that boots from USB on legacy BIOS | D |
| HB-F-006 | `WIFI`/`WIFIPSK`/`WIFICOUNTRY` declared without `HARDBOOT wifi` is a hard error with an actionable message | T |
| HB-F-007 | `HARDBOOT wifi` declared without `WIFI` is a hard error — a WiFi image with no network is never what was meant | T |
| HB-F-008 | `WIFICOUNTRY` is mandatory for `HARDBOOT wifi` and validated as ISO 3166-1 alpha-2 | T |
| HB-F-009 | The guest associates with the declared WPA2-PSK network and obtains an address over `wlan0` | D |
| HB-F-010 | `WIFIPSK` accepts either a passphrase or a pre-derived 64-hex-char PSK | T |
| HB-F-011 | All three WiFi directives support `ARG`/`--build-arg` substitution so credentials need not be committed | T |
| HB-F-012 | WiFi association failure produces a specific console diagnostic distinguishing: no device, no firmware, no AP found, auth rejected | D |
| HB-F-013 | Ethernet and WiFi coexist — if both link, the existing DHCP/static precedence rules apply per interface, deterministically | D |
| HB-F-014 | `HARDBOOT eth+wifi` produces one kernel with both `baremetal-eth.fragment` and `baremetal-wifi.fragment` merged, and requires `WIFI`/`WIFIPSK`/`WIFICOUNTRY` exactly as `HARDBOOT wifi` does (added post-baseline, AD-042) | T |

### Provenance & supply chain

| ID | Requirement | Method |
|---|---|---|
| HB-P-001 | `pieces.json` records the boot profile the pieces were built for | T |
| HB-P-002 | `build-disk` refuses a `none`-profile pieces set for a `HARDBOOT` Nimbusfile, and vice versa, with an actionable message | T |
| HB-P-003 | Every firmware blob shipped is recorded in `pieces.json` with upstream source URL, version/commit, and SHA-256 | T |
| HB-P-004 | The supplicant source tarball is hash-pinned and verified on every run, including cache hits — same rule as BusyBox and iptables | T |
| HB-P-005 | Firmware blobs are covered by `pieces.sha256` | T |

### Security

| ID | Requirement | Method |
|---|---|---|
| HB-S-001 | The generated supplicant configuration is mode `0600` **in the produced image**, verified by reading the image, not the generator | D |
| HB-S-002 | The PSK never appears in build output, `prepare`/`build-disk` logs, or the `.lock` manifest | T |
| HB-S-003 | The PSK is never passed on a command line reachable via `/proc/<pid>/cmdline` | D |
| HB-S-004 | A documented path exists to supply the PSK at runtime via `AGENT`, producing an image that carries no credential | D |
| HB-S-005 | Adding drivers does not weaken the kernel hardening baseline — the security fragment still applies and is still asserted | T |

### Non-functional

| ID | Budget | Method |
|---|---|---|
| HB-N-001 | `HARDBOOT eth` image ≤ 64 MiB (NFR-003) | T |
| HB-N-002 | `HARDBOOT wifi` image ≤ 192 MiB (NFR-004) | T |
| HB-N-003 | `HARDBOOT none` image size unchanged from baseline, ±0 bytes | T |
| HB-N-004 | WiFi association completes within 20 s of `rcS` reaching the network stage, or fails loudly | D |

## Explicitly out of scope for v1.0

- **WPA-Enterprise / 802.1X / EAP.** Requires certificate handling, a
  much larger supplicant build, and a credential model this project has
  no story for. PSK only.
- **WPA3-SAE.** Deferred — verify supplicant support and driver
  coverage before promising it. WPA2-PSK is the interoperability floor.
- **Hidden SSIDs, roaming, band steering, multiple configured networks.**
  One network, declared, connected at boot.
- **Bluetooth, sound, graphics, USB peripherals.** `HARDBOOT` is about
  *reaching the network on real hardware*, not general desktop support.
- **Automatic chipset detection at build time.** The driver set is a
  fixed, curated list per profile. A machine outside it is unsupported,
  and says so at boot rather than hanging.
- **Secure Boot on bare metal.** Tracked separately under the UKI work.

## Open questions requiring verification before design is final

These are load-bearing and **must not be assumed**. Each has a
verification obligation before the design below is committed.

1. **Does any WiFi driver we need require `CONFIG_MODULES=y`?** This
   kernel is fully built-in (`=y` only) from `tinyconfig`. Most WiFi
   drivers are `tristate`, which *can* be `=y` — but some pull in
   dependencies that cannot. If any required driver is module-only, the
   whole "no modules" property is at risk and the design changes
   materially. **Verify by real build, per driver family.**
2. **When exactly can a built-in driver load firmware from a
   filesystem?** A built-in driver probes during kernel init, before the
   SquashFS root is mounted. Firmware placed in the stage-2 root is
   therefore plausibly *too late*. See design.md for the proposed
   answer; it needs empirical confirmation.
3. **Actual firmware sizes per chipset family.** NFR-004's 192 MiB is an
   estimate, not a measurement. Measure before committing.
4. **Minimum viable supplicant.** Confirm the supplicant can be built
   with internal crypto (no OpenSSL dependency) and PSK-only feature
   flags, and measure the resulting static binary.
5. **Regulatory database.** Confirm whether the signed regulatory
   database file is required for the chosen drivers, and what degrades
   without it.
6. **`tinyconfig` interaction.** Every symbol below must be checked for
   an enclosing `menuconfig`/`if` block that `allnoconfig` collapses —
   this project has been bitten three separate times.

## Acceptance

The feature is accepted when: HB-F-009 and HB-F-012 are demonstrated on
**real physical hardware** with a real access point, evidence captured
per VERIFICATION.md's E4 standard; HB-N-003 is proven by a byte-identical
default-profile artifact; and HB-S-001/003 are verified by inspecting the
produced image, not the code that produces it.
