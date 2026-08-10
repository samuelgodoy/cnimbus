# hardboot-eth

Boots a real, physical wired-Ethernet chipset (Intel e1000/e1000e
family) instead of the VM-only `virtio-net` driver, and produces a raw
disk image meant for a USB stick or bare-metal boot rather than a VM.

## Build

`HARDBOOT eth` changes which kernel gets compiled, so `prepare` needs
the same flag -- not just the Nimbusfile:

```bash
cnimbus prepare --hardboot eth --out ./pieces
```

Then build the image itself:

```bash
GOOS=linux GOARCH=amd64 go build -o helloserver ../../cmd/helloserver
cnimbus build-disk -f Nimbusfile --pieces ./pieces
```

This produces `cnimbus-hardboot-eth-demo.img`, a GPT raw disk.

## Two ways to boot it

**Virtual pre-check (cheap, does this today):** QEMU's and
VirtualBox's default emulated NIC is a real Intel e1000-family chip, so
you can boot the exact same driver stack under either before touching
real hardware:

```bash
cnimbus run --backend qemu --format raw cnimbus-hardboot-eth-demo.img
# or
cnimbus run --backend vbox --format raw cnimbus-hardboot-eth-demo.img
```

This proves the kconfig/driver combination isn't obviously broken, but
it does **not** exercise the real USB-boot path (QEMU/VirtualBox boot
the disk image directly, never through a real MBR/USB enumeration), or
any chipset beyond e1000/e1000e.

**The real deliverable -- a physical USB boot:**

```bash
# Linux/macOS
sudo dd if=cnimbus-hardboot-eth-demo.img of=/dev/sdX bs=4M status=progress conv=fsync

# Windows: use Rufus, "DD Image" mode, pointed at the .img file
```

Boot the target machine from that USB stick via its UEFI boot menu
(not legacy BIOS -- `raw`'s ESP is UEFI-only). Watch the console (serial
if available, or pass `--vga` to `cnimbus prepare` for a framebuffer
console) for `e1000`/`e1000e` driver-probe lines and `NIC Link is Up`,
then a DHCP lease and `helloserver` starting.
