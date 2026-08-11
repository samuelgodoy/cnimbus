# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/). Releases follow
[SemVer](https://semver.org/) -- see [RELEASING.md](RELEASING.md) for the
tagging process and [BUILD.md](BUILD.md) for the underlying
`-ldflags -X main.version=` mechanism `cnimbus version` reports. Each
release section below describes the tool's capabilities at that point,
not a chronological log of how each one was built -- see
`.specs/project/STATE.md` for the decision-by-decision history if
that's what you're after.

## [Unreleased]

## [v0.1.2] - 2026-08-10

### Added
- A responsive, SEO-friendly landing page (`index.html`), published via
  GitHub Pages at https://samuelgodoy.github.io/cnimbus/, with a
  quick-start terminal walkthrough, feature overview, and links to the
  repository and latest release.

### Security
- Fixed a shell-script-injection vector in `release.yml`: tag/repository
  values interpolated directly into a `run:` step are now passed through
  `env:` and referenced as shell variables instead.
- Added explicit least-privilege `permissions:` to `ci.yml`.

### Fixed
- `ci.yml` was triggering on `branches: [main]`, but the repository's
  default branch is `master` -- CI had never actually run on a push
  before this fix. Corrected the trigger and fixed everything CI's first
  real run surfaced: `gofmt` drift, and 21 `golangci-lint` findings
  (errcheck, gosec G115, staticcheck, unused).
- Fixed a build-reproducibility bug in the embedded `cnimbusagent`
  binaries: `-trimpath` alone doesn't produce byte-identical builds
  across separate invocations. The Go linker's build ID
  (`-buildid=`) and Go's automatic VCS/git-dirty-state stamping
  (`-buildvcs=false`) both needed to be pinned; verified byte-for-byte
  identical across independent clean builds.

## [v0.1.1] - 2026-08-10

### Security
- Updated `golang.org/x/crypto` (v0.41.0 -> v0.54.0) and
  `github.com/cloudflare/circl` (v1.6.2 -> v1.6.5), closing 16 advisories
  in the vendored SSH/crypto code paths (7 critical, 2 high, 6 moderate,
  1 low). No functional changes to cnimbus itself.

## [v0.1.0] - 2026-08-10

### CLI

- `init`, `prepare`, `build-disk`, `validate`, `clean`, `run`,
  `kv-serve`, `keygen`, `version`. `prepare` (needs Docker) compiles the
  kernel/BusyBox/iptables/WPA-supplicant "pieces"; `build-disk` (pure
  Go, no Docker ever) assembles a bootable image purely from those
  pieces. `run` boots an image locally via `--backend qemu` (default),
  `vbox`, `vmware`, or `hyperv`, and automates the host-port-forwarding
  setup for the first three.

### Nimbusfile directives

Image identity/network: `KERNEL`, `BUSYBOX`, `ARCH`, `VGA`, `HOSTNAME`,
`DHCP`, `IP`, `DNS`, `NTP`. Runtime: `ENV`, `USER`, `WORKDIR`, `LABEL`,
`EXPOSE`, `ARG` (with `${VAR}`/`$VAR` substitution and `--build-arg`),
`COPY`/`ADD` (with `--chmod`, directory/glob sources), `ENTRYPOINT`,
`CMD`, `SERVICE`, `RESTART`, `HEALTHCHECK`, `STOPGRACE`, `TMPSIZE`.
Storage: `VOLUME` (repeatable, `vfat`/`ext4`). Security: `FIREWALL`
(IPv4) and `FIREWALL6` (IPv6, RFC 4890 ICMPv6 auto-injected),
`FIREWALL_ON_ERROR`, `PIECESKEY`. Live config: `AGENT` (`http`,
`vboxguest`, `virtio-serial`, `aws-imds`, `ibm-imds`, `vmware`, each with
an optional `header`/interval). Bare metal: `HARDBOOT`
(`none`/`eth`/`wifi`/`eth+wifi`), `WIFI`/`WIFIPSK`/`WIFICOUNTRY`.

### Image formats and boot chain

`FORMAT iso` (El Torito, BIOS+UEFI on amd64, UEFI-only on arm64),
`FORMAT raw` (GPT: UEFI ESP + a separate SquashFS-root partition),
`FORMAT vhd` (a Fixed VHD wrapper around the raw layout, ready to attach
to Hyper-V). A two-stage boot (a small BusyBox initramfs that finds and
mounts a read-only SquashFS root) keeps the assembled image itself
dependency-free -- no `mksquashfs`, no `grub-mkrescue`, no `xorriso`,
just `go-diskfs` and the kernel's own EFI stub. `--secureboot`/`--uki`
sign the EFI-stub kernel or assemble and sign a full Unified Kernel
Image, in pure Go (no `sbsign`/`objcopy`, no Docker).

### Bare-metal boot (`HARDBOOT`)

Opt-in, inert by default. `eth` builds real Intel e1000/e1000e and
Realtek R8169-family Ethernet drivers; `wifi` builds a curated 802.11
chipset/firmware set plus a statically-linked WPA supplicant (BusyBox
has none); `eth+wifi` builds both. Real RTL8168h Ethernet firmware is
bundled and loads automatically on hardware that needs it.

### Supply chain

Kernel tarballs are PGP-verified against kernel.org's signer keys
(fetched live via Web Key Directory, fingerprint-pinned). BusyBox and
iptables tarballs are SHA-256 pinned. `pieces.sha256` covers every
compiled piece and can be Ed25519-signed (`cnimbus keygen`,
`--pieces-sign-key`/`--pieces-verify-key`/`PIECESKEY`) so `build-disk`
can authenticate who published a pieces source, not just verify its
integrity. `verifyFragmentsApplied` fails the kernel build by name if
Kconfig silently drops a requested hardening/driver symbol instead of
shipping a quietly-incomplete kernel.

### Validated

Real (not simulated) boots across QEMU (BIOS+UEFI, amd64/arm64),
VirtualBox, VMware Player/Workstation, Hyper-V (Generation 1 and 2),
Firecracker (via WSL2's `/dev/kvm`), real physical hardware, and a real
Proxmox VM -- see [README.md](README.md)'s "Validated" section for the
current per-platform matrix.

### Not done yet

No automated boot-test harness in CI (all boot validation is manual,
against real hypervisor installs); no riscv64 *guest* support (the CLI
itself cross-compiles and runs on riscv64 today, but a Nimbusfile can't
yet target it as the image architecture); `cnimbus run --backend
hyperv` needs an already-attached Hyper-V raw/VHD image built by
`build-disk` (it doesn't build one itself); WiFi real-radio association
is implemented and inspected but not yet confirmed against a real
access point. See [Tasks.md](Tasks.md) for the full current backlog.
