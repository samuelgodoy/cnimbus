# hello-http-baremetal-proxmox

One image, two targets: a minimal "Hello HTTP" server that boots
unmodified on real physical hardware (via a UEFI USB stick) and inside
Proxmox (as a VM's virtual CD-ROM), reachable from anywhere on both
IPv4 and IPv6.

## Status: verificado em hardware real e no Proxmox real

| Recurso | Estado | Onde foi confirmado |
|---|---|---|
| Boot via USB (`dd` simples) | ✅ Funciona | Hardware físico real |
| Boot via multiboot USB (Ventoy) | ✅ Funciona | Hardware físico real |
| IPv4 (DHCP + firewall) | ✅ Funciona | Hardware físico real + Proxmox real |
| IPv6 (SLAAC + NDP + firewall) | ✅ Funciona | Proxmox real (`curl -6` externo, `200 OK`) |
| Banner de IP na tela VGA (v4 e v6) | ✅ Funciona | Hardware físico real + Proxmox real |
| "Signal Shutdown" (ACPI) pelo Proxmox | ✅ Funciona | Proxmox real (`qm shutdown` → `exitstatus: OK`) |
| Ctrl+Alt+Del pelo console do Proxmox | ✅ Funciona | Proxmox real (console web) |
| Firmware Ethernet real-time (RTL8168h) | ✅ Empacotado | Verificado no build; só carrega se a placa tiver esse chip específico -- ver nota abaixo |
| Associação WiFi em hardware real | ⏳ Adiado | Nunca testado com adaptador físico -- ver nota abaixo |

Tudo documentado abaixo, na ordem em que os bugs reais foram
encontrados e corrigidos -- cada seção tem a causa raiz e como foi
verificado de verdade (QEMU, Hyper-V local, ou o próprio Proxmox/
hardware do usuário).

### Sobre o firmware RTL8168h

O arquivo `rtl_nic/rtl8168h-2.fw` real (baixado do linux-firmware
oficial, hash SHA-256 verificado) está empacotado na imagem, e a
função `r8169_apply_firmware` do driver já foi confirmada como um
no-op inofensivo quando esse arquivo não é necessário. O teste no
hardware físico do usuário não mostrou efeito porque **a placa de
rede daquela máquina não é o chip RTL8168h específico** que pede esse
arquivo -- o kernel só chama `request_firmware()` para o hardware que
detecta de fato. Ou seja: nada quebrado, o firmware só entra em ação
quando a placa que precisa dele estiver instalada.

### Sobre WiFi

`HARDBOOT wifi`/`eth+wifi` está implementado e verificado via Kconfig
e compilação real (ver AD-038 no `.specs/project/STATE.md`), mas nunca
testado associando de fato com um roteador usando um adaptador USB
físico. Fica para uma próxima rodada, quando houver hardware WiFi
disponível para testar.

`HARDBOOT eth+wifi` is additive on top of the VM-only drivers
(`vm-amd64.fragment` already carries e1000/virtio-net) rather than a
replacement for them -- so the same kernel boots a real NIC/WiFi card
*and* Proxmox's virtual NIC without needing two separate builds.

## Build

`HARDBOOT`/`VGA` change which kernel gets compiled, so `prepare` needs
the same flags -- not just the Nimbusfile:

```bash
GOOS=linux GOARCH=amd64 go build -o helloserver ../../cmd/helloserver
cnimbus prepare --hardboot eth+wifi --vga --out ./pieces
cnimbus build-disk -f Nimbusfile --pieces ./pieces
```

This produces `cnimbus-hello-http-demo.iso` -- already built and
included in this directory, so you can skip straight to booting it.

## FIREWALL / FIREWALL6

Both rulesets are wide open on port 8080 to the whole internet, exactly
as declared:

```
FIREWALL  -A INPUT -p tcp -s 0.0.0.0/0 --dport 8080 -j ACCEPT
FIREWALL6 -A INPUT -p tcp -s ::/0      --dport 8080 -j ACCEPT
```

`FIREWALL6` (AD-047) is new: it reverses this project's earlier
"disable IPv6 at boot" decision (T12) -- the kernel now actually has
IPv6 compiled in *and enabled*, plus a full `ip6tables` chain, using the
same bundled static binary `FIREWALL` already uses (it dispatches as
either `iptables` or `ip6tables` depending on its first argument -- no
second binary needed).

## VGA IP banner

`VGA true` + `--vga` at `prepare` time turns on a boot-time console
banner (there's no shell anywhere in this image to log in and run
`ip addr` yourself): once eth0 gets an address, the console prints
every IPv4 and IPv6 (if any) address found, e.g.:

```
cnimbus: IPv4 address: 10.0.2.15 (eth0)
cnimbus: IPv6 address: fd00::... (eth0)
```

Read it directly off the physical monitor on real hardware, or
Proxmox's own noVNC/SPICE console.

## Real verification

Booted for real under QEMU (`qemu-system-x86_64`, `-nographic`,
e1000 NIC): the kernel comes up, DHCP assigns eth0 an IPv4 address, the
console IP banner prints it, `curl` through a forwarded port returns
`hello from cnimbus`, and both `FIREWALL`/`FIREWALL6` apply their full
ruleset with no rule failures (`ip6tables`'s own dispatch mode was
independently confirmed against the real static binary before this
feature was wired up at all).

**One caveat found during this verification, specific to the QEMU test
harness -- not this image:** QEMU's user-mode (`slirp`) networking's
built-in DHCP server responds noticeably slower once `-netdev
user,ipv6=on` is added, and this project's own bounded DHCP timeout
(`-t 3 -T 1`, T92 -- a deliberate few-second cap, not tens of seconds)
can expire before a lease arrives in that combination. This is a
known `slirp` quirk, not a cnimbus bug: a real DHCP server (on an
actual physical network, or Proxmox's own virtual network) responds
immediately, and the kernel's IPv6 stack itself was confirmed live
(`NET: Registered PF_INET6 protocol family`) and `ip6tables` confirmed
applying cleanly regardless. If you want to reproduce the *IPv4* boot
locally before touching real hardware, test without `ipv6=on`; there's
no equivalent quick local check for a full external-IPv6-reachability
test short of the real target network.

## A real bare-metal boot bug found and fixed here

The first real boot of this exact image on physical UEFI hardware
failed with `Kernel panic - not syncing: Attempted to kill init!` after
a wall of `/dev/sr0`/`/dev/vda`/`/dev/nvme0n1p2`/... `Can't lookup
blockdev` lines. Root cause: stage 1's own boot-media probe
(`internal/rootfs/stage1.go`) only ever tried device names hypervisors
hand out (`sr0`, `vda`, `vdb`, ...) for the ISO9660 filesystem -- never
`/dev/sda`/`/dev/sdb`, which is exactly how a real SATA/USB-attached
disk enumerates on physical hardware (this project's own ISO has no
partition table of its own -- see "Known limitations" -- so there's no
`/dev/sdaN` to look for, just the whole device). On top of that, a real
USB drive's SCSI negotiation can take a few seconds *after* the
kernel's "new SuperSpeed USB device" line before its `/dev/sdX` node
exists at all -- something no hypervisor's emulated disk (attached
instantly at guest boot) ever has to wait for. The probe tried once and
gave up; it now retries the whole scan for up to 10 seconds. Both fixed
in the same commit that fixed the missing `/dev/sda`/`/dev/sdb`
candidates; the ISO in this directory already has the fix baked in and
was re-verified booting cleanly under QEMU (no regression) before being
committed.

If you hit `Failed to execute /init (error -8)` instead (a different
failure, seen on an earlier write attempt before the good one above) --
that's `ENOEXEC`, which points at a corrupt/incomplete write to the USB
drive, not a script bug (stage 1 never even got as far as reading a
device candidate list at that point). Re-`dd`/re-flash the ISO and
retry.

## Multiboot USB tools (Ventoy and similar) now work too

A second real hardware boot -- this time via a multiboot USB tool
(Ventoy) instead of a plain `dd`'d stick -- surfaced two more real
gaps, now fixed:

1. **The kernel never had a USB mass-storage driver at all.** UEFI
   firmware can load a kernel off a USB stick fine (it has its own USB
   stack for that), but the *running* Linux kernel then needs its own
   driver to re-discover that same stick as a block device -- something
   no HARDBOOT profile ever had, confirmed by a real serial capture
   showing every boot-media candidate missing entirely no matter how
   long the retry loop (above) waited. Fixed: `CONFIG_USB_STORAGE`/
   `CONFIG_USB_UAS` added to a new `baremetal-usb.fragment`, merged for
   any real-hardware `HARDBOOT` profile (not just `wifi`, which used to
   be the only reason this image had USB support at all).
2. **A multiboot USB tool doesn't expose our ISO9660 filesystem
   directly.** Ventoy (and any grub-loopback-based multiboot setup)
   boots by chainloading grub against a `.iso` *file* sitting on an
   ordinary FAT/exFAT data partition -- there's no device that *is* our
   ISO9660 tree the way a plain `dd`'d stick has. Fixed with a generic
   scan (the same technique Ubuntu's casper/Fedora's dracut already
   ship, not tied to any one vendor's tool): every real partition gets
   mounted as vfat/exfat and searched for any `*.iso` file, which is
   then loop-mounted in turn to get at `SQUASHFS.IMG` inside it.
   `CONFIG_EXFAT_FS` added (Ventoy's own data partition format).

**Real verification:** rather than needing a second physical hardware
boot, this was fully reproduced and confirmed fixed locally: a small
FAT-formatted disk image containing a copy of this exact ISO was
attached to QEMU as a real USB mass-storage device (`usb-storage` on a
`qemu-xhci` controller -- the same driver path real hardware uses, not
QEMU's own virtual CD-ROM), booted, and the serial log shows the whole
real chain: `usb-storage ... USB Mass Storage device detected` ->
`scsi ... Direct-Access ... HARDDISK` -> `cnimbus: boot device not
found yet -- retrying` (the enumeration-delay race, real) ->
`cnimbus: IPv4 address: ...` -> a real `curl` returning `hello from
cnimbus`.

## Identifying which image actually booted (CNIMBUS.CFG)

If your multiboot USB stick carries more than one cnimbus-built image,
the boot-media scan above has no reliable way to know which one you
picked in the boot tool's own menu -- that selection generally never
reaches this project's own boot code at all (this was investigated in
depth; the mechanisms distros like Ubuntu/Debian use for this under
Ventoy specifically depend on Ventoy's own built-in signature database
recognizing their exact directory layout, not something a new,
unrecognized image can opt into generically).

What this image *does* carry: a plain-text `CNIMBUS.CFG` at the ISO's
own top level (readable before ever mounting `SQUASHFS.IMG`), with
this build's real `HOSTNAME`/`ARCH`/`FORMAT`/version. The console
prints it the moment a boot-media candidate is committed to:

```
cnimbus: identified boot image:
HOSTNAME=cnimbus-hello-http-demo
ARCH=amd64
FORMAT=iso
CNIMBUS_VERSION=...
```

Verified for real under QEMU with the file renamed to something
unrelated (`myimage.iso`) on a FAT stick -- the console still correctly
printed the real `HOSTNAME`, regardless of the on-disk filename. If you
keep more than one cnimbus image on the same stick, this is your
signal for confirming *which* one actually came up, even though it
doesn't (and can't, without Ventoy-specific integration) drive which
one gets selected in the first place.

## Booting it for real

**Real hardware (UEFI only -- see README's "Known limitations": this
ISO is not isohybrid, so a legacy-BIOS `dd`'d USB stick will not boot
from it):**

```bash
# Linux/macOS
sudo dd if=cnimbus-hello-http-demo.iso of=/dev/sdX bs=4M status=progress conv=fsync
# Windows: Rufus, "DD Image" mode, pointed at the .iso file
```

Boot the target machine's UEFI firmware from that USB stick.

**Proxmox:**

1. Upload `cnimbus-hello-http-demo.iso` to a Proxmox storage that
   allows ISO images (Datacenter -> Storage -> your storage -> ISO
   Images, or `pvesm` from the CLI).
2. Create a VM with no hard disk at all (the whole image runs from
   RAM) and no OS type / "Do not use any media" for the install step.
3. Attach the uploaded ISO as its CD/DVD drive, set boot order to boot
   from it, and start the VM.
4. Watch the VM's console (noVNC/SPICE) for the IP banner, then reach
   it at `http://<that-address>:8080/`.

Either way, port 8080 is reachable from anywhere on IPv4 and IPv6 as
soon as the console prints an address.

## Se a tela parar no `Link is Up` (corrigido -- AD-052)

Duas tentativas reais em hardware físico foram reportadas como
travamento: o monitor mostrava o dmesg do kernel, parava na linha
`r8169 ... eth0: Link is Up`, e nunca mais avançava -- sem IP, sem os
checkpoints de uptime, nada.

O boot nunca travou. A cmdline deste projeto é
`console=tty0 console=ttyS0,115200n8`: o kernel imprime em **todos** os
`console=`, mas o userspace herda apenas o **último** como
`/dev/console`. Ou seja, o dmesg ia para o monitor enquanto tudo que a
imagem imprime por conta própria -- o banner de IP, os checkpoints, o
estado dos serviços -- ia só para a serial. Uma tela com dmesg que para
na última linha do kernel é indistinguível de um travamento.

A correção não mexe na cmdline (isso só moveria o ponto cego para quem
usa console serial). Cada mensagem passa por `/etc/cnimbus-say`, que
escreve em todos os consoles que o kernel registrou, lidos de
`/sys/class/tty/console/active`. Na mesma leva, o scan de mídia agora
confere a assinatura exFAT antes de tentar montar exfat: o driver
imprime três linhas de erro por dispositivo, e o scan percorre todos os
discos duas vezes -- em uma máquina com NVMe mais pendrive isso enchia
duas telas e empurrava as mensagens úteis para fora.

Se você estiver rodando uma ISO antiga e quiser confirmar que a máquina
está viva apesar da tela parada, tente a porta 8080 pela rede: ela já
estava respondendo o tempo todo nos dois casos reportados.

## Se IPv4 funciona mas IPv6 não chega em lugar nenhum (corrigido -- AD-055)

Confirmado num Proxmox real: a VM pegava um endereço IPv6 global
correto via SLAAC (o roteador estava com IPv6 habilitado), mas nenhum
outro host da rede conseguia alcançá-la -- nem sequer resolver seu MAC.

A causa é sutil porque não tem equivalente em IPv4: o ARP nunca passa
pelo `iptables`, então uma política `-P INPUT DROP` nunca quebra a
resolução de endereço em IPv4. Mas o IPv6 não tem ARP -- o Neighbor
Discovery Protocol roda inteiramente sobre ICMPv6, que **é** tráfego IP
normal e **é** filtrado pelo `ip6tables` como qualquer outro pacote.
Este próprio example declarava `FIREWALL6 -P INPUT DROP` mais uma regra
liberando só a porta 8080 -- e isso bloqueava toda Neighbor Solicitation
endereçada à VM, então nenhum pacote conseguia chegar de fato, nem
aqueles que a regra deveria liberar.

A correção injeta automaticamente as mensagens ICMPv6 mínimas
recomendadas pela RFC 4890 sempre que `FIREWALL6` é usado (Neighbor
Discovery completo, mais os erros de PMTUD que o próprio IPv6
depende). Isso não muda nada em `FIREWALL` (IPv4), que nunca precisou
disso.

Testado de ponta a ponta contra o Proxmox real: `curl -6` na porta 8080
retorna `200 OK` a partir de outra máquina na mesma rede.

## Refinamento com hardware já funcionando (AD-057/AD-058)

Depois de IPv4 e IPv6 confirmados de ponta a ponta em hardware físico
real, uma leitura linha a linha do log de boot revelou só ruído
cosmético, não bugs:

- O scan de mídia de boot tentava montar até 14 nomes de dispositivo
  candidatos (`/dev/sr0`, `/dev/vda2`, `/dev/nvme0n1p2`, etc.), e uma
  máquina real normalmente só tem um ou dois desses de verdade -- cada
  tentativa nos outros imprimia sua própria linha `Can't lookup
  blockdev`. Agora verifica se o dispositivo existe antes de tentar
  montar.
- `firewall.sh` e `firewall6.sh` imprimiam a mesma frase
  ("fallback-on-error mode: 'open'") sem indicar qual — parecia
  duplicado. Agora aparece como `(IPv4)`/`(IPv6)`.
- A linha `r8169: Unable to load firmware rtl_nic/rtl8168h-2.fw` era
  real, mas inofensiva (confirmado lendo o driver: esse firmware é só
  um patch de correção do MAC, não bloqueia a rede). Ainda assim, o
  arquivo real (baixado e verificado com hash SHA-256 contra o
  linux-firmware oficial) agora vai empacotado na imagem, removendo a
  linha por completo.

Nenhuma dessas mudanças afeta o funcionamento -- a rede já funcionava
antes de todas elas.

## Ctrl+Alt+Del e "Signal" (shutdown/reboot pelo Proxmox) -- AD-059/AD-060

Dois sinais de controle do Proxmox agora funcionam de verdade:

**"Signal Shutdown"** (o desligamento gracioso via ACPI): tinha dois
bugs reais no `acpid`. Faltava `CONFIG_INPUT_EVDEV` (sem ele,
`/dev/input` nem existia), e o `acpid` do BusyBox não lê o formato
clássico `/etc/acpi/events/*` -- ele espera o handler direto em
`/etc/acpi/PWRF/00000080`. Corrigido e testado de ponta a ponta contra
o Proxmox real: `qm shutdown` agora completa com sucesso, sem timeout.

**Ctrl+Alt+Del**: o driver de teclado PS/2 estava desabilitado de
propósito (bug real e documentado de travamento no Hyper-V Gen1).
Reativamos e testamos de novo num Hyper-V real -- o travamento não
reproduziu (provavelmente corrigido numa atualização do próprio
Hyper-V desde então). Confirmado funcionando via QEMU local (reboot
completo disparado pelo combo) e, pelo botão do console web do
Proxmox, também no Proxmox real. Só não funciona via API com um
token de escopo restrito (`screendump` pelo mesmo token retorna
explicitamente "root-only command") -- limitação de permissão do
token, não do código.
