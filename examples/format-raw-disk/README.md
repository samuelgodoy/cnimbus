# FORMAT raw example

Shows `FORMAT raw`: instead of an ISO, `cnimbus build-disk` produces a
GPT-partitioned raw disk image with a UEFI-only ESP (no BIOS boot
path) -- for hypervisors/clouds that attach a virtual disk directly
rather than booting from optical media.

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o examples/format-raw-disk/helloserver ./cmd/helloserver
cd examples/format-raw-disk
cnimbus build-disk -f Nimbusfile
```

This produces `cnimbus-rawdisk-demo.img` (not `.iso` -- the output
filename's default extension follows `FORMAT` automatically; override
it with `-o <path>` if you want a different name).

## Boot

Attach `cnimbus-rawdisk-demo.img` as the VM's *disk* (not its optical
drive), with UEFI firmware enabled (VirtualBox: Settings -> System ->
Enable EFI; QEMU: `-bios OVMF.fd` or `-machine ... -drive
if=pflash,...`). BIOS/legacy boot mode will not boot this image --
`FORMAT raw` is UEFI-only.

## Notes

- `*.img` is gitignored at the repo root (see `.gitignore`) since it's
  a build output, same treatment as `*.iso`.
- Everything else about the Nimbusfile (directives, `COPY`, `ENV`,
  `AGENT`, ...) works identically under `FORMAT raw` as under the
  default `FORMAT iso` -- only the on-disk container format changes.
