# VOLUME example

Shows the `VOLUME` directive: mounting a disk the hypervisor already
attached to the VM, for state that survives a reboot. `cnimbus` never
formats this disk -- it must already be formatted before the VM boots
(this example uses FAT/`vfat`, the default; add `ext4` as a third word
on the `VOLUME` line if you'd rather pre-format as `ext4` instead), and
if the mount fails for any reason, boot continues without it (nothing
gets written to the device by cnimbus itself either way). `VOLUME` is
repeatable if you need more than one disk.

## 1. Create and pre-format the disk (once, from the host)

VirtualBox, on the host:

```bash
VBoxManage createmedium disk --filename data.vdi --size 256 --format VDI
VBoxManage storageattach <vm> --storagectl SATA --port 1 --device 0 --type hdd --medium data.vdi
```

Then, from *inside* any Linux VM that can see the disk once (even a
throwaway one), format it FAT and detach:

```bash
mkfs.vfat /dev/sdb   # or whichever device the disk shows up as
```

QEMU: `-drive file=data.img,if=virtio,format=raw` after creating and
formatting `data.img` the same way (`qemu-img create data.img 256M`,
then format it FAT from any Linux guest/host with `mkfs.vfat`).

## 2. Build and attach

```bash
cnimbus build-disk -f Nimbusfile
```

Attach the pre-formatted disk as a second drive alongside the ISO, and
boot. Depending on the hypervisor and boot order, the disk usually
shows up as `/dev/vda` (virtio) or `/dev/sda`/`/dev/sdb` -- adjust the
`VOLUME` line in the Nimbusfile to match, then rebuild.

## 3. Confirm persistence

Boot once, check the serial/VGA console for the `cat`'d log, power off
(a clean ACPI shutdown, not a hard kill), boot again -- the second
boot's log line should be appended after the first's, proving the file
survived on the actual disk and wasn't RAM-backed.

## Notes

- Never point `VOLUME` at the boot device itself (the ISO/raw image
  cnimbus just built) -- it's a second, separate disk.
- If `/mnt/data/log.txt` never accumulates lines across reboots, the
  mount is silently failing (wrong device name, not FAT, or not
  attached) -- check the boot console for cnimbus's own `mount ... ||`
  fallback message, which names the device and mountpoint it tried.
