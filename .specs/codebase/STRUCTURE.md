# Project Structure

**Root:** `cnimbus/` (the repository root — this doc doesn't assume any
particular local checkout path)

## Directory Tree

```
cnimbus/
├── cmd/
│   ├── cnimbus/            # main CLI binary (prepare, build-disk, run, ...)
│   ├── cnimbusagent/       # in-guest agent, embedded into every built image
│   ├── helloserver/        # example workload for examples/ and tests
│   └── thunder/            # in-container build orchestrator (runs inside `prepare`'s Docker step)
├── internal/
│   ├── agentruntime/       # shared in-guest agent runtime helpers
│   ├── assets/             # go:embed'd kconfig fragments, syslinux, thunder-src, agent binaries
│   ├── compileagent/       # kernel/BusyBox/iptables build + kernel.org PGP verification
│   ├── dockerrun/          # the only place `docker` is shelled out to
│   ├── isoimage/           # ISO9660 + El Torito (BIOS+UEFI) writer
│   ├── kernelinfo/         # resolves kernel.org symbolic versions
│   ├── nimbusfile/         # Nimbusfile parser
│   ├── pieces/             # fetch/hash-verify/sig-verify prebuilt pieces
│   ├── rawimage/           # GPT + UEFI-only raw disk writer
│   ├── rootfs/             # assembles stage-1 initramfs + stage-2 SquashFS root
│   └── secureboot/         # pure-Go EFI-stub signing (Authenticode) + UKI section assembly
├── examples/               # sample, buildable Nimbusfiles (one per directive/feature)
├── docs/manual/            # LaTeX user manual (cnimbus-manual.tex) + compiled PDF
├── Tasks.md                # active backlog (open items + compressed closed-ticket history)
├── ROADMAP.md               # architectural wrinkles + rejected ideas
└── .github/workflows/ci.yml
```

## Where Things Live

| Concern | Location |
|---|---|
| CLI entrypoints | `cmd/cnimbus/main.go` (dispatch) + one file per subcommand |
| Nimbusfile parsing | `internal/nimbusfile/nimbusfile.go` — single `Parse()` entry, one `apply()` switch |
| Stage 1 (init/initramfs) | `internal/rootfs/stage1.go` + `cpio.go` |
| Stage 2 (SquashFS root) | `internal/rootfs/squashfsroot.go` + `frompieces.go` |
| Disk image writers | `internal/isoimage/isoimage.go`, `internal/rawimage/rawimage.go` — selected by Nimbusfile `FORMAT` |
| The four run backends | `cmd/cnimbus/run.go` (qemu default + vbox) + `run_vmware.go`, `run_hyperv.go`, `run_vmdk.go`, `run_vhd.go` (Fixed VHD footer -- also reused directly by `build-disk --format vhd`, not just `run --backend hyperv`) |
| In-guest agent | `cmd/cnimbusagent/` — `main.go` dispatches on `kind`; `http.go` (HTTP + AWS/IBM IMDS), `vboxguest_linux.go`, `virtioserial.go`, `vmware_linux_amd64.go`/`.s` |
| Kernel/BusyBox/iptables build (Thunder) | `cmd/thunder/main.go` (runs in-container) calling `internal/compileagent/` (`kernel.go`, `busybox.go`, `iptables.go`, `fetch.go`, `verify.go`, `provenance.go`) |
| Embedded assets | `internal/assets/` — kconfig fragments, syslinux binaries, vendored Thunder source, prebuilt per-arch agent binaries |

## Module Organization

Feature/domain-based, not layer-based: each `internal/` package owns one
concern end to end (parsing, image writing, build orchestration) rather
than being split into generic `models`/`services`/`handlers` layers.
Cross-package coupling is deliberately minimized — see CONVENTIONS.md's
note on mirrored types (`nimbusfile.Volume`/`rootfs.Volume`) existing
independently rather than being shared via import.
