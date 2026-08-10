package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// virtioSerialFetch is the AGENT virtio-serial kind: device holds the
// guest-side path QEMU's virtio-console exposes (e.g. "/dev/vport0p1",
// named by the host's own "-device virtserialport,name=..."). Unlike
// the HTTP kind's simple request/response, a virtio-console port is a
// raw byte stream with no message framing of its own -- the host side
// is expected to write one complete value per push, captured here
// within one poll interval (previously done by `timeout <interval> cat
// <device>` -- see internal/rootfs's old buildVirtioSerialScript).
func virtioSerialFetch(device string, interval time.Duration) func() ([]byte, error) {
	return func() ([]byte, error) {
		f, err := os.Open(device)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()

		// Best-effort: virtio-console ports are pollable char devices, so
		// this deadline stops the read after one interval instead of
		// blocking indefinitely if the host stays silent -- the same
		// behavior `timeout <interval> cat <device>` gave the shell-script
		// predecessor of this fetcher. If the device doesn't support Go's
		// deadline machinery, SetReadDeadline's error is ignored and the
		// read just blocks until the host writes something.
		_ = f.SetReadDeadline(time.Now().Add(interval))

		data, err := io.ReadAll(f)
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("virtio-serial: no data from %s within %s", device, interval)
		}
		return data, nil
	}
}
