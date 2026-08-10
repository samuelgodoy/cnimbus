# AGENT (HTTP) example

Shows the `AGENT <url> [interval]` directive: a running VM polls a
plain HTTP(S) endpoint on the host and writes the response body to
`/var/run/cnimbus-kv.json`, so a value you change on the host shows up
in the guest within one poll interval -- no rebuild, no reboot. Works
on any hypervisor with guest networking; no vendor guest tools needed.

## 1. Build helloserver (the demo app that reads the KV file)

From the repo root:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o examples/agent-http-kv/helloserver ./cmd/helloserver
```

## 2. Start the host-side KV server

```bash
cnimbus kv-serve --file kv.json --addr :9999
```

This creates an empty `kv.json` (`{}`) if it doesn't exist yet, and
serves its current contents on every request -- no restart needed to
change what it serves, just edit and save the file.

## 3. Build and boot

```bash
cd examples/agent-http-kv
cnimbus build-disk -f Nimbusfile
```

Boot `cnimbus-agent-http-demo.iso` with NAT networking (VirtualBox's
default). Request the guest's port 8080 (forward it in VirtualBox's NAT
settings, or however your hypervisor exposes guest ports) and you'll
see:

```
hello from cnimbus (cnimbus-agent-http-demo)
```

(no second line yet -- `kv.json` is still `{}`)

## 4. Change the value live

With the VM still running, edit `kv.json` on the host:

```json
{"message": "updated without rebuilding or rebooting"}
```

Save it. Within 5 seconds (the interval in the Nimbusfile), request the
guest again:

```
hello from cnimbus (cnimbus-agent-http-demo)
agent says: updated without rebuilding or rebooting
```

## Notes

- `10.0.2.2` is specifically VirtualBox NAT's address for the host from
  inside the guest. QEMU's user-mode networking uses the same
  `10.0.2.2` convention by default. Adjust the URL if you're on a
  different network mode (bridged, host-only) -- use whatever address
  actually reaches the host running `cnimbus kv-serve`.
- The response body just needs to be valid JSON with a `"message"` key
  for helloserver to pick up -- `cnimbus kv-serve` doesn't validate or
  interpret it at all, it's just serving a file.
