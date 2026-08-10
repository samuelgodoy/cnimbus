# stopgrace-graceful-shutdown

Demonstrates `STOPGRACE`: how long a shutdown (ACPI power button,
`poweroff` from inside the guest, Ctrl-Alt-Del) waits for `ENTRYPOINT`
to exit on its own after `SIGTERM`, before the guest escalates to
`SIGKILL` and halts anyway.

`slow-shutdown-server.go` installs a `SIGTERM` handler that waits for
an in-flight 8-second "transaction" to finish before calling
`os.Exit(0)` -- exactly the kind of in-flight work (a buffered write,
an open transaction, a request already accepted) that BusyBox init's
own default shutdown budget (about 1 second) isn't long enough for.

## Build and run

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o slow-shutdown-server slow-shutdown-server.go
cnimbus build-disk -f Nimbusfile --pieces ../../pieces
cnimbus run cnimbus-stopgrace-demo.iso
```

## Reproducing it

1. From the host, fire a request that lands mid-flight when you shut
   down (e.g. `curl http://127.0.0.1:8080/` right before the next step
   -- it holds the connection open for 8 seconds).
2. Trigger a shutdown from inside the guest console (`poweroff`), or
   send the VM an ACPI shutdown from your hypervisor.
3. Watch the console: `slow-shutdown-server: SIGTERM received,
   finishing in-flight work...` appears immediately, followed about 8
   seconds later by `slow-shutdown-server: in-flight work done,
   exiting cleanly` -- the guest halts only after that, not at the
   1-second mark a default `STOPGRACE` would have forced.

Try commenting out the `STOPGRACE 15` line (or setting it to `2`) to
see the difference: the process gets killed mid-transaction instead,
and the client-side `curl` from step 1 never gets a response.
