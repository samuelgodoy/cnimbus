package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// runKVServe implements "cnimbus kv-serve": the host side of the AGENT
// directive. It serves the *current* contents of a local JSON file,
// re-read from disk on every request -- so editing that file (any text
// editor, no restart, no special API) and saving takes effect the very
// next time a running VM's AGENT loop polls it. This is deliberately
// the whole mechanism: no database, no write API -- the file on disk is
// the single source of truth, exactly as readable and editable as any
// other config file.
//
// Defaults to binding 127.0.0.1, not every interface: a guest reaches
// this over the hypervisor's NAT gateway (10.0.2.2 for VirtualBox, its
// own equivalent elsewhere), which arrives at the host's loopback
// address the same as any other host-side listener, no wide bind
// needed. --addr 0.0.0.0:<port> (or any explicit host) opts back into a
// wide bind for setups (e.g. Bridged networking) where the guest reaches
// the host's real LAN IP instead -- --token is strongly recommended
// whenever binding wider than loopback, since there's otherwise nothing
// but network topology standing between this and anyone who can reach
// the port. --tls-cert/--tls-key (set together) serve HTTPS instead of
// plain HTTP -- worth pairing with --token in that same wide-bind case,
// since a bearer token sent over plain HTTP is exactly as visible to
// anyone on the network path as no token at all.
func runKVServe(args []string) error {
	fs := flag.NewFlagSet("kv-serve", flag.ExitOnError)
	file := fs.String("file", "kv.json", "JSON file to serve; edit and save it any time")
	addr := fs.String("addr", "127.0.0.1:9999", "address to listen on (use 0.0.0.0:<port> for Bridged-networking setups)")
	token := fs.String("token", "", "require this bearer token (Authorization: Bearer <token>) on every "+
		"request; the guest's own AGENT poller can send it too -- add \"AGENT header Authorization: "+
		"Bearer <token>\" right after the Nimbusfile's \"AGENT <url>\" line (see README's Live-config "+
		"section) -- strongly recommended whenever binding wider than loopback")
	generateToken := fs.Bool("generate-token", false, "generate a random --token value and print it instead of requiring one to be passed")
	tlsCert := fs.String("tls-cert", "", "PEM certificate file -- serves HTTPS instead of plain HTTP when set together with --tls-key")
	tlsKey := fs.String("tls-key", "", "PEM private key file -- serves HTTPS instead of plain HTTP when set together with --tls-cert")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be set together (got --tls-cert=%q --tls-key=%q)", *tlsCert, *tlsKey)
	}

	if *generateToken {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("generating token: %w", err)
		}
		*token = hex.EncodeToString(buf)
		fmt.Printf("generated token: %s\n", *token)
	}

	if _, err := os.Stat(*file); os.IsNotExist(err) {
		if err := os.WriteFile(*file, []byte("{}\n"), 0o644); err != nil {
			return fmt.Errorf("creating %s: %w", *file, err)
		}
		fmt.Printf("wrote empty %s -- edit it to add keys\n", *file)
	} else if data, err := os.ReadFile(*file); err == nil {
		if !json.Valid(data) {
			return fmt.Errorf("%s does not contain valid JSON -- fix it before serving "+
				"(a guest's AGENT loop would otherwise write this invalid content straight "+
				"into /var/run/cnimbus-kv.json)", *file)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if *token != "" {
			const prefix = "Bearer "
			auth := r.Header.Get("Authorization")
			if len(auth) != len(prefix)+len(*token) || auth[:len(prefix)] != prefix ||
				subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(*token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !json.Valid(data) {
			http.Error(w, fmt.Sprintf("%s no longer contains valid JSON", *file), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data) // a write failure here just means the client already went away
	})

	scheme := "http"
	if *tlsCert != "" {
		scheme = "https"
	}
	fmt.Printf("serving %s on %s://%s\n", *file, scheme, *addr)
	fmt.Printf("point a Nimbusfile's AGENT directive at it from the guest, e.g.:\n")
	fmt.Printf("  AGENT %s://10.0.2.2:9999/ 5   (VirtualBox NAT's gateway IP reaches the host; adjust the scheme/port to match --addr/--tls-cert)\n", scheme)
	fmt.Printf("edit %s and save -- no restart needed, guests pick it up on their next poll\n", *file)

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
		// A bare http.ListenAndServe has no read/write deadlines at all --
		// a slow-loris-style client (or just a hung guest) could hold a
		// connection open indefinitely. These are generous enough for
		// this server's tiny JSON responses while still bounding worst case.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	if *tlsCert != "" {
		return srv.ListenAndServeTLS(*tlsCert, *tlsKey)
	}
	return srv.ListenAndServe()
}
