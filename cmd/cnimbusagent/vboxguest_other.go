//go:build !linux

// vboxguestFetch's real implementation (vboxguest_linux.go) needs
// golang.org/x/sys/unix ioctls that only exist on Linux. This stub
// exists purely so `go build ./...`/`go vet ./...` succeed on every
// platform cnimbus itself is developed on -- cnimbusagent only ever
// runs cross-compiled into a cnimbus image's Linux guest.
package main

import "fmt"

func vboxGuestFetch(string) (func() ([]byte, error), error) {
	return nil, fmt.Errorf("vboxguest: linux-only; this binary is meant to run inside a cnimbus guest, not on the build host")
}
