# AGENT (vboxguest) example

Shows `AGENT vboxguest <property> [interval]`: VirtualBox's own real
Guest Properties channel, reached from scratch via the mainline Linux
`VBoxGuest` driver (`CONFIG_VBOXGUEST`) -- no Guest Additions installed
in the image. Unlike [agent-http-kv](../agent-http-kv/), this needs no
guest networking path to the host at all; it's VirtualBox-only in
exchange.

## 1. Build helloserver

From the repo root:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o examples/agent-vboxguest-kv/helloserver ./cmd/helloserver
```

## 2. Build and boot

```bash
cd examples/agent-vboxguest-kv
cnimbus build-disk -f Nimbusfile
```

Attach `cnimbus-agent-vbox-demo.iso` to a VirtualBox VM and boot it.

## 3. Set the property from the host

`cnimbusagent` writes whatever value you set, verbatim, to
`/var/run/cnimbus-kv.json` -- no envelope of its own added. The demo
`helloserver` looks for a `{"message": "..."}` shape in that file, so
set the property to that JSON directly:

```bash
MSYS_NO_PATHCONV=1 VBoxManage guestproperty set <vm-name> /cnimbus/message '{"message":"hello from the host"}'
```

(If your own ENTRYPOINT/SERVICE reads the kv file itself instead of
using the demo `helloserver`, set the property to whatever raw value
or shape it expects -- there's no fixed contract beyond "exactly the
bytes you set, unmodified".)

**Windows/Git Bash note:** without `MSYS_NO_PATHCONV=1`, Git Bash's
MSYS layer silently rewrites `/cnimbus/message` into an absolute
Windows path (e.g. `C:/Program Files/Git/cnimbus/message`) before
`VBoxManage` ever sees it -- the property name arrives mangled and
`cnimbusagent` never finds it. Confirm the name actually landed correctly
with `VBoxManage guestproperty enumerate <vm-name>`.

Within 5 seconds, request the guest's port 8080:

```
hello from cnimbus (cnimbus-agent-vbox-demo)
agent says: hello from the host
```

Change the property again (`VBoxManage guestproperty set` with a new
value) and the guest picks it up on its next poll -- no rebuild, no
reboot, no network path needed between guest and host at all.

## Notes

- `cnimbus prepare`'s kernel fragment already enables
  `CONFIG_VIRT_DRIVERS`/`CONFIG_VBOXGUEST`; nothing extra to configure
  for this to work.
- The property name is entirely yours to choose -- `/cnimbus/message` is
  just this example's convention, not a fixed path.
