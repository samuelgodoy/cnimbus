package isoimage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/diskfs/go-diskfs"
)

// writeTempFile writes content to a fresh file under t.TempDir() and
// returns its path -- SquashfsImgPath (T75) takes a path rather than a
// []byte, so tests need a real file on disk to point it at.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "squashfs-src")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// buildTinyImage assembles a minimal but structurally valid image (tiny
// placeholder bytes in place of a real kernel/initramfs/squashfs -- this
// test only cares that Write's own filesystem assembly round-trips, not
// that the result actually boots) and returns its path.
func buildTinyImage(t *testing.T, arch string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "test.iso")
	img := Image{
		VolumeLabel:     "TESTVOL",
		Arch:            arch,
		Vmlinuz:         []byte("fake-kernel-bytes"),
		InitramfsImg:    []byte("fake-initrd-bytes"),
		SquashfsImgPath: writeTempFile(t, "fake-squashfs-bytes"),
	}
	if arch == "amd64" {
		// go-diskfs's El Torito "no emulation" BIOS entry requires the
		// boot image's on-disk size to already be block-aligned (real
		// isolinux.bin binaries always are, being themselves a compiled
		// boot sector image) -- padded here since this test's stand-in
		// content is arbitrary text, not a real bootloader.
		img.IsolinuxBin = padTo512([]byte("fake-isolinux-bin"))
		img.LdlinuxC32 = []byte("fake-ldlinux-c32")
		img.IsolinuxCfg = []byte("DEFAULT cnimbus\n")
	}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out
}

func padTo512(data []byte) []byte {
	if rem := len(data) % 512; rem != 0 {
		data = append(data, make([]byte, 512-rem)...)
	}
	return data
}

func readISOFile(t *testing.T, isoPath, pathname string) []byte {
	t.Helper()
	d, err := diskfs.Open(isoPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("diskfs.Open: %v", err)
	}
	defer d.Backend.Close()
	fs, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatalf("GetFilesystem: %v", err)
	}
	f, err := fs.OpenFile(pathname, os.O_RDONLY)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", pathname, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll(%q): %v", pathname, err)
	}
	return data
}

func TestWriteAmd64ContainsExpectedFiles(t *testing.T) {
	isoPath := buildTinyImage(t, "amd64")

	tests := []struct {
		path string
		want []byte
	}{
		{"/SQUASHFS.IMG", []byte("fake-squashfs-bytes")},
		{"/ISOLINUX/LDLINUX.C32", []byte("fake-ldlinux-c32")},
		{"/EFI/BOOT/BOOTX64.EFI", []byte("fake-kernel-bytes")},
		{"/EFI/BOOT/INITRD.IMG", []byte("fake-initrd-bytes")},
	}
	for _, tt := range tests {
		got := readISOFile(t, isoPath, tt.path)
		if !bytes.Equal(got, tt.want) {
			t.Errorf("%s = %q, want %q", tt.path, got, tt.want)
		}
	}

	// ISOLINUX.BIN's own content isn't byte-for-byte comparable: the
	// BootTable:true El Torito option (see isoimage.go) has go-diskfs
	// patch a real "boot info table" into its first bytes (LBA/size/
	// checksum isolinux itself reads at boot) -- expected, intentional
	// mutation, not corruption. Only its length should be unchanged.
	got := readISOFile(t, isoPath, "/ISOLINUX/ISOLINUX.BIN")
	want := padTo512([]byte("fake-isolinux-bin"))
	if len(got) != len(want) {
		t.Errorf("ISOLINUX.BIN length = %d, want %d", len(got), len(want))
	}
}

// AD-050: CNIMBUS.CFG is a plain-text identity manifest at the ISO9660
// tree's top level, readable before ever mounting SQUASHFS.IMG --
// motivated by a real multiboot-USB boot where a generic boot-media
// scan found more than one candidate .iso and had no way to identify
// which one it had landed on.
func TestWriteIncludesMetadataWhenSet(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test.iso")
	img := Image{
		VolumeLabel:     "TESTVOL",
		Arch:            "amd64",
		Vmlinuz:         []byte("fake-kernel-bytes"),
		InitramfsImg:    []byte("fake-initrd-bytes"),
		SquashfsImgPath: writeTempFile(t, "fake-squashfs-bytes"),
		IsolinuxBin:     padTo512([]byte("fake-isolinux-bin")),
		LdlinuxC32:      []byte("fake-ldlinux-c32"),
		IsolinuxCfg:     []byte("DEFAULT cnimbus\n"),
		Metadata:        []byte("HOSTNAME=myvm\nARCH=amd64\n"),
	}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readISOFile(t, out, "/CNIMBUS.CFG")
	if !bytes.Equal(got, img.Metadata) {
		t.Errorf("CNIMBUS.CFG = %q, want %q", got, img.Metadata)
	}
}

// A Nimbusfile with no reason to carry this (Metadata left unset, the
// zero value) must not produce an empty CNIMBUS.CFG on disk at all.
func TestWriteOmitsMetadataFileWhenUnset(t *testing.T) {
	isoPath := buildTinyImage(t, "amd64")
	d, err := diskfs.Open(isoPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("diskfs.Open: %v", err)
	}
	defer d.Backend.Close()
	fs, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatalf("GetFilesystem: %v", err)
	}
	if _, err := fs.OpenFile("/CNIMBUS.CFG", os.O_RDONLY); err == nil {
		t.Error("expected no CNIMBUS.CFG when Metadata is unset")
	}
}

// T78: the ISO used to carry a third, redundant copy of the kernel and
// initramfs at /BOOT/ that nothing (isolinux included, once ISOLINUX.CFG
// itself points at /EFI/BOOT/) ever reads.
func TestWriteDoesNotDuplicateKernelAtBootPath(t *testing.T) {
	isoPath := buildTinyImage(t, "amd64")
	d, err := diskfs.Open(isoPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("diskfs.Open: %v", err)
	}
	defer d.Backend.Close()
	fs, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/BOOT/VMLINUZ", "/BOOT/INITRD.IMG"} {
		if _, err := fs.OpenFile(p, os.O_RDONLY); err == nil {
			t.Errorf("%s should not exist -- the kernel/initramfs should only live under /EFI/BOOT/ now", p)
		}
	}
}

func TestWriteArm64HasNoISOLINUXAndUsesAA64(t *testing.T) {
	isoPath := buildTinyImage(t, "arm64")

	got := readISOFile(t, isoPath, "/EFI/BOOT/BOOTAA64.EFI")
	if !bytes.Equal(got, []byte("fake-kernel-bytes")) {
		t.Errorf("BOOTAA64.EFI = %q", got)
	}

	d, err := diskfs.Open(isoPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Backend.Close()
	fs, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.OpenFile("/ISOLINUX/ISOLINUX.BIN", os.O_RDONLY); err == nil {
		t.Error("arm64 image should not carry an ISOLINUX entry (no BIOS-equivalent boot path)")
	}
}

func TestWriteDefaultsVolumeLabelAndArch(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test.iso")
	img := Image{
		Vmlinuz:         []byte("k"),
		InitramfsImg:    []byte("i"),
		SquashfsImgPath: writeTempFile(t, "s"),
		IsolinuxBin:     padTo512([]byte("b")),
		LdlinuxC32:      []byte("c"),
		IsolinuxCfg:     []byte("cfg"),
	}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write with empty VolumeLabel/Arch should still succeed: %v", err)
	}
	// Defaulting to amd64 means an EFI/BOOT/BOOTX64.EFI entry should exist.
	got := readISOFile(t, out, "/EFI/BOOT/BOOTX64.EFI")
	if string(got) != "k" {
		t.Errorf("default-arch image missing expected EFI boot file content: %q", got)
	}
}

// T79: TmpDir must actually be honored -- a caller sets it specifically
// to avoid the OS temp dir (e.g. a small Windows system drive) when the
// destination disk has room, so Write must create its workspace there,
// not silently fall back to the OS default.
func TestWriteHonorsTmpDir(t *testing.T) {
	customTmp := filepath.Join(t.TempDir(), "custom-workspace")
	if err := os.MkdirAll(customTmp, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "test.iso")
	img := Image{
		Vmlinuz:         []byte("k"),
		InitramfsImg:    []byte("i"),
		SquashfsImgPath: writeTempFile(t, "s"),
		IsolinuxBin:     padTo512([]byte("b")),
		LdlinuxC32:      []byte("c"),
		IsolinuxCfg:     []byte("cfg"),
		TmpDir:          customTmp,
	}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write with a custom TmpDir should still succeed: %v", err)
	}
	// Write cleans up its workspace on success (defer os.RemoveAll), so
	// this only asserts TmpDir was actually used as os.MkdirTemp's parent
	// by pointing it at a directory that doesn't exist -- MkdirTemp fails
	// immediately in that case, proving the field reaches the call.
	nonexistentTmp := filepath.Join(customTmp, "does-not-exist")
	img.TmpDir = nonexistentTmp
	if err := Write(out, img); err == nil {
		t.Fatal("expected Write to fail when TmpDir doesn't exist, proving TmpDir is actually used")
	}
}
