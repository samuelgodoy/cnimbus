// Package rootfs assembles a busybox-init initramfs (the "newc" cpio
// format gzip'd) entirely in Go: no cpio/gzip/find binaries, no
// container. It runs on the host, after the compile phase has already
// produced a BusyBox install tree and a kernel.
package rootfs

import (
	"bytes"
	"fmt"
	"io"
)

// cpioWriter emits entries in the "newc" (SVR4 no-CRC) format that the
// Linux kernel's initramfs unpacker expects.
type cpioWriter struct {
	w   io.Writer
	ino uint32 // synthetic, just needs to be unique per entry
}

func newCPIOWriter(w io.Writer) *cpioWriter {
	return &cpioWriter{w: w, ino: 1}
}

// mode bits, matching Linux's S_IFDIR/S_IFREG/S_IFLNK.
const (
	modeDir     = 0o040000
	modeReg     = 0o100000
	modeSymlink = 0o120000
)

func (c *cpioWriter) writeEntry(name string, mode uint32, data []byte) error {
	c.ino++
	header := fmt.Sprintf(
		"070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		c.ino,     // ino
		mode,      // mode
		0,         // uid
		0,         // gid
		1,         // nlink
		0,         // mtime
		len(data), // filesize
		0, 0,      // devmajor, devminor
		0, 0, // rdevmajor, rdevminor
		len(name)+1, // namesize (includes trailing NUL)
		0,           // check
	)
	if _, err := io.WriteString(c.w, header); err != nil {
		return err
	}
	if _, err := io.WriteString(c.w, name); err != nil {
		return err
	}
	if _, err := c.w.Write([]byte{0}); err != nil {
		return err
	}
	if err := c.pad(len(header) + len(name) + 1); err != nil {
		return err
	}
	if _, err := c.w.Write(data); err != nil {
		return err
	}
	return c.pad(len(data))
}

// pad writes zero bytes so the total bytes written so far since the
// start of this header+name (or this file body) is 4-byte aligned.
func (c *cpioWriter) pad(n int) error {
	if rem := n % 4; rem != 0 {
		_, err := c.w.Write(make([]byte, 4-rem))
		return err
	}
	return nil
}

func (c *cpioWriter) writeTrailer() error {
	return c.writeEntry("TRAILER!!!", 0, nil)
}

// buildCPIO renders a fileTree into a raw (uncompressed) newc cpio
// archive.
func buildCPIO(tree *fileTree) ([]byte, error) {
	buf := &bytes.Buffer{}
	cw := newCPIOWriter(buf)

	for _, e := range tree.entries {
		var mode uint32
		switch e.kind {
		case entryDir:
			mode = modeDir | 0o755
		case entryFile:
			mode = modeReg | e.perm
		case entrySymlink:
			mode = modeSymlink | 0o777
		}
		data := e.data
		if e.kind == entrySymlink {
			data = []byte(e.linkTarget)
		}
		if err := cw.writeEntry(e.path, mode, data); err != nil {
			return nil, fmt.Errorf("writing cpio entry %s: %w", e.path, err)
		}
	}
	if err := cw.writeTrailer(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
