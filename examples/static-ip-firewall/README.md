# IP + FIREWALL example

Shows a static network config (`IP <address> <netmask> <gateway>`,
which wins over `DHCP` whenever both are set) plus `FIREWALL` rules
applied at boot.

**Note:** `cnimbus prepare` now compiles and embeds a real, static
`iptables` (netfilter-legacy) automatically, so `FIREWALL` rules take
effect out of the box -- no `COPY` needed. If your Nimbusfile does
`COPY` its own `iptables` binary onto `PATH` anyway, that copy takes
priority over the embedded one, which is what this example still does
below (useful if you need a different build than the one `prepare`
produces).

## 1. Get a static iptables binary (optional -- prepare already embeds one)

Any statically-linked `iptables-legacy` binary for the target `ARCH`
works. One way to get one without installing anything system-wide:

```bash
docker run --rm -v "$PWD/examples/static-ip-firewall:/out" alpine sh -c \
  "apk add --no-cache iptables && cp /sbin/iptables-legacy /out/iptables"
```

(Alpine's `iptables` package is musl-static by default, which matches
the CGO_ENABLED=0 story the rest of this project follows.)

## 2. Adjust the addresses

`192.168.56.10`/`.1` above are VirtualBox's default "host-only"
network range -- change them to match whatever host-only or bridged
network you actually attach the VM to.

## 3. Build and boot

```bash
cd examples/static-ip-firewall
cnimbus build-disk -f Nimbusfile
```

Boot on a host-only (or bridged) adapter matching the `IP` line's
subnet. Confirm the static address took (no DHCP round-trip needed) and
that port 8080 answers while anything else (e.g. attempting to reach a
port with no matching FIREWALL rule) is dropped.

## Notes

- Rule order matters, same as raw `iptables` -- the default-DROP
  policy line must come first, ACCEPT rules after, exactly as written
  here.
- Loopback traffic and established/related connections (the replies to
  the guest's own outbound DNS/NTP/AGENT-poller requests) are always
  auto-accepted before any `FIREWALL` rule from the Nimbusfile runs --
  no need to declare either yourself.
- If you skip the `COPY ./iptables ...` line entirely, `FIREWALL` rules
  still apply -- they fall back to the static `iptables` `prepare`
  embeds automatically.
