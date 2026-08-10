package main

import (
	"fmt"
	"os"
)

// writeFileAtomic writes data to path via a same-directory temp file,
// Sync()s it, then os.Renames over the final name. Without this (T47), a
// process kill or a full disk between create and the last write leaves a
// truncated-but-existing file sitting at exactly the path a later step
// (build-disk reading pieces.sha256, a user opening a .lock file) trusts
// as complete -- syntactically valid, silently missing whatever entries
// hadn't been written yet.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp) // best-effort; the write error above is what's returned
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp) // best-effort; the sync error above is what's returned
		return fmt.Errorf("syncing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp) // best-effort; the close error above is what's returned
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort; the rename error above is what's returned
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}
