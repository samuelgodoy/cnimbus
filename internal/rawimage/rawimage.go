// Package rawimage assembles a bootable raw disk image (FORMAT raw): a
// GPT disk with two partitions --
//
//  1. a small, fixed-size FAT32 EFI System Partition holding only the
//     kernel (as its own EFI stub) and the initramfs, in the same
//     /EFI/BOOT/<BOOTX64.EFI|BOOTAA64.EFI> + /EFI/BOOT/INITRD.IMG layout
//     internal/isoimage already uses for the ISO's El Torito EFI
//     payload -- the same kernel CONFIG_CMDLINE's "initrd=" path works
//     unmodified either way.
//  2. a second partition, typed as a generic Linux filesystem
//     (0FC63DAF-8483-4772-8E79-3D69D8477DE4), holding the SquashFS
//     root's raw bytes directly as the partition's own contents -- not
//     a file inside a filesystem. Stage 1 (internal/rootfs/stage1.go)
//     mounts it straight off the block device (e.g. /dev/vda2), no
//     losetup/mknod involved.
//
// This is T76's fix for the previous single-partition layout, which
// wrote SQUASHFS.IMG as a file inside the ESP itself: that made the ESP
// grow to the size of the entire root filesystem (multi-GiB "EFI System
// Partitions" are not a thing any tool expects), hit FAT32's 4 GiB
// file-size ceiling outright for a large enough root, and left no block
// device for a future dm-verity hash tree to attach to (a file on FAT
// has to be losetup'd first). Splitting the root onto its own partition
// fixes all three at once: the ESP is now small and fixed
// (see espSize, matching Microsoft's documented 200 MB minimum -- see
// T43), and the root partition boots directly off a real block device.
//
// UEFI only, deliberately: unlike an ISO (which needs El Torito's BIOS
// entry for older firmware), a raw disk's BIOS-boot equivalent needs
// its own MBR boot code plus a syslinux install -- meaningfully more
// machinery for a boot path modern hypervisors (and cloud/Proxmox
// templates, this format's actual target) default away from anyway.
package rawimage

import (
	"fmt"
	"io"
	"os"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/backend"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

// Image is everything rawimage.Write needs to assemble one raw disk.
type Image struct {
	Arch         string // "amd64" or "arm64"
	Vmlinuz      []byte
	InitramfsImg []byte // stage 1: the tiny cpio the kernel unpacks directly
	// SquashfsImgPath (T75) is a path to stage 2's finished image on disk
	// rather than its bytes -- see isoimage.Image.SquashfsImgPath's doc
	// comment for why (this file can legitimately be gigabytes; the
	// smaller, kernel/initramfs-bounded fields stay []byte).
	SquashfsImgPath string
}

const (
	sectorSize = 512
	// 1MiB alignment for both partitions' start sectors, the same
	// convention every mainstream partitioner (parted, gdisk, Windows'
	// own diskpart) uses.
	mibSectors     = (1024 * 1024) / sectorSize
	espStartSector = 2 * mibSectors
	// espSize is now fixed (T76): the ESP holds only the kernel+initramfs,
	// never the (potentially multi-GiB) SquashFS root, so there is no
	// longer a reason for it to grow with the image's content the way it
	// did before this ticket. 200 MiB matches Microsoft's own documented
	// EFI System Partition minimum for 512-byte-sector media (T43) --
	// achievable now that the root filesystem no longer lives inside it.
	// Still grows (see Write) in the pathological case of a kernel+
	// initramfs combination that wouldn't otherwise fit, so this is a
	// floor, not a hard ceiling.
	espSize = 200 * 1024 * 1024
	// FAT32 requires >=65525 clusters; espSize comfortably clears that
	// floor on its own.
	// headroom for the kernel+initramfs actually fitting inside the ESP
	// on top of FAT32's own bookkeeping overhead.
	espContentHeadroom = 16 * 1024 * 1024
	// room for the GPT headers/partition arrays at both ends of the disk.
	gptOverhead = 3 * 1024 * 1024
)

// Write assembles a raw disk image at path.
func Write(path string, img Image) error {
	efiBootFile, err := efiBootFileName(img.Arch)
	if err != nil {
		return err
	}

	squashfsSize, err := fileSize(img.SquashfsImgPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", img.SquashfsImgPath, err)
	}

	thisEspSize := int64(espSize)
	if content := int64(len(img.Vmlinuz)+len(img.InitramfsImg)) + espContentHeadroom; content > thisEspSize {
		thisEspSize = content
	}
	espSectors := roundUpSectors(thisEspSize)

	rootStartSector := roundUpToMiB(espStartSector + espSectors)
	rootSectors := roundUpSectors(squashfsSize)
	if rootSectors == 0 {
		rootSectors = 1 // a partition needs at least one sector even for an empty SquashFS image
	}

	diskSize := int64(rootStartSector+rootSectors)*sectorSize + gptOverhead
	// Rounded up to the next whole MiB: Azure's own documented upload
	// requirement is that a VHD's virtual disk size be aligned to 1 MiB
	// (confirmed against Microsoft's disk-upload-prep docs) -- this
	// raw image is the payload a Fixed VHD wrapper would eventually
	// carry unmodified (see ROADMAP.md's M7 milestone), so getting the
	// size right here means that wrapper never has to pad or reject a
	// misaligned image later.
	const mib = 1024 * 1024
	if rem := diskSize % mib; rem != 0 {
		diskSize += mib - rem
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing %s: %w", path, err)
	}
	d, err := diskfs.Create(path, diskSize, diskfs.SectorSize512)
	if err != nil {
		return fmt.Errorf("creating raw disk image: %w", err)
	}
	// No defer here, deliberately: go-diskfs's Disk.Close zeroes *d on
	// success (disk/disk.go), so a deferred Close after the explicit one
	// below would call Backend.Close on a nil backend and panic. Every
	// early return below leaves the fd open until process exit, which
	// only happens on a failure path this function is already reporting.

	table := &gpt.Table{
		LogicalSectorSize:  sectorSize,
		PhysicalSectorSize: sectorSize,
		ProtectiveMBR:      true,
		Partitions: []*gpt.Partition{
			{
				Index: 1,
				Start: espStartSector,
				End:   uint64(espStartSector + espSectors - 1),
				Type:  gpt.EFISystemPartition,
				Name:  "EFI System",
			},
			{
				Index: 2,
				Start: rootStartSector,
				End:   uint64(rootStartSector + rootSectors - 1),
				// T76: a generic Linux filesystem type, not EFISystemPartition
				// -- this partition holds the SquashFS root's raw bytes
				// directly (see writeRootPartition below), never formatted
				// as a filesystem go-diskfs itself knows about. Stage 1
				// mounts it directly as squashfs off the block device.
				Type: gpt.LinuxFilesystem,
				Name: "cnimbus-root",
			},
		},
	}
	if err := d.Partition(table); err != nil {
		return fmt.Errorf("writing GPT partition table: %w", err)
	}

	fs, err := d.CreateFilesystem(disk.FilesystemSpec{
		Partition:   1,
		FSType:      filesystem.TypeFat32,
		VolumeLabel: "ESP",
	})
	if err != nil {
		return fmt.Errorf("formatting ESP as FAT32: %w", err)
	}

	if err := fs.Mkdir("/EFI"); err != nil {
		return fmt.Errorf("creating /EFI: %w", err)
	}
	if err := fs.Mkdir("/EFI/BOOT"); err != nil {
		return fmt.Errorf("creating /EFI/BOOT: %w", err)
	}
	if err := writeESPFile(fs, "/EFI/BOOT/"+efiBootFile, img.Vmlinuz); err != nil {
		return err
	}
	if err := writeESPFile(fs, "/EFI/BOOT/INITRD.IMG", img.InitramfsImg); err != nil {
		return err
	}

	// T76: the SquashFS root is written as the second partition's own raw
	// contents, not as a file inside a filesystem -- go-diskfs's
	// filesystem writers (FAT32 here, and SquashFS itself elsewhere in
	// this project) have no "just dd this file's bytes onto a partition"
	// operation, so this goes straight through the disk's backend at the
	// partition's own byte offset instead.
	rootOffset := int64(rootStartSector) * sectorSize
	if err := writeRootPartition(d.Backend, rootOffset, int64(rootSectors)*sectorSize, img.SquashfsImgPath); err != nil {
		return err
	}

	if err := d.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}

func writeESPFile(fs filesystem.FileSystem, pathname string, data []byte) error {
	f, err := fs.OpenFile(pathname, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("opening %s in ESP: %w", pathname, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close() // the write error is what matters; already returned below
		return fmt.Errorf("writing %s in ESP: %w", pathname, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s in ESP: %w", pathname, err)
	}
	return nil
}

// writeRootPartition streams srcPath's bytes (the finished SquashFS
// image) directly onto the disk backend at [offset, offset+size), with
// no filesystem layer in between -- this partition's contents *are* the
// SquashFS image, byte for byte, exactly what stage 1 expects to
// `mount -t squashfs` directly off the block device. backend.Sub scopes
// writes to this partition's own byte range, so a bug here can never
// spill into the ESP or the GPT metadata around it.
func writeRootPartition(store backend.Storage, offset, size int64, srcPath string) error {
	src, err := os.Open(srcPath) // #nosec G304 -- srcPath is cnimbus's own generated temp file, not user input
	if err != nil {
		return fmt.Errorf("opening %s: %w", srcPath, err)
	}
	defer func() { _ = src.Close() }()

	sub := backend.Sub(store, offset, size)
	wf, err := sub.Writable()
	if err != nil {
		return fmt.Errorf("opening root partition for writing: %w", err)
	}

	buf := make([]byte, 1024*1024)
	var written int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := wf.WriteAt(buf[:n], written); err != nil {
				return fmt.Errorf("writing root partition at offset %d: %w", written, err)
			}
			written += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", srcPath, readErr)
		}
	}
	return nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// roundUpSectors converts a byte size into a whole number of
// sectorSize-byte sectors, rounding up.
func roundUpSectors(size int64) uint64 {
	return uint64((size + sectorSize - 1) / sectorSize)
}

// roundUpToMiB rounds a sector number up to the next 1 MiB-aligned
// sector boundary -- the same alignment convention espStartSector
// itself already follows.
func roundUpToMiB(sector uint64) uint64 {
	rem := sector % mibSectors
	if rem == 0 {
		return sector
	}
	return sector + (mibSectors - rem)
}

func efiBootFileName(arch string) (string, error) {
	switch arch {
	case "amd64":
		return "BOOTX64.EFI", nil
	case "arm64":
		return "BOOTAA64.EFI", nil
	default:
		return "", fmt.Errorf("unsupported arch %q for FORMAT raw", arch)
	}
}
