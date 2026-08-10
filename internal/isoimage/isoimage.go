// Package isoimage assembles a bootable ISO entirely in Go: no
// grub-mkrescue, no xorriso, no container involved.
//
// "BIOS+UEFI hybrid" below means the El Torito boot catalog carries
// both a BIOS and a UEFI "no emulation" entry, so a hypervisor or
// virtual-optical-drive tool can boot either way from the same ISO --
// it does NOT mean this is an isohybrid image (T80): there is no MBR
// boot code at byte 0, so `dd`ing this ISO onto a physical USB stick
// will not boot a bare-metal BIOS/legacy machine (see README's "Known
// limitations" -- FORMAT raw is the USB/bare-metal path instead).
//
// amd64 layout (BIOS+UEFI hybrid):
//
//	/ISOLINUX/ISOLINUX.BIN  <- El Torito BIOS ("no emulation") boot image
//	/ISOLINUX/LDLINUX.C32   <- isolinux's COM32 core
//	/ISOLINUX/ISOLINUX.CFG  <- BIOS boot menu; KERNEL/initrd= point straight
//	                           at /EFI/BOOT/ below (T78) -- isolinux loads a
//	                           bzImage regardless of its filename, and the
//	                           EFI-stub kernel *is* that same bzImage, so no
//	                           separate /BOOT/ copy is needed just to give
//	                           BIOS boot its own path to read from.
//	/EFIBOOT.IMG            <- FAT image, El Torito EFI ("no emulation") boot image
//	  /EFI/BOOT/BOOTX64.EFI  <- same vmlinuz, loaded directly as a PE/EFI
//	                            application via the kernel's own EFI stub
//	  /EFI/BOOT/INITRD.IMG   <- loaded by the EFI stub via "initrd=" on CONFIG_CMDLINE
//
// The /EFI/BOOT/ pair above also exists directly in the ISO9660 tree
// itself (not only inside EFIBOOT.IMG's FAT payload) for tools that boot
// ISO files by reading their filesystem directly rather than through
// firmware-level El Torito -- Ventoy chief among them.
//
// arm64 layout (UEFI only -- arm64 has no BIOS-equivalent legacy boot
// path at all, so there is no ISOLINUX entry or files):
//
//	/EFIBOOT.IMG
//	  /EFI/BOOT/BOOTAA64.EFI <- UEFI's arm64 default boot file name
//	  /EFI/BOOT/INITRD.IMG
//
// isolinux.bin and ldlinux.c32 are not built by cnimbus: they are the
// syslinux project's own prebuilt, redistributable bootloader
// binaries (the same ones every mainstream Linux ISO ships), vendored
// as data. The kernel needs no separate UEFI bootloader at all on
// either architecture -- CONFIG_EFI_STUB makes the kernel image a
// valid PE32+ executable in its own right.
//
// The ISO9660 (+ El Torito) and FAT filesystem formats themselves are
// handled by github.com/diskfs/go-diskfs, a maintained, test-covered
// library -- not hand-rolled here.
package isoimage

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/fat16"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
	"github.com/diskfs/go-diskfs/partition/mbr"
)

// ErrEFIPayloadTooLarge wraps Write's El Torito 16-bit sector-count
// failure (T77) so a caller with visibility into what grew the payload
// (cmd/cnimbus/build.go, which knows which COPY/ADD lines landed in
// stage 1's tmpfs) can add an actionable message via errors.Is, rather
// than the user only ever seeing this package's own EFI/El-Torito-centric
// error text with no connection back to the Nimbusfile line that caused it.
var ErrEFIPayloadTooLarge = errors.New("EFI boot image exceeds El Torito's 16-bit sector-count field")

// Image is the fixed set of inputs needed to produce a bootable ISO.
type Image struct {
	VolumeLabel  string
	Arch         string // "amd64" (BIOS+UEFI hybrid) or "arm64" (UEFI only)
	IsolinuxBin  []byte // amd64 only
	LdlinuxC32   []byte // amd64 only
	IsolinuxCfg  []byte // amd64 only
	Vmlinuz      []byte
	InitramfsImg []byte // stage 1: the tiny cpio the kernel unpacks directly
	// Metadata (AD-050) is a small, plain-text identity manifest written
	// to the ISO9660 tree's top level as CNIMBUS.CFG -- readable by
	// anything that can read the filesystem at all, *before* ever
	// mounting SQUASHFS.IMG. Exists so a boot-media scan that finds more
	// than one candidate .iso (a multiboot USB stick genuinely can carry
	// several cnimbus-built images at once) has something to identify
	// each one by -- HOSTNAME chief among the fields -- rather than
	// silently committing to whichever one it happens to find first.
	// Not a substitute for knowing *which* image a user actually
	// selected in a boot-menu tool's own UI (that selection never
	// reaches this project's own boot code at all on most such tools --
	// see AD-050's own discussion), just a way to make whichever
	// candidate this ends up booting nameable in its own console output
	// instead of anonymous.
	Metadata []byte
	// SquashfsImgPath (T75) is a path to stage 2's finished image on disk
	// rather than its bytes: this can legitimately be gigabytes (a large
	// COPY/VOLUME payload), so it's streamed via io.Copy into the ISO
	// workspace instead of being held as a single in-memory []byte the
	// way Vmlinuz/InitramfsImg still are (those are bounded by kernel +
	// initramfs size, typically low tens of MB, not a user-controlled
	// payload size).
	SquashfsImgPath string
	// TmpDir (T79) is the directory the ISO assembly workspace is
	// created under; "" means the OS default ($TMPDIR/%TEMP%), unchanged
	// from before this field existed. Set this to the output file's own
	// directory to avoid an ENOSPC on a small system temp drive when the
	// destination disk has plenty of room -- a real failure mode on
	// Windows, where %TEMP% is almost always the (often small) system
	// drive regardless of where the user is writing the final image.
	TmpDir string
}

// efiBootFileName is the filename UEFI firmware looks for by default
// on a given architecture's ESP (EFI System Partition).
func efiBootFileName(arch string) string {
	if arch == "arm64" {
		return "BOOTAA64.EFI"
	}
	return "BOOTX64.EFI"
}

// Write assembles the image and writes it to path.
func Write(path string, img Image) error {
	if img.VolumeLabel == "" {
		img.VolumeLabel = "CNIMBUS"
	}
	if img.Arch == "" {
		img.Arch = "amd64"
	}
	efiBootFile := efiBootFileName(img.Arch)

	workspace, err := os.MkdirTemp(img.TmpDir, "cnimbus-iso-*")
	if err != nil {
		return fmt.Errorf("creating iso workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspace) }() // best-effort temp-dir cleanup

	if img.Arch == "amd64" {
		if err := writeFile(workspace, "ISOLINUX/ISOLINUX.BIN", img.IsolinuxBin); err != nil {
			return err
		}
		if err := writeFile(workspace, "ISOLINUX/LDLINUX.C32", img.LdlinuxC32); err != nil {
			return err
		}
		if err := writeFile(workspace, "ISOLINUX/ISOLINUX.CFG", img.IsolinuxCfg); err != nil {
			return err
		}
	}
	// Stage 1's /init mounts this same ISO9660 filesystem itself (via
	// /dev/sr0, once Linux is actually running) and loop-mounts this file
	// from it -- same real filesystem regardless of whether BIOS or UEFI
	// booted the kernel, so unlike VMLINUZ/INITRD.IMG this needs no
	// second copy inside EFIBOOT.IMG's El Torito FAT payload.
	if err := writeFileFromPath(workspace, "SQUASHFS.IMG", img.SquashfsImgPath); err != nil {
		return err
	}
	if len(img.Metadata) > 0 {
		if err := writeFile(workspace, "CNIMBUS.CFG", img.Metadata); err != nil {
			return err
		}
	}
	// Also expose /EFI/BOOT/<bootfile> directly in the ISO9660 tree (not
	// just inside EFIBOOT.IMG's El Torito FAT payload): tools that boot
	// ISO files by reading their filesystem directly rather than through
	// firmware-level El Torito -- Ventoy chief among them -- look for
	// this exact path themselves.
	if err := writeFile(workspace, "EFI/BOOT/"+efiBootFile, img.Vmlinuz); err != nil {
		return err
	}
	if err := writeFile(workspace, "EFI/BOOT/INITRD.IMG", img.InitramfsImg); err != nil {
		return err
	}

	efiImgPath := filepath.Join(workspace, "EFIBOOT.IMG")
	if err := buildEFIBootImage(efiImgPath, efiBootFile, img.Vmlinuz, img.InitramfsImg); err != nil {
		return fmt.Errorf("building EFI boot image: %w", err)
	}
	efiImgInfo, err := os.Stat(efiImgPath)
	if err != nil {
		return fmt.Errorf("stat EFIBOOT.IMG: %w", err)
	}
	// go-diskfs's own "sectors to load" auto-detection for El Torito EFI
	// entries has proven unreliable on images this size (observed
	// truncated to a fraction of the real size, leaving OVMF unable to
	// find the FAT payload) -- compute and set it ourselves instead.
	//
	// El Torito's own "no emulation" boot image size field is 16 bits of
	// 512-byte units (~32MB max representable, the same ceiling
	// buildEFIBootImage's own FAT16-vs-FAT32 choice is built around) --
	// silently truncating past that would produce an EFI entry whose
	// declared load size doesn't match the real payload, which firmware
	// reads as a corrupt/incomplete FAT image rather than failing loudly.
	efiLoadSectors := (efiImgInfo.Size() + 511) / 512
	if efiLoadSectors > math.MaxUint16 {
		// %w wraps ErrEFIPayloadTooLarge (T77) so a caller that assembled
		// img.InitramfsImg itself (cmd/cnimbus/build.go, where stage 1's
		// contents -- and therefore which COPY/ADD lines grew it -- are
		// still known) can recognize this specific failure via errors.Is
		// and add a more actionable message naming the actual cause,
		// instead of this package guessing at causes it has no visibility
		// into (isoimage only ever sees the already-assembled bytes).
		return fmt.Errorf("EFI boot image %s is %d sectors, exceeding El Torito's 16-bit sector-count field (max %d) -- "+
			"the kernel+initramfs are too large for this boot path: %w", efiImgPath, efiLoadSectors, math.MaxUint16, ErrEFIPayloadTooLarge)
	}
	efiLoadBlocks := uint16(efiLoadSectors) // #nosec G115 -- bounds-checked against math.MaxUint16 immediately above

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	// Double-closing an *os.File is harmless (the second call just
	// returns an error, ignored here) -- this is a safety net for the
	// early-return error paths below; the success path closes and
	// checks explicitly, since Finalize's writes only actually reach
	// disk once Close flushes them.
	defer func() { _ = out.Close() }()

	backend := file.New(out, false)
	const blockSize = 2048
	fs, err := iso9660.Create(backend, 0, 0, blockSize, workspace)
	if err != nil {
		return fmt.Errorf("iso9660.Create: %w", err)
	}

	efiEntry := &iso9660.ElToritoEntry{
		Platform:   iso9660.EFI,
		Emulation:  iso9660.NoEmulation,
		BootFile:   "/EFIBOOT.IMG",
		SystemType: mbr.Fat16b,
	}
	efiEntry.SetLoadSize(efiLoadBlocks)

	entries := []*iso9660.ElToritoEntry{efiEntry}
	bootCatalog := "/EFIBOOT.CAT"
	if img.Arch == "amd64" {
		// The BIOS entry must be first: go-diskfs's validation entry
		// (and thus the platform BIOS reads to decide "is this my
		// entry?") is derived from Entries[0]'s Platform.
		entries = []*iso9660.ElToritoEntry{
			{
				Platform:  iso9660.BIOS,
				Emulation: iso9660.NoEmulation,
				BootFile:  "/ISOLINUX/ISOLINUX.BIN",
				BootTable: true, // patches isolinux's boot info table for us
			},
			efiEntry,
		}
		bootCatalog = "/ISOLINUX/BOOT.CAT"
	}

	err = fs.Finalize(iso9660.FinalizeOptions{
		VolumeIdentifier: img.VolumeLabel,
		ElTorito: &iso9660.ElTorito{
			BootCatalog: bootCatalog,
			Entries:     entries,
		},
	})
	if err != nil {
		return fmt.Errorf("finalizing iso: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}

func writeFile(workspace, relPath string, data []byte) error {
	full := filepath.Join(workspace, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// writeFileFromPath is writeFile's streaming counterpart (T75): it
// copies srcPath's bytes into the workspace via io.Copy instead of
// requiring the caller to hold the whole source in memory as a []byte
// first -- used for the SquashFS root, which can legitimately be
// gigabytes.
func writeFileFromPath(workspace, relPath, srcPath string) error {
	full := filepath.Join(workspace, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	src, err := os.Open(srcPath) // #nosec G304 -- srcPath is cnimbus's own generated temp file, not user input
	if err != nil {
		return fmt.Errorf("opening %s: %w", srcPath, err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("creating %s: %w", full, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copying %s to %s: %w", srcPath, full, err)
	}
	return dst.Close()
}

// buildEFIBootImage writes a small FAT16 image at outPath containing
// the El Torito EFI payload: \EFI\BOOT\<efiBootFile> (the kernel
// itself, via its EFI stub) and \EFI\BOOT\INITRD.IMG alongside it,
// matching the path the kernel's CONFIG_CMDLINE "initrd=" option
// expects.
func buildEFIBootImage(outPath, efiBootFile string, vmlinuz, initramfs []byte) error {
	const blockSize = 512
	size := int64(8*1024*1024) + int64(len(vmlinuz)) + int64(len(initramfs))
	// The FAT32 spec requires >= 65525 clusters; below that, a strict
	// reader (UEFI firmware's own FAT driver very much included) may
	// refuse to recognize the volume as FAT32 at all -- that floor sits
	// at ~33.5MB, which in turn exceeds El Torito's own "no emulation"
	// boot image size field (16 bits, 512B units -> ~32MB max
	// representable). Those two constraints leave no valid FAT32 size at
	// all for a boot image this small, so this uses FAT16 instead: its
	// cluster-count floor is ~2MB, comfortably under El Torito's ceiling.
	const minFAT16Size = 3 * 1024 * 1024
	if size < minFAT16Size {
		size = minFAT16Size
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	// Safety net for the early-return error paths below; the success
	// path closes and checks explicitly further down (fat16's writes
	// only actually reach disk once Close flushes them).
	defer func() { _ = f.Close() }()

	backend := file.New(f, false)
	fs, err := fat16.Create(backend, size, 0, blockSize, "EFIBOOT", false)
	if err != nil {
		return fmt.Errorf("fat16.Create: %w", err)
	}
	// fat16.Create only writes metadata sectors up front; the backing
	// file stays exactly as long as whatever gets written afterward
	// (sparse-in-practice), even though the FAT16 BPB already declares
	// the full logical volume size. Pre-extend it now so the file on
	// disk actually matches that declared size -- otherwise the tail of
	// the volume is simply absent, and firmware reading past where our
	// writes stopped (as El Torito's declared load size expects it to)
	// finds nothing there.
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("truncating EFI FAT image to %d bytes: %w", size, err)
	}

	if err := fs.Mkdir("/EFI"); err != nil {
		return err
	}
	if err := fs.Mkdir("/EFI/BOOT"); err != nil {
		return err
	}
	if err := writeFATFile(fs, "/EFI/BOOT/"+efiBootFile, vmlinuz); err != nil {
		return err
	}
	if err := writeFATFile(fs, "/EFI/BOOT/INITRD.IMG", initramfs); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", outPath, err)
	}
	return nil
}

func writeFATFile(fs *fat16.FileSystem, pathname string, data []byte) error {
	f, err := fs.OpenFile(pathname, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("opening %s in FAT image: %w", pathname, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close() // the write error is what matters; already returned below
		return fmt.Errorf("writing %s in FAT image: %w", pathname, err)
	}
	return f.Close()
}
