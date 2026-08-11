// slow-shutdown-server simulates a service with real in-flight work
// (an 8-second "transaction") that must finish before the process
// exits on SIGTERM -- the exact scenario STOPGRACE exists for.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	var wg sync.WaitGroup
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("slow-shutdown-server: SIGTERM received, finishing in-flight work...")
		wg.Wait()
		log.Println("slow-shutdown-server: in-flight work done, exiting cleanly")
		os.Exit(0)
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		wg.Add(1)
		defer wg.Done()
		time.Sleep(8 * time.Second) // the "transaction"
		if _, err := fmt.Fprintln(w, "slow-shutdown-server: transaction committed"); err != nil {
			log.Println("slow-shutdown-server: write failed:", err)
		}
	})

	log.Println("slow-shutdown-server listening on :8080")
	srv := &http.Server{
		Addr:              ":8080",
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
