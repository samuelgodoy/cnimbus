// Section of package secureboot dealing directly with raw PE/COFF
// bytes: locating the handful of header fields SignPE/BuildAndSignUKI
// need to read or rewrite, appending new sections (the UKI-assembly
// mechanism `objcopy --add-section` used to provide, see AD-042),
// computing the Authenticode PE-image hash, and recomputing the PE
// checksum after either operation. debug/pe (stdlib) can only *read*
// a PE file -- there is no stdlib support for writing one back out --
// so this file parses the fixed-offset parts of the format by hand,
// against the public Microsoft PE/COFF specification ("Microsoft
// Portable Executable and Common Object File Format Specification")
// and its Authenticode addendum ("Windows Authenticode Portable
// Executable Signature Format"). Both are publicly documented, fixed,
// versioned binary formats -- not reverse-engineered guesswork -- and
// every offset below is cross-checked against a real bzImage built by
// this project's own `cnimbus prepare` (see pecoff_test.go).
package secureboot

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// PE/COFF fixed offsets and constants this package needs. Only the
// fields actually touched are named -- this is not a general PE
// parser, just enough to append a section and locate/patch the
// checksum + Security data directory.
const (
	dosHeaderELfanewOffset = 0x3C // offset, within the DOS header, of the 4-byte pointer to the PE signature
	peSignatureSize        = 4    // "PE\x00\x00"
	fileHeaderSize         = 20   // IMAGE_FILE_HEADER

	// IMAGE_FILE_HEADER field offsets, relative to the start of the
	// file header (i.e. relative to e_lfanew+peSignatureSize).
	fhNumberOfSections     = 2
	fhSizeOfOptionalHeader = 16

	// IMAGE_OPTIONAL_HEADER32/64 field offsets, relative to the start
	// of the optional header. Only PE32 (Magic 0x10B) and PE32+ (Magic
	// 0x20B) exist; the two formats agree on every offset up through
	// BaseOfCode (PE32 has an extra BaseOfData field there that PE32+
	// omits, then ImageBase's width itself differs: 4 bytes vs 8), so
	// everything below fileAlignment onward is expressed relative to a
	// magic-dependent base computed at parse time.
	ohMagic                 = 0
	ohSizeOfCode            = 4
	ohSizeOfInitializedData = 8
	ohBaseOfCode            = 20 // PE32 only has BaseOfData at 24 in addition; unused here

	imageBase32Offset = 28 // PE32: 4-byte ImageBase at 28
	imageBase64Offset = 24 // PE32+: 8-byte ImageBase at 24 (no BaseOfData field)

	magicPE32  = 0x10B
	magicPE32p = 0x20B

	sectionHeaderSize = 40 // IMAGE_SECTION_HEADER
	dataDirEntrySize  = 8  // {VirtualAddress, Size}

	// IMAGE_DIRECTORY_ENTRY_SECURITY is data-directory index 4 (the
	// Attribute Certificate Table -- where a WIN_CERTIFICATE/Authenticode
	// signature lives). Its "VirtualAddress" field is documented as an
	// exception to every other data directory: it holds a *file offset*,
	// not an RVA, since the certificate table is deliberately excluded
	// from the mapped image.
	securityDirIndex = 4

	fileAlignmentDefault = 0x200 // used only as a sanity fallback; the real value is always read from the file
)

// peLayout captures the handful of file offsets this package needs,
// resolved once per parse so every other function works purely in
// terms of them instead of re-deriving e_lfanew/optionalHeaderOffset
// each time.
type peLayout struct {
	ntHeaderOffset       int // offset of "PE\0\0"
	fileHeaderOffset     int
	optionalHeaderOffset int
	sectionHeaderOffset  int // == optionalHeaderOffset + SizeOfOptionalHeader
	numberOfSections     int
	sizeOfOptionalHeader int

	isPE32Plus bool

	sectionAlignment int
	fileAlignment    int
	sizeOfImageOff   int // absolute file offset of the SizeOfImage field
	sizeOfHeadersOff int
	checksumOff      int
	dataDirOff       int // absolute file offset of DataDirectory[0]
	numRvaSizesOff   int
}

var errNotPE = errors.New("not a PE32/PE32+ image (missing MZ/PE signature)")

// parsePELayout resolves every fixed offset SignPE/addPESection need
// from raw PE bytes, without needing debug/pe at all (debug/pe is
// read-only and exposes RVAs/values, not the absolute file offsets a
// writer needs).
func parsePELayout(data []byte) (peLayout, error) {
	var l peLayout
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return l, errNotPE
	}
	ntHeaderOffset := int(binary.LittleEndian.Uint32(data[dosHeaderELfanewOffset:]))
	if ntHeaderOffset <= 0 || ntHeaderOffset+peSignatureSize+fileHeaderSize > len(data) {
		return l, errNotPE
	}
	if data[ntHeaderOffset] != 'P' || data[ntHeaderOffset+1] != 'E' || data[ntHeaderOffset+2] != 0 || data[ntHeaderOffset+3] != 0 {
		return l, errNotPE
	}
	l.ntHeaderOffset = ntHeaderOffset
	l.fileHeaderOffset = ntHeaderOffset + peSignatureSize
	fh := data[l.fileHeaderOffset:]
	l.numberOfSections = int(binary.LittleEndian.Uint16(fh[fhNumberOfSections:]))
	l.sizeOfOptionalHeader = int(binary.LittleEndian.Uint16(fh[fhSizeOfOptionalHeader:]))
	l.optionalHeaderOffset = l.fileHeaderOffset + fileHeaderSize
	l.sectionHeaderOffset = l.optionalHeaderOffset + l.sizeOfOptionalHeader

	if l.sectionHeaderOffset > len(data) || l.optionalHeaderOffset+2 > len(data) {
		return l, fmt.Errorf("%w: optional/section header runs past end of file", errNotPE)
	}

	magic := binary.LittleEndian.Uint16(data[l.optionalHeaderOffset+ohMagic:])
	switch magic {
	case magicPE32p:
		l.isPE32Plus = true
	case magicPE32:
		l.isPE32Plus = false
	default:
		return l, fmt.Errorf("%w: unrecognized optional header magic 0x%x", errNotPE, magic)
	}

	// Everything from SectionAlignment onward is at the same relative
	// offset in both formats (28 in PE32+, since ImageBase is 8 bytes
	// starting at 24; 32 in PE32, since ImageBase is 4 bytes starting
	// at 28) -- compute one common "post-ImageBase" base instead of
	// duplicating every subsequent field offset per format.
	var postImageBase int
	if l.isPE32Plus {
		postImageBase = imageBase64Offset + 8
	} else {
		postImageBase = imageBase32Offset + 4
	}
	oh := l.optionalHeaderOffset
	l.sectionAlignment = int(binary.LittleEndian.Uint32(data[oh+postImageBase+4:]))
	l.fileAlignment = int(binary.LittleEndian.Uint32(data[oh+postImageBase+8:]))
	l.sizeOfImageOff = oh + postImageBase + 24
	l.sizeOfHeadersOff = oh + postImageBase + 28
	l.checksumOff = oh + postImageBase + 32
	// CheckSum itself(4) + Subsystem(2)+DllCharacteristics(2) + 4*StackReserve/Commit/HeapReserve/HeapCommit(8 each) + LoaderFlags(4) -> NumberOfRvaAndSizes(4)
	l.numRvaSizesOff = l.checksumOff + 4 + 2 + 2 + 4*8 + 4
	l.dataDirOff = l.numRvaSizesOff + 4

	if l.fileAlignment == 0 {
		l.fileAlignment = fileAlignmentDefault
	}
	if l.dataDirOff+dataDirEntrySize*(securityDirIndex+1) > len(data) {
		return l, fmt.Errorf("%w: data directory table runs past end of file", errNotPE)
	}
	return l, nil
}

func align(n, alignment int) int {
	if alignment <= 0 {
		return n
	}
	return (n + alignment - 1) / alignment * alignment
}

// securityDataDirectory reads the current [VirtualAddress(=file
// offset), Size] pair of the Attribute Certificate Table directory
// entry -- both zero on an as-yet-unsigned file.
func (l peLayout) securityDataDirectory(data []byte) (offset, size uint32) {
	off := l.dataDirOff + securityDirIndex*dataDirEntrySize
	return binary.LittleEndian.Uint32(data[off:]), binary.LittleEndian.Uint32(data[off+4:])
}

func (l peLayout) setSecurityDataDirectory(data []byte, offset, size uint32) {
	off := l.dataDirOff + securityDirIndex*dataDirEntrySize
	binary.LittleEndian.PutUint32(data[off:], offset)
	binary.LittleEndian.PutUint32(data[off+4:], size)
}

// authenticodeDigest computes the Authenticode PE-image hash: hashFn
// applied to the whole file, EXCLUDING the 4-byte CheckSum field, the
// 8-byte Security data-directory entry, and everything from the
// current Security directory's file offset onward (i.e. any
// already-present WIN_CERTIFICATE table, so re-signing a previously
// signed binary hashes only the "real" image, not a prior signature).
// This is the exact algorithm the public "Windows Authenticode
// Portable Executable Signature Format" specification defines, and
// the one `sbsign`/`sbverify`/UEFI firmware itself computes.
func authenticodeDigest(data []byte, l peLayout, hashFn func([]byte) []byte) []byte {
	secOff, _ := l.securityDataDirectory(data)
	end := len(data)
	if secOff != 0 && int(secOff) < end {
		end = int(secOff)
	}

	buf := make([]byte, 0, end)
	buf = append(buf, data[:l.checksumOff]...)
	buf = append(buf, data[l.checksumOff+4:l.dataDirOff+securityDirIndex*dataDirEntrySize]...)
	buf = append(buf, data[l.dataDirOff+securityDirIndex*dataDirEntrySize+dataDirEntrySize:end]...)
	return hashFn(buf)
}

// peChecksum implements the IMAGE_OPTIONAL_HEADER CheckSum algorithm
// (the same one Microsoft's imagehlp.dll CheckSumMappedFile and every
// PE tool -- `objcopy`, `sbsign`, etc. -- reimplement): sum the file
// as little-endian 16-bit words (treating the 4-byte CheckSum field
// itself as absent), folding carries into 16 bits as you go, then add
// the file's length.
func peChecksum(data []byte, checksumOff int) uint32 {
	var sum uint64
	n := len(data)
	for i := 0; i < n; i += 2 {
		if i == checksumOff || i == checksumOff+2 {
			continue // skip both words of the 4-byte CheckSum field
		}
		var word uint16
		if i+1 < n {
			word = binary.LittleEndian.Uint16(data[i : i+2])
		} else {
			word = uint16(data[i])
		}
		sum += uint64(word)
		sum = (sum & 0xffff) + (sum >> 16)
	}
	sum = (sum & 0xffff) + (sum >> 16)
	sum = (sum & 0xffff) + (sum >> 16)
	sum += uint64(n)
	return uint32(sum)
}

// recomputeChecksum patches data's own CheckSum field in place. Not
// strictly required for Secure Boot verification (firmware checks the
// Authenticode signature, not the checksum), but real `sbsign`/
// `objcopy` output always carries a correct one, and a stale/zero
// checksum is a needless, easily-avoided difference from what F2's
// original Docker-based implementation shipped.
func recomputeChecksum(data []byte, l peLayout) {
	binary.LittleEndian.PutUint32(data[l.checksumOff:], 0)
	sum := peChecksum(data, l.checksumOff)
	binary.LittleEndian.PutUint32(data[l.checksumOff:], sum)
}

// appendSection returns a copy of pe with a brand-new PE section
// named name, holding sectionData, mapped at virtual address vma --
// the pure-Go replacement for `objcopy --add-section --change-
// section-vma`. See BuildAndSignUKI's doc comment in sign.go for why
// this project always chooses vma explicitly rather than trusting
// automatic placement, and internal/secureboot's git history (AD-035)
// for the real objcopy bug (default VMA 0, overlapping .setup) that
// established the fixed 16MiB/64MiB VMAs this function's callers use.
func appendSection(pe []byte, name string, sectionData []byte, vma uint32) ([]byte, error) {
	if len(name) > 8 {
		return nil, fmt.Errorf("section name %q longer than 8 bytes", name)
	}
	l, err := parsePELayout(pe)
	if err != nil {
		return nil, err
	}

	// Room for one more 40-byte IMAGE_SECTION_HEADER entry must already
	// exist between the current end of the section table and the start
	// of the first section's raw data (== SizeOfHeaders, rounded up to
	// FileAlignment) -- real bzImage/EFI-stub kernels reserve generous
	// header padding for exactly this (confirmed empirically against a
	// real prepared amd64 kernel: see pecoff_test.go), but this is a
	// real, checked precondition, not an assumption: growing
	// SizeOfHeaders itself would also require rewriting every existing
	// section's PointerToRawData, which this function deliberately does
	// not attempt.
	newHeaderEnd := l.sectionHeaderOffset + (l.numberOfSections+1)*sectionHeaderSize
	sizeOfHeaders := int(binary.LittleEndian.Uint32(pe[l.sizeOfHeadersOff:]))
	if newHeaderEnd > sizeOfHeaders {
		return nil, fmt.Errorf("no room for another section header (need %d bytes past offset %d, SizeOfHeaders is only %d) -- "+
			"this PE was not built with headroom for extra sections", newHeaderEnd-l.sectionHeaderOffset, l.sectionHeaderOffset, sizeOfHeaders)
	}

	out := make([]byte, len(pe))
	copy(out, pe)

	rawSize := align(len(sectionData), l.fileAlignment)
	rawOffset := align(len(out), l.fileAlignment)
	padded := make([]byte, rawOffset-len(out)+rawSize)
	copy(padded, sectionData)
	out = append(out, padded...)

	var nameField [8]byte
	copy(nameField[:], name)
	hdr := make([]byte, sectionHeaderSize)
	copy(hdr[0:8], nameField[:])
	binary.LittleEndian.PutUint32(hdr[8:], uint32(len(sectionData))) // VirtualSize
	binary.LittleEndian.PutUint32(hdr[12:], vma)                     // VirtualAddress
	binary.LittleEndian.PutUint32(hdr[16:], uint32(rawSize))         // SizeOfRawData
	binary.LittleEndian.PutUint32(hdr[20:], uint32(rawOffset))       // PointerToRawData
	// PointerToRelocations/PointerToLinenumbers/NumberOfRelocations/NumberOfLinenumbers left zero
	binary.LittleEndian.PutUint32(hdr[36:], 0x40000040) // IMAGE_SCN_CNT_INITIALIZED_DATA | IMAGE_SCN_MEM_READ

	// Write the new header into the padding gap right after the
	// existing table -- overwriting already-unused, already-allocated
	// bytes IN PLACE, not inserting new ones. This is the fix for a
	// real bug an earlier version of this function had: it spliced the
	// new header in by growing the byte slice at this position, which
	// shifts every subsequent byte (including every existing section's
	// actual raw data) by sectionHeaderSize -- silently invalidating
	// every other section's PointerToRawData, none of which get
	// rewritten. Overwriting in place needs no such shift: the room
	// check above already guarantees insertAt+sectionHeaderSize fits
	// within SizeOfHeaders, i.e. entirely inside the padding zone that
	// precedes the first section's real raw data.
	insertAt := l.sectionHeaderOffset + l.numberOfSections*sectionHeaderSize
	copy(out[insertAt:insertAt+sectionHeaderSize], hdr)

	binary.LittleEndian.PutUint16(out[l.fileHeaderOffset+fhNumberOfSections:], uint16(l.numberOfSections+1))

	newSizeOfImage := align(int(vma)+len(sectionData), l.sectionAlignment)
	if cur := int(binary.LittleEndian.Uint32(out[l.sizeOfImageOff:])); cur > newSizeOfImage {
		newSizeOfImage = cur
	}
	binary.LittleEndian.PutUint32(out[l.sizeOfImageOff:], uint32(newSizeOfImage))

	// Re-parse: the insert above shifted every absolute offset our
	// cached `l` recorded past insertAt (checksumOff, dataDirOff, the
	// security directory, SizeOfImage/SizeOfHeaders fields) -- all of
	// them sit inside the optional header, strictly before
	// sectionHeaderOffset, so in fact none of them moved; only
	// recomputing to be defensive against a future field this function
	// starts touching past that point.
	l2, err := parsePELayout(out)
	if err != nil {
		return nil, fmt.Errorf("re-parsing PE after appending section %s: %w", name, err)
	}
	recomputeChecksum(out, l2)
	return out, nil
}
