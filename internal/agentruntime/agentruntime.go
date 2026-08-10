// Package agentruntime is the polling-loop and atomic-write logic shared
// by every AGENT kind cmd/cnimbusagent implements: fetch a value, write
// it atomically to /var/run/cnimbus-kv.json, sleep, repeat. Kept
// separate from cmd/cnimbusagent so each kind's own fetch logic
// (net/http, VBoxGuest ioctl, VMware backdoor I/O, a virtio-console
// device read) is the only thing that differs between them.
package agentruntime

import (
	"fmt"
	"os"
	"time"
)

// writeExclusive creates path fresh (refusing to follow a pre-existing
// symlink or regular file) and writes value to it. A pre-existing entry
// at path is unlinked and retried once -- WriteKV always wants the tmp
// file gone by the time it's done with it anyway (it's renamed over the
// real path next), so an EEXIST here only ever means a stale leftover
// from a previous run, not a conflict to preserve.
func writeExclusive(path string, value []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if os.IsExist(err) {
		if rmErr := os.Remove(path); rmErr != nil {
			return fmt.Errorf("removing stale %s: %w", path, rmErr)
		}
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(value)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// KVPath is where every AGENT kind writes its fetched value.
const KVPath = "/var/run/cnimbus-kv.json"

// WriteKV atomically replaces path's contents with value: written to a
// sibling ".tmp" file first, then renamed over the real path, so a
// reader (any ENTRYPOINT/SERVICE) never observes a half-written fetch.
// 0o644, deliberately: whatever reads this file may run as an
// unprivileged USER, a different uid than this agent (root) -- it must
// stay world-readable for that to work.
func WriteKV(path string, value []byte) error {
	tmp := path + ".tmp"
	if err := writeExclusive(tmp, value, 0o644); err != nil { // #nosec G306
		return err
	}
	return os.Rename(tmp, path)
}

// Loop calls fetch every interval, forever, writing whatever it returns
// to path via WriteKV. A fetch error is logged to stderr and otherwise
// ignored -- the last-known-good value stays in place, exactly like
// every AGENT kind before this package existed.
func Loop(path string, interval time.Duration, fetch func() ([]byte, error)) {
	for {
		value, err := fetch()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cnimbusagent: %v\n", err)
		} else if err := WriteKV(path, value); err != nil {
			fmt.Fprintf(os.Stderr, "cnimbusagent: writing kv file: %v\n", err)
		}
		time.Sleep(interval)
	}
}
