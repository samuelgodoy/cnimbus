// flaky-server answers HTTP requests normally for the first ~20 seconds
// after each start, then stops responding (without exiting or
// crashing) -- deliberately wedged, to exercise HEALTHCHECK's
// interval/retries-driven SIGTERM-then-SIGKILL restart path in the
// hardcoded-restart-restart Nimbusfile example, instead of the
// crash-loop backoff a real exit would trigger.
package main

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

func main() {
	var wedged atomic.Bool

	go func() {
		time.Sleep(20 * time.Second)
		wedged.Store(true)
		log.Println("flaky-server: going silent now (simulating a hang)")
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if wedged.Load() {
			select {} // block forever, never respond
		}
		fmt.Fprintln(w, "flaky-server: ok")
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if wedged.Load() {
			select {}
		}
		w.WriteHeader(http.StatusOK)
	})

	log.Println("flaky-server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
