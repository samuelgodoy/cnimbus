🇧🇷 [Leia em português](README.md) · 🇺🇸 English (this file)

# cnimbus

A tool that builds a minimal, distroless-style, bootable Linux VM image
from a declarative manifest (a **Nimbusfile**, Dockerfile-style), with
amd64 or arm64 as the guest architecture. Runs on QEMU, VirtualBox,
VMware, Hyper-V, Proxmox, Firecracker, and physical hardware -- see
"Supported platforms" below for what each one offers.

Worth separating two things up front: the **guest** architecture (the
VM image cnimbus builds) is amd64 or arm64; the **host** architecture
(the machine you run `cnimbus` itself on) also includes **riscv64** on
Linux, on top of amd64/arm64 on Windows, Linux, and macOS -- see
"Building from source" below.

Unikernel-shaped, not a unikernel: the goal (single workload, no shell,
no login, minimal attack surface, boots straight into your service) is
the same one projects like MirageOS, Unikraft, or OSv pursue, but
cnimbus gets there differently -- a real, unmodified mainline Linux
kernel plus BusyBox, not a purpose-built library OS linked directly
into your application's address space. That trade-off means less raw
minimalism than a true unikernel, in exchange for running an ordinary
Go (or any statically-linked) binary unchanged, real POSIX semantics,
and the kernel's own driver/hardware support -- no rewriting your
workload against a unikernel-specific runtime.

A full LaTeX user manual, in Brazilian Portuguese (installation through
every Nimbusfile directive, every backend, security/Secure Boot,
bare-metal boot, and a worked walkthrough of every example below --
commands, flags, and directive names stay in English throughout), is at
[docs/manual/cnimbus-manual.pdf](docs/manual/cnimbus-manual.pdf)
(source: [cnimbus-manual.tex](docs/manual/cnimbus-manual.tex)).

## One binary, eight subcommands, two very different worlds

`cnimbus` is a single standalone executable:

- **`cnimbus init`** -- writes an example Nimbusfile.
- **`cnimbus prepare`** -- the *only* command that touches Docker: it
  compiles a small Go program called **Thunder** for the target
  architecture (in a throwaway `golang` container -- no local Go
  toolchain needed), then uses it to compile the Linux kernel + BusyBox
  + a static `iptables` inside a second throwaway container, exporting
  the result as "pieces" (`vmlinuz`, a static `busybox` binary, its
  applet manifest, and a static `iptables` binary). Docker is
  unavoidable here: compiling the kernel needs Kbuild (make + a
  Linux-targeting C compiler + a POSIX shell), which doesn't exist
  natively on Windows or macOS. If a Nimbusfile is present in the
  current directory, `prepare` reads its `KERNEL`/`BUSYBOX`/`ARCH`/`VGA`
  directives (see "Nimbusfile vs. flags" below); with no Nimbusfile, it
  falls back to kernel.org's latest stable release and BusyBox's own
  built-in default version. The kernel tarball's PGP signature is
  verified against kernel.org's signer key, fetched live via Web Key
  Directory -- no key material embedded in `cnimbus` itself
  (`--insecure-skip-kernel-verify` to disable, e.g. for an offline mirror).
- **`cnimbus build-disk`** -- never touches Docker, never touches a
  compiler. Pure Go: fetches those pieces (from a local directory,
  cached locally, or a plain HTTP(S) URL, hash-checked against a
  `pieces.sha256` written by `prepare`) and assembles a bootable image
  (`FORMAT iso` or `FORMAT raw`), writing a `<output>.lock` build
  manifest alongside it.
- **`cnimbus validate`** -- checks a Nimbusfile without building
  anything: syntax, that every `COPY`/`ADD` local source exists, and
  that any copied ELF binary's architecture actually matches the
  Nimbusfile's declared `ARCH`.
- **`cnimbus clean`** -- removes the Docker volumes/images `prepare`
  creates (and optionally the local pieces cache), with `--dry-run`.
- **`cnimbus run`** -- boots a built image locally, via QEMU if it's on
  `PATH` or via `--vbox` (VirtualBox, through `VBoxManage`); otherwise
  prints the manual boot command for your hypervisor.
- **`cnimbus kv-serve`** -- the host side of the `AGENT` directive's
  HTTP transport; see "Live config" below.
- **`cnimbus version`** -- prints the build version.

Run `prepare` once per architecture you need (or whenever you want a
fresh kernel), publish its output directory somewhere, and `build-disk`
never needs Docker installed at all -- it just downloads and
assembles.

## Quick start

```bash
# one-time, or whenever you want a fresh kernel: produces ./pieces/amd64 (needs Docker)
cnimbus prepare --arch amd64 --out ./pieces

# and/or, for arm64:
cnimbus prepare --arch arm64 --out ./pieces

# write an example Nimbusfile
cnimbus init

# edit Nimbusfile, then (no Docker involved from here on):
cnimbus build-disk --pieces ./pieces -o my-image.iso
```

Debugging a boot in a GUI hypervisor (VirtualBox chief among them) and
seeing nothing? By default the image only outputs to the serial
console (what QEMU/Proxmox setups normally rely on) -- `console=tty0`
is declared but has no real video driver behind it, so a GUI display
window stays black even on a successful boot. Add `--vga` to `prepare`
to enable one:

```bash
cnimbus prepare --arch amd64 --vga --out ./pieces
```

`--pieces` also accepts an `http://`/`https://` URL prefix once you've
published a `prepare --out` directory somewhere (it fetches
`<url>/<arch>/vmlinuz`, `<url>/<arch>/busybox`,
`<url>/<arch>/busybox-manifest.tsv`). You can set `CNIMBUS_PIECES` instead
of passing `--pieces` every time.

See [examples/](examples/) for complete, working Nimbusfiles covering
`ENV`, `VOLUME`, both `AGENT` modes, `SERVICE`, a static `IP` +
`FIREWALL`, and `FORMAT raw` -- each with its own build/boot
instructions.

## Reaching a service running in the guest from your host

If your Nimbusfile's `ENTRYPOINT`/`CMD` runs something that listens on a
port (like the demo `helloserver` above on `:8080`), the default
networking mode on every mainstream hypervisor (NAT) hides the guest
behind an address only the guest itself can see -- `localhost:8080` on
your host will just refuse the connection until you add a port-forward
rule mapping a host port to that guest port. This is a hypervisor
setting, not something `cnimbus` controls.

**VirtualBox** -- GUI: select the VM -> *Settings* -> *Network* ->
*Advanced* -> *Port Forwarding* -> add a rule (leave "Host IP" and
"Guest IP" blank, set Host Port `8080` and Guest Port `8080`). CLI
equivalent, run before starting the VM:

```bash
VBoxManage modifyvm "<vm-name>" --natpf1 "http,tcp,127.0.0.1,8080,,8080"
```

Then `curl http://127.0.0.1:8080/` from the host reaches the guest.

**VMware Workstation/Player (Windows/Linux)** -- *Edit* -> *Virtual
Network Editor* -> select the NAT network (usually VMnet8) -> *NAT
Settings* -> *Add* -> Host port `8080` -> the guest's IP + port `8080`,
TCP. Requires running Virtual Network Editor as Administrator/root for
the Add button to be enabled.

**VMware Fusion (macOS)** -- edit
`/Library/Preferences/VMware Fusion/vmnet8/nat.conf` directly, add
under `[incomingtcp]`:

```
8080 = <guest-ip>:8080
```

then restart networking: `sudo /Applications/VMware Fusion.app/Contents/Library/vmnet-cli --stop`
followed by `--start`.

**QEMU** -- pass a `hostfwd` rule on the user-mode networking backend,
no extra setup needed:

```bash
qemu-system-x86_64 ... -netdev user,id=n0,hostfwd=tcp:127.0.0.1:8080-:8080 -device virtio-net-pci,netdev=n0 ...
```

(`127.0.0.1` binds the forwarded port to loopback only -- the same
default `cnimbus run` itself uses; an empty host address there instead
binds every interface, reachable from your whole network.)

**Hyper-V** -- unlike the others, Hyper-V's own "Default Switch" NAT
rejects inbound connections from the host entirely, so there's no
simple per-VM port-forward setting to reach for. See `--backend
hyperv` below, which scripts a working equivalent (a switch `cnimbus`
owns, plus a static IP requirement) instead of a manual walkthrough.

**Simpler alternative, any hypervisor**: switch the VM's network
adapter from NAT to **Bridged**. The guest then gets its own IP on your
LAN and you reach it directly at `http://<guest-ip>:8080` -- no
port-forward rules to maintain, at the cost of the guest being visible
to your whole network instead of just the host.

**Or skip all of the above**: `cnimbus run --hostfwd 8080:8080`
automates exactly this port-forward for `--backend qemu` (its own
`hostfwd`), `vbox` (`VBoxManage --natpf1`), and `hyperv` (the switch
described above) -- see `cnimbus run -h`. `--backend vmware` doesn't
automate it (VMware's NAT port-forward config is a single shared
`vmnetnat.conf`, not per-VM), so its own section above still applies.
All three automated backends bind the forwarded port to loopback
(`127.0.0.1`) by default; pass `--hostfwd-bind 0.0.0.0` (or a specific
address) to deliberately expose it beyond this machine.

## Live config: the AGENT directive

Everything else in a Nimbusfile (`ENV`, `COPY`, ...) is baked in at
`build-disk` time -- changing a value means rebuilding the image and
rebooting the VM. `AGENT` is the one exception: a value you change on
the host reaches a *running* VM within a few seconds, no rebuild, no
reboot. Several transports, pick whichever fits your hypervisor/cloud:

```dockerfile
AGENT http://10.0.2.2:9999/ 3           # plain HTTP -- any hypervisor with guest networking
AGENT vboxguest /cnimbus/message 3      # VirtualBox's own real channel -- see below
AGENT virtio-serial /dev/vport0p1 3     # QEMU/Proxmox, no qemu-guest-agent needed
AGENT aws-imds /latest/meta-data/tags 3 # AWS EC2 IMDSv2
AGENT ibm-imds /metadata/v1/instance 3  # IBM Cloud VPC metadata
```

Both write to the same place -- `/var/run/cnimbus-kv.json` (tmpfs;
survives until reboot, gone on power-off, not meant to persist across
boots -- pair with `VOLUME` if you need that instead) -- so anything
reading it (the demo `helloserver` re-reads it on every request) works
identically regardless of which transport is behind it.

### `AGENT <url> [interval]` -- plain HTTP

Works on any hypervisor with guest networking, nothing hypervisor-
specific involved:

1. **`cnimbus kv-serve`** runs on your host and serves the live content of
   a local JSON file, re-read from disk on every request:
   ```bash
   cnimbus kv-serve --file kv.json --addr :9999
   ```
   Edit `kv.json` and save -- no restart, no API call, the next request
   already returns the new content.
2. **The guest's `AGENT` loop** (BusyBox `wget` in a `while true` loop,
   nothing more) fetches that URL every `[interval]` seconds and writes
   the response body to the kv file.

Reaching the host from the guest needs the right address for your
hypervisor's NAT gateway -- `10.0.2.2` for VirtualBox (used above),
similarly a fixed gateway IP for most others; switch the guest to
**Bridged** networking (see above) and use the host's real LAN IP
instead if you'd rather not look it up per-hypervisor.

If `--addr` binds wider than loopback (a Bridged setup), pair `cnimbus
kv-serve --token <token>` (or `--generate-token`) with an `AGENT
header` line so the guest's own poller authenticates too:

```bash
cnimbus kv-serve --file kv.json --addr :9999 --token secret123
```

```dockerfile
AGENT http://192.168.1.50:9999/ 3
AGENT header Authorization: Bearer secret123
```

### `AGENT vboxguest <property> [interval]` -- VirtualBox's real channel, no Guest Additions

VirtualBox has its own native guest-integration mechanism for exactly
this (Guest Properties, a simple key-value store the host can set at
any time), normally reached through Guest Additions -- a real
out-of-tree kernel module plus a userspace daemon (`VBoxService`),
meaningfully heavier than this whole image otherwise is. This mode
reaches the *same* channel without any of that, because Oracle
upstreamed the guest-side driver into mainline Linux itself
(`drivers/virt/vboxguest/`, since ~4.14): enabling `CONFIG_VBOXGUEST` in
the kernel `cnimbus prepare` already builds from source gives `/dev/vboxguest`
for free. What mainline Linux does *not* include is a way to actually
call the Guest Properties HGCM service through that device --
`cmd/cnimbusagent` (its `vboxguest` kind) is a small from-scratch client for exactly that (built
against the documented `/dev/vboxguest` ioctl ABI and VirtualBox's own
published `GuestPropertySvc` wire protocol), embedded into `cnimbus`
prebuilt for both architectures, placed into the image automatically
when this mode is used.

Set the value from the host:

```bash
VBoxManage guestproperty set <vm-name> /cnimbus/message "some value"
```

Works with no Guest Additions installed: `VBoxManage guestproperty set`
on the host reaches the running guest's `helloserver` response within
one poll interval.

**Windows/Git Bash note**: a property name starting with `/` looks like
an absolute path to MSYS's automatic path conversion, which silently
rewrites it (`/cnimbus/message` becomes something like
`C:/Program Files/Git/cnimbus/message`). Prefix the command with
`MSYS_NO_PATHCONV=1` if you're running `VBoxManage guestproperty` from
Git Bash on Windows.

## Nimbusfile vs. flags

These aren't two ways of doing the same thing -- they answer different
questions and complement each other:

- **The Nimbusfile describes the image**: which kernel and BusyBox,
  which architecture, what goes inside, what runs at boot. Commit it --
  it's what makes a build reproducible, and it's read by *both*
  `prepare` and `build-disk` so one file drives the whole pipeline.
- **Flags describe this invocation**: which Nimbusfile to read (`-f`),
  where pieces come from (`--pieces`) or go (`--out`), where the output
  lands (`-o`). Machine-specific, nothing you'd commit.

Four settings can be expressed in both places (`KERNEL`, `BUSYBOX`,
`ARCH`, `VGA`), and there is exactly one rule for all four: **the
Nimbusfile declares it; a flag actually passed on the command line
overrides it.** That includes turning something back off --
`--vga=false` beats a Nimbusfile saying `VGA true` -- so a flag override
is never one-way.

| | Nimbusfile | flag | who reads it |
|---|---|---|---|
| kernel version | `KERNEL` | `--kernel` | `prepare` |
| busybox version | `BUSYBOX` | `--busybox` | `prepare` |
| architecture | `ARCH` | `--arch` | both |
| VGA console | `VGA` | `--vga[=false]` | `prepare` |
| hostname, DHCP, FORMAT, COPY/ADD, ENTRYPOINT/CMD, AGENT | yes | -- | `build-disk` |
| Nimbusfile path, pieces source/destination, output path | -- | yes | as applicable |

## Nimbusfile

Same idea as a Dockerfile: declare what you want, run one command.

```dockerfile
KERNEL latest-stable
BUSYBOX latest
ARCH amd64

HOSTNAME cnimbus
DHCP true

COPY ./helloserver /usr/bin/helloserver
ENTRYPOINT /usr/bin/helloserver
CMD :8080

FORMAT iso
```

`cnimbus init` writes a fuller version of this with commented-out
alternatives for every version-like directive (an exact `KERNEL`
version, `latest-longterm`, a pinned `BUSYBOX` version, `ARCH arm64`),
so you can see the format options without having to look them up here.

| Directive | Meaning |
|---|---|
| `KERNEL <version>` | `latest-stable`, `latest-longterm`, or an explicit version (e.g. `6.9.4`). Read by `cnimbus prepare` (which decides which kernel.org release to compile) -- `cnimbus build-disk` never looks at this, it just uses whatever pieces you point it at, however they were produced. |
| `BUSYBOX <version>` | explicit version (e.g. `1.36.1`), or `latest` for cnimbus's own built-in default. Also only read by `cnimbus prepare`. |
| `ARCH <amd64\|arm64>` | target architecture (default `amd64`). Read by *both* commands: `prepare` to pick what it compiles, `build-disk` to pick which arch-namespaced pieces to fetch. Overridable with `--arch` on either. |
| `VGA <true\|false>` | enable a real VGA/framebuffer console for `console=tty0` (default `false`). Only read by `cnimbus prepare`, overridable with `--vga[=false]`. Off by default because the serial console (`ttyS0`/`ttyAMA0`) is always on regardless -- turn this on only to see boot output in a GUI hypervisor's own display window (VirtualBox chief among them). |
| `HOSTNAME <name>` | image hostname |
| `DHCP <true\|false>` | bring up `eth0` via DHCP at boot |
| `IP <addr> <netmask> <gw>` | static IP instead of DHCP; wins over DHCP if both are set. `eth1`/`eth2`/`eth3` are also brought up automatically via DHCP in the background if present, regardless of this setting. |
| `DNS <addr>` | an explicit nameserver, written to `/etc/resolv.conf` at boot, overriding whatever DHCP itself supplied. Repeatable for multiple servers. |
| `NTP <server\|false>` | sync the clock at boot against `<server>` (default `pool.ntp.org`). Repeatable for multiple servers -- one `ntpd` call queries all of them and picks the best answer. `false` disables it (and clears any servers an earlier `NTP` line already added). Needs `DHCP` or `IP` configured -- skipped with no networking at all. |
| `FORMAT <iso\|raw>` | the image *type* to produce. `iso`: dual El Torito boot catalog entries on amd64 (BIOS *and* UEFI, whichever the hypervisor/virtual optical drive presents it to), UEFI-only on arm64 (same as before) -- **not an isohybrid image**, see the note below if you plan to `dd`/Rufus it onto a USB stick. `raw`: a GPT disk with two partitions -- a small, fixed-size UEFI ESP holding just the kernel+initramfs, and a second partition holding the SquashFS root directly as its own raw contents; no BIOS/legacy path on either architecture -- meant for Proxmox/cloud-style disk-image templates, and also the format to reach for bare-metal/USB boot instead of `iso`. Not a path: the output file path is set with `cnimbus build-disk -o <path>` (default `<hostname>.iso`, or `<hostname>.img` for `raw`). |
| `USER <name>` | drop every `ENTRYPOINT`/`CMD`/`SERVICE` to this unprivileged account (uid/gid 1000) instead of root, via BusyBox's `setuidgid`. Default: root, unchanged. There is no shell anywhere in the image regardless -- see "Known limitations". Ports below 1024 need root. |
| `WORKDIR <path>` | working directory for `ENTRYPOINT`/`CMD`/every `SERVICE`. Default `/`. |
| `LABEL <KEY>=<VALUE>` | free-form image metadata, written to `/etc/cnimbus-release`. Repeatable, no effect on boot behavior. |
| `EXPOSE <port>[/tcp\|/udp]` | documents a port the image listens on (default proto `tcp`); informational only -- `cnimbus` doesn't open a firewall rule or a hypervisor port-forward for you. Repeatable. |
| `ARG <NAME>[=<default>]` | declares a build-time variable usable as `${NAME}`/`$NAME` in any later directive's arguments, resolved once at parse time. Override with `--build-arg NAME=value` (repeatable) on `prepare`/`build-disk`; an `ARG` with no default and no override is a parse error. A literal `$` not starting a valid identifier (e.g. a shell price like `$5`) passes through unchanged; `$$` is the escape for a literal `$` when it *would* otherwise start one (Docker's own convention) -- needed so `ENTRYPOINT`/`CMD` can pass a literal `$VAR` through for BusyBox's `sh` to expand at *runtime* (from an `ENV` directive) instead of at Nimbusfile-parse time, e.g. `ENTRYPOINT ["/bin/sh", "-c", "echo $$HOME"]`. |
| `VOLUME <device> <mount> [fstype] [required]` | mount `<device>` (e.g. `/dev/vda`) at `<mount>` at boot for persistent storage; `fstype` is `vfat` (default) or `ext4`. **Never formats it** -- the device must already be a real, pre-formatted disk you attached yourself in your hypervisor. Without `required`: if it doesn't mount, boot just continues without it. With `required`: a failed mount halts boot with a FATAL message instead, before any service starts against what would otherwise be missing storage. Repeatable. Optional -- everything else in the image is RAM-only/read-only. |
| `AGENT <url> [interval]` | poll `<url>` (plain HTTP(S), any server) every `[interval]` seconds (default `5`) and write the response body to `/var/run/cnimbus-kv.json` -- lets a running VM pick up config changes without rebuilding the image or rebooting, on any hypervisor with guest networking. See "Live config" below. |
| `AGENT header <name>: <value>` | an extra HTTP header sent with the `AGENT <url>` request above (only valid right after an `http`-kind `AGENT` line) -- covers cloud metadata endpoints that require one, e.g. GCE's `Metadata-Flavor: Google` or OCI's `Authorization: Bearer ...`. Repeatable. |
| `AGENT vboxguest <property> [interval]` | same live-config mechanism, but reads VirtualBox's own Guest Properties directly (mainline `CONFIG_VBOXGUEST`, no Guest Additions installed). VirtualBox-only. See "Live config" below. |
| `AGENT virtio-serial <device> [interval]` | reads the live value from a QEMU/Proxmox `virtio-serial` character device (e.g. `/dev/vport0p1`) -- no `qemu-guest-agent` needed. |
| `AGENT aws-imds <path> [interval]` / `AGENT ibm-imds <path> [interval]` | fetches `<path>` from the AWS EC2 IMDSv2 or IBM Cloud VPC metadata service, via the embedded `cnimbusagent` binary (implements the PUT-token-then-GET handshake both services require -- BusyBox's own `wget` can't do that). |
| `AGENT vmware <key> [interval]` | reads a `guestinfo.<key>` variable set on the VMware host (a `.vmx` `guestinfo.*` line, or `vmrun writeVariable <vmx> guestVar <key> <value>`), via VMware's own low-bandwidth backdoor I/O protocol -- no VMware Tools/open-vm-tools installed. `linux/amd64` only (needs ring-3 I/O privilege, granted via `iopl(3)`); `arm64` prints an explicit "not implemented" message instead of silently misbehaving. |
| `ENV <KEY>=<VALUE>` | environment variable exported into every `ENTRYPOINT`/`CMD`/`SERVICE`. Repeatable; a later `ENV` with the same key overrides an earlier one. |
| `FIREWALL <rule>` | one `iptables` (IPv4) rule line, run at boot via a static `iptables` `cnimbus prepare` now builds and embeds automatically (a `COPY`'d `iptables` on `PATH` takes priority if you supply your own). Repeatable. |
| `FIREWALL6 <rule>` | the IPv6 counterpart of `FIREWALL`: same rule syntax, run via the same bundled binary invoked as `ip6tables` instead. Independent ruleset -- declaring one has no effect on the other. Repeatable. |
| `FIREWALL_ON_ERROR <open\|closed>` | what to fall back to if a `FIREWALL`/`FIREWALL6` rule fails to apply at boot (e.g. a kernel missing a requested match). `open` (default): flush to accept-all, so boot never hangs behind a broken ruleset. `closed`: drop everything except loopback and already-established connections instead -- for a Nimbusfile whose rules chose a DROP-default policy specifically, where falling back to accept-all would invert that intent. Applies to both rulesets; no effect without at least one `FIREWALL`/`FIREWALL6` line. |
| `HEALTHCHECK [--interval=<n>] [--retries=<n>] <cmd...>` | applies to the `ENTRYPOINT` process only (mirroring Docker's one-`HEALTHCHECK`-per-container model): runs `<cmd...>` every `--interval` seconds (default `30`) while the process is alive, and kills/restarts it after `--retries` (default `3`) consecutive failures. |
| `RESTART <target> <always\|on-failure\|no>` | restart policy for `entrypoint` or a named `SERVICE`. `always` (default): respawn unconditionally, capped-linear backoff. `on-failure`: respawn only on nonzero exit. `no`: run once and stop supervising. |
| `STOPGRACE <seconds>` | how long a shutdown (ACPI power button, `poweroff`, Ctrl-Alt-Del) waits for the `ENTRYPOINT`/`CMD` process to exit on its own after `SIGTERM` before escalating to `SIGKILL`, before the guest halts. Default `10`. Without this, BusyBox init's own shutdown sequence gives every process only ~1 second -- too short for in-flight work (buffered writes, an open transaction, in-flight HTTP requests) to finish cleanly. Precisely targeted (a real, tracked PID, not the whole guest) only for the process a `HEALTHCHECK` directive already tracks -- in practice, `ENTRYPOINT`/`CMD` -- since that's the one path that already knows the workload's real PID rather than a logging pipe's; other `SERVICE`s still get the same overall grace window before the guest halts, just without an individually targeted signal. |
| `TMPSIZE <size>` | overrides the `size=` of the four in-RAM exec-directory tmpfs mounts (`bin`, `sbin`, `usr/bin`, `usr/sbin`) stage 1 recreates at every boot; default `32m`. `<size>` is a positive integer optionally suffixed with `k`/`m`/`g` (same syntax the kernel's own tmpfs `size=` mount option accepts), e.g. `TMPSIZE 128m`. A `COPY`/`ADD` destined for one of those four directories whose total for that directory exceeds this size fails `build-disk` immediately with a clear message, rather than only failing at boot with `ENOSPC`. |
| `PIECESKEY <hex-pubkey>` | pins the Ed25519 public key (see `cnimbus keygen`) that `pieces.sha256` must be signed by. `build-disk` then refuses to build unless the pieces source published a matching `pieces.sha256.sig` -- see "Signing pieces" below. A `--pieces-verify-key` flag passed on the command line overrides this, same rule as every other Nimbusfile-vs-flag setting. |

`FORMAT iso` has a second, independent ceiling on top of `TMPSIZE`: the combined kernel + stage-1-initramfs payload is also loaded via El Torito's "no emulation" boot entry, whose own size field is 16 bits of 512-byte units (~32 MiB total, shared across both files, not per-file). A `COPY`/`ADD` into `bin`/`sbin`/`usr/bin`/`usr/sbin` large enough to push the *compressed* stage-1 initramfs past roughly a 24 MiB practical budget fails `build-disk` with a message naming the largest offending file(s); raising `TMPSIZE` does not help with this ceiling (it's a different limit, on the ISO boot mechanism itself, not on RAM). `FORMAT raw` has no such limit at all -- switch to it, or move the large file outside those four directories, for anything bigger.
| `COPY [--chmod=<mode>] <src> <dest>` | local file, directory, or glob -> image path. A directory's *contents* are copied (not the directory itself, matching Docker's `COPY` semantics); `--chmod` sets the octal permission explicitly. **Must match ARCH, and must be a static Linux binary** -- the image carries no dynamic linker or libc of its own, and there's no shell to invoke an interpreter with, so a copied executable needs to be statically linked for `linux/<amd64\|arm64>`. This isn't Go-specific: any language that can produce a static Linux binary for the target architecture works the same way -- Go (`GOOS=linux GOARCH=<amd64\|arm64> CGO_ENABLED=0`), Rust (`--target <arch>-unknown-linux-musl`), Zig, C/C++ (`-static`, typically against musl), Crystal, FreePascal, Dart (AOT-compiled), .NET (published as Native AOT), and others. `cnimbus validate` checks the resulting ELF's architecture for you regardless of source language. |
| `ADD [--chmod=<mode>] <src> <dest>` | like `COPY`, but `src` may be a URL, and a local `.tar`/`.tar.gz`/`.tgz` is auto-extracted into `dest` (matches Docker's actual `ADD` semantics) |
| `ENTRYPOINT <cmd...>` | the main service respawned at boot, under crash-loop backoff (capped-linear, up to 30s between restarts) unless overridden by `RESTART entrypoint ...`. Shell form (`ENTRYPOINT /usr/bin/foo`) or exec form (`ENTRYPOINT ["/usr/bin/foo", "arg"]`) |
| `CMD <args...>` | default args appended after `ENTRYPOINT`, or (with no `ENTRYPOINT`) the whole respawned command |
| `SERVICE <name> <cmd...>` | an additional respawned, supervised process alongside `ENTRYPOINT`/`CMD` -- same backoff (overridable via `RESTART <name> ...`), same `ENV`/`USER`. Repeatable. |

## Signing pieces

`pieces.sha256` (written by `prepare` alongside `vmlinuz`/`busybox`/
`busybox-manifest.tsv`) is a hash manifest: `build-disk` checks every
fetched file against it, so a corrupted download or an in-flight
substitution is caught. That's *integrity* -- it only proves the bytes
match what the manifest claims, never *who* published the manifest in
the first place. An attacker with write access to wherever pieces are
published (an S3 bucket, an internal mirror) can replace `vmlinuz` and
`pieces.sha256` together, and the hash check alone would report success.

Signing closes that gap:

```bash
cnimbus keygen --out my-signing-key.hex
# wrote private key seed to my-signing-key.hex (keep this secret -- never commit it)
# public key (pin this with --pieces-verify-key or a Nimbusfile PIECESKEY line):
# <64 hex characters>

cnimbus prepare --pieces-sign-key my-signing-key.hex
# ...writes pieces.sha256.sig alongside the usual output

cnimbus build-disk --pieces-verify-key <the-public-key-printed-above>
# (or put "PIECESKEY <public-key>" in the Nimbusfile instead of the flag)
```

With `--pieces-verify-key`/`PIECESKEY` set, `build-disk` refuses to build
unless the pieces source published a `pieces.sha256.sig` that verifies
against that exact key -- a source with no signature at all, or one
signed by a different key, is rejected the same way a hash mismatch
already is (exit code 4, see "Exit codes" in `cnimbus help`). With
neither set, nothing changes: an unsigned `pieces.sha256` still builds
exactly as it always has.

This covers the first, most tractable step of the chain of trust after
kernel.org's own PGP signature (verified at `prepare` time): authenticating
`pieces.sha256` itself. Signing the EFI-stub kernel object the firmware
actually executes, and eventually UKI/measured boot, are larger,
follow-on milestones -- see [ROADMAP.md](ROADMAP.md).

## Architecture

```
cnimbus prepare (needs Docker)                            cnimbus build-disk (no Docker, ever)
────────────────────────────                            ────────────────────────────────
kernel.org releases.json ──┐
  (resolve KERNEL version) │
                            ▼
        ┌───────────────────────────────────┐
        │ 1. golang container                │
        │    --platform linux/<ARCH>         │        Nimbusfile
        │    compiles Thunder from its       │            │
        │    embedded source                 │            ▼
        └────────────┬────────────────────────┘     ┌──────────────┐
                     │ Thunder binary (linux/<ARCH>) │  build-disk    │
                     ▼                                │  (pure Go)    │
        ┌───────────────────────────────────┐         └──────┬───────┘
        │ 2. gcc:14-trixie container          │                │
        │    --platform linux/<ARCH>         │                │
        │    (native gcc -- the container     │                │
        │    *is* ARCH, no cross-compiler)    │                │
        │    Thunder runs make/gcc            │                │
        └────────────┬────────────────────────┘                │
                     │                                          │
               vmlinuz, busybox,                                │
               busybox-manifest.tsv                              │
                     │                                           │
                     └──────────────► published ─────────────────┘
                                      pieces
                                         │
                                         ▼
                          stage 1: tiny cpio/gzip initramfs
                          (BusyBox + applet symlinks + /init)
                                         │
                                         ▼
                          stage 2: SquashFS root (pure Go,
                          github.com/diskfs/go-diskfs) -- /etc,
                          supervisor scripts, your COPY/ADD
                                         │
                                         ▼
                    amd64: ISO9660+El Torito, BIOS+UEFI    arm64: ISO9660, UEFI only
                    (isolinux + EFI stub, pure Go)          (no BIOS-equivalent on arm64)
                          FORMAT raw: GPT + ESP (kernel/initramfs) + a second
                          partition holding the SquashFS root directly, both
                          archs (no BIOS path)
                                         │
                                         ▼
                              bootable .iso / .img
```

### Two-stage boot: why, and the one real gap it has

Booting straight into a read-only root needs a real block device to
mount a filesystem from -- an all-RAM cpio initramfs (what this project
used before) can't be read-only in any meaningful sense, since the
whole thing already lives in freely-writable memory. So `build-disk` now
produces two images instead of one:

1. **Stage 1** -- the actual kernel-loaded initramfs, small and
   RAM-resident like before: BusyBox, its ~400 applet symlinks, and an
   `/init` that finds the SquashFS root and `switch_root`'s into it. For
   `FORMAT iso`, that means finding the CD-ROM device, `losetup`ing
   `SQUASHFS.IMG` (a file on the ISO9660 tree) onto a loop device, and
   mounting it read-only. For `FORMAT raw`, there's no loop device
   at all: the SquashFS root is its own GPT partition, so `/init` tries a
   short list of likely second-partition names (e.g. `/dev/vda2`) and
   mounts each directly as squashfs.
2. **Stage 2** -- a genuine SquashFS root built with
   `github.com/diskfs/go-diskfs` (no `mksquashfs`, no container): `/etc`,
   the generated inittab/rcS/supervisor scripts, and every `COPY`/`ADD`
   destination *except* one case below.

**The one gap**: BusyBox's whole design depends on ~400 symlinks (every
applet name pointing at the same binary), and go-diskfs's SquashFS
*writer* has `Symlink`/`Link` stubbed out (`filesystem.ErrNotImplemented`
in the version this project vendors) -- there's no way to represent
those symlinks inside a SquashFS image with this library. The
workaround: `bin/`, `sbin/`, `usr/bin/`, and `usr/sbin/` are `tmpfs`,
not SquashFS -- stage 1 mounts tmpfs over each of them and recreates
every symlink there fresh on every boot, from the exact same manifest
it already carries. This means those four directories are the one part
of a cnimbus image that *isn't* actually immutable; everything else
genuinely is. It also means any `COPY`/`ADD` destined for one of those
four directories (like the demo's own `/usr/bin/helloserver`) has to
travel through stage 1 too, copied into place after the tmpfs mounts --
handled automatically, not something a Nimbusfile needs to account for.

Why this shape, concretely:

- **Kbuild is unavoidable.** There is no reasonable way to reimplement
  Kconfig's dependency resolution or the kernel's build system in
  Go/Rust/Zig. `prepare` shells out to `make`/`gcc` inside a Linux
  container for that reason alone -- and to nothing else. There are no
  `.sh` scripts anywhere in this repo; Thunder (the thing that
  actually runs inside that container) is itself a Go program.
- **Kconfig's failure mode for a requested symbol is silence, so
  `internal/compileagent/kernel.go`'s `verifyFragmentsApplied` checks
  for it explicitly.** If a fragment asks for `CONFIG_X=y` but some
  parent gate it depends on isn't satisfied, Kconfig just drops the
  line -- `merge_config.sh` logs a "Previous value" note that scrolls
  past in a multi-thousand-line build, `olddefconfig` removes it, `make`
  succeeds, the image builds and boots, and only some specific runtime
  feature is quietly missing. Now `prepare` compares the final `.config`
  against everything the fragments asked for and fails the build by name
  if Kconfig dropped anything, instead of shipping a silently-incomplete
  kernel.
- **Thunder is compiled on demand, not pre-built and embedded.**
  `cnimbus` embeds Thunder's *source* (plus a vendored copy of its one
  dependency), and `prepare` compiles it fresh, inside a throwaway
  `golang` container, for whichever architecture the Nimbusfile declares.
  That's what makes "ARCH arm64" produce an entirely arm64 pipeline --
  compiler container, Thunder, and the kernel/BusyBox it builds -- with
  no local Go toolchain needed and no pre-built arm64 binary bloating
  every copy of `cnimbus`.
- **Every container runs natively as the target architecture**, via
  `docker --platform linux/<arch>` -- not cross-compiled from an amd64
  host. Docker Desktop's Rosetta/QEMU emulation makes an arm64
  container work transparently on an amd64 host (and vice versa), so
  plain, unprefixed `gcc` is correct for the kernel and BusyBox on
  *both* architectures -- no `gcc-aarch64-linux-gnu` cross-compiler
  package needed.
- **The builder image is as minimal as it can be while still running
  Kbuild**: no curl (Thunder fetches sources itself, in Go), no
  busybox (that's the target *artifact*, never a dependency of the
  container that builds it). It cannot be fully shell-less
  ("distroless"): Kbuild's own build system hard-requires a POSIX
  shell to execute Makefile recipes -- that's the kernel's own
  architecture, not a choice made here.
- **The assembled VM image itself is distroless-style, genuinely**:
  there is no respawned shell anywhere in the inittab, no login, no
  getty -- the *only* entry points into a running image are whatever
  `ENTRYPOINT`/`CMD`/`SERVICE` a Nimbusfile declares. No Nimbusfile-declared
  service means the image just boots and sits there, on purpose.
- **The ISO itself is pure Go.** No `grub-mkrescue`, no `xorriso`. It
  uses `github.com/diskfs/go-diskfs` for ISO9660 + El Torito and
  vendors syslinux's own prebuilt, redistributable
  `isolinux.bin`/`ldlinux.c32` (the same files every mainstream Linux
  ISO ships) as the amd64 BIOS boot stage. UEFI needs no separate
  bootloader at all on either architecture: the kernel is built with
  `CONFIG_EFI_STUB=y`, so the same kernel image is *also* a valid
  PE32+ executable UEFI firmware can load directly (`BOOTX64.EFI` on
  amd64, `BOOTAA64.EFI` on arm64). arm64 images have no BIOS/isolinux
  entry at all -- there's no arm64 equivalent of legacy BIOS boot.
- **BusyBox's install tree ships as (binary, symlink manifest), not a
  directory of real symlinks.** A Docker Desktop bind mount silently
  turns every one of BusyBox's ~400 applet symlinks into a full copy of
  the binary when the host is Windows -- and Go's own
  `os.Symlink`/`os.Readlink` don't reliably round-trip POSIX symlinks
  on Windows either. The manifest (`path<TAB>target`, one per line) is
  the only representation that survives every host OS losslessly.

## Supported platforms

An image built by cnimbus reaches a working network and runs your
userland binary on:

- **QEMU** -- amd64 BIOS+UEFI, arm64 UEFI.
- **VirtualBox** -- amd64, full two-stage SquashFS boot, `VGA`, `USER`,
  both `AGENT` modes (`http`, `vboxguest`).
- **VMware Player/Workstation** -- amd64, including `AGENT vmware` via
  VMware's own backdoor I/O protocol.
- **Hyper-V** -- amd64, Generation 1 and 2, `--hostfwd` via a
  cnimbus-owned Internal switch (Hyper-V's own Default Switch NAT can't
  accept inbound host connections).
- **Firecracker** -- via WSL2's `/dev/kvm`.
- **Physical hardware and Proxmox** -- IPv4 and IPv6, boot messages on
  both VGA and serial regardless of which one the kernel treats as
  primary, `FIREWALL`/`FIREWALL6` enforcement, and both of Proxmox's
  control-panel actions (ACPI "Signal Shutdown" and Ctrl+Alt+Del)
  shutting down/rebooting the VM correctly -- the same holds for
  Hyper-V Generation 1.
- **Ventoy and other grub-loopback multiboot USB tools** -- boots a
  cnimbus ISO chainloaded from a `.iso` file on a FAT/exFAT stick
  (rather than a `dd`'d device); `CNIMBUS.CFG`, a plain-text manifest at
  the ISO's top level, lets the console report which image actually
  booted regardless of what it was named on the stick.
- **`cnimbus run`'s four backends**, `cnimbus clean`, `FORMAT raw`
  (+`--uefi`), `FIREWALL`/`FIREWALL6`, `IP` (static addressing),
  `SERVICE`, `ENV` (including the `$$VAR` runtime-expansion escape),
  `VOLUME` (with and without a device attached) -- see
  [examples/](examples/) for a runnable Nimbusfile per feature.

Still open: multiple NICs, `ext4` `VOLUME`, `DNS`/`resolv.conf`,
`HEALTHCHECK`, `AGENT virtio-serial`, `AGENT aws-imds`/`ibm-imds`,
`cnimbus run --backend hyperv` with `FORMAT raw`, and a real WiFi
radio associating (`HARDBOOT wifi` is implemented, pending hardware to
test). See [ROADMAP.md](ROADMAP.md) for the full backlog.

## Known limitations

- **IPv6 requires its own `FIREWALL6` rules.** IPv6 is on by default,
  and `FIREWALL`/`FIREWALL6` are two independent rulesets -- a
  `FIREWALL`-only Nimbusfile leaves IPv6 completely unfiltered. Declare
  `FIREWALL6` explicitly for any Nimbusfile that shouldn't be reachable
  over IPv6.
- **amd64 and arm64 only as a Nimbusfile's `ARCH`.** No riscv64 *guest*
  support yet (the CLI itself runs fine on riscv64 -- see
  [BUILD.md](BUILD.md)); no hardware available here to validate a
  riscv64 guest boot.
- **`bin/`, `sbin/`, `usr/bin/`, `usr/sbin/` aren't part of the
  immutable root.** They're tmpfs, recreated fresh every boot -- see
  "Two-stage boot" above for why. Everything else genuinely is
  read-only.
- **No shell anywhere, ever, by design.** No respawned shell, no login,
  no getty in any cnimbus image. The only way in is whatever
  `ENTRYPOINT`/`CMD`/`SERVICE` you declare.
- **No real piece hosting yet.** `prepare`'s output has to be published
  somewhere yourself; nothing is hosted by this project.
- **Kernel signature verification is best-effort.** An explicit kernel
  version not in kernel.org's live release index falls back to a
  guessed `cdn.kernel.org` URL with no PGP check;
  `--insecure-skip-kernel-verify` opts out entirely for offline mirrors.
- **No UEFI Secure Boot support for a plain (unsigned) image.** Use
  `--secureboot`/`--uki` (see "Image formats and boot chain" in
  [CHANGELOG.md](CHANGELOG.md)) if the target firmware requires it.
- **`AGENT vmware` is `linux/amd64`-only** -- `arm64` prints an explicit
  "not implemented" message rather than silently misbehaving (VMware on
  Windows doesn't run arm64 guests anyway).
- **`cnimbus run --backend hyperv` + `FORMAT raw`** is code-complete and
  unit-tested but not yet boot-validated against a real Hyper-V host.

  | `--backend` | `FORMAT iso`, BIOS | `FORMAT iso`, `--uefi` | `FORMAT raw` (UEFI-only by design) |
  | --- | --- | --- | --- |
  | `qemu` | yes | yes | yes |
  | `vbox` | yes | yes | yes (forces UEFI regardless of `--uefi`) |
  | `vmware` | yes | yes | yes (forces UEFI regardless of `--uefi`) |
  | `hyperv` | yes (Gen 1) | yes (Gen 2) | yes, not yet boot-validated (above) |

  arm64 images are UEFI-only on every backend (no arm64 BIOS-equivalent
  boot path exists), so the BIOS column only ever applies to amd64.
- **`FORMAT iso` is not an isohybrid image.** There's a real El Torito
  BIOS+UEFI boot catalog, but no isohybrid MBR at byte 0 -- `dd`ing this
  ISO onto a physical USB stick will not boot a legacy-BIOS machine from
  it. Use `FORMAT raw` for USB/bare-metal boot instead.
- **No automated boot-test harness in CI.** Boot validation is done by
  hand against real hypervisor/hardware installs -- see
  [ROADMAP.md](ROADMAP.md).

See [ROADMAP.md](ROADMAP.md) and
[`.specs/project/STATE.md`](.specs/project/STATE.md) for the reasoning
and evidence behind any of the above.

## Repo layout

```
cmd/cnimbus/           the CLI: init, prepare, build-disk, kv-serve, version
cmd/thunder/           compiled on demand by `prepare`, runs inside the build container (not user-facing)
cmd/helloserver/       demo Go HTTP server used to validate COPY/ENTRYPOINT and AGENT
cmd/cnimbusagent/      every AGENT kind's guest-side client (http, virtio-serial, vboxguest,
                       aws-imds, ibm-imds, vmware); prebuilt, embedded, placed via COPY-like
                       mechanism -- see internal/assets

internal/nimbusfile/   Nimbusfile parser
internal/pieces/       fetches prebuilt pieces (local dir or URL), arch-namespaced
internal/rootfs/       busybox-init rootfs, two-stage SquashFS boot assembly (pure Go)
internal/isoimage/     ISO9660 + El Torito (BIOS+UEFI, amd64/arm64) assembly (pure Go)
internal/rawimage/     FORMAT raw: GPT + ESP + SquashFS-root-partition assembly (pure Go)
internal/compileagent/ kernel + busybox build logic (Thunder's own code; runs inside the container)
internal/kernelinfo/   resolves KERNEL version against kernel.org
internal/dockerrun/    docker CLI wrapper (prepare only)
internal/assets/       embedded assets: isolinux, Dockerfile, kconfig fragments, Thunder's
                       source, cnimbusagent's prebuilt amd64/arm64 binaries

docs/manual/           the full LaTeX user manual (cnimbus-manual.tex) and its compiled PDF
examples/              self-contained, buildable Nimbusfile examples, one per directive/feature
```

## Building from source

Docker is the only prerequisite -- no local Go install needed:

```bash
docker run --rm -v "$(pwd)":/src -w /src \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
  golang:1.26.5 go build -o cnimbus ./cmd/cnimbus
```

No prerequisite build step beyond that (Thunder is embedded as
*source*, not a prebuilt binary; `cnimbus prepare` compiles it itself,
on demand, inside a container). `cnimbus` itself (the CLI, as opposed
to the guest images it builds) runs natively on 7 targets: Windows
(amd64, arm64), Linux (amd64, arm64, **riscv64**), and macOS (amd64,
arm64) -- see [BUILD.md](BUILD.md) for cross-compiling all 7 and for
running `prepare` itself inside Docker too. Note this riscv64 support
is for the CLI's own *host* platform only; a Nimbusfile's `ARCH` (the
architecture of the *guest* image being built) is still amd64 or arm64
only -- see "Known limitations" below.
