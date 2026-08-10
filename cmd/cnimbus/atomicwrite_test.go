package main

import (
	"os"
	"path/filepath"
	"testing"
)

// T47/T60: writeFileAtomic is the shared mechanism behind
// pieces.sha256, the .lock file, and the output image itself -- all
// three previously written directly to their final path with no
// temp+rename, leaving a truncated-but-existing file there on an
// interrupted write.
func TestWriteFileAtomicWritesAndCleansUpTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := writeFileAtomic(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected the .tmp file to be gone after a successful write, stat err = %v", err)
	}
}

func TestWriteFileAtomicNeverLeavesATruncatedFileAtTheFinalPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	// Simulate "the process died mid-write": create the .tmp file by hand
	// (as if a prior writeFileAtomic call had gotten partway through) and
	// confirm the real path was never touched -- the whole point of
	// writing to a temp name first.
	if err := os.WriteFile(path+".tmp", []byte("half-writ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("test setup: expected %s not to exist yet", path)
	}

	if err := writeFileAtomic(path, []byte("full content"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != "full content" {
		t.Errorf("content = %q, want %q (a stale .tmp should be overwritten, not read from)", got, "full content")
	}
}
