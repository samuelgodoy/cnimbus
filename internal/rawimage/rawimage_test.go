package rawimage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/partition/gpt"
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

func buildTinyRaw(t *testing.T, arch string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "test.img")
	img := Image{
		Arch:            arch,
		Vmlinuz:         []byte("fake-kernel-bytes"),
		InitramfsImg:    []byte("fake-initrd-bytes"),
		SquashfsImgPath: writeTempFile(t, "fake-squashfs-bytes"),
	}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out
}

func readESPFile(t *testing.T, imgPath, pathname string) []byte {
	t.Helper()
	d, err := diskfs.Open(imgPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("diskfs.Open: %v", err)
	}
	defer d.Backend.Close()
	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatalf("GetFilesystem(1) (the ESP): %v", err)
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

// readRootPartitionPrefix reads the first n bytes of the second GPT
// partition's raw contents directly, the same way stage 1's
// `mount -t squashfs` would see them off the block device -- T76's root
// partition is never a go-diskfs filesystem, so this reads the backend
// directly at the partition's own byte offset rather than going through
// GetFilesystem.
func readRootPartitionPrefix(t *testing.T, imgPath string, n int) []byte {
	t.Helper()
	d, err := diskfs.Open(imgPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("diskfs.Open: %v", err)
	}
	defer d.Backend.Close()
	table, err := d.GetPartitionTable()
	if err != nil {
		t.Fatalf("GetPartitionTable: %v", err)
	}
	gptTable, ok := table.(*gpt.Table)
	if !ok {
		t.Fatalf("expected a GPT partition table, got %T", table)
	}
	if len(gptTable.Partitions) < 2 {
		t.Fatalf("expected at least 2 partitions, got %d", len(gptTable.Partitions))
	}
	root := gptTable.Partitions[1]
	offset := int64(root.Start) * sectorSize
	buf := make([]byte, n)
	if _, err := d.Backend.ReadAt(buf, offset); err != nil && err != io.EOF {
		t.Fatalf("reading root partition at offset %d: %v", offset, err)
	}
	return buf
}

func TestWriteAmd64(t *testing.T) {
	imgPath := buildTinyRaw(t, "amd64")

	tests := []struct {
		path string
		want []byte
	}{
		{"/EFI/BOOT/BOOTX64.EFI", []byte("fake-kernel-bytes")},
		{"/EFI/BOOT/INITRD.IMG", []byte("fake-initrd-bytes")},
	}
	for _, tt := range tests {
		got := readESPFile(t, imgPath, tt.path)
		if !bytes.Equal(got, tt.want) {
			t.Errorf("%s = %q, want %q", tt.path, got, tt.want)
		}
	}

	want := []byte("fake-squashfs-bytes")
	got := readRootPartitionPrefix(t, imgPath, len(want))
	if !bytes.Equal(got, want) {
		t.Errorf("root partition contents = %q, want %q", got, want)
	}
}

// T76: the ESP must never contain the SquashFS root -- that's precisely
// the layout this ticket replaces (a multi-GiB "EFI System Partition",
// FAT32's 4 GiB file-size ceiling, no block device for dm-verity).
func TestWriteESPDoesNotContainSquashfs(t *testing.T) {
	imgPath := buildTinyRaw(t, "amd64")
	d, err := diskfs.Open(imgPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Backend.Close()
	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"/SQUASHFS.IMG", "/squashfs.img"} {
		if _, err := fs.OpenFile(name, os.O_RDONLY); err == nil {
			t.Errorf("expected %s to not exist in the ESP", name)
		}
	}
}

// The root partition's type GUID must be a generic Linux filesystem
// type, not EFISystemPartition -- firmware only ever looks at the ESP
// (partition 1); the root partition existing as a distinct, correctly
// typed partition is what lets stage 1 (and, eventually, dm-verity)
// address it as a real block device.
func TestWriteRootPartitionHasLinuxFilesystemType(t *testing.T) {
	imgPath := buildTinyRaw(t, "amd64")
	d, err := diskfs.Open(imgPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Backend.Close()
	table, err := d.GetPartitionTable()
	if err != nil {
		t.Fatal(err)
	}
	gptTable, ok := table.(*gpt.Table)
	if !ok {
		t.Fatalf("expected a GPT partition table, got %T", table)
	}
	if len(gptTable.Partitions) != 2 {
		t.Fatalf("expected exactly 2 partitions, got %d", len(gptTable.Partitions))
	}
	if gptTable.Partitions[0].Type != gpt.EFISystemPartition {
		t.Errorf("partition 1 type = %v, want EFISystemPartition", gptTable.Partitions[0].Type)
	}
	if gptTable.Partitions[1].Type != gpt.LinuxFilesystem {
		t.Errorf("partition 2 type = %v, want LinuxFilesystem", gptTable.Partitions[1].Type)
	}
}

// A SquashFS root larger than the old single-partition layout's fixed
// ESP floor must still work end to end -- it now lands in its own
// appropriately-sized partition instead of forcing the ESP itself to
// grow to match.
func TestWriteLargeSquashfsGrowsRootPartitionNotESP(t *testing.T) {
	large := bytes.Repeat([]byte("x"), 300*1024*1024) // bigger than espSize
	path := filepath.Join(t.TempDir(), "squashfs-src")
	if err := os.WriteFile(path, large, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "test.img")
	img := Image{Arch: "amd64", Vmlinuz: []byte("k"), InitramfsImg: []byte("i"), SquashfsImgPath: path}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := readRootPartitionPrefix(t, out, len(large))
	if !bytes.Equal(got, large) {
		t.Error("root partition contents do not match the large SquashFS source")
	}

	// The ESP itself must still be readable/small -- confirms it was
	// never resized to hold the (much larger) root.
	d, err := diskfs.Open(out, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Backend.Close()
	table, err := d.GetPartitionTable()
	if err != nil {
		t.Fatal(err)
	}
	gptTable := table.(*gpt.Table)
	espSectorsUsed := gptTable.Partitions[0].End - gptTable.Partitions[0].Start + 1
	if espSectorsUsed*sectorSize >= uint64(len(large)) {
		t.Errorf("ESP grew to %d bytes -- expected it to stay near the fixed espSize floor regardless of SquashFS size", espSectorsUsed*sectorSize)
	}
}

func TestWriteArm64UsesAA64BootFile(t *testing.T) {
	imgPath := buildTinyRaw(t, "arm64")
	got := readESPFile(t, imgPath, "/EFI/BOOT/BOOTAA64.EFI")
	if !bytes.Equal(got, []byte("fake-kernel-bytes")) {
		t.Errorf("BOOTAA64.EFI = %q", got)
	}
}

func TestWriteDiskSizeIsMiBAligned(t *testing.T) {
	// Azure's own documented VHD upload requirement: virtual disk size
	// must be a whole-MiB multiple. Several content sizes, deliberately
	// including ones that wouldn't naturally land on a MiB boundary on
	// their own, to catch the rounding being silently skipped for some
	// sizes but not others.
	for _, contentSize := range []int{0, 1, 1024, 1024 * 1024, 5*1024*1024 + 137} {
		content := make([]byte, contentSize)
		out := filepath.Join(t.TempDir(), "test.img")
		img := Image{Arch: "amd64", Vmlinuz: content, InitramfsImg: []byte("i"), SquashfsImgPath: writeTempFile(t, "s")}
		if err := Write(out, img); err != nil {
			t.Fatalf("Write (content size %d): %v", contentSize, err)
		}
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		const mib = 1024 * 1024
		if info.Size()%mib != 0 {
			t.Errorf("content size %d: image size %d is not 1 MiB-aligned", contentSize, info.Size())
		}
	}
}

func TestWriteRejectsUnsupportedArch(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test.img")
	err := Write(out, Image{Arch: "mips", Vmlinuz: []byte("k"), InitramfsImg: []byte("i"), SquashfsImgPath: writeTempFile(t, "s")})
	if err == nil {
		t.Fatal("expected error for unsupported architecture")
	}
}

func TestWriteOverwritesExistingFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test.img")
	if err := os.WriteFile(out, []byte("stale content"), 0o644); err != nil {
		t.Fatal(err)
	}
	img := Image{Arch: "amd64", Vmlinuz: []byte("k"), InitramfsImg: []byte("i"), SquashfsImgPath: writeTempFile(t, "s")}
	if err := Write(out, img); err != nil {
		t.Fatalf("Write should overwrite an existing file: %v", err)
	}
	got := readESPFile(t, out, "/EFI/BOOT/BOOTX64.EFI")
	if string(got) != "k" {
		t.Errorf("expected fresh content, got %q", got)
	}
}
