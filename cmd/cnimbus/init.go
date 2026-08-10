package main

import (
	"fmt"
	"os"
)

const exampleNimbusfile = `# Nimbusfile -- declarative manifest for a cnimbus image.
# "cnimbus prepare" reads KERNEL/BUSYBOX/ARCH/VGA from this file (if present
# in the current directory) and compiles matching pieces -- needs Docker.
# "cnimbus build-disk" reads the rest and assembles a bootable ISO from
# whatever pieces you point it at (--pieces) -- no Docker, no compiler.

# KERNEL accepts a moving target ("latest-stable", "latest-longterm") or
# an exact version. Only "cnimbus prepare" acts on this -- it decides which
# kernel.org release gets compiled; "cnimbus build-disk" just uses whatever
# pieces already exist, it never checks this value against them.
KERNEL latest-stable
# KERNEL latest-longterm   # the current long-term-support branch instead
# KERNEL 6.9.4             # or pin an exact released version

# BUSYBOX likewise: "latest" defers to cnimbus's own known-good default
# version; pin an exact version if you need reproducibility.
BUSYBOX latest
# BUSYBOX 1.36.1

# ARCH is also read by "cnimbus prepare", so the same Nimbusfile drives both
# commands: "amd64" or "arm64", defaults to "amd64" if omitted entirely.
ARCH amd64
# ARCH arm64

# VGA gives console=tty0 an actual video driver, so boot output shows up
# in a GUI hypervisor's own display window (VirtualBox chief among them).
# Off by default -- most VMs only need the serial console (already always
# on, see ttyS0/ttyAMA0 in the boot cmdline). Only read by "cnimbus prepare",
# and overridable either way from the command line: --vga turns it on,
# --vga=false turns it off even if this file says true.
# VGA true

HOSTNAME cnimbus
DHCP true

# IP configures a static address instead of DHCP -- wins over DHCP if
# both are set, so you don't have to delete the line above to switch.
# IP 192.168.1.50 255.255.255.0 192.168.1.1

# DNS sets explicit nameservers, winning over whatever DHCP itself
# provides. Repeatable. The only source of DNS at all when using IP
# (static addressing has no DHCP lease to carry it in).
# DNS 8.8.8.8
# DNS 1.1.1.1

# NTP syncs the clock at boot (needs DHCP or IP configured). Repeatable
# for multiple servers -- ntpd queries all of them and picks the best
# answer itself. "false" disables it entirely (and clears any servers
# already added by an earlier NTP line), e.g. for a network with no
# outbound access.
NTP pool.ntp.org
# NTP 1.pool.ntp.org   # add more servers with additional NTP lines
# NTP 2.pool.ntp.org
# NTP false

# FORMAT is the image *type* to produce, not a path -- the output path
# is set with "cnimbus build-disk -o <path>" (defaults to "<hostname>.iso",
# or "<hostname>.img" for raw). "raw" is a GPT disk with a single UEFI
# ESP -- no BIOS/legacy boot path, unlike "iso".
FORMAT iso
# FORMAT raw

# USER drops every ENTRYPOINT/CMD/SERVICE to this unprivileged account
# instead of root (uid/gid 1000). There is no shell anywhere in a cnimbus
# image regardless of this setting -- it only changes what your own
# services run as, not whether an interactive login exists (it never
# does). Ports below 1024 need root, i.e. no USER set.
# USER app

# VOLUME mounts <device> at <mount> at boot for persistent storage --
# everything else in the image is read-only/RAM-only otherwise. Never
# formats it -- <device> must already be a real, pre-formatted disk
# (fstype defaults to "vfat"; "ext4" gets real POSIX permissions/
# ownership) you attached yourself in VirtualBox/VMware/Proxmox/QEMU; if
# it doesn't mount, boot just continues without it. Repeatable.
# VOLUME /dev/vda /data
# VOLUME /dev/vdb /backup ext4

# ENV sets an environment variable for every ENTRYPOINT/CMD/SERVICE.
# Repeatable.
# ENV PORT=8080
# ENV LOG_LEVEL=debug

# FIREWALL is one iptables rule line, applied at boot via a static
# iptables-legacy binary "cnimbus prepare" builds and bundles
# automatically -- no COPY needed (a COPY'd "iptables" still wins if
# you'd rather bring your own). Repeatable.
# FIREWALL -P INPUT DROP
# FIREWALL -A INPUT -p tcp --dport 8080 -j ACCEPT

# Bring your own static binary into the image and run it at boot. It
# must be built for the same ARCH as above (e.g. GOOS=linux GOARCH=amd64
# for a Go binary). src may be a single file, a directory (its contents
# land under dest, not the directory itself), or a glob; --chmod sets
# an explicit mode instead of the default (0755).
# COPY ./helloserver /usr/bin/helloserver
# COPY --chmod=0644 ./app.conf /etc/app.conf
# COPY ./dist/ /var/www/
# ENTRYPOINT /usr/bin/helloserver
# CMD :8080

# SERVICE adds another respawned, supervised process alongside
# ENTRYPOINT/CMD -- same crash-loop backoff, same ENV/USER. Repeatable.
# SERVICE sidecar /usr/bin/sidecar --flag

# RESTART sets a restart policy for "entrypoint" or a SERVICE already
# declared above: "always" (default), "on-failure" (only respawn on a
# non-zero exit), or "no" (run once, never respawn).
# RESTART entrypoint on-failure
# RESTART sidecar no

# WORKDIR is the directory ENTRYPOINT/CMD/every SERVICE runs in (default "/").
# WORKDIR /app

# HEALTHCHECK runs a command against the entrypoint periodically;
# exceeding --retries consecutive failures kills and respawns it, same
# as a crash. Defaults: --interval=30 --retries=3.
# HEALTHCHECK --interval=10 --retries=3 /usr/bin/curl -f http://localhost:8080/

# LABEL and EXPOSE are informational metadata, written to
# /etc/cnimbus-release (LABEL) or just documented there (EXPOSE) --
# cnimbus itself never opens or forwards a port based on EXPOSE.
# LABEL version=1.0
# EXPOSE 8080
# EXPOSE 53/udp

# ARG declares a build-time variable, substituted via ${NAME} or $NAME
# in every directive after it; --build-arg NAME=VALUE on "cnimbus
# build-disk" overrides the default (or is required if there is none).
# ARG APP_VERSION=1.0
# ENV VERSION=${APP_VERSION}

# AGENT polls for live config a running VM can pick up without a
# rebuild or reboot, writing it to /var/run/cnimbus-kv.json for your
# ENTRYPOINT/SERVICE to read verbatim -- every kind below writes the
# fetched value through unmodified, no envelope added. Kinds:
# AGENT http://10.0.2.2:9999/ 3               # plain HTTP; run "cnimbus kv-serve" on the host
# AGENT header Metadata-Flavor: Google        # extra header for the http kind above (repeatable)
# AGENT vboxguest /cnimbus/message 3          # VirtualBox's real channel, no Guest Additions
# AGENT aws-imds instance-id 5                # AWS EC2 IMDSv2 (two-step token dance)
# AGENT ibm-imds instance 5                   # IBM Cloud VPC's equivalent
# AGENT virtio-serial /dev/vport0p1 5         # QEMU/Proxmox virtio-console channel
# AGENT vmware message 5                      # reads guestinfo.message via VMware's backdoor protocol
`

func runInit(args []string) error {
	path := "Nimbusfile"
	if len(args) > 0 {
		path = args[0]
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.WriteFile(path, []byte(exampleNimbusfile), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	return nil
}
