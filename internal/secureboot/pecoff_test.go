package secureboot

import (
	"debug/pe"
	"encoding/binary"
	"os"
	"testing"
)

// buildMinimalPE returns a small, syntactically real PE32+ image with
// one section (.text) and deliberately generous header room (the
// same headroom real bzImage/EFI-stub kernels carry -- see AD-042's
// real objcopy findings this project's sign.go doc comment
// references) so appendSection has space to add extraSections more
// 40-byte IMAGE_SECTION_HEADER entries without any further surgery.
// Every offset here is hand-computed against the same public PE/COFF
// layout pecoff.go itself parses, so this doubles as a cross-check of
// parsePELayout against a file this test fully controls (unlike a
// real bzImage, whose exact byte layout this package can only
// observe, not construct).
func buildMinimalPE(t *testing.T, extraSections int) []byte {
	t.Helper()

	const (
		lfanew         = 0x80
		fileHeaderOff  = lfanew + 4
		optionalHdrOff = fileHeaderOff + 20
		sizeOfOptHdr   = 112 + 16*8 // NumberOfRvaAndSizes fixed at 16 data directories
		sectionHdrOff  = optionalHdrOff + sizeOfOptHdr
		fileAlignment  = 0x200
		sectionAlign   = 0x1000
		textVA         = 0x1000
		textRawSize    = 0x1000
	)
	// sizeOfHeaders leaves room for exactly extraSections more 40-byte
	// section headers past the one (.text) this PE already has -- 0
	// extra means EXACTLY zero headroom, deliberately, for
	// TestAppendSectionErrorsWithoutHeaderRoom (so this value is
	// intentionally not rounded up to fileAlignment the way a real
	// linker's SizeOfHeaders normally would be -- rounding up would
	// always leave some slack space, undermining that test's "no room
	// at all" premise). textRawOff (where .text's own raw bytes
	// actually start on disk) is file-aligned separately, same as a
	// real PE.
	sizeOfHeaders := sectionHdrOff + (1+extraSections)*sectionHeaderSize
	textRawOff := align(sizeOfHeaders, fileAlignment)

	buf := make([]byte, textRawOff+textRawSize)
	buf[0], buf[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(buf[0x3C:], lfanew)
	copy(buf[lfanew:], []byte("PE\x00\x00"))

	fh := buf[fileHeaderOff:]
	binary.LittleEndian.PutUint16(fh[0:], 0x8664) // Machine: AMD64
	binary.LittleEndian.PutUint16(fh[2:], 1)       // NumberOfSections
	binary.LittleEndian.PutUint16(fh[16:], sizeOfOptHdr)

	oh := buf[optionalHdrOff:]
	binary.LittleEndian.PutUint16(oh[0:], magicPE32p)
	binary.LittleEndian.PutUint32(oh[4:], textRawSize)    // SizeOfCode
	binary.LittleEndian.PutUint32(oh[16:], textVA)        // AddressOfEntryPoint
	binary.LittleEndian.PutUint32(oh[20:], textVA)        // BaseOfCode
	binary.LittleEndian.PutUint64(oh[24:], 0x140000000)   // ImageBase
	binary.LittleEndian.PutUint32(oh[32:], sectionAlign)  // SectionAlignment
	binary.LittleEndian.PutUint32(oh[36:], fileAlignment) // FileAlignment
	binary.LittleEndian.PutUint32(oh[56:], uint32(textVA+align(textRawSize, sectionAlign))) // SizeOfImage
	binary.LittleEndian.PutUint32(oh[60:], uint32(sizeOfHeaders))                          // SizeOfHeaders
	binary.LittleEndian.PutUint16(oh[68:], 10)                                     // Subsystem: EFI application
	binary.LittleEndian.PutUint32(oh[108:], 16)                                    // NumberOfRvaAndSizes

	sh := buf[sectionHdrOff:]
	copy(sh[0:8], []byte(".text\x00\x00\x00"))
	binary.LittleEndian.PutUint32(sh[8:], textRawSize)  // VirtualSize
	binary.LittleEndian.PutUint32(sh[12:], textVA)       // VirtualAddress
	binary.LittleEndian.PutUint32(sh[16:], textRawSize)  // SizeOfRawData
	binary.LittleEndian.PutUint32(sh[20:], uint32(textRawOff)) // PointerToRawData
	binary.LittleEndian.PutUint32(sh[36:], 0x60000020)   // CODE|EXECUTE|READ

	for i := textRawOff; i < len(buf); i++ {
		buf[i] = 0xCC
	}

	// Sanity: debug/pe (stdlib, read-only) must agree this is valid.
	f, err := pe.NewFile(newReaderAt(buf))
	if err != nil {
		t.Fatalf("stdlib debug/pe rejected the synthetic test PE: %v", err)
	}
	if len(f.Sections) != 1 || f.Sections[0].Name != ".text" {
		t.Fatalf("synthetic test PE didn't come back with the expected .text section: %+v", f.Sections)
	}
	return buf
}

func newReaderAt(b []byte) *sectionReaderAt { return &sectionReaderAt{b} }

type sectionReaderAt struct{ b []byte }

func (r *sectionReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.b)) {
		return 0, os.ErrInvalid
	}
	n := copy(p, r.b[off:])
	return n, nil
}

func TestAppendSectionAddsSectionWithoutOverlap(t *testing.T) {
	base := buildMinimalPE(t, 1)
	data := []byte("hello initrd payload")
	out, err := appendSection(base, ".initrd", data, 0x4000000)
	if err != nil {
		t.Fatal(err)
	}

	f, err := pe.NewFile(newReaderAt(out))
	if err != nil {
		t.Fatalf("stdlib debug/pe rejected appendSection's output: %v", err)
	}
	if len(f.Sections) != 2 {
		t.Fatalf("want 2 sections after append, got %d", len(f.Sections))
	}
	sec := f.Sections[1]
	if sec.Name != ".initrd" {
		t.Fatalf("want .initrd, got %q", sec.Name)
	}
	if sec.VirtualAddress != 0x4000000 {
		t.Fatalf("want VMA 0x4000000, got %#x", sec.VirtualAddress)
	}
	raw, err := sec.Data()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:len(data)]) != string(data) {
		t.Fatalf("section data mismatch: got %q", raw[:len(data)])
	}

	// No overlap: every section's [VA, VA+VirtualSize) range must be disjoint.
	for i, a := range f.Sections {
		for j, b := range f.Sections {
			if i == j {
				continue
			}
			if a.VirtualAddress < b.VirtualAddress+b.VirtualSize && b.VirtualAddress < a.VirtualAddress+a.VirtualSize {
				t.Fatalf("sections %q and %q overlap", a.Name, b.Name)
			}
		}
	}
}

func TestAppendSectionErrorsWithoutHeaderRoom(t *testing.T) {
	base := buildMinimalPE(t, 0) // no room for a 2nd section header
	if _, err := appendSection(base, ".initrd", []byte("x"), 0x4000000); err == nil {
		t.Fatal("want an error when there's no header room for another section, got nil")
	}
}

func TestAppendSectionRejectsZeroLengthSilently(t *testing.T) {
	// Mirrors AD-042/AD-035's real objcopy finding: callers (see
	// sign.go's BuildAndSignUKI) must skip calling appendSection at all
	// for empty data, since a zero-length section is a real
	// objcopy/appendSection no-op, not "an empty section is added".
	// appendSection itself doesn't special-case this -- confirm it
	// still produces a structurally valid (if degenerate) section
	// rather than corrupting the file, so callers relying on the
	// skip-when-empty convention have a well-defined fallback.
	base := buildMinimalPE(t, 1)
	out, err := appendSection(base, ".cmdline", nil, 0x1000000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pe.NewFile(newReaderAt(out)); err != nil {
		t.Fatalf("appendSection produced an invalid PE for zero-length data: %v", err)
	}
}

func TestPEChecksumRecomputedAfterAppend(t *testing.T) {
	base := buildMinimalPE(t, 1)
	l, err := parsePELayout(base)
	if err != nil {
		t.Fatal(err)
	}
	before := binary.LittleEndian.Uint32(base[l.checksumOff:])

	out, err := appendSection(base, ".initrd", []byte("payload"), 0x4000000)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := parsePELayout(out)
	if err != nil {
		t.Fatal(err)
	}
	after := binary.LittleEndian.Uint32(out[l2.checksumOff:])
	if before == after {
		t.Fatalf("checksum unchanged after appending a section (before=after=%#x)", before)
	}
	// The checksum field itself must be excluded from its own
	// computation -- recomputing it a second time must be a no-op.
	recomputeChecksum(out, l2)
	after2 := binary.LittleEndian.Uint32(out[l2.checksumOff:])
	if after != after2 {
		t.Fatalf("checksum not stable across repeated recompute: %#x vs %#x", after, after2)
	}
}

func TestAuthenticodeDigestExcludesChecksumAndSecurityDirectory(t *testing.T) {
	base := buildMinimalPE(t, 1)
	l, err := parsePELayout(base)
	if err != nil {
		t.Fatal(err)
	}
	digestBefore := authenticodeDigest(base, l, sha256Sum)

	// Flipping the checksum field must not change the digest.
	mutated := append([]byte{}, base...)
	binary.LittleEndian.PutUint32(mutated[l.checksumOff:], 0xDEADBEEF)
	digestAfter := authenticodeDigest(mutated, l, sha256Sum)
	if string(digestBefore) != string(digestAfter) {
		t.Fatal("authenticodeDigest changed after mutating only the CheckSum field")
	}

	// But flipping an actual section byte must change the digest.
	mutated2 := append([]byte{}, base...)
	mutated2[len(mutated2)-1] ^= 0xFF
	digestAfter2 := authenticodeDigest(mutated2, l, sha256Sum)
	if string(digestBefore) == string(digestAfter2) {
		t.Fatal("authenticodeDigest did not change after mutating real image data")
	}
}
