# Building cnimbus from source

`cnimbus` is a single static Go binary, and building it -- for any of
the 7 target platforms below -- requires **only Docker**. No local Go
install, no SDKs, no cross toolchains. This isn't a fallback for
machines without Go; it's the one supported way to build this project,
so a `git clone` plus a Docker install is the entire prerequisite chain,
on any host OS.

## Prerequisites

- Docker (or Podman, drop-in CLI-compatible -- swap `docker` for
  `podman` in every command below).
- That's it.

## Compiling cnimbus itself, entirely inside Docker

```bash
docker run --rm -v "$(pwd)":/src -w /src \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
  golang:1.26.5 go build -o cnimbus ./cmd/cnimbus
```

**Always set `GOOS`/`GOARCH` explicitly**, even when building "for this
machine" -- unlike a bare `go build`, which defaults to the *host's*
OS/arch, a containerized build defaults to the *container image's*
OS/arch (`golang:1.26.5` is a Linux image, so an `-e GOOS`-less build on
Windows or macOS silently produces a Linux binary).

On Windows, PowerShell's `$(pwd)` needs `${PWD}` instead:

```powershell
docker run --rm -v "${PWD}:/src" -w /src `
  -e CGO_ENABLED=0 -e GOOS=windows -e GOARCH=amd64 `
  golang:1.26.5 go build -o cnimbus.exe ./cmd/cnimbus
```

The container never persists anything beyond the mounted source
directory (Go's module/build cache lives inside the throwaway container
and is discarded with it) -- slower on repeat builds than a locally
cached Go install, but a genuinely zero-host-dependency build.

## Running `prepare` entirely inside Docker too

`cnimbus prepare` itself needs Docker (it orchestrates the kernel/
BusyBox/iptables build in throwaway containers) -- but the `cnimbus`
process running `prepare` doesn't have to live on the bare host either.
The root [`Dockerfile`](Dockerfile) builds cnimbus and wraps it in a
minimal image carrying just the `docker` CLI, so `prepare` can run as a
container itself, reaching the host's Docker daemon through a mounted
socket (the standard Docker-out-of-Docker pattern):

```bash
docker build -t cnimbus .

docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$(pwd)":/work -w /work \
  cnimbus prepare --arch amd64 --out ./pieces
```

Everything up through a populated `./pieces` directory happens without
installing anything beyond Docker itself.

## `build-disk` needs none of the above

Once `pieces/` exists (built as above, fetched from a URL, or copied
from another machine), `build-disk` is pure Go with zero dependencies
-- no Docker, no compiler, nothing beyond the `cnimbus` binary itself
and the `pieces/` folder's contents:

```bash
cnimbus build-disk --pieces ./pieces -o my-image.iso
```

This is why the two commands are split the way they are: `prepare`
(needs Docker) produces portable, hash-verified pieces once; anyone
downstream with just those pieces and a `cnimbus` binary assembles an
image on a machine with nothing else installed at all.

## The 7 release targets

| GOOS | GOARCH | Binary name | Who this is for |
|---|---|---|---|
| `windows` | `amd64` | `cnimbus-amd64-windows.exe` | Windows 10/11, Intel/AMD |
| `windows` | `arm64` | `cnimbus-arm64-windows.exe` | Windows on ARM (Surface Pro X, etc.) |
| `linux` | `amd64` | `cnimbus-amd64-linux` | most Linux desktops/servers/CI |
| `linux` | `arm64` | `cnimbus-arm64-linux` | Raspberry Pi 4/5, AWS Graviton, etc. |
| `linux` | `riscv64` | `cnimbus-riscv64-linux` | RISC-V Linux hosts |
| `darwin` | `amd64` | `cnimbus-amd64-darwin` | Intel Macs |
| `darwin` | `arm64` | `cnimbus-arm64-darwin` | Apple Silicon Macs (M1/M2/M3/M4) |

Binary names are `cnimbus-<arch>-<os>[.exe]` -- name, then CPU
architecture, then OS. Note: this is the architecture of the machine
*running* `cnimbus`, entirely independent of the `ARCH` a Nimbusfile
declares for the *image being built* -- `cnimbus` runs anywhere in this
table and can produce either amd64 or arm64 guest images.

### Building all 7, via Docker (bash/zsh)

```bash
mkdir -p bin

build() {
  local goos=$1 goarch=$2 ext=$3
  echo "building $goos/$goarch..."
  docker run --rm -v "$(pwd)":/src -w /src \
    -e CGO_ENABLED=0 -e GOOS="$goos" -e GOARCH="$goarch" \
    golang:1.26.5 go build -trimpath -ldflags="-s -w" \
    -o "bin/cnimbus-$goarch-$goos$ext" ./cmd/cnimbus
}

build windows amd64 .exe
build windows arm64 .exe
build linux   amd64
build linux   arm64
build linux   riscv64
build darwin  amd64
build darwin  arm64

ls -la bin/
```

### Building all 7, via Docker (PowerShell)

```powershell
New-Item -ItemType Directory -Force bin | Out-Null

function Build-Cnimbus {
    param($Goos, $Goarch, $Ext = "")
    Write-Host "building $Goos/$Goarch..."
    docker run --rm -v "${PWD}:/src" -w /src `
      -e CGO_ENABLED=0 -e GOOS=$Goos -e GOARCH=$Goarch `
      golang:1.26.5 go build -trimpath -ldflags="-s -w" `
      -o "bin/cnimbus-$Goarch-$Goos$Ext" ./cmd/cnimbus
}

Build-Cnimbus windows amd64 ".exe"
Build-Cnimbus windows arm64 ".exe"
Build-Cnimbus linux   amd64
Build-Cnimbus linux   arm64
Build-Cnimbus linux   riscv64
Build-Cnimbus darwin  amd64
Build-Cnimbus darwin  arm64

Get-ChildItem bin
```

## Verifying a cross-built binary

You can't *run* a binary built for a different OS/arch than your
machine, but you can confirm the build actually targeted what you
asked for:

```bash
file bin/cnimbus-arm64-linux
# -> ELF 64-bit LSB executable, ARM aarch64, ...
```

```powershell
Get-Item bin\cnimbus-arm64-windows.exe | Select-Object Name, Length
```

## Modifying Thunder's source

Thunder's source lives in two places that must stay in sync:

- `cmd/thunder/` and `internal/compileagent/` -- the real, normal Go
  source, part of `cnimbus`'s own module.
- `internal/assets/data/thunder-src/` -- an embedded *copy* of the
  above (via `//go:embed all:data/thunder-src` in
  `internal/assets/assets.go`), used at runtime by `cnimbus prepare` to
  compile Thunder inside a container.

If you change anything under `cmd/thunder/` or `internal/compileagent/`,
copy it into the embedded tree before rebuilding `cnimbus`:

```bash
cp cmd/thunder/main.go internal/assets/data/thunder-src/cmd/thunder/main.go
cp internal/compileagent/*.go internal/assets/data/thunder-src/internal/compileagent/
```

The embedded copy's `go.mod` is named `go.mod.embed` (Go's `go:embed`
refuses to embed a directory containing a real `go.mod`, treating it as
crossing into "a different module"; `cnimbus prepare` renames it back
before compiling). If you add a new dependency to
`internal/compileagent`, update it and re-vendor from inside that
directory, using the same Docker-only pattern as everything else here:

```bash
docker run --rm -v "$(pwd)":/src -w /src/internal/assets/data/thunder-src \
  golang:1.26.5 sh -c "mv go.mod.embed go.mod && go mod tidy && go mod vendor && mv go.mod go.mod.embed"
```

## Embedding a version string (optional)

`cnimbus version` already exists (`cmd/cnimbus/main.go`'s `var version =
"dev"`, printed by the `version` subcommand) -- it just prints `"dev"`
unless you inject a real value at build time via the standard Go
`-ldflags -X` pattern:

```bash
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
docker run --rm -v "$(pwd)":/src -w /src \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
  golang:1.26.5 go build -ldflags="-s -w -X main.version=$VERSION" \
  -o cnimbus ./cmd/cnimbus
```

Add this to any of the build commands above (the `-ldflags` values
combine) if you want `cnimbus version` to report something other than
`dev`.

## Troubleshooting

- **`go:embed all:data/thunder-src: cannot embed directory ... in
  different module`** -- the embedded tree's `go.mod` got renamed back
  to `go.mod` and left that way. It must be named `go.mod.embed` in the
  committed source (see "Modifying Thunder's source" above).
- **`cnimbus prepare` fails with `exec format error` or similar inside
  the container** -- this would mean the builder image's platform
  doesn't match Thunder's own architecture. `internal/dockerrun` always
  passes `--platform linux/<arch>` matching the Nimbusfile's `ARCH` to
  both the Thunder-compiling step and the kernel-building step
  specifically to prevent this; if you see it, check you're running a
  `cnimbus` built from this same source tree, not an older build.
- **`cnimbus prepare --arch arm64` works but is slow on a non-arm64
  host** -- expected: Docker Desktop emulates arm64 via QEMU/TCG
  software emulation when the host isn't natively arm64, meaningfully
  slower than native compilation. Inherent to cross-architecture
  container emulation, not something `cnimbus` can avoid.
- **`docker run --privileged --rm tonistiigi/binfmt --install all`** --
  run this once if a bare Docker Engine host (Linux, not Docker
  Desktop) fails to run a non-native `--platform`; Docker Desktop
  registers this automatically, but plain Docker Engine sometimes
  doesn't.
