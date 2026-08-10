# Architecture

**Pattern:** Two-phase build pipeline (Docker-based compile phase +
pure-Go assembly phase) producing a single-purpose, distroless bootable
microVM image, plus a separate multi-backend run/orchestration layer.

## High-Level Structure

```
Nimbusfile ──► nimbusfile.Parse ──► *Nimbusfile
                                         │
   cnimbus prepare (needs Docker)        │    cnimbus build-disk (pure Go)
   ┌─────────────────────────┐           │    ┌──────────────────────────────┐
   │ Thunder (in container)  │           ▼    │ pieces.Resolve (hash+sig ok?)│
   │  kernel.org ──PGP verify│──► pieces/  ───►│ rootfs: stage1 (initramfs)   │
   │  busybox.net, netfilter │    <arch>/      │        + stage2 (SquashFS)  │
   │  .org                   │    vmlinuz,     │ isoimage.Write / rawimage.  │
   └─────────────────────────┘    busybox,     │ Write ──► bootable .iso/.img│
                                   iptables,    └──────────────────────────────┘
                                   pieces.json,                │
                                   pieces.sha256                ▼
                                                        cnimbus run --backend
                                                        {qemu,vbox,vmware,hyperv}
```

## Identified Patterns

### Two-stage boot (stage1 → stage2)
**Location:** `internal/rootfs/stage1.go` (initramfs), `squashfsroot.go`
(SquashFS root).
**Purpose:** minimize what's mutable/writable at runtime — only a small
BusyBox-only gzip'd cpio initramfs is writable-ish (tmpfs-backed exec
dirs), the actual application root is a read-only SquashFS image.
**Implementation:** stage1's `/init` probes boot media (ISO9660 loop
device or a raw GPT partition), mounts stage2's SquashFS directly (no
intermediate loop device on the raw-disk path since T76), then
`switch_root`s into it.

### Sentinel-error-plus-exit-code classification
**Location:** every package defines its own `Err*` sentinels;
`cmd/cnimbus/main.go`'s `exitCodeFor` maps them to distinct process exit
codes.
**Purpose:** let CI/tooling programmatically distinguish "never safe to
retry" (verification failure) from "safe to retry" (upstream fetch
error) without parsing error strings.
**Example:** `pieces.ErrHashMismatch`/`ErrSignatureInvalid`,
`compileagent.ErrVerification` → exit 4; `kernelinfo.ErrUpstreamFetch`
→ exit 5.

### Flag-over-Nimbusfile-directive precedence
**Location:** every subcommand that has both a Nimbusfile directive and
a same-purpose CLI flag (KERNEL, BUSYBOX, ARCH, VGA, PIECESKEY).
**Purpose:** let a one-off CLI override win without editing the
Nimbusfile, while the Nimbusfile stays the source of truth by default.
**Implementation:** `fs.Visit` populates a `passed map[string]bool` —
explicit-pass detection, not a zero-value comparison (so `--vga=false`
correctly overrides `VGA true`).

### Mirrored, independently-defined types across package boundaries
**Location:** `nimbusfile.Volume`/`rootfs.Volume`,
`nimbusfile.StaticIP`/`rootfs.StaticIP`, etc.
**Purpose:** keep `internal/rootfs` usable standalone (not coupled to
the Nimbusfile parser's own types) — a deliberate low-coupling choice,
documented inline, not accidental duplication.

### Provenance chain (pieces.json → pieces.sha256 → optional .sig)
**Location:** `internal/compileagent/provenance.go` (writes
`pieces.json` during `prepare`), `internal/pieces/pieces.go` (verifies
during `build-disk`), `internal/pieces/sign.go` (optional Ed25519 layer).
**Purpose:** let a `build-disk` consumer downstream of `prepare` (a
different machine, a CI artifact, a colleague) verify what actually went
into their image without re-running the build.

## Data Flow

### Build (prepare → build-disk)
1. `cnimbus prepare` resolves kernel version via `kernelinfo`, verifies
   the kernel.org tarball's PGP signature via WKD (pinned fingerprints),
   compiles kernel+BusyBox+iptables in a hardened, capability-dropped
   Docker container, writes `pieces.json` + `pieces.sha256` (+ optional
   `.sig`) to `<out>/<arch>/`.
2. `cnimbus build-disk` parses the Nimbusfile, resolves pieces (local dir
   or `http(s)://`, hash- and optionally signature-verified), assembles
   the stage-1 initramfs and stage-2 SquashFS root from pieces + the
   Nimbusfile's `COPY`/`ADD`/`SERVICE`/etc. directives, and writes the
   final `.iso` (`internal/isoimage`) or `.img` (`internal/rawimage`) —
   atomically, via a `.partial` + rename.

### Guest boot
1. Bootloader (isolinux for BIOS, or UEFI firmware directly) loads the
   kernel + stage-1 initramfs.
2. Stage-1 `/init` probes for the boot device, mounts stage-2's
   SquashFS root, `switch_root`s into it.
3. BusyBox `init` (PID 1) runs `rcS` (networking, firewall, mounts),
   then supervises the Nimbusfile's `SERVICE`/`ENTRYPOINT` under
   restart-backoff + `HEALTHCHECK` logic, with a `STOPGRACE` window on
   shutdown.

### Live config (AGENT)
The in-guest agent (`cmd/cnimbusagent`) polls one of several transports
(HTTP, AWS/IBM cloud metadata, VirtualBox guest properties, QEMU
virtio-serial, VMware backdoor protocol) on an interval and writes the
result verbatim to `/var/run/cnimbus-kv.json` for the workload to read —
no rebuild/reboot needed to change config.

## Code Organization

**Approach:** domain/feature-based `internal/` packages, each owning one
concern end to end (parse, build, verify, write) rather than generic
layers. See STRUCTURE.md for the full package map.

**Module boundaries:** intentionally loose — packages avoid importing
each other's types even when the shapes overlap (see "mirrored types"
above), trading a little duplication for each package staying usable in
isolation and testable without the whole tree.
