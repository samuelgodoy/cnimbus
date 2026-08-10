package rootfs

import (
	"os"
	"testing"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
)

// Real boot (2026-08-06) found that a VOLUME mountpoint under a path not
// already baked into the image -- e.g. this project's own
// examples/volume-persistent-disk/Nimbusfile's "VOLUME /dev/vda /mnt/data"
// -- never actually mounts: buildRCScript's "mkdir -p /mnt/data" runs
// against the read-only SquashFS root at boot, silently no-ops (no error
// checking), and the subsequent mount call fails with ENOENT because the
// target directory was never created. This test proves the fix: every
// VOLUME's mountpoint directory (including intermediate path components)
// must already exist in the produced SquashFS image itself.
func TestBuildSquashfsRootCreatesVolumeMountpoints(t *testing.T) {
	spec := PiecesSpec{
		Volumes: []Volume{
			{Device: "/dev/vda", Mountpoint: "/mnt/data", FSType: "ext4"},
			{Device: "/dev/vdb", Mountpoint: "/deeply/nested/backup", FSType: "vfat"},
		},
	}

	path, err := buildSquashfsRoot(spec, nil)
	if err != nil {
		t.Fatalf("buildSquashfsRoot: %v", err)
	}
	defer os.Remove(path)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening produced squashfs image: %v", err)
	}
	defer f.Close()

	fs, err := squashfs.Read(file.New(f, true), 0, 0, 0)
	if err != nil {
		t.Fatalf("squashfs.Read: %v", err)
	}

	for _, want := range []string{"mnt/data", "deeply", "deeply/nested", "deeply/nested/backup"} {
		if _, err := fs.ReadDir(want); err != nil {
			t.Errorf("expected directory %q to exist in the built image, got: %v", want, err)
		}
	}
}
