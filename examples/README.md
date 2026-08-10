# Examples

Each subdirectory is a self-contained Nimbusfile demonstrating one
directive (or a small group of related ones) end to end, with its own
README covering what it does and how to build/boot it. All of them
assume you've already built `cnimbus` itself (see [BUILD.md](../BUILD.md))
and run `cnimbus prepare` at least once for the target `ARCH` (see the
main [README.md](../README.md#quick-start)) so `./pieces` exists.

| Example | Directive(s) | What it shows |
|---|---|---|
| [env-config](env-config/) | `ENV` | Passing config into ENTRYPOINT as environment variables |
| [volume-persistent-disk](volume-persistent-disk/) | `VOLUME` | Mounting a pre-formatted, pre-attached disk for state that survives a reboot |
| [agent-http-kv](agent-http-kv/) | `AGENT <url>` | Live config from the host over plain HTTP, no rebuild/reboot -- works on any hypervisor |
| [agent-vboxguest-kv](agent-vboxguest-kv/) | `AGENT vboxguest` | Same live-config idea, but over VirtualBox's real Guest Properties channel -- no guest networking needed |
| [multi-service](multi-service/) | `SERVICE` | Running more than one respawned process in the same image |
| [static-ip-firewall](static-ip-firewall/) | `IP`, `FIREWALL` | A static network config plus iptables rules applied at boot |
| [format-raw-disk](format-raw-disk/) | `FORMAT raw` | Producing a GPT+UEFI raw disk image instead of an ISO |
| [healthcheck-restart](healthcheck-restart/) | `HEALTHCHECK`, `RESTART` | Detecting and recovering from a wedged (not crashed) process |
| [stopgrace-graceful-shutdown](stopgrace-graceful-shutdown/) | `STOPGRACE` | Giving in-flight work time to finish before a shutdown kills the process |
| [hardboot-eth](hardboot-eth/) | `HARDBOOT eth` | Real wired-Ethernet chipset support + a USB/bare-metal-bootable raw disk |
| [secureboot-uki](secureboot-uki/) | `--secureboot`/`--uki` (build-disk flags) | Signing the kernel and booting it with Secure Boot enabled |

Every example builds the same way once you `cd` into its directory:

```bash
cnimbus build-disk -f Nimbusfile
```

(or `-f Nimbusfile --pieces ../../pieces` if `./pieces` isn't next to the
example -- see each example's own README for anything it needs beyond that).
