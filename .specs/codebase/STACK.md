# Tech Stack

**Analyzed:** 2026-08-06

## Core

- Language: Go 1.26.5 (pinned in `go.mod`; CI reads it via `go-version-file: go.mod`)
- Runtime: none — `cnimbus` is a single static CLI binary, `CGO_ENABLED=0`
- Package manager: Go modules (`go.mod`/`go.sum`)

## Build tooling

- **Docker** is used for exactly one thing: `cnimbus prepare` compiles the
  from-scratch kernel + BusyBox + iptables inside a throwaway Linux
  container. `cnimbus build-disk` is pure Go and never touches Docker.
- Builder image: `golang:1.26.5@sha256:...` — pinned by tag **and** digest
  so a registry-side tag mutation can't silently swap the build
  environment (`cmd/cnimbus/prepare.go`, `goBuilderImage`).
- A project-specific "forge" image is built around a freshly cross-
  compiled **Thunder** binary (the in-container orchestrator, source
  embedded under `internal/assets/data/thunder-src/`) from an embedded
  Dockerfile (`internal/assets.ForgeDockerfile`).
- Every `docker run`/`docker build` passes `--platform linux/<arch>`
  explicitly — the container runs the target arch natively via Docker
  Desktop's emulation layer, never cross-compiled.
- `docker run` for the real kernel/BusyBox build is hardened:
  `--security-opt=no-new-privileges`, `--cap-drop=ALL` + a narrow
  `--cap-add` allowlist (CHOWN/DAC_OVERRIDE/FOWNER/FSETID/SETGID/SETUID),
  `--pids-limit=4096`, and on Linux with a non-root invoking user,
  `--user=<uid>:<gid>` after a volume-ownership fixup step
  (`internal/dockerrun/dockerrun.go`).

## Key third-party dependencies

- `github.com/diskfs/go-diskfs v1.9.4` — disk-image library behind both
  image writers (`internal/isoimage`, `internal/rawimage`).
- `golang.org/x/sys v0.43.0` — low-level syscall access (e.g. the VMware
  backdoor I/O-port protocol in `cmd/cnimbusagent`).
- `github.com/ProtonMail/go-crypto v1.4.1` — OpenPGP, verifies the
  kernel.org release tarball's detached signature.
- `github.com/ulikunitz/xz v0.5.15` — pure-Go xz decompression (the
  kernel.org signature covers the decompressed tar stream, not the raw
  `.tar.xz`).

Notably absent: no testify/mocking framework, no CLI framework (stdlib
`flag`), no logging framework beyond stdlib `fmt`/`os.Stderr`.

## Testing

- Unit/integration: stdlib `testing`, table-driven style, no assertion
  library.
- End-to-end: `cmd/cnimbus/build_e2e_test.go` runs the real `runBuild`
  function against fixture pieces and inspects the produced artifact's
  actual on-disk bytes (ISO9660/El Torito, GPT/ESP structure).
- Live-network integration: `internal/compileagent/verify_test.go`'s
  `TestVerifyKernelTarballLive` hits real kernel.org, gated behind
  `testing.Short()` and `CNIMBUS_TEST_NETWORK=1`.

## CI

GitHub Actions (`.github/workflows/ci.yml`), three jobs on
`ubuntu-latest`:
1. `fmt-vet-test` — gofmt/vet/build/`go test -short ./...` + a Thunder
   embedded-source drift check (`go generate ./internal/assets`).
2. `lint` — `golangci-lint-action@v6`.
3. `build-all-targets` — 6-way `goos × goarch` matrix
   (windows/linux/darwin × amd64/arm64), `CGO_ENABLED=0`, ldflags stamp
   a real version via `git describe --tags --always --dirty`.

CI is explicitly build-only — it never boots an image under any
hypervisor (standing product decision, see project memory).

## Development tools

- Local `go test` on Windows must run inside a `golang` Docker container,
  not natively (Windows Defender deletes freshly-compiled Go test
  binaries before they execute).
