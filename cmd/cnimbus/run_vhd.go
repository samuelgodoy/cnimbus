package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
)

// vhdCookie is the fixed 8-byte magic every VHD footer starts with, per
// Microsoft's "Virtual Hard Disk (VHD) Image Format Specification".
const vhdCookie = "conectix"

// vhdEpoch is the VHD format's own timestamp epoch (spec: "stored as the
// number of seconds since January 1, 2000 12:00:00 AM UTC") -- distinct
// from Unix's 1970 epoch, so every timestamp field must be offset against
// this, not time.Unix() directly.
var vhdEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// vhdFooter builds the 512-byte trailing footer of a Hyper-V "Fixed"
// (not Dynamic or Differencing) VHD image for a disk of diskSizeBytes,
// per the Microsoft spec's documented field layout and CHS geometry
// algorithm (Appendix: "CHS Calculation"). A Fixed VHD is exactly this
// footer appended to the disk's own raw bytes -- no header copy at
// offset 0 and no block allocation table, unlike Dynamic/Differencing.
//
// diskSizeBytes must be a multiple of 512 (the sector size every field
// below assumes) -- internal/rawimage.Write already produces images
// aligned to a full MiB, so this is never a practical constraint.
func vhdFooter(diskSizeBytes int64) ([]byte, error) {
	if diskSizeBytes <= 0 || diskSizeBytes%512 != 0 {
		return nil, fmt.Errorf("VHD disk size must be a positive multiple of 512 bytes, got %d", diskSizeBytes)
	}

	footer := make([]byte, 512)
	copy(footer[0:8], vhdCookie)

	// Features: bit 1 ("Reserved") must always be set per the spec; bit 0
	// ("Temporary") is left clear -- this is a real, persistent disk image.
	binary.BigEndian.PutUint32(footer[8:12], 0x00000002)
	// File Format Version: 1.0, the only version the spec defines.
	binary.BigEndian.PutUint32(footer[12:16], 0x00010000)
	// Data Offset: 0xFFFFFFFFFFFFFFFF for a Fixed disk -- the spec's way
	// of saying "there is no Dynamic Disk Header to find".
	binary.BigEndian.PutUint64(footer[16:24], 0xFFFFFFFFFFFFFFFF)
	binary.BigEndian.PutUint32(footer[24:28], uint32(time.Now().UTC().Sub(vhdEpoch).Seconds()))
	// Creator Application / Creator Host OS: 4-byte ASCII tags the spec
	// defines as free-form identification, not machine-checked by any
	// consumer -- "Wi2k" is the spec's own documented tag for "created on
	// Windows" (this backend is Windows-only; see run_hyperv.go).
	copy(footer[28:32], "cnim")
	binary.BigEndian.PutUint32(footer[32:36], 0x00010000) // Creator Version 1.0
	copy(footer[36:40], "Wi2k")
	binary.BigEndian.PutUint64(footer[40:48], uint64(diskSizeBytes)) // Original Size
	binary.BigEndian.PutUint64(footer[48:56], uint64(diskSizeBytes)) // Current Size

	cylinders, heads, sectorsPerTrack := vhdCHSGeometry(diskSizeBytes / 512)
	binary.BigEndian.PutUint16(footer[56:58], cylinders)
	footer[58] = heads
	footer[59] = sectorsPerTrack

	binary.BigEndian.PutUint32(footer[60:64], 2) // Disk Type: 2 = Fixed hard disk

	id, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("generating VHD unique ID: %w", err)
	}
	copy(footer[68:84], id[:])
	// footer[84] (Saved State) and footer[85:512] (Reserved) are left
	// zero, matching the spec for an image with no saved VM state.

	// Checksum: one's complement of the sum of every byte in the footer
	// with the checksum field itself treated as zero -- computed last, so
	// every other field above is already in its final place.
	var sum uint32
	for _, b := range footer {
		sum += uint32(b)
	}
	binary.BigEndian.PutUint32(footer[64:68], ^sum)

	return footer, nil
}

// vhdCHSGeometry implements the Microsoft VHD spec's documented CHS
// (cylinders/heads/sectors-per-track) calculation exactly -- the same
// algorithm QEMU's block/vpc.c and VirtualBox's own VHD support use,
// since the spec defines one canonical answer, not a per-implementation
// heuristic. cylinders is capped at 65535 (a uint16 field), so this is
// never called with more sectors than that implies.
func vhdCHSGeometry(totalSectors int64) (cylinders uint16, heads, sectorsPerTrack uint8) {
	const maxSectors = 65535 * 16 * 255
	if totalSectors > maxSectors {
		totalSectors = maxSectors
	}

	var sectorsPerTrackInt, headsInt int64
	var cylTimesHeads int64
	if totalSectors >= 65535*16*63 {
		sectorsPerTrackInt = 255
		headsInt = 16
		cylTimesHeads = totalSectors / sectorsPerTrackInt
	} else {
		sectorsPerTrackInt = 17
		cylTimesHeads = totalSectors / sectorsPerTrackInt
		headsInt = (cylTimesHeads + 1023) / 1024
		if headsInt < 4 {
			headsInt = 4
		}
		if cylTimesHeads >= headsInt*1024 || headsInt > 16 {
			sectorsPerTrackInt = 31
			headsInt = 16
			cylTimesHeads = totalSectors / sectorsPerTrackInt
		}
		if cylTimesHeads >= headsInt*1024 {
			sectorsPerTrackInt = 63
			headsInt = 16
			cylTimesHeads = totalSectors / sectorsPerTrackInt
		}
	}
	return uint16(cylTimesHeads / headsInt), uint8(headsInt), uint8(sectorsPerTrackInt)
}

// writeFixedVHD writes a Hyper-V-attachable Fixed VHD at vhdPath: rawPath's
// bytes verbatim, followed by the 512-byte trailing footer vhdFooter
// builds for its size. rawPath itself is never modified -- this always
// produces a new file, matching writeFlatVMDK's non-destructive contract
// for the VMware backend.
func writeFixedVHD(vhdPath, rawPath string) error {
	info, err := os.Stat(rawPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", rawPath, err)
	}
	footer, err := vhdFooter(info.Size())
	if err != nil {
		return fmt.Errorf("building VHD footer for %s: %w", rawPath, err)
	}

	src, err := os.Open(rawPath) // #nosec G304 -- rawPath is this build's own output image, not user-influenced-by-name input
	if err != nil {
		return fmt.Errorf("opening %s: %w", rawPath, err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.Create(vhdPath) // #nosec G304 -- vhdPath is a caller-controlled workDir path, not attacker input
	if err != nil {
		return fmt.Errorf("creating %s: %w", vhdPath, err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copying %s into %s: %w", rawPath, vhdPath, err)
	}
	if _, err := dst.Write(footer); err != nil {
		return fmt.Errorf("appending VHD footer to %s: %w", vhdPath, err)
	}
	return nil
}
