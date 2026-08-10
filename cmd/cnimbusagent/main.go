// Command cnimbusagent is the single runtime binary behind the
// Nimbusfile AGENT directive, for every kind that needs a real
// implementation rather than a plain BusyBox wget/cat shell loop:
// http, virtio-serial, vboxguest, aws-imds, ibm-imds, and vmware --
// replacing what used to be two shell scripts (internal/rootfs's
// buildAgentScript/buildVirtioSerialScript) plus two separate binaries
// (cmd/vboxagent, cmd/imdsagent) with one binary sharing one
// loop/atomic-write implementation (internal/agentruntime).
//
// Invocation: cnimbusagent <kind> <value> <interval-seconds>
// "value" means different things per kind (see internal/nimbusfile's
// AGENT doc comment): a URL for http, a device path for virtio-serial, a
// Guest Property name for vboxguest, a metadata path for aws-imds/
// ibm-imds, a guestinfo.* key name for vmware. Kind http's headers, if
// any, travel via the CNIMBUS_AGENT_HEADERS environment variable (one
// "Name: Value" per line) rather than argv -- see internal/rootfs's
// buildAgentScript for why.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"cnimbus/internal/agentruntime"
)

func main() {
	if len(os.Args) < 4 {
		usage()
	}
	kind := os.Args[1]
	value := os.Args[2]
	intervalSec, err := strconv.Atoi(os.Args[3])
	if err != nil || intervalSec <= 0 {
		fmt.Fprintf(os.Stderr, "cnimbusagent: invalid interval %q\n", os.Args[3])
		os.Exit(2)
	}
	interval := time.Duration(intervalSec) * time.Second

	var fetch func() ([]byte, error)
	switch kind {
	case "http":
		fetch = httpFetch(value, parseHeadersEnv(os.Getenv("CNIMBUS_AGENT_HEADERS")))
	case "virtio-serial":
		fetch = virtioSerialFetch(value, interval)
	case "vboxguest":
		f, err := vboxGuestFetch(value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cnimbusagent: %v\n", err)
			os.Exit(1)
		}
		fetch = f
	case "aws-imds":
		fetch = func() ([]byte, error) { return awsFetch(value) }
	case "ibm-imds":
		fetch = func() ([]byte, error) { return ibmFetch(value) }
	case "vmware":
		f, err := vmwareFetch(value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cnimbusagent: %v\n", err)
			os.Exit(1)
		}
		fetch = f
	default:
		fmt.Fprintf(os.Stderr, "cnimbusagent: unknown AGENT kind %q\n", kind)
		os.Exit(2)
	}

	agentruntime.Loop(agentruntime.KVPath, interval, fetch)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: cnimbusagent <kind> <value> <interval-seconds>`)
	os.Exit(2)
}

// parseHeadersEnv reads one "Name: Value" header per line out of raw --
// the CNIMBUS_AGENT_HEADERS environment variable, only ever set for
// kind "http" (see internal/rootfs's buildAgentScript, which builds it
// from the Nimbusfile's AGENT header lines). An empty raw (the common
// case -- no headers configured) returns an empty map.
func parseHeadersEnv(raw string) map[string]string {
	headers := map[string]string{}
	if raw == "" {
		return headers
	}
	for _, line := range strings.Split(raw, "\n") {
		name, val, ok := strings.Cut(line, ":")
		if ok {
			headers[strings.TrimSpace(name)] = strings.TrimSpace(val)
		}
	}
	return headers
}
