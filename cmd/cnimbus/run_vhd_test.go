package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// F4: writeVHDFooter's own fixed-field layout, per Microsoft's VHD
// Image Format Specification. Values here are asserted against the
// spec's documented byte offsets/values directly, not against any
// external reference file -- there's no golden VHD to compare against
// in this repo, so this is the safety net against a field landing at
// the wrong offset.
func TestVHDFooterFixedFields(t *testing.T) {
	const diskSize = 512 * 2000 // 2000 sectors, well inside every geometry branch
	footer, err := vhdFooter(diskSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(footer) != 512 {
		t.Fatalf("footer length = %d, want 512", len(footer))
	}
	if string(footer[0:8]) != "conectix" {
		t.Errorf("cookie = %q, want \"conectix\"", footer[0:8])
	}
	if got := binary.BigEndian.Uint32(footer[8:12]); got != 0x00000002 {
		t.Errorf("features = 0x%08x, want 0x00000002 (Reserved bit set)", got)
	}
	if got := binary.BigEndian.Uint32(footer[12:16]); got != 0x00010000 {
		t.Errorf("file format version = 0x%08x, want 0x00010000", got)
	}
	if got := binary.BigEndian.Uint64(footer[16:24]); got != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("data offset = 0x%016x, want all-ones (Fixed disk, no dynamic header)", got)
	}
	if got := binary.BigEndian.Uint64(footer[40:48]); got != diskSize {
		t.Errorf("original size = %d, want %d", got, diskSize)
	}
	if got := binary.BigEndian.Uint64(footer[48:56]); got != diskSize {
		t.Errorf("current size = %d, want %d", got, diskSize)
	}
	if got := binary.BigEndian.Uint32(footer[60:64]); got != 2 {
		t.Errorf("disk type = %d, want 2 (Fixed hard disk)", got)
	}
	// Unique ID (footer[68:84]) must not be the zero UUID -- a zero value
	// there would mean uuid.NewRandom silently failed and was ignored.
	if bytes.Equal(footer[68:84], make([]byte, 16)) {
		t.Error("unique ID is all-zero, expected a real random UUID")
	}
}

func TestVHDFooterRejectsNonSectorAlignedSize(t *testing.T) {
	for _, size := range []int64{0, -512, 511, 1000} {
		if _, err := vhdFooter(size); err == nil {
			t.Errorf("vhdFooter(%d): expected an error, got nil", size)
		}
	}
}

// The checksum must be exactly what a reader recomputing it (sum every
// byte with the checksum field itself treated as zero, then one's
// complement) would independently derive -- this is the one field a
// real VHD consumer (Hyper-V included) is documented to actually verify.
func TestVHDFooterChecksumRoundTrips(t *testing.T) {
	footer, err := vhdFooter(512 * 100000)
	if err != nil {
		t.Fatal(err)
	}
	stored := binary.BigEndian.Uint32(footer[64:68])

	recomputed := make([]byte, 512)
	copy(recomputed, footer)
	binary.BigEndian.PutUint32(recomputed[64:68], 0)
	var sum uint32
	for _, b := range recomputed {
		sum += uint32(b)
	}
	if want := ^sum; stored != want {
		t.Errorf("stored checksum = 0x%08x, recomputed = 0x%08x", stored, want)
	}
}

// Two footers built back to back must carry different Unique IDs --
// Hyper-V (and any other VHD consumer) uses this field to distinguish
// otherwise-identical disks, so a constant or colliding value would be
// a real correctness bug, not just a cosmetic one.
func TestVHDFooterUniqueIDsDiffer(t *testing.T) {
	a, err := vhdFooter(512 * 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := vhdFooter(512 * 1000)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[68:84], b[68:84]) {
		t.Error("two independently-built footers share the same Unique ID")
	}
}

// vhdCHSGeometry's documented invariants (Microsoft's spec Appendix,
// "CHS Calculation"): heads is always in [4, 16], sectorsPerTrack is
// always one of the four values the algorithm's branches can produce,
// and cylinders always fits the 16-bit field it's stored in.
func TestVHDCHSGeometryInvariants(t *testing.T) {
	validSPT := map[uint8]bool{17: true, 31: true, 63: true, 255: true}
	sizes := []int64{
		1000,               // tiny
		2 * 1024 * 1024,    // 2 MiB in sectors
		1024 * 1024 * 1024, // 1 GiB in sectors
		65535 * 16 * 63,    // exactly the branch boundary
		65535*16*63 + 100,  // just past it
		65535 * 16 * 255,   // the absolute maximum this format can address
	}
	for _, totalSectors := range sizes {
		cyl, heads, spt := vhdCHSGeometry(totalSectors)
		if heads < 4 || heads > 16 {
			t.Errorf("totalSectors=%d: heads = %d, want [4,16]", totalSectors, heads)
		}
		if !validSPT[spt] {
			t.Errorf("totalSectors=%d: sectorsPerTrack = %d, not one of the algorithm's defined values", totalSectors, spt)
		}
		// cyl is already a uint16 -- the real assertion is that the
		// algorithm never divides by a zero heads value, which would
		// panic rather than return a bogus cyl; reaching this line at
		// all is the proof.
		_ = cyl
	}
}

func TestWriteFixedVHDAppendsFooterToRawBytes(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "disk.raw")
	rawData := bytes.Repeat([]byte{0xAB}, 512*10)
	if err := os.WriteFile(rawPath, rawData, 0o644); err != nil {
		t.Fatal(err)
	}

	vhdPath := filepath.Join(dir, "disk.vhd")
	if err := writeFixedVHD(vhdPath, rawPath); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(vhdPath)
	if err != nil {
		t.Fatal(err)
	}
	wantLen := len(rawData) + 512
	if len(got) != wantLen {
		t.Fatalf("vhd length = %d, want %d (raw + 512-byte footer)", len(got), wantLen)
	}
	if !bytes.Equal(got[:len(rawData)], rawData) {
		t.Error("raw disk bytes were altered by writeFixedVHD -- they must pass through unmodified")
	}
	footer := got[len(rawData):]
	if string(footer[0:8]) != "conectix" {
		t.Errorf("appended footer's cookie = %q, want \"conectix\"", footer[0:8])
	}
	if got := binary.BigEndian.Uint64(footer[40:48]); got != uint64(len(rawData)) {
		t.Errorf("footer's original size = %d, want %d (the raw file's own size)", got, len(rawData))
	}

	// rawPath itself must be untouched -- writeFixedVHD always produces a
	// separate file, mirroring writeFlatVMDK's non-destructive contract.
	stillThere, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stillThere, rawData) {
		t.Error("rawPath was modified -- writeFixedVHD must never touch its source")
	}
}

func TestWriteFixedVHDMissingSourceErrors(t *testing.T) {
	dir := t.TempDir()
	err := writeFixedVHD(filepath.Join(dir, "out.vhd"), filepath.Join(dir, "does-not-exist.raw"))
	if err == nil {
		t.Fatal("expected an error for a missing source raw image")
	}
}
