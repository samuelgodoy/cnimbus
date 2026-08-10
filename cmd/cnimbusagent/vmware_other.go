//go:build !(linux && amd64)

// vmwareFetch's real implementation (vmware_linux_amd64.go/.s) pokes
// the VMware backdoor I/O port directly via a hand-written amd64 IN
// instruction -- inherently amd64-specific (VMware's ARM64 guest
// backdoor uses a different mechanism entirely), and Linux-only like
// every other AGENT kind's real implementation. This stub exists purely
// so `go build ./...`/`go vet ./...` succeed everywhere else --
// cnimbusagent only ever runs cross-compiled into a cnimbus image.
package main

import "fmt"

func vmwareFetch(string) (func() ([]byte, error), error) {
	return nil, fmt.Errorf("vmware: only implemented for linux/amd64 guests; see ROADMAP.md")
}
