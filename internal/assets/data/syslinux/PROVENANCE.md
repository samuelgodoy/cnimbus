# Provenance: embedded syslinux binaries

`isolinux.bin` and `ldlinux.c32` in this directory are unmodified files
extracted from the official syslinux 6.03 release tarball, verified by
sha256 against a fresh download at the time this file was written
(2026-07-31):

- Upstream release: **syslinux 6.03**
- Tarball URL: `https://www.kernel.org/pub/linux/utils/boot/syslinux/syslinux-6.03.tar.xz`
  (kernel.org's own long-standing mirror of the syslinux project)
- Tarball SHA-256: `26d3986d2bea109d5dc0e4f8c4822a459276cf021125e8c9f23c3cca5d8c850e`

Per-file SHA-256 (both match the corresponding file inside the tarball
above exactly — these are prebuilt binaries the syslinux project itself
ships pre-compiled in the release tarball, not something rebuilt from
source here):

| File | Path inside the tarball | SHA-256 |
|---|---|---|
| `isolinux.bin` | `bios/core/isolinux.bin` | `c5e4e775a7aada9aa2b227806724c52c66625b88699b3f167b5ec690a7addb91` |
| `ldlinux.c32` | `bios/com32/elflink/ldlinux/ldlinux.c32` | `5cef9ad0d0ca04097262241686c6c3a7306ab9b9cdf24b9d4ee3b16af01a5af2` |

Unlike the kernel (PGP-verified at `prepare` time) and BusyBox/iptables
(SHA-256-pinned at `prepare` time, see `internal/compileagent`), these
two files are embedded directly into the `cnimbus` binary itself
(`internal/assets/assets.go`'s `//go:embed`), fetched once by a human
and committed rather than downloaded fresh on every `prepare` run --
there is no per-build fetch step to attach a hash check to. The
regression test this file's own hashes back
(`internal/assets/assets_syslinux_provenance_test.go`) is what actually
guards against these two files silently drifting from what this
document claims they are.
