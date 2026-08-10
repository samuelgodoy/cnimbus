package rootfs

import (
	"os"
	"path/filepath"
	"testing"
)

// T79: PiecesSpec.TmpDir must actually reach buildSquashfsRoot's
// os.CreateTemp call, not be silently ignored -- proven the same way
// isoimage's equivalent test does: pointing it at a directory that
// doesn't exist must fail immediately (os.CreateTemp's own behavior for
// a nonexistent parent), which only happens if the field is really used.
func TestBuildSquashfsRootHonorsTmpDir(t *testing.T) {
	spec := PiecesSpec{}
	if _, err := buildSquashfsRoot(spec, nil); err != nil {
		t.Fatalf("buildSquashfsRoot with no TmpDir override should still succeed: %v", err)
	}

	spec.TmpDir = filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := os.Stat(spec.TmpDir); err == nil {
		t.Fatal("test setup bug: TmpDir unexpectedly exists")
	}
	if _, err := buildSquashfsRoot(spec, nil); err == nil {
		t.Fatal("expected buildSquashfsRoot to fail when TmpDir doesn't exist, proving TmpDir is actually used")
	}
}
