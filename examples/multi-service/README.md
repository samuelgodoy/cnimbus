# SERVICE example

Shows the `SERVICE <name> <cmd...>` directive: additional respawned
processes alongside `ENTRYPOINT`/`CMD`, each under its own supervisor
entry (so one crashing and restarting doesn't affect the others).
Repeatable -- this example uses it twice.

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o examples/multi-service/helloserver ./cmd/helloserver
cd examples/multi-service
cnimbus build-disk -f Nimbusfile
```

## Boot and check

Boot `cnimbus-multiservice-demo.iso`. Three processes come up
independently:

- `helloserver :8080` -- the `ENTRYPOINT`/`CMD`
- `helloserver-admin` (`helloserver :8081`) -- a second instance of the
  same binary on a different port, via `SERVICE`
- `heartbeat` -- a plain shell loop, showing `SERVICE` works with any
  command, not just COPY'd binaries

Request ports 8080 and 8081 separately -- both respond independently.
Watch the console for `heartbeat` printing every 30 seconds.

## Notes

- `USER` (if set) applies to every `SERVICE` the same as it applies to
  `ENTRYPOINT`/`CMD` -- there's no per-service user override.
- Exec form (`["/bin/sh", "-c", "..."]`) and shell form
  (`/usr/bin/helloserver :8081`) are both accepted for `SERVICE`'s
  command, exactly like `ENTRYPOINT`/`CMD`.
