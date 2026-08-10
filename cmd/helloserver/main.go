// helloserver is a minimal HTTP service baked into the cnimbus rootfs as
// a smoke test: proof that a userland Go binary (statically
// cross-compiled, no libc) runs correctly under the busybox-init image
// this tool assembles.
//
// It also doubles as the live demo for the AGENT directive: if
// /var/run/cnimbus-kv.json exists (AGENT's polling loop writes it -- see
// internal/rootfs/build.go's buildAgentScript), its "message" key is
// read fresh on every request and included in the response, proving a
// value set on the host propagates into a *running* VM without
// rebuilding the image or rebooting.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const kvPath = "/var/run/cnimbus-kv.json"

func main() {
	addr := ":8080"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		if _, err := fmt.Fprintf(w, "hello from cnimbus (%s)\n", hostname); err != nil {
			return
		}
		if msg, ok := readAgentMessage(); ok {
			_, _ = fmt.Fprintf(w, "agent says: %s\n", msg)
		}
	})

	log.Println("helloserver listening on", addr) // #nosec G706 -- addr is this demo binary's own argv[1] on its own host, not remote/untrusted input
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// readAgentMessage re-reads kvPath on every call -- no caching -- so
// each request reflects whatever the AGENT loop most recently fetched,
// however often that is. Missing file (no AGENT configured) or
// malformed JSON just means no line gets added, not an error response.
func readAgentMessage() (string, bool) {
	data, err := os.ReadFile(kvPath)
	if err != nil {
		return "", false
	}
	var kv struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &kv); err != nil || kv.Message == "" {
		return "", false
	}
	return kv.Message, true
}
