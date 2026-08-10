🇧🇷 Português (este arquivo) · 🇺🇸 [Read in English](README.en.md)

# cnimbus

O cnimbus constrói imagens mínimas de VM Linux, no espírito distroless,
a partir de um manifesto declarativo chamado **Nimbusfile** (a sintaxe
lembra um Dockerfile), tanto para amd64 quanto para arm64 como
arquitetura do guest. Roda em QEMU, VirtualBox, VMware, Hyper-V,
Proxmox, Firecracker e hardware físico — veja "Plataformas suportadas"
mais abaixo para o que cada uma oferece.

Vale separar duas coisas desde já: a arquitetura do **guest** (a imagem
de VM que o cnimbus constrói) é amd64 ou arm64; a arquitetura do
**host** (a máquina onde você roda o próprio `cnimbus`) inclui também
**riscv64** no Linux, além de amd64 e arm64 em Windows/Linux/macOS —
veja "Compilando a partir do fonte" mais abaixo.

A ideia lembra a de um unikernel, mas não é bem isso: a meta é a mesma
(uma única carga de trabalho, nenhum shell, nenhum login, superfície de
ataque mínima, o boot vai direto pro seu serviço), o mesmo objetivo de
projetos como MirageOS, Unikraft ou OSv — só que o cnimbus chega lá por
outro caminho. Em vez de um library OS feito sob medida e colado no
espaço de memória da aplicação, ele usa um kernel Linux mainline de
verdade, sem modificações, mais o BusyBox. Isso custa um pouco do
minimalismo extremo de um unikernel de verdade, mas em troca você roda
um binário Go comum (ou qualquer binário estático) sem tocar nele,
tem semântica POSIX de verdade, e ainda herda todo o suporte a
driver/hardware do kernel — nada de reescrever sua aplicação para um
runtime específico de unikernel.

Tem um manual completo em LaTeX, em português, cobrindo desde a
instalação até cada diretiva do Nimbusfile, cada backend, segurança e
Secure Boot, boot em hardware físico, e um passo a passo de cada
exemplo lá embaixo (comandos, flags e nomes de diretivas ficam sempre
em inglês, mesmo no manual). Ele está em
[docs/manual/cnimbus-manual.pdf](docs/manual/cnimbus-manual.pdf), com o
fonte em [cnimbus-manual.tex](docs/manual/cnimbus-manual.tex).

## Um binário só, oito subcomandos, dois mundos bem diferentes

O `cnimbus` é um executável único e autônomo. É isso que ele faz:

- **`cnimbus init`** escreve um Nimbusfile de exemplo pra você editar.
- **`cnimbus prepare`** é o *único* comando que encosta no Docker.
  Primeiro ele compila um programinha Go chamado **Thunder** para a
  arquitetura alvo, dentro de um container `golang` descartável (sem
  precisar de toolchain Go instalada na sua máquina). Depois usa o
  Thunder para compilar o kernel Linux, o BusyBox e um `iptables`
  estático dentro de um segundo container, também descartável. O
  resultado disso tudo — chamamos de "pieces" — é o `vmlinuz`, um
  binário `busybox` estático com seu manifesto de applets, e um
  binário `iptables` estático. Não dá pra fugir do Docker aqui: compilar
  o kernel exige Kbuild, e Kbuild exige make, um compilador C que gere
  código pra Linux, e um shell POSIX — nada disso existe nativamente no
  Windows ou no macOS. Se houver um Nimbusfile no diretório atual, o
  `prepare` lê as diretivas `KERNEL`, `BUSYBOX`, `ARCH` e `VGA` dele
  (mais detalhes em "Nimbusfile vs. flags"); sem Nimbusfile, ele usa a
  última release estável do kernel.org e a versão padrão do próprio
  BusyBox. A assinatura PGP do tarball do kernel é conferida contra a
  chave do kernel.org, buscada na hora via Web Key Directory — o
  `cnimbus` não carrega nenhuma chave embutida (dá pra desligar essa
  checagem com `--insecure-skip-kernel-verify`, útil pra um mirror
  offline).
- **`cnimbus build-disk`** nunca toca no Docker nem em compilador
  nenhum. É Go puro: pega essas pieces (de um diretório local, do
  cache, ou de uma URL HTTP(S) simples, sempre conferindo o hash contra
  o `pieces.sha256` que o `prepare` gerou) e monta a imagem final,
  `FORMAT iso` ou `FORMAT raw`, junto com um manifesto de build
  `<output>.lock`.
- **`cnimbus validate`** confere um Nimbusfile sem construir nada:
  checa a sintaxe, confirma que todo `COPY`/`ADD` aponta pra um arquivo
  que existe, e verifica se a arquitetura de cada binário ELF copiado
  bate com o `ARCH` declarado.
- **`cnimbus clean`** remove os volumes e imagens Docker que o
  `prepare` cria (e, se quiser, o cache local de pieces também). Tem
  `--dry-run` pra você ver o que seria apagado antes.
- **`cnimbus run`** inicializa uma imagem localmente — via QEMU se
  estiver no `PATH`, ou via `--vbox` usando o `VBoxManage`. Se nenhum
  dos dois estiver disponível, ele te mostra o comando de boot manual
  pro seu hypervisor.
- **`cnimbus kv-serve`** é o lado servidor (no host) do transporte HTTP
  da diretiva `AGENT` — veja "Configuração ao vivo" mais abaixo.
- **`cnimbus version`** mostra a versão do build.

Na prática, você roda o `prepare` uma vez por arquitetura (ou sempre
que quiser um kernel atualizado), publica a pasta de saída em algum
lugar, e a partir daí o `build-disk` nunca mais vai precisar de Docker
— ele só baixa e monta.

## Começando rápido

```bash
# uma vez só, ou quando quiser um kernel novo: gera ./pieces/amd64 (precisa de Docker)
cnimbus prepare --arch amd64 --out ./pieces

# e/ou pra arm64:
cnimbus prepare --arch arm64 --out ./pieces

# gera um Nimbusfile de exemplo
cnimbus init

# edite o Nimbusfile e, daqui pra frente, sem Docker nenhum:
cnimbus build-disk --pieces ./pieces -o my-image.iso
```

Se você está depurando um boot num hypervisor com interface gráfica
(o VirtualBox é o caso mais comum) e a tela fica preta, calma: por
padrão a imagem só manda saída pro console serial, que é o que
QEMU/Proxmox normalmente usam. O cmdline até declara `console=tty0`,
mas sem um driver de vídeo de verdade atrás dele — então a janela do
hypervisor fica preta mesmo com o boot dando certo. Pra resolver, passe
`--vga` no `prepare`:

```bash
cnimbus prepare --arch amd64 --vga --out ./pieces
```

O `--pieces` também aceita uma URL `http://`/`https://`, contanto que
você já tenha publicado uma pasta gerada por `prepare --out` em algum
lugar — nesse caso ele busca `<url>/<arch>/vmlinuz`,
`<url>/<arch>/busybox` e `<url>/<arch>/busybox-manifest.tsv`. Se não
quiser passar `--pieces` toda hora, defina a variável de ambiente
`CNIMBUS_PIECES`.

Dá uma olhada em [examples/](examples/): tem Nimbusfiles completos e
funcionais cobrindo `ENV`, `VOLUME`, os dois modos de `AGENT`,
`SERVICE`, um `IP` estático combinado com `FIREWALL`, e `FORMAT raw` —
cada um com as próprias instruções de build e boot.

## Como acessar, do seu host, um serviço rodando dentro do guest

Se o `ENTRYPOINT`/`CMD` do seu Nimbusfile sobe algo escutando numa
porta (tipo o `helloserver` de demonstração, na `:8080`), tem um
detalhe que pega muita gente: por padrão, todo hypervisor usa NAT pra
rede, e isso esconde o guest atrás de um endereço que só ele mesmo
enxerga. Ou seja, `localhost:8080` no seu host simplesmente não vai
funcionar até você criar uma regra de port-forward ligando uma porta do
host a essa porta do guest. Isso é configuração do hypervisor — o
`cnimbus` não tem controle sobre isso.

**VirtualBox** — pela interface: selecione a VM, vá em *Settings* ->
*Network* -> *Advanced* -> *Port Forwarding* e adicione uma regra
(deixe "Host IP" e "Guest IP" em branco, e coloque `8080` tanto em Host
Port quanto em Guest Port). Via linha de comando, antes de ligar a VM:

```bash
VBoxManage modifyvm "<vm-name>" --natpf1 "http,tcp,127.0.0.1,8080,,8080"
```

A partir daí, `curl http://127.0.0.1:8080/` no host já alcança o
guest.

**VMware Workstation/Player (Windows/Linux)** — abra *Edit* -> *Virtual
Network Editor*, selecione a rede NAT (geralmente VMnet8), vá em *NAT
Settings* -> *Add*, e mapeie a porta `8080` do host pro IP do guest na
porta `8080`, TCP. Só um detalhe: o botão "Add" só fica habilitado se
você abrir o Virtual Network Editor como Administrador/root.

**VMware Fusion (macOS)** — edite direto o arquivo
`/Library/Preferences/VMware Fusion/vmnet8/nat.conf` e adicione, dentro
de `[incomingtcp]`:

```
8080 = <guest-ip>:8080
```

Depois reinicie a rede: `sudo /Applications/VMware Fusion.app/Contents/Library/vmnet-cli --stop`
e em seguida `--start`.

**QEMU** — basta passar uma regra `hostfwd` no backend de rede em modo
usuário, sem nenhuma configuração extra:

```bash
qemu-system-x86_64 ... -netdev user,id=n0,hostfwd=tcp:127.0.0.1:8080-:8080 -device virtio-net-pci,netdev=n0 ...
```

(Usar `127.0.0.1` limita a porta encaminhada ao loopback — igual ao
padrão do próprio `cnimbus run`. Se deixar o endereço do host vazio, a
porta fica exposta em todas as interfaces, ou seja, alcançável pela sua
rede inteira.)

**Hyper-V** é o caso diferente da turma: o NAT do "Default Switch"
padrão dele rejeita qualquer conexão de entrada vinda do host, então
não existe um port-forward simples por VM pra configurar ali. Veja
`--backend hyperv` mais abaixo, que automatiza um equivalente funcional
(um switch próprio do `cnimbus`, combinado com IP estático) em vez de
um passo a passo manual.

**Alternativa mais simples, vale pra qualquer hypervisor**: troque o
adaptador de rede da VM de NAT para **Bridged**. Aí o guest ganha o
próprio IP direto na sua LAN, e você acessa em
`http://<guest-ip>:8080` sem regra nenhuma de port-forward pra manter —
o custo é que o guest fica visível pra rede inteira, não só pro host.

**Ou, mais fácil ainda, pule tudo isso**: `cnimbus run --hostfwd
8080:8080` já automatiza esse port-forward sozinho, pros backends
`qemu` (usando o próprio `hostfwd`), `vbox` (via `VBoxManage
--natpf1`) e `hyperv` (o switch descrito acima) — veja `cnimbus run
-h`. O único que fica de fora é `--backend vmware`, porque a
configuração de NAT do VMware é um único arquivo compartilhado
(`vmnetnat.conf`), não algo por VM — nesse caso, a seção específica
acima ainda se aplica. Nos três backends automatizados, a porta
encaminhada fica presa ao loopback (`127.0.0.1`) por padrão; se quiser
expor de propósito além dessa máquina, use `--hostfwd-bind 0.0.0.0` (ou
um endereço específico).

## Configuração ao vivo: a diretiva AGENT

Tudo o que você declara num Nimbusfile — `ENV`, `COPY`, etc. — fica
fixo desde o momento do `build-disk`: pra mudar qualquer coisa, você
tem que reconstruir a imagem e reiniciar a VM. `AGENT` é a exceção: um
valor que você muda no host chega numa VM *já rodando* em poucos
segundos, sem rebuild e sem reboot. Existem vários transportes — use o
que fizer mais sentido pro seu hypervisor ou nuvem:

```dockerfile
AGENT http://10.0.2.2:9999/ 3           # HTTP puro -- qualquer hypervisor com rede no guest
AGENT vboxguest /cnimbus/message 3      # canal nativo do VirtualBox -- detalhes abaixo
AGENT virtio-serial /dev/vport0p1 3     # QEMU/Proxmox, sem precisar de qemu-guest-agent
AGENT aws-imds /latest/meta-data/tags 3 # AWS EC2 IMDSv2
AGENT ibm-imds /metadata/v1/instance 3  # metadados do IBM Cloud VPC
```

Não importa o transporte: todos escrevem no mesmo lugar,
`/var/run/cnimbus-kv.json` (um tmpfs — sobrevive até o próximo reboot,
some no desligamento; não serve pra persistir dados entre boots, pra
isso use `VOLUME`). Então qualquer coisa que leia esse arquivo (o
`helloserver` de demonstração relê a cada requisição) se comporta igual
não importa qual transporte está por trás.

### `AGENT <url> [interval]` — HTTP puro

Esse funciona em qualquer hypervisor que tenha rede no guest, sem
nenhuma dependência específica de plataforma:

1. **`cnimbus kv-serve`** roda no seu host e serve o conteúdo ao vivo
   de um arquivo JSON local, relendo o disco a cada requisição:
   ```bash
   cnimbus kv-serve --file kv.json --addr :9999
   ```
   Basta editar o `kv.json` e salvar — sem reiniciar nada, sem chamar
   API nenhuma, a próxima requisição já vem com o conteúdo novo.
2. **O loop de `AGENT` dentro do guest** é só um `wget` do BusyBox
   rodando dentro de um `while true`: ele busca essa URL a cada
   `[interval]` segundos e grava a resposta no arquivo de kv.

Pra chegar do guest até o host, você precisa saber o gateway NAT do seu
hypervisor — `10.0.2.2` no caso do VirtualBox usado acima, e um IP fixo
parecido na maioria dos outros. Se preferir não decorar isso pra cada
hypervisor, mude a rede do guest pra **Bridged** e use o IP real da sua
LAN.

Se o `--addr` estiver escutando além do loopback (caso de um setup
Bridged), combine `cnimbus kv-serve --token <token>` (ou
`--generate-token`) com uma linha `AGENT header`, pra que o poller do
guest também se autentique:

```bash
cnimbus kv-serve --file kv.json --addr :9999 --token secret123
```

```dockerfile
AGENT http://192.168.1.50:9999/ 3
AGENT header Authorization: Bearer secret123
```

### `AGENT vboxguest <property> [interval]` — o canal nativo do VirtualBox, sem Guest Additions

O VirtualBox já tem um mecanismo próprio pra isso, chamado Guest
Properties: um key-value store simples que o host pode alterar a
qualquer momento. O caminho normal pra acessar isso é via Guest
Additions — um módulo de kernel fora da árvore principal mais um daemon
em userspace (`VBoxService`) — bem mais pesado do que a imagem inteira
costuma ser. Este modo chega no *mesmo* canal sem nada disso, porque a
própria Oracle levou o driver do lado guest pro Linux mainline
(`drivers/virt/vboxguest/`, desde a versão ~4.14 do kernel). Bastando
habilitar `CONFIG_VBOXGUEST`, algo que o `cnimbus prepare` já faz na
hora de compilar o kernel a partir do fonte, você ganha
`/dev/vboxguest` de graça. O que o Linux mainline não traz pronto é uma
forma de chamar o serviço HGCM de Guest Properties através desse
device — e é exatamente isso que o `cmd/cnimbusagent` resolve, no seu
tipo `vboxguest`: um cliente pequeno, escrito do zero, seguindo a ABI
ioctl documentada de `/dev/vboxguest` e o protocolo `GuestPropertySvc`
que a própria VirtualBox publica. Ele já vem embutido no `cnimbus`,
pré-compilado pras duas arquiteturas, e é colocado na imagem
automaticamente quando você usa esse modo.

Pra definir o valor a partir do host:

```bash
VBoxManage guestproperty set <vm-name> /cnimbus/message "some value"
```

Funciona sem nenhuma Guest Additions instalada — a mudança feita com
`VBoxManage guestproperty set` no host chega ao guest dentro de um
intervalo de poll.

**Nota pra quem usa Windows/Git Bash**: um nome de propriedade que
começa com `/` parece um caminho absoluto pra conversão automática de
paths do MSYS, que silenciosamente reescreve isso — `/cnimbus/message`
vira algo tipo `C:/Program Files/Git/cnimbus/message`. Rode o comando
com `MSYS_NO_PATHCONV=1` na frente sempre que usar `VBoxManage
guestproperty` a partir do Git Bash no Windows.

## Nimbusfile vs. flags

Essas duas coisas não competem entre si — cada uma resolve um problema
diferente:

- **O Nimbusfile descreve a imagem**: qual kernel, qual BusyBox, qual
  arquitetura, o que vai dentro, o que roda no boot. É o arquivo que
  você deve versionar, porque é ele que garante que o build seja
  reproduzível — tanto `prepare` quanto `build-disk` leem o mesmo
  arquivo, então um único Nimbusfile comanda o pipeline inteiro.
- **As flags descrevem a execução em si**: qual Nimbusfile ler (`-f`),
  de onde vêm as pieces (`--pieces`) ou pra onde vai a saída (`-o`).
  São coisas específicas da máquina onde você está rodando — nada que
  você comitaria no repositório.

Quatro configurações podem ser passadas dos dois jeitos — `KERNEL`,
`BUSYBOX`, `ARCH` e `VGA` — e a regra pras quatro é sempre a mesma: o
Nimbusfile declara o valor, mas se você passar uma flag na linha de
comando, ela ganha. Isso vale até pra desligar alguma coisa:
`--vga=false` vence mesmo se o Nimbusfile tiver `VGA true` escrito. A
flag sempre tem a palavra final, nos dois sentidos.

| | Nimbusfile | flag | quem lê |
|---|---|---|---|
| versão do kernel | `KERNEL` | `--kernel` | `prepare` |
| versão do busybox | `BUSYBOX` | `--busybox` | `prepare` |
| arquitetura | `ARCH` | `--arch` | os dois |
| console VGA | `VGA` | `--vga[=false]` | `prepare` |
| hostname, DHCP, FORMAT, COPY/ADD, ENTRYPOINT/CMD, AGENT | sim | -- | `build-disk` |
| caminho do Nimbusfile, origem/destino das pieces, caminho de saída | -- | sim | conforme o caso |

## Nimbusfile

A ideia é a mesma de um Dockerfile: você declara o que quer, roda um
comando, pronto.

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

O `cnimbus init` gera uma versão bem mais completa disso, com
alternativas comentadas pra cada diretiva que aceita versão (uma
versão exata de `KERNEL`, `latest-longterm`, uma versão fixa de
`BUSYBOX`, `ARCH arm64`) — dá uma olhada nele se quiser ver todas as
opções sem precisar procurar aqui.

| Diretiva | O que faz |
|---|---|
| `KERNEL <version>` | Aceita `latest-stable`, `latest-longterm`, ou uma versão exata (ex.: `6.9.4`). Só o `cnimbus prepare` lê isso — é ele quem decide qual release do kernel.org compilar. O `build-disk` nem olha pra esse valor, ele simplesmente usa as pieces que você apontar, não importa como foram geradas. |
| `BUSYBOX <version>` | Versão exata (ex.: `1.36.1`), ou `latest` pra usar o padrão embutido do próprio cnimbus. Também só o `prepare` lê. |
| `ARCH <amd64\|arm64>` | Arquitetura alvo, `amd64` por padrão. Os dois comandos leem isso: o `prepare` pra saber o que compilar, o `build-disk` pra saber quais pieces buscar (elas ficam separadas por arquitetura). Dá pra sobrescrever com `--arch` em qualquer um dos dois. |
| `VGA <true\|false>` | Liga um console VGA/framebuffer de verdade em `console=tty0`; vem `false` por padrão. Só o `prepare` lê, e dá pra sobrescrever com `--vga[=false]`. Fica desligado por padrão porque o console serial (`ttyS0`/`ttyAMA0`) já está sempre ativo de qualquer forma — só vale a pena ligar se você precisar ver a saída de boot na tela de um hypervisor com interface gráfica, o VirtualBox sendo o caso mais comum. |
| `HOSTNAME <name>` | Hostname da imagem. |
| `DHCP <true\|false>` | Sobe a `eth0` via DHCP no boot. |
| `IP <addr> <netmask> <gw>` | IP estático em vez de DHCP; se os dois estiverem presentes, o IP estático ganha. `eth1`, `eth2` e `eth3` continuam subindo automaticamente via DHCP em segundo plano se existirem, independente dessa configuração. |
| `DNS <addr>` | Define um nameserver explícito em `/etc/resolv.conf` no boot, sobrescrevendo o que o DHCP tiver mandado. Pode repetir a linha pra mais de um servidor. |
| `NTP <server\|false>` | Sincroniza o relógio no boot contra `<server>` (`pool.ntp.org` por padrão). Repetível pra vários servidores — o `ntpd` consulta todos numa chamada só e escolhe a melhor resposta. `false` desliga tudo (e limpa qualquer servidor que uma linha `NTP` anterior tenha adicionado). Precisa de `DHCP` ou `IP` configurado; sem rede nenhuma, essa etapa é simplesmente pulada. |
| `FORMAT <iso\|raw>` | O *tipo* de imagem a gerar, não é um caminho de arquivo. Com `iso`: duas entradas de boot El Torito em amd64 (BIOS e UEFI, dependendo do que o hypervisor ou a unidade óptica virtual oferecer) e só UEFI em arm64 — repare que **não é uma imagem isohybrid**, veja a nota abaixo antes de gravar num pendrive com `dd`/Rufus. Com `raw`: um disco GPT com duas partições — uma ESP UEFI pequena e de tamanho fixo, só com o kernel e o initramfs, e uma segunda partição com a raiz SquashFS direto, sem sistema de arquivos por cima; não existe caminho BIOS/legado em nenhuma das duas arquiteturas. Esse formato é pensado pra templates de disco estilo Proxmox/nuvem, e também é o caminho certo pra boot em hardware físico/USB no lugar de `iso`. O caminho de saída em si é definido com `cnimbus build-disk -o <path>` (por padrão `<hostname>.iso`, ou `<hostname>.img` no caso do `raw`). |
| `USER <name>` | Faz todo `ENTRYPOINT`/`CMD`/`SERVICE` rodar sob essa conta sem privilégios (uid/gid 1000) em vez de root, usando o `setuidgid` do BusyBox. Por padrão continua root, sem mudar nada. De qualquer forma não existe shell nenhum na imagem — veja "Limitações conhecidas". Portas abaixo de 1024 exigem root. |
| `WORKDIR <path>` | Diretório de trabalho pra `ENTRYPOINT`, `CMD` e cada `SERVICE`. Padrão é `/`. |
| `LABEL <KEY>=<VALUE>` | Metadado livre da imagem, gravado em `/etc/cnimbus-release`. Repetível, e não muda em nada o comportamento do boot. |
| `EXPOSE <port>[/tcp\|/udp]` | Só documenta que a imagem escuta numa porta (protocolo `tcp` por padrão) — é puramente informativo, o `cnimbus` não abre firewall nem cria port-forward nenhum por conta disso. Repetível. |
| `ARG <NAME>[=<default>]` | Declara uma variável de build-time, usável como `${NAME}` ou `$NAME` em qualquer diretiva depois dela, resolvida uma única vez durante o parse. Dá pra sobrescrever com `--build-arg NAME=value` (repetível) no `prepare` ou no `build-disk`; um `ARG` sem valor padrão e sem sobrescrita vira erro de parse. Um `$` sozinho, que não inicia um identificador válido (tipo um preço de US$5, `$5`), passa direto sem mudança; já `$$` é o jeito de escapar um `$` literal quando ele *iniciaria* um identificador (mesma convenção do Docker) — isso é necessário pra que um `ENTRYPOINT`/`CMD` consiga passar um `$VAR` literal adiante, deixando o `sh` do BusyBox expandir isso em *runtime* (a partir de uma diretiva `ENV`) em vez de expandir já no parse do Nimbusfile. Exemplo: `ENTRYPOINT ["/bin/sh", "-c", "echo $$HOME"]`. |
| `VOLUME <device> <mount> [fstype] [required]` | Monta `<device>` (ex.: `/dev/vda`) em `<mount>` no boot, pra armazenamento persistente; `fstype` é `vfat` por padrão, ou `ext4`. **Nunca formata o disco** — o device já precisa chegar pronto, formatado, anexado por você mesmo no hypervisor. Sem `required`: se não montar, o boot simplesmente segue sem esse disco. Com `required`: uma falha na montagem trava o boot com uma mensagem FATAL, antes de qualquer serviço subir contra um armazenamento que na verdade não existe. Repetível. É opcional — o resto da imagem inteira é RAM-only e somente leitura de qualquer forma. |
| `AGENT <url> [interval]` | Consulta `<url>` (HTTP(S) simples, qualquer servidor) a cada `[interval]` segundos (`5` por padrão) e grava o corpo da resposta em `/var/run/cnimbus-kv.json`. É assim que uma VM já rodando consegue captar mudanças de configuração sem rebuild nem reboot, em qualquer hypervisor com rede no guest. Detalhes em "Configuração ao vivo" mais acima. |
| `AGENT header <name>: <value>` | Um header HTTP extra, mandado junto com a requisição da `AGENT <url>` acima (só vale logo depois de uma linha `AGENT` do tipo `http`). Serve pra cobrir endpoints de metadados de nuvem que exigem header próprio, tipo `Metadata-Flavor: Google` no GCE ou `Authorization: Bearer ...` no OCI. Repetível. |
| `AGENT vboxguest <property> [interval]` | Mesmo mecanismo de config ao vivo, mas lendo direto as Guest Properties do próprio VirtualBox (via `CONFIG_VBOXGUEST` do mainline, sem precisar de Guest Additions). Só funciona no VirtualBox. Detalhes em "Configuração ao vivo". |
| `AGENT virtio-serial <device> [interval]` | Lê o valor ao vivo direto de um character device `virtio-serial` do QEMU/Proxmox (ex.: `/dev/vport0p1`), sem precisar de `qemu-guest-agent`. |
| `AGENT aws-imds <path> [interval]` / `AGENT ibm-imds <path> [interval]` | Busca `<path>` no serviço de metadados AWS EC2 IMDSv2 ou no IBM Cloud VPC, usando o binário embutido `cnimbusagent` — ele já sabe fazer o handshake de PUT-token-depois-GET que os dois serviços exigem, coisa que o `wget` do BusyBox sozinho não consegue. |
| `AGENT vmware <key> [interval]` | Lê uma variável `guestinfo.<key>` que você definiu no host VMware (numa linha `guestinfo.*` do `.vmx`, ou via `vmrun writeVariable <vmx> guestVar <key> <value>`), usando o protocolo backdoor de I/O de baixa banda do próprio VMware — sem precisar de VMware Tools nem open-vm-tools instalados. Só funciona em `linux/amd64` (precisa de privilégio de I/O em ring-3, conseguido via `iopl(3)`); em `arm64` você recebe uma mensagem clara de "não implementado" em vez de um comportamento silenciosamente errado. |
| `ENV <KEY>=<VALUE>` | Variável de ambiente exportada pra todo `ENTRYPOINT`/`CMD`/`SERVICE`. Repetível; se repetir a mesma chave, a última declaração ganha. |
| `FIREWALL <rule>` | Uma linha de regra `iptables` (IPv4), aplicada no boot através de um `iptables` estático que o próprio `cnimbus prepare` compila e embute automaticamente (se você trouxer o seu via `COPY` e ele estiver no `PATH`, esse tem prioridade). Repetível. |
| `FIREWALL6 <rule>` | O equivalente em IPv6 de `FIREWALL`: mesma sintaxe de regra, só que rodando o mesmo binário chamado como `ip6tables`. É um conjunto de regras totalmente independente — declarar um não afeta o outro em nada. Repetível. |
| `FIREWALL_ON_ERROR <open\|closed>` | Define o que acontece se uma regra de `FIREWALL`/`FIREWALL6` falhar ao aplicar no boot (por exemplo, o kernel não tem o match que a regra pede). `open` (padrão): esvazia tudo pra aceitar qualquer conexão, assim o boot nunca trava por causa de um conjunto de regras quebrado. `closed`: bloqueia tudo, exceto loopback e conexões já estabelecidas — pensado pra um Nimbusfile cuja política já era DROP por padrão, onde cair pra "aceita tudo" inverteria completamente a intenção original. Vale pros dois conjuntos de regras, e não tem efeito nenhum se não existir nem `FIREWALL` nem `FIREWALL6`. |
| `HEALTHCHECK [--interval=<n>] [--retries=<n>] <cmd...>` | Vale só pro processo do `ENTRYPOINT` (seguindo o mesmo modelo do Docker, um `HEALTHCHECK` por container). Roda `<cmd...>` a cada `--interval` segundos (`30` por padrão) enquanto o processo estiver vivo, e mata/reinicia depois de `--retries` (`3` por padrão) falhas seguidas. |
| `RESTART <target> <always\|on-failure\|no>` | Política de reinício pro `entrypoint` ou pra um `SERVICE` nomeado. `always` (padrão): reinicia sempre, com backoff linear limitado. `on-failure`: só reinicia se sair com código diferente de zero. `no`: roda uma vez só e para de supervisionar. |
| `STOPGRACE <seconds>` | Por quanto tempo um desligamento (botão de energia ACPI, `poweroff`, Ctrl-Alt-Del) espera o processo do `ENTRYPOINT`/`CMD` sair sozinho depois de receber `SIGTERM`, antes de escalar pra `SIGKILL` e derrubar o guest de vez. Padrão: `10` segundos. Sem essa diretiva, a sequência de desligamento do init do BusyBox dá só ~1 segundo pra cada processo — pouquíssimo tempo pra terminar trabalho em andamento, tipo uma escrita em buffer, uma transação aberta ou uma requisição HTTP no meio do caminho. O sinal é direcionado com precisão (um PID real, rastreado, não o guest inteiro), mas só pro processo que uma diretiva `HEALTHCHECK` já rastreia — na prática, o `ENTRYPOINT`/`CMD` — já que é o único caminho que conhece o PID de verdade da carga de trabalho, e não o de um pipe de log. Outros `SERVICE`s continuam ganhando a mesma janela de tempo geral antes do guest desligar, só que sem um sinal individual direcionado a eles. |
| `TMPSIZE <size>` | Sobrescreve o `size=` das quatro montagens tmpfs de diretório executável em RAM (`bin`, `sbin`, `usr/bin`, `usr/sbin`) que o estágio 1 recria a cada boot; o padrão é `32m`. `<size>` é um número inteiro positivo, com sufixo opcional `k`/`m`/`g` (a mesma sintaxe que a opção `size=` do tmpfs do próprio kernel já aceita) — por exemplo, `TMPSIZE 128m`. Se um `COPY`/`ADD` destinado a um desses quatro diretórios ultrapassar esse limite, o `build-disk` falha na hora, com uma mensagem clara, em vez de só descobrir isso no boot com um erro de `ENOSPC`. |
| `PIECESKEY <hex-pubkey>` | Fixa a chave pública Ed25519 (gerada com `cnimbus keygen`) que o `pieces.sha256` precisa estar assinado. A partir daí, o `build-disk` recusa construir se a fonte de pieces não publicar um `pieces.sha256.sig` correspondente a essa chave — veja "Assinando pieces" mais abaixo. Uma flag `--pieces-verify-key` passada na linha de comando sobrescreve isso, seguindo a mesma regra de qualquer outra configuração que exista tanto no Nimbusfile quanto como flag. |

`FORMAT iso` tem um segundo limite, independente do `TMPSIZE`: o
pacote combinado de kernel + initramfs do estágio 1 também precisa
caber na entrada de boot "sem emulação" do El Torito, cujo campo de
tamanho é de 16 bits em unidades de 512 bytes — cerca de 32 MiB no
total, compartilhado entre os dois arquivos, não por arquivo separado.
Se um `COPY`/`ADD` em `bin`, `sbin`, `usr/bin` ou `usr/sbin` for grande
o bastante pra empurrar o initramfs *já comprimido* do estágio 1 além
de uns 24 MiB (o orçamento prático), o `build-disk` falha e aponta
exatamente qual arquivo é o culpado. Aumentar o `TMPSIZE` não resolve
esse problema — é um limite diferente, do próprio mecanismo de boot do
ISO, não da RAM. Já o `FORMAT raw` não tem esse teto — se você precisar
copiar algo maior, mude pra ele ou tire o arquivo grande desses quatro
diretórios.
| `COPY [--chmod=<mode>] <src> <dest>` | Copia um arquivo local, um diretório ou um glob pra dentro da imagem. Quando é um diretório, só o *conteúdo* dele é copiado, não o diretório em si (igual ao `COPY` do Docker); `--chmod` define a permissão em octal, explicitamente. **Precisa combinar com o ARCH, e precisa ser um binário Linux estático** — a imagem não carrega linker dinâmico nem libc próprios, e nem shell existe pra rodar um interpretador, então tudo o que você copiar precisa ser estaticamente vinculado pra `linux/<amd64\|arm64>`. Isso não é uma exigência específica de Go: qualquer linguagem capaz de gerar um binário Linux estático pra essa arquitetura funciona igual — Go (`GOOS=linux GOARCH=<amd64\|arm64> CGO_ENABLED=0`), Rust (`--target <arch>-unknown-linux-musl`), Zig, C/C++ (`-static`, normalmente contra musl), Crystal, FreePascal, Dart compilado AOT, .NET publicado como Native AOT, entre outras. O `cnimbus validate` confere a arquitetura do ELF resultante pra você, não importa de onde ele veio. |
| `ADD [--chmod=<mode>] <src> <dest>` | Igual ao `COPY`, mas `src` também pode ser uma URL, e um `.tar`/`.tar.gz`/`.tgz` local é extraído automaticamente dentro de `dest` (a mesma semântica do `ADD` real do Docker). |
| `ENTRYPOINT <cmd...>` | O serviço principal, reiniciado no boot sob um backoff de crash-loop (linear e limitado, até 30s entre tentativas), a menos que você sobrescreva isso com `RESTART entrypoint ...`. Aceita forma shell (`ENTRYPOINT /usr/bin/foo`) ou forma exec (`ENTRYPOINT ["/usr/bin/foo", "arg"]`). |
| `CMD <args...>` | Argumentos padrão, anexados depois do `ENTRYPOINT` — ou, se não houver `ENTRYPOINT`, o comando inteiro que será reiniciado. |
| `SERVICE <name> <cmd...>` | Um processo extra, também reiniciado e supervisionado, rodando junto com `ENTRYPOINT`/`CMD` — mesmo backoff (sobrescrevível via `RESTART <name> ...`), mesmo `ENV`/`USER`. Repetível. |

## Assinando pieces

O `pieces.sha256` (que o `prepare` grava junto com `vmlinuz`,
`busybox` e `busybox-manifest.tsv`) é um manifesto de hashes: o
`build-disk` confere cada arquivo baixado contra ele, então um download
corrompido ou uma substituição no meio do caminho é sempre detectada.
Só que isso garante *integridade*, não *autenticidade* — prova que os
bytes batem com o que o manifesto diz, mas não prova quem publicou
esse manifesto em primeiro lugar. Se alguém conseguir escrever no lugar
onde as pieces estão publicadas (um bucket S3, um mirror interno), dá
pra trocar o `vmlinuz` e o `pieces.sha256` juntos, e a checagem de hash
sozinha vai reportar sucesso do mesmo jeito.

É pra fechar essa brecha que existe a assinatura:

```bash
cnimbus keygen --out my-signing-key.hex
# gravou a semente da chave privada em my-signing-key.hex (mantenha isso em segredo -- nunca comite)
# chave pública (fixe com --pieces-verify-key ou uma linha PIECESKEY no Nimbusfile):
# <64 caracteres hexadecimais>

cnimbus prepare --pieces-sign-key my-signing-key.hex
# ...grava pieces.sha256.sig junto com a saída de sempre

cnimbus build-disk --pieces-verify-key <a-chave-publica-impressa-acima>
# (ou coloque "PIECESKEY <chave-publica>" no Nimbusfile em vez de usar a flag)
```

Com `--pieces-verify-key`/`PIECESKEY` definido, o `build-disk` só
constrói se a fonte de pieces tiver publicado um `pieces.sha256.sig`
que verifique certinho contra essa chave — uma fonte sem assinatura
nenhuma, ou assinada com outra chave, é rejeitada do mesmo jeito que
uma divergência de hash já seria (código de saída 4; veja "Exit codes"
em `cnimbus help`). Sem nenhum dos dois configurado, nada muda: um
`pieces.sha256` sem assinatura continua funcionando normalmente, como
sempre funcionou.

Isso cobre o primeiro passo, o mais simples, da cadeia de confiança que
começa com a própria assinatura PGP do kernel (conferida na hora do
`prepare`): autenticar o `pieces.sha256` em si. Assinar o objeto de
kernel EFI-stub que o firmware realmente executa — e, mais pra frente,
UKI e measured boot — são marcos maiores, ainda no roadmap. Veja
[ROADMAP.md](ROADMAP.md).

## Arquitetura

```
cnimbus prepare (precisa de Docker)                      cnimbus build-disk (nunca precisa de Docker)
────────────────────────────                            ────────────────────────────────
kernel.org releases.json ──┐
  (resolve versão do KERNEL)│
                            ▼
        ┌───────────────────────────────────┐
        │ 1. container golang                │
        │    --platform linux/<ARCH>         │        Nimbusfile
        │    compila o Thunder a partir do   │            │
        │    seu fonte embutido              │            ▼
        └────────────┬────────────────────────┘     ┌──────────────┐
                     │ binário Thunder (linux/<ARCH>)│  build-disk    │
                     ▼                                │  (Go puro)    │
        ┌───────────────────────────────────┐         └──────┬───────┘
        │ 2. container gcc:14-trixie          │                │
        │    --platform linux/<ARCH>         │                │
        │    (gcc nativo -- o container       │                │
        │    *é* ARCH, sem cross-compiler)    │                │
        │    Thunder roda make/gcc            │                │
        └────────────┬────────────────────────┘                │
                     │                                          │
               vmlinuz, busybox,                                │
               busybox-manifest.tsv                              │
                     │                                           │
                     └──────────────► publicadas ─────────────────┘
                                      pieces
                                         │
                                         ▼
                          estágio 1: initramfs cpio/gzip minúsculo
                          (BusyBox + symlinks de applets + /init)
                                         │
                                         ▼
                          estágio 2: raiz SquashFS (Go puro,
                          github.com/diskfs/go-diskfs) -- /etc,
                          scripts de supervisor, seu COPY/ADD
                                         │
                                         ▼
                    amd64: ISO9660+El Torito, BIOS+UEFI    arm64: ISO9660, só UEFI
                    (isolinux + EFI stub, Go puro)          (sem equivalente a BIOS em arm64)
                          FORMAT raw: GPT + ESP (kernel/initramfs) + uma segunda
                          partição contendo a raiz SquashFS diretamente, ambas
                          as arquiteturas (sem caminho BIOS)
                                         │
                                         ▼
                              .iso / .img bootável
```

### Boot em dois estágios: o porquê, e a única brecha real que isso deixa

Pra dar boot direto numa raiz somente leitura, você precisa de um block
device de verdade pra montar o filesystem — um initramfs cpio inteiro
em RAM (que era o que este projeto usava antes) não consegue ser
somente leitura de forma nenhuma que faça sentido, já que a coisa toda
já mora numa memória livremente gravável. Por isso o `build-disk` hoje
gera duas imagens em vez de uma:

1. **Estágio 1** — o initramfs de verdade, carregado pelo kernel,
   pequeno e vivendo em RAM como antes: BusyBox, seus ~400 symlinks de
   applets, e um `/init` que localiza a raiz SquashFS e faz
   `switch_root` pra ela. No `FORMAT iso`, isso significa achar o
   device de CD-ROM, dar `losetup` no `SQUASHFS.IMG` (um arquivo dentro
   da árvore ISO9660) num loop device, e montar tudo isso como somente
   leitura. Já no `FORMAT raw`, nem existe loop device: a raiz SquashFS
   é sua própria partição GPT, então o `/init` tenta uma lista curta de
   nomes prováveis pra segunda partição (tipo `/dev/vda2`) e monta cada
   um deles direto como squashfs.
2. **Estágio 2** — uma raiz SquashFS de verdade, construída com
   `github.com/diskfs/go-diskfs` (sem `mksquashfs`, sem container
   nenhum): o `/etc`, os scripts de inittab/rcS/supervisor que são
   gerados, e todo destino de `COPY`/`ADD` — exceto um caso, explicado
   a seguir.

**A tal da brecha**: o design inteiro do BusyBox depende de uns 400
symlinks (cada nome de applet apontando pro mesmo binário), e o
*writer* de SquashFS do go-diskfs deixa `Symlink`/`Link` como stub
(`filesystem.ErrNotImplemented`, na versão vendorizada aqui) — ou seja,
simplesmente não tem como representar esses symlinks dentro de uma
imagem SquashFS usando essa biblioteca. A saída que encontramos foi:
`bin/`, `sbin/`, `usr/bin/` e `usr/sbin/` viram `tmpfs`, não SquashFS —
o estágio 1 monta tmpfs em cima de cada um deles e recria todos os
symlinks do zero, a cada boot, a partir do mesmo manifesto que ele já
carrega de qualquer jeito. Na prática, isso significa que esses quatro
diretórios são a única parte de uma imagem cnimbus que *não* é
realmente imutável — todo o resto genuinamente é. E também significa
que qualquer `COPY`/`ADD` que caia num desses quatro diretórios (como o
próprio `/usr/bin/helloserver` do exemplo) precisa passar pelo estágio
1 também, sendo copiado pro lugar certo depois das montagens de tmpfs
— isso já acontece automaticamente, você não precisa se preocupar com
isso no seu Nimbusfile.

Por que o projeto tomou esse formato, na prática:

- **Não tem como fugir do Kbuild.** Não existe forma razoável de
  reimplementar a resolução de dependências do Kconfig, nem o sistema
  de build do kernel, em Go, Rust ou Zig. É só por isso que o
  `prepare` chama `make`/`gcc` dentro de um container Linux — e só por
  isso. Aliás, não tem um único script `.sh` neste repositório inteiro;
  o Thunder, que é quem de fato roda dentro desse container, é ele
  mesmo um programa Go.
- **O Kconfig falha em silêncio quando um símbolo pedido não pode ser
  aplicado — por isso o `verifyFragmentsApplied`, em
  `internal/compileagent/kernel.go`, checa isso explicitamente.** Se um
  fragment pede `CONFIG_X=y` mas algum portão do qual ele depende não
  está satisfeito, o Kconfig simplesmente joga a linha fora. O
  `merge_config.sh` até registra um aviso de "Previous value", mas isso
  passa despercebido no meio de um build com milhares de linhas de
  saída; o `olddefconfig` remove o símbolo, o `make` funciona
  normalmente, a imagem constrói e até dá boot — só que alguma
  funcionalidade específica simplesmente não está lá, sem nenhum aviso.
  Hoje o `prepare` compara o `.config` final com tudo que os fragments
  pediram, e derruba o build citando pelo nome qualquer símbolo que o
  Kconfig tenha descartado, em vez de deixar passar um kernel
  silenciosamente incompleto.
- **O Thunder é compilado sob demanda, nunca embutido pronto.** O
  `cnimbus` carrega o *fonte* do Thunder embutido (mais uma cópia
  vendorizada da única dependência que ele tem), e o `prepare` compila
  isso na hora, dentro de um container `golang` descartável, pra
  arquitetura que o Nimbusfile pedir. É por isso que `ARCH arm64`
  produz um pipeline inteiramente arm64 — container compilador,
  Thunder, e o kernel/BusyBox que ele constrói — sem precisar de
  toolchain Go instalada localmente, e sem um binário arm64
  pré-compilado inchando toda cópia do `cnimbus` por aí.
- **Cada container roda nativamente na arquitetura alvo**, usando
  `docker --platform linux/<arch>` — nunca cross-compilado a partir de
  um host amd64. A emulação Rosetta/QEMU do Docker Desktop faz um
  container arm64 funcionar tranquilamente num host amd64 (e
  vice-versa), então um `gcc` comum, sem prefixo nenhum, já é a escolha
  certa tanto pro kernel quanto pro BusyBox nas duas arquiteturas — sem
  precisar do pacote cross-compiler `gcc-aarch64-linux-gnu`.
- **A imagem do builder é a mais enxuta possível, dentro do que o
  Kbuild ainda permite**: sem curl (o Thunder busca os fontes sozinho,
  em Go), sem busybox (esse é o *artefato* final, nunca uma dependência
  do container que o constrói). Ela não pode ser totalmente sem shell
  ("distroless"): o próprio sistema de build do Kbuild exige um shell
  POSIX pra executar as receitas do Makefile — isso é uma característica
  do próprio kernel, não uma escolha feita aqui.
- **A imagem de VM montada, essa sim, é distroless de verdade**: não
  existe shell reiniciado em lugar nenhum do inittab, sem login, sem
  getty — os *únicos* pontos de entrada numa imagem rodando são
  exatamente o que o Nimbusfile declarar em `ENTRYPOINT`, `CMD` ou
  `SERVICE`. Se o Nimbusfile não declarar serviço nenhum, a imagem
  simplesmente sobe e fica ali parada — de propósito.
- **O ISO em si é Go puro.** Sem `grub-mkrescue`, sem `xorriso`. Ele usa
  `github.com/diskfs/go-diskfs` pra montar ISO9660 + El Torito, e
  vendoriza os binários pré-compilados e redistribuíveis do próprio
  syslinux (`isolinux.bin`/`ldlinux.c32`, os mesmos arquivos que
  qualquer ISO Linux comum usa) como estágio de boot BIOS em amd64. Já
  o UEFI não precisa de bootloader nenhum separado, em nenhuma das duas
  arquiteturas: o kernel é compilado com `CONFIG_EFI_STUB=y`, então a
  própria imagem do kernel também é um executável PE32+ válido, que o
  firmware UEFI consegue carregar direto (`BOOTX64.EFI` em amd64,
  `BOOTAA64.EFI` em arm64). Imagens arm64 nem têm entrada
  BIOS/isolinux nenhuma — simplesmente não existe equivalente de boot
  BIOS legado em arm64.
- **A árvore de instalação do BusyBox é distribuída como (binário +
  manifesto de symlinks), nunca como um diretório com symlinks de
  verdade.** Um bind mount do Docker Desktop transforma silenciosamente
  cada um dos ~400 symlinks de applets do BusyBox numa cópia completa
  do binário, quando o host é Windows — e nem o `os.Symlink`/
  `os.Readlink` do próprio Go conseguem fazer o round-trip de symlinks
  POSIX de forma confiável no Windows. O manifesto (`path<TAB>target`,
  um por linha) é a única representação que sobrevive intacta em
  qualquer sistema operacional host.

## Plataformas suportadas

Uma imagem gerada pelo cnimbus alcança rede e roda o seu binário em
userland nos seguintes ambientes:

- **QEMU** — amd64 BIOS+UEFI, arm64 UEFI.
- **VirtualBox** — amd64, boot completo em dois estágios com SquashFS,
  `VGA`, `USER`, e os dois modos de `AGENT` (`http`, `vboxguest`).
- **VMware Player/Workstation** — amd64, incluindo `AGENT vmware` via o
  protocolo backdoor de I/O do próprio VMware.
- **Hyper-V** — amd64, Geração 1 e 2, `--hostfwd` via um switch
  Internal de propriedade do cnimbus (o Default Switch nativo do
  Hyper-V não aceita conexões de entrada vindas do host).
- **Firecracker** — usando o `/dev/kvm` do WSL2.
- **Hardware físico e Proxmox** — IPv4 e IPv6, boot aparecendo tanto no
  VGA quanto no serial não importa qual dos dois o kernel trate como
  principal, aplicação de `FIREWALL`/`FIREWALL6`, e as duas ações do
  painel do Proxmox ("Signal Shutdown" via ACPI, e Ctrl+Alt+Del)
  desligando/reiniciando a VM corretamente — o mesmo vale pra Hyper-V
  Geração 1.
- **Ventoy e outras ferramentas multiboot USB baseadas em
  grub-loopback** — dá boot num ISO do cnimbus encadeado a partir de um
  arquivo `.iso` num pendrive FAT/exFAT (em vez de um device gravado
  direto com `dd`). O `CNIMBUS.CFG`, um manifesto em texto puro no
  topo do ISO, deixa o console reportar qual imagem realmente subiu,
  não importa o nome que ela tinha no pendrive.
- **Os quatro backends do `cnimbus run`**, `cnimbus clean`, `FORMAT
  raw` (com `--uefi`), `FIREWALL`/`FIREWALL6`, `IP` estático,
  `SERVICE`, `ENV` (incluindo o escape `$$VAR` pra expansão em
  runtime), `VOLUME` (com e sem device anexado) — veja
  [examples/](examples/) pra um Nimbusfile executável de cada
  funcionalidade.

Ainda em aberto: múltiplas placas de rede, `VOLUME` com `ext4`,
`DNS`/`resolv.conf`, `HEALTHCHECK`, `AGENT virtio-serial`, `AGENT
aws-imds`/`ibm-imds`, `cnimbus run --backend hyperv` com `FORMAT raw`,
e um rádio WiFi associando de verdade (`HARDBOOT wifi` já está
implementado, falta hardware pra testar). Veja
[ROADMAP.md](ROADMAP.md) pro backlog completo.

## Limitações conhecidas

- **IPv6 precisa de regras próprias em `FIREWALL6`.** O IPv6 vem
  ligado por padrão, e `FIREWALL`/`FIREWALL6` são dois conjuntos de
  regras totalmente independentes — um Nimbusfile que só declara
  `FIREWALL` deixa o IPv6 completamente sem filtro nenhum. Se o seu
  Nimbusfile não deve ficar acessível via IPv6, declare `FIREWALL6`
  explicitamente.
- **`ARCH` do Nimbusfile só aceita amd64 e arm64.** Ainda não dá pra
  usar riscv64 como *guest* (a CLI em si já roda tranquila em riscv64 —
  veja [BUILD.md](BUILD.md)); simplesmente não temos hardware
  disponível aqui pra validar um boot de guest riscv64.
- **`bin/`, `sbin/`, `usr/bin/` e `usr/sbin/` não fazem parte da raiz
  imutável.** Eles são tmpfs, recriados do zero em todo boot — veja
  "Boot em dois estágios" acima pra entender o porquê. Todo o resto da
  imagem, esse sim, é genuinamente somente leitura.
- **Nunca existe shell, em lugar nenhum, por design.** Nada de shell
  reiniciado, login ou getty em nenhuma imagem cnimbus. A única porta
  de entrada é o que quer que você declare em `ENTRYPOINT`, `CMD` ou
  `SERVICE`.
- **Ainda não tem hospedagem própria de pieces.** A saída do `prepare`
  precisa ser publicada em algum lugar por você mesmo; este projeto não
  hospeda nada.
- **A verificação de assinatura do kernel é best-effort.** Se você
  pedir uma versão de kernel que não está no índice de releases ao vivo
  do kernel.org, o `cnimbus` recorre a uma URL adivinhada em
  `cdn.kernel.org`, sem checagem PGP nenhuma nesse caso;
  `--insecure-skip-kernel-verify` desliga a checagem completamente,
  útil pra mirrors offline.
- **Sem suporte a UEFI Secure Boot pra uma imagem simples, não
  assinada.** Se o firmware alvo exigir isso, use `--secureboot` ou
  `--uki` (veja "Image formats and boot chain" no
  [CHANGELOG.md](CHANGELOG.md)).
- **`AGENT vmware` só funciona em `linux/amd64`** — em `arm64` você
  recebe uma mensagem explícita de "não implementado", em vez de um
  comportamento silenciosamente errado (de qualquer forma, o VMware no
  Windows não roda guests arm64).
- **`cnimbus run --backend hyperv` combinado com `FORMAT raw`** está
  com o código pronto e testado por unidade, mas ainda não passou por
  um boot real contra um Hyper-V de verdade.

  | `--backend` | `FORMAT iso`, BIOS | `FORMAT iso`, `--uefi` | `FORMAT raw` (sempre UEFI, por design) |
  | --- | --- | --- | --- |
  | `qemu` | sim | sim | sim |
  | `vbox` | sim | sim | sim (força UEFI, independente do `--uefi`) |
  | `vmware` | sim | sim | sim (força UEFI, independente do `--uefi`) |
  | `hyperv` | sim (Gen 1) | sim (Gen 2) | sim, mas ainda sem validação de boot real (acima) |

  Imagens arm64 só usam UEFI em qualquer backend, já que não existe
  caminho de boot BIOS-equivalente em arm64 — por isso a coluna de BIOS
  só faz sentido pra amd64.
- **`FORMAT iso` não gera uma imagem isohybrid.** O catálogo de boot El
  Torito BIOS+UEFI é real, mas não tem MBR isohybrid no byte 0 — gravar
  esse ISO num pendrive USB com `dd` não vai dar boot numa máquina
  BIOS legada. Pra USB/hardware físico, use `FORMAT raw`.
- **Sem harness automatizado de teste de boot em CI.** Toda validação
  de boot é feita na mão, contra instalações reais de
  hypervisor/hardware — veja [ROADMAP.md](ROADMAP.md).

Veja [ROADMAP.md](ROADMAP.md) e
[`.specs/project/STATE.md`](.specs/project/STATE.md) pra entender o
raciocínio e as evidências por trás de qualquer um desses pontos.

## Layout do repositório

```
cmd/cnimbus/           a CLI: init, prepare, build-disk, kv-serve, version
cmd/thunder/           compilado sob demanda pelo `prepare`, roda dentro do container de build (não é voltado ao usuário)
cmd/helloserver/       servidor HTTP Go de demonstração usado para validar COPY/ENTRYPOINT e AGENT
cmd/cnimbusagent/      cliente do lado guest de todo tipo de AGENT (http, virtio-serial, vboxguest,
                       aws-imds, ibm-imds, vmware); pré-compilado, embutido, colocado via mecanismo
                       tipo COPY -- veja internal/assets

internal/nimbusfile/   parser do Nimbusfile
internal/pieces/       busca pieces pré-construídas (diretório local ou URL), com namespace de arquitetura
internal/rootfs/       rootfs com init do busybox, montagem de boot SquashFS de dois estágios (Go puro)
internal/isoimage/     montagem de ISO9660 + El Torito (BIOS+UEFI, amd64/arm64) (Go puro)
internal/rawimage/     FORMAT raw: montagem de GPT + ESP + partição-raiz-SquashFS (Go puro)
internal/compileagent/ lógica de build de kernel + busybox (o próprio código do Thunder; roda dentro do container)
internal/kernelinfo/   resolve a versão do KERNEL contra o kernel.org
internal/dockerrun/    wrapper da CLI do docker (só no prepare)
internal/assets/       assets embutidos: isolinux, Dockerfile, fragments de kconfig, fonte do
                       Thunder, binários pré-compilados amd64/arm64 do cnimbusagent

docs/manual/           o manual de usuário completo em LaTeX (cnimbus-manual.tex) e seu PDF compilado
examples/              exemplos de Nimbusfile autocontidos e compiláveis, um por diretiva/funcionalidade
```

## Compilando a partir do fonte

O único pré-requisito é o Docker — não precisa instalar Go na sua
máquina:

```bash
docker run --rm -v "$(pwd)":/src -w /src \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
  golang:1.26.5 go build -o cnimbus ./cmd/cnimbus
```

Não tem nenhum passo prévio além disso — o Thunder é embutido como
*fonte*, não como binário pronto, e é o próprio `cnimbus prepare` quem
compila ele, sob demanda, dentro de um container. O `cnimbus` em si (a
CLI, não as imagens de guest que ele gera) roda nativamente em 7
plataformas: Windows (amd64, arm64), Linux (amd64, arm64, **riscv64**)
e macOS (amd64, arm64). Veja [BUILD.md](BUILD.md) pra compilação
cruzada nas 7 plataformas, e pra saber como rodar até o `prepare`
dentro do Docker. Só um detalhe importante: esse suporte a riscv64 vale
só pra plataforma *host* da própria CLI — o `ARCH` de um Nimbusfile (a
arquitetura da imagem *guest* que está sendo construída) continua
sendo só amd64 ou arm64, veja "Limitações conhecidas" acima.
