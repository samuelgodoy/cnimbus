# cnimbus builder image.
#
# Runs identically on Windows/macOS/Linux hosts via Docker Desktop or
# Docker Engine — this is the one place where Kbuild (the Linux
# kernel's build system) actually executes, since it hard-depends on
# make/sh/bc/flex/bison and a Linux-targeting C compiler, none of which
# exist natively on Windows.
#
# This image only compiles the kernel and BusyBox — it does not
# assemble the final rootfs/initramfs/ISO. That happens afterward, in
# plain Go on the host, with no container involved (see
# internal/rootfs and internal/isoimage). So this image carries no
# packaging tools (no cpio, no mkfs.*, no grub/xorriso) — only what
# Kbuild and BusyBox's build actually need.
#
# Toolchain: plain, native GCC for both the kernel and BusyBox -- no
# cross-compiler package. This image is always run with `docker
# --platform linux/<arch>` matching the Nimbusfile's declared ARCH (see
# internal/dockerrun), so for an arm64 build this container simply *is*
# an arm64 container (Docker Desktop's Rosetta/QEMU emulation handles
# it transparently on a non-arm64 host) -- native gcc, no
# gcc-aarch64-linux-gnu needed at all. This replaced an earlier
# zig-cc-based approach: zig cc identifies as clang to Kbuild, and
# Kbuild's build system relies on GCC-or-full-LLVM=1 compiler behavior
# in a few corners (verified empirically -- clang outside LLVM=1 mode
# fails partway through the kernel build on a Kbuild-internal
# flag-detection quirk, unrelated to any missing zig flag).
#
# As minimal as this can get while still running Kbuild: the kernel's
# own build system hard-requires a POSIX shell to execute Makefile
# recipes, so a fully shell-less ("distroless") image is not an option
# here -- that's Kbuild's own architecture, not a choice made here.
# Nothing else is installed beyond what Kbuild and BusyBox's build
# actually invoke: no curl (Thunder fetches sources itself, in Go), no
# busybox (that's the target *artifact*, built for the image being
# assembled -- never a dependency of the container that builds it).
#
# Thunder (a Go binary, compiled by `cnimbus prepare` just before this
# image is built -- see internal/assets -- not a shell script) is the
# entrypoint: it downloads kernel/BusyBox sources itself (pure Go:
# net/http + archive/tar + a Go xz/bzip2 decoder) and only shells out to
# the actual build tools (make, gcc, and Kbuild's own bundled
# merge_config.sh).

# Pinned by both tag (readability) and digest (so a registry-side tag
# mutation can't silently swap out the build environment) -- verified
# with `docker inspect --format='{{index .RepoDigests 0}}' gcc:14-trixie`.
# Bump both together alongside gcc image releases.
#
# Deliberately NOT the newest available GCC: GCC 15 switched the
# compiler's *default* C dialect from gnu17 to gnu23 (confirmed
# empirically: `echo | gcc -dM -E -x c - | grep __STDC_VERSION__` prints
# 202311L on gcc:16.1.0, 201710L here on gcc:14-trixie). Under C23,
# `bool` becomes a language keyword, which collides with
# include/linux/types.h's own `typedef _Bool bool;` -- the top-level
# kernel Makefile forces `-std=gnu11` for most of the tree, but
# drivers/firmware/efi/libstub's own Makefile builds its KBUILD_CFLAGS
# from scratch and never re-adds a -std flag, so that one subtree falls
# through to the compiler's bare default. Caught for real only when a
# kernel version (6.9.4, pinned for the manual's example) was built
# fresh against gcc:16.1.0 for the first time -- it failed with
# "'bool' cannot be defined via 'typedef'" partway through
# drivers/firmware/efi/libstub/efi-stub-helper.o. Linux only started
# actively supporting C23 (removing this typedef) around the 6.15/6.16
# era, well past what 6.9.4-and-older can tolerate from a too-new
# default-dialect compiler. gcc:14-trixie is the newest release still
# defaulting to a pre-C23 dialect *and* still on the same Debian trixie
# base the apt-get pins below were resolved against (gcc:14.2.0 is
# bookworm-based instead -- verified via /etc/os-release -- which would
# silently invalidate every exact-version apt pin below); it stays
# compatible with every kernel version this project supports without
# needing a per-kernel -std override plumbed through Thunder.
FROM gcc:14-trixie@sha256:468b5b959b5f4025724d8549f45550c6ad38a32474733cbf130f8f2c1bc80805

# Pinned to the exact version this base image's own Debian trixie
# archive resolved at the time these were verified (T49) -- otherwise
# the digest-pinned FROM above creates an *appearance* of a fully
# reproducible toolchain that this layer quietly breaks: two `prepare`
# runs months apart, from the identical commit, could resolve different
# bison/flex/libelf-dev versions from Debian's rolling archive and
# produce a different vmlinuz, with pieces.json's builder-image-digest
# field (T37) staying identical across both and actively hiding the
# difference. `apt-get install pkg=version` hard-fails if that exact
# version is no longer available in the archive (Debian trixie's own
# stable-release archive keeps historical package versions available,
# unlike testing/unstable) -- a loud, actionable failure instead of a
# silent drift, which is the point.
#
# `bc` alone needs a per-arch pin: real V3 boot-validation (2026-08-06)
# found `cnimbus prepare --arch arm64` failing outright with "Version
# '1.07.1-4' for 'bc' was not found" -- Debian trixie's arm64 archive
# rebuilt that exact source version as the binNMU `1.07.1-4+b1`, never
# `1.07.1-4` itself, while amd64's archive has the reverse. The other
# six packages verified identical across both arches (`apt-cache
# madison` against a real `--platform linux/arm64` pull), so only `bc`
# needs the branch. TARGETARCH is BuildKit's automatic build arg,
# populated from `docker build --platform` (see internal/dockerrun).
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) BC_VERSION=1.07.1-4 ;; \
      arm64) BC_VERSION=1.07.1-4+b1 ;; \
      *) echo "cnimbus: unpinned bc version for TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apt-get update && apt-get install -y --no-install-recommends \
      bc="$BC_VERSION" \
      bison=2:3.8.2+dfsg-1+b2 \
      flex=2.6.4-8.2+b4 \
      perl=5.40.1-6 \
      findutils=4.10.0-3 \
      pkg-config=1.8.1-4 \
      libelf-dev=0.192-4 \
    && rm -rf /var/lib/apt/lists/*

# sbsigntool: F2's EFI-stub/UKI Secure Boot signing (`sbsign`), invoked by
# Thunder after Kbuild produces vmlinuz and again on the assembled UKI, only
# when the caller opts in via --secureboot/--uki (see cmd/cnimbus/build.go
# and internal/dockerrun). Verified identical across both arches (`apt-cache
# madison sbsigntool` against real `--platform linux/amd64` AND
# `--platform linux/arm64` pulls, both trixie/main -> 0.9.4-3.2) so, unlike
# `bc` above, no per-arch branch is needed here. No UKI-assembly tool
# (systemd's `ukify`) is installed: the UKI is built with `objcopy`
# (already present -- part of this image's own GCC/binutils toolchain) by
# adding PE sections directly, the same mechanism ukify itself uses under
# the hood, without pulling in ukify's own dependency chain (systemd-udev,
# Python) into an image that otherwise carries nothing beyond what Kbuild
# and BusyBox's build need.
RUN apt-get update && apt-get install -y --no-install-recommends \
      sbsigntool=0.9.4-3.2 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work
# Thunder's own container-local scratch dir for the BusyBox build tree
# (see thunder-src/cmd/thunder/main.go's localDir="/build" comment --
# it needs a real filesystem path, not /out, for BusyBox's install-tree
# symlinks). World-writable because the real UID that will write here
# is the *invoking host user's*, passed in later via `docker run --user
# <uid>:<gid>` (see internal/dockerrun.Run's dropPrivileges path) --
# unknown at image-build time. Without this, a non-root invoking user
# (any real Linux/WSL host) fails outright with "mkdir /build:
# permission denied" -- found via a real `cnimbus prepare` run on WSL2,
# never triggered on Windows/macOS Docker Desktop where the invoking
# process is uid 0 from the container's point of view.
RUN mkdir -p /build && chmod 0777 /build
COPY agent/thunder /opt/cnimbus/thunder
COPY kconfig/minimal.fragment /opt/cnimbus/kconfig/minimal.fragment
COPY kconfig/vm-amd64.fragment /opt/cnimbus/kconfig/vm-amd64.fragment
COPY kconfig/vm-arm64.fragment /opt/cnimbus/kconfig/vm-arm64.fragment
COPY kconfig/vga.fragment /opt/cnimbus/kconfig/vga.fragment
COPY kconfig/agent-vmware.fragment /opt/cnimbus/kconfig/agent-vmware.fragment
COPY kconfig/security-baseline.fragment /opt/cnimbus/kconfig/security-baseline.fragment
COPY kconfig/baremetal-eth.fragment /opt/cnimbus/kconfig/baremetal-eth.fragment
COPY kconfig/baremetal-usb.fragment /opt/cnimbus/kconfig/baremetal-usb.fragment
COPY kconfig/baremetal-wifi.fragment /opt/cnimbus/kconfig/baremetal-wifi.fragment
RUN chmod +x /opt/cnimbus/thunder

ENTRYPOINT ["/opt/cnimbus/thunder"]
