package main

import (
	"fmt"
	"os"
)

// writeFlatVMDK writes a "monolithicFlat" VMDK descriptor at vmdkPath
// that points at rawPath as its single data extent -- VMware's own
// documented way to present an existing raw disk image as an attachable
// virtual disk without copying or converting it (the same trick Packer's
// vmware builder uses). The descriptor is a small plain-text file; the
// actual disk bytes stay exactly where rawPath already is.
func writeFlatVMDK(vmdkPath, rawPath string) error {
	info, err := os.Stat(rawPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", rawPath, err)
	}
	const sectorSize = 512
	sectors := info.Size() / sectorSize
	if info.Size()%sectorSize != 0 {
		return fmt.Errorf("%s is %d bytes, not a multiple of the %d-byte sector size VMDK requires", rawPath, info.Size(), sectorSize)
	}

	// CHS geometry VMware's own vmware-vdiskmanager uses for a flat
	// extent it didn't create itself: 16 heads, 63 sectors/track (the
	// traditional "large disk" BIOS translation), cylinders derived from
	// the extent's real sector count so heads*sectors*cylinders never
	// exceeds it.
	const heads, sectorsPerTrack = 16, 63
	cylinders := sectors / (heads * sectorsPerTrack)
	if cylinders == 0 {
		cylinders = 1
	}

	descriptor := fmt.Sprintf(`# Disk DescriptorFile
version=1
CID=fffffffe
parentCID=ffffffff
createType="monolithicFlat"

# Extent description
RW %d FLAT "%s" 0

# The Disk Data Base
#DDB

ddb.virtualHWVersion = "19"
ddb.geometry.cylinders = "%d"
ddb.geometry.heads = "%d"
ddb.geometry.sectors = "%d"
ddb.adapterType = "ide"
`, sectors, rawPath, cylinders, heads, sectorsPerTrack)

	if err := os.WriteFile(vmdkPath, []byte(descriptor), 0o644); err != nil { // #nosec G306 -- a VMDK descriptor is plain VM config, not a secret
		return fmt.Errorf("writing %s: %w", vmdkPath, err)
	}
	return nil
}
