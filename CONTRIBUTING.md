# Contributing to cnimbus

## Before you send a change

- `gofmt -l cmd internal` must print nothing.
- `go vet ./...` must pass.
- `go build ./...` must pass.
- `go test ./...` must pass. If you're adding behavior to
  `internal/nimbusfile`, `internal/rootfs`, `internal/pieces`, or
  `internal/kernelinfo`, add or extend a test in the same package --
  these are pure functions with no I/O (or I/O behind an interface/env
  var override), there's no excuse for a change to go in untested.
- If you touch `cmd/thunder/` or `internal/compileagent/`, run
  `go generate ./internal/assets` (see [BUILD.md](BUILD.md#modifying-thunders-source))
  so the embedded copy under `internal/assets/data/thunder-src/` stays
  byte-identical -- CI checks this and fails the build otherwise.

## Local CI equivalent

```bash
gofmt -l cmd internal
go vet ./...
go build ./...
go test ./...
go generate ./internal/assets && git diff --exit-code internal/assets/data/thunder-src
```

`.github/workflows/ci.yml` runs exactly this, plus a build of all 6
`GOOS`/`GOARCH` targets from [BUILD.md](BUILD.md).

## Scope boundaries worth knowing before you start

- **No shell anywhere in a built image, ever, by design** (see README's
  "Known limitations"). Don't add one, even behind a flag.
- **`VOLUME` never formats a device.** An earlier version of this
  project did, and it silently destroyed data. Don't reintroduce that.
- **Windows hosts can't preserve POSIX symlinks or execute bits.**
  Anything that walks a real directory tree (rather than working from
  in-memory bytes or a manifest) needs to keep working when `cnimbus`
  itself runs on Windows -- this is why BusyBox's install tree travels
  as (binary, manifest) rather than a directory of real symlinks.
- **Every container `docker run`/`docker build` call passes
  `--platform linux/<arch>` explicitly.** Never rely on Docker's default
  platform selection.

## Where things live

See README.md's "Repo layout" section.

## Testing without real hardware

There is no automated boot-test harness yet (see ROADMAP.md's "Em
aberto"/open items) -- runtime behavior changes (anything touching
`internal/rootfs`'s generated scripts) are covered by unit tests of the
*generated* shell scripts, not by an actual kernel boot. If you're
changing boot-time behavior, validate it for real before relying on the
unit tests alone. **Every kernel-config change in particular must be
validated by an actual boot, not just `cnimbus prepare` succeeding** --
`verifyFragmentsApplied` (`internal/compileagent/kernel.go`) catches a
requested symbol Kconfig silently dropped, but it can't catch a symbol
that's present in `.config` yet still behaves wrong on one specific
hypervisor (the VMware `no-vmw-sched-clock` bug below is exactly that
case: the symbol built fine, the boot still hung).

### Per-backend boot test

All four take the same image (`cnimbus build-disk` output) and the same
`--hostfwd host:guest` convention to reach a service inside the guest,
except where noted.

- **QEMU** -- `cnimbus run [--hostfwd 8080:8080] <image>`. No extra
  setup; this is the fastest loop and should be your first check for
  any change.
- **VirtualBox** -- `cnimbus run --backend vbox --hostfwd 8080:8080
  <image>`. Prints the exact `VBoxManage controlvm ... poweroff` /
  `unregistervm ... --delete` commands to clean up when done.
- **VMware Workstation/Player** -- `cnimbus run --backend vmware
  <image>`. `--hostfwd` isn't automated here (VMware's NAT port-forward
  config, `vmnetnat.conf`, is one shared file, not per-VM) -- instead,
  grep the guest's DHCP-assigned IP out of the printed `serial.log`
  (`grep -oP 'lease of \K[0-9.]+' <serial.log>`) and either `curl` it
  directly (VMware's NAT network, unlike Hyper-V's, is actually
  reachable from the host) or set up `netsh interface portproxy`
  yourself pointing at it. Prints the exact `vmrun ... stop` command to
  clean up.
- **Hyper-V** (Windows only) -- needs an **elevated** (Administrator)
  PowerShell session; every Hyper-V cmdlet and `netsh portproxy` itself
  require it, `VBoxManage`/`vmrun`/`qemu-system-*` do not.
  `cnimbus run --backend hyperv --hostfwd 8080:8080 <image>` requires
  the guest's own Nimbusfile to set a **static IP** matching the
  backend's fixed convention (see `run_hyperv.go`'s doc comment for
  why: Hyper-V's Default Switch has an asymmetric NAT the host can't
  open inbound connections through at all, so `cnimbus` creates its own
  Internal switch instead -- which has no DHCP server):
  ```
  IP 192.168.200.10 255.255.255.0 192.168.200.1
  ```
  The command self-verifies with a real HTTP request (not just a TCP
  connect -- `netsh portproxy` accepts a local connection instantly
  regardless of whether the guest is even up, so a bare connect check
  is a false-positive machine) before printing success, which can take
  up to ~90s on a slow first boot -- that's the guest's own boot time,
  not overhead worth optimizing away.

### Common gotchas when testing repeatedly

Iterating on a boot-time change means starting/stopping the same VM
name and reusing the same host port many times in a row. Everything
below was hit for real while validating this project's own kconfig
changes:

- **Stale VirtualBox registration blocks `createvm`.** If a previous
  run didn't clean up (crashed, or you killed `cnimbus run` mid-flight),
  `VBoxManage createvm` fails with "Machine settings file ... already
  exists". Fix: `VBoxManage unregistervm <name> --delete`. If deleting
  the machine folder by hand instead hits "file in use", an orphaned
  `VBoxHeadless.exe` from an earlier attempt is still holding it open --
  `Get-Process VBoxHeadless | Stop-Process -Force` first.
- **A VM can vanish from `unregistervm`'s success message but still show
  up in `list vms`.** This points at stale state in `VBoxSVC.exe` (the
  service that outlives any individual VM process) rather than the VM
  itself -- restart it: `Stop-Process -Name VBoxSVC,VBoxSDS -Force`
  (VirtualBox restarts them on the next `VBoxManage` call).
- **A leftover `netsh portproxy` rule silently wins over a new one on
  the same port.** It accepts the incoming connection immediately, then
  hangs trying to reach whatever stale address it still points at --
  which looks exactly like "the guest isn't responding" even when the
  guest is fine. Always `netsh interface portproxy delete v4tov4
  listenport=<port> listenaddress=0.0.0.0` before adding a new rule
  pointed elsewhere (the `hyperv` backend does this itself; do the same
  by hand if you're testing manually).
- **Only one backend can hold a given host port at a time.** QEMU's
  `hostfwd`, VBox's NAT engine, and a Hyper-V `netsh portproxy` rule are
  all independent listeners -- stop/clean up the previous backend
  (see above) before starting the next one on the same `--hostfwd` port.
- **A kernel-only config change doesn't need `cnimbus prepare` again if
  you're only touching `cmd/cnimbus` or the isolinux/UEFI cmdline
  string** (e.g. the VMware `no-vmw-sched-clock` fix) -- only
  `internal/compileagent`, `cmd/thunder`, or a `kconfig/*.fragment`
  change needs a real (multi-minute, Docker-based) kernel rebuild.
  Rebuilding just `cnimbus.exe` + `cnimbus build-disk` is enough
  otherwise, and much faster to iterate on.

Building a Docker-based QEMU boot harness (so none of the above
requires a local QEMU/VirtualBox/VMware/Hyper-V install) is an open
item -- see ROADMAP.md.
