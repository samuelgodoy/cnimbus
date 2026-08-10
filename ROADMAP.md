# Roadmap

This file holds forward-looking architectural notes and the record of
ideas explicitly considered and rejected. For the milestone-by-milestone
path to v1.0 and current status, see
[`.specs/project/ROADMAP.md`](.specs/project/ROADMAP.md). For the
decision-by-decision history (what was built, why, how it was verified),
see [`.specs/project/STATE.md`](.specs/project/STATE.md).

## Known architectural wrinkles (accepted, not urgent)

- **Two kernel command lines, one intent.** The compiled-in
  `CONFIG_CMDLINE` (baked into `vmlinuz` by `prepare`) and the generated
  isolinux `APPEND` line (written by `build-disk`) are two separately
  maintained string literals. A boot parameter added to one doesn't
  reach `FORMAT raw`/arm64 images, which only get the compiled-in half.
  Fix: single-source the cmdline as its own piece, published alongside
  `vmlinuz` and read by both writers. Not yet done.
- **The vendored SquashFS writer (`go-diskfs`) can't create symlinks.**
  This is why `bin/`, `sbin/`, `usr/bin/`, `usr/sbin/` are tmpfs
  recreated at every boot instead of part of the immutable root (see
  README's "Two-stage boot" section for the full explanation). Patching
  third-party code with no regression tests of its own is judged not
  worth the risk; the tmpfs workaround already covers every practical
  case.

## Future work (post-v1.0)

See `.specs/project/ROADMAP.md`'s own "Future Considerations" section
for the current list (AF_VSOCK as an AGENT transport, BusyBox applet
minimization, fleet signing + dm-verity, a security-baseline kconfig
fragment that asserts hardening posture at build time, OCI-based pieces
distribution, an optional self-hosted CI runner for real boot tests).

## Explicitly rejected (recorded so they aren't re-litigated)

- **LSM (AppArmor/SELinux).** One workload, no policy-authoring surface
  in a Nimbusfile, nowhere immutable-friendly to store a policy.
- **Per-service cgroup memory/CPU limits.** The hypervisor-assigned
  vCPU/RAM allocation is already the resource boundary for a
  single-tenant appliance.
- **NUMA, memory/CPU hotplug, VIRTIO_IOMMU, VIRTIO_PMEM, SR-IOV/VFIO
  passthrough.** Not a fit for sub-100MB, short-lived, single-purpose
  VMs.
- **eBPF/XDP**, in the guest or on the host's SLIRP netdev. No netdev to
  attach to, no in-guest loader, and it would add a JIT+verifier to an
  image whose entire security story is "there is nothing here."
- **FIPS 140-3 validation.** Real organizational cost, no current
  customer/program requires it.
- **musl instead of glibc.** Neither BusyBox nor the bundled iptables
  use glibc's NSS resolver, so the usual static-glibc caveat doesn't
  apply here — no benefit to switching.
- **Aggressive PIE for the static BusyBox/iptables binaries.**
  Diminishing returns beyond the stack-protector/FORTIFY/RELRO flags
  already applied; these two small C programs' defense is minimal
  attack surface, not exploit-hardening of a large one.
- **Isohybrid MBR for `FORMAT iso`.** `FORMAT raw`'s GPT+ESP layout is
  this project's real USB/bare-metal path; adding MBR boot code to the
  ISO writer as well was judged not worth the added complexity.
- **Patching the vendored SquashFS writer's symlink gap directly** — see
  "Known architectural wrinkles" above.
