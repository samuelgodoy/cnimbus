# healthcheck-restart

Demonstrates `HEALTHCHECK` (detecting a wedged-but-not-crashed process
and forcing a restart) and `RESTART` (the respawn policy those
restarts, and any plain crash, go through).

`flaky-server.go` answers normally for the first ~20 seconds after
every start, then goes silent forever without exiting -- the one
failure mode a plain crash-loop restart can't catch on its own, since
the process never actually dies.

## Build and run

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o flaky-server flaky-server.go
cnimbus build-disk -f Nimbusfile --pieces ../../pieces
cnimbus run cnimbus-healthcheck-demo.iso
```

## What to watch for

After ~20 seconds the server stops answering `/health`. Three
consecutive `HEALTHCHECK` failures (15 seconds at the 5s interval
above) escalate: `SIGTERM` first, then `SIGKILL` if it's still alive a
moment later, then a fresh respawn under `RESTART entrypoint always`.
On the guest console you'll see, in order:

```
healthcheck failed (1/3)
healthcheck failed (2/3)
healthcheck failed (3/3), exceeded retry limit, sending SIGTERM
did not exit after SIGTERM, sending SIGKILL
entrypoint exited (code 137)
restart #1
flaky-server listening on :8080
```

`code 137` is `128 + 9` -- confirming the kill was really `SIGKILL`,
not a graceful exit. The cycle then repeats every ~20+15 seconds.
