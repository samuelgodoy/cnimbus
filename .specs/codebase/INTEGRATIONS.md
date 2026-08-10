# External Integrations

## Docker (for `prepare`)

**Location**: `internal/dockerrun/dockerrun.go` — the sole place the
`docker` CLI is invoked, always as a subprocess (`os/exec`), never via
the daemon socket/SDK (works unmodified against Docker Desktop and
native Linux Docker Engine without juggling transports).

- `CheckAvailable()` — `docker` on PATH + daemon reachable + serving
  **Linux** containers specifically (`docker info --format
  '{{.OSType}}'` must be `"linux"`). Fails with sentinel `ErrUnavailable`.
- `BuildImage`/`Run` — always pass explicit `--platform linux/<arch>`.
  `Run` is hardened: `--security-opt=no-new-privileges`,
  `--cap-drop=ALL` + narrow `--cap-add` allowlist, `--pids-limit=4096`;
  on Linux with a non-root user, a disposable fixup container `chown`s
  any named-volume mount first, then `--user=<uid>:<gid>` is added.
  `--network=none` deliberately **not** set — Thunder fetches sources
  over the network from inside the container.
- Named containers (`cnimbus-prepare-<arch>-<pid>`) + a `cmd.Cancel`
  hook running `docker rm -f` on Ctrl-C.

**Auth/verification**: none needed to reach the local daemon. The
Thunder-builder image is pinned by tag *and* digest so a registry-side
tag mutation can't silently swap the build environment.

## QEMU / VirtualBox / VMware / Hyper-V (for `run`)

**Location**: `cmd/cnimbus/run.go` (QEMU default + VirtualBox),
`run_vmware.go`, `run_hyperv.go`, `run_vmdk.go`, `run_vhd.go` (Fixed VHD
footer -- also reused directly by `build-disk --format vhd`).

- **QEMU**: `qemu-system-<arch>`, located via `findTool` (PATH, then
  well-known Windows install paths). Args built entirely from parsed
  flags (`qemuArgv`) — annotated `#nosec G204` since no user-controlled
  string reaches the shell unsanitized. Supports `--accel`, `--uefi`
  (OVMF auto-detect or explicit `--ovmf-code`/`--ovmf-vars`),
  `--hostfwd`.
- **VirtualBox**: scripts `VBoxManage`.
- **VMware**: scripts `vmrun` (`run_vmware.go`).
- **Hyper-V**: scripts the Hyper-V PowerShell module — amd64-only,
  ISO-only.

**Auth/verification**: none — local hypervisor tooling, no network
trust boundary. `--hostfwd-bind` defaults to `127.0.0.1`.

## kernel.org (source + PGP signature verification via WKD)

**Location**: `internal/kernelinfo/kernelinfo.go` (version resolution),
`internal/compileagent/verify.go` (`VerifyKernelTarball`).

- Version resolution fetches kernel.org's `releases.json`. Failure wraps
  the sentinel `kernelinfo.ErrUpstreamFetch` → exit code 5 (retryable).
- Signature verification: **no PGP key material embedded** — every key
  is fetched live over HTTPS from kernel.org's own Web Key Directory
  (WKD, direct method). Each trusted identity's *primary-key
  fingerprint* is pinned as a compile-time constant (`releaseSigners`:
  gregkh, torvalds, sashal — verified by hand against
  kernel.org/signature.html). Any WKD-fetched key whose fingerprint
  doesn't match is rejected before it reaches the verification keyring.
- Signature covers the decompressed tar stream (verified empirically),
  decompressed via the vendored pure-Go `ulikunitz/xz`.
- Opt-out: `--insecure-skip-kernel-verify` ("only for a trusted offline
  mirror without a matching `.tar.sign`").
- Failure is sentinel `compileagent.ErrVerification` → exit code 4
  (never safe to retry as-is).

## busybox.net / netfilter.org (BusyBox / iptables sources)

**Location**: `internal/compileagent/busybox.go`/`iptables.go`, via
Thunder. No PGP/WKD-style signature check for these sources — integrity
relies on the pieces.sha256/pieces.sha256.sig chain applied *after* the
fact, not a source-fetch-time signature.

## Pieces distribution (integrity/authenticity layer)

**Location**: `internal/pieces/pieces.go`, consumed from
`cmd/cnimbus/build.go`.

- `Resolve(source, arch, opts)` fetches from a local directory or
  `http(s)://` prefix.
- **Integrity**: `pieces.sha256` checked against every fetched file;
  mismatch is a hard error (`pieces.ErrHashMismatch`), never downgraded.
  Missing manifest is a warning normally, a hard error for HTTP sources
  unless `--pieces-allow-unverified`.
- **Authenticity**: optional `--pieces-verify-key`/Nimbusfile `PIECESKEY`
  (Ed25519) verifies `pieces.sha256.sig` or refuses the build
  (`pieces.ErrSignatureInvalid`).
- **Transport trust**: plain `http://` refused by default unless
  `--pieces-insecure-http`.

## Cloud metadata endpoints (in-guest agent)

**Location**: `cmd/cnimbusagent/http.go`.

- **AWS EC2 IMDSv2**: real two-step token dance against
  `169.254.169.254` (`PUT .../api/token` with
  `X-aws-ec2-metadata-token-ttl-seconds`, then `GET .../meta-data/<path>`
  with the token header).
- **IBM Cloud VPC**: equivalent two-step dance against the same
  link-local address, bearer-token style.
- **Plain HTTP** (`http` kind): generic — any URL + custom headers,
  also covers GCE/OCI metadata conventions without dedicated code.
- **VirtualBox** (`vboxguest_linux.go`): Guest Property via the mainline
  `VBoxGuest` kernel driver, no Guest Additions needed.
- **QEMU/Proxmox** (`virtioserial.go`): virtio-console device.
- **VMware** (`vmware_linux_amd64.go`/`.s`): raw backdoor I/O-port
  protocol, linux/amd64-only.
- All fetched values capped at 1 MiB (`maxAgentResponseBytes`), written
  verbatim to `/var/run/cnimbus-kv.json`.
- **Auth**: AWS/IBM's token dance *is* the auth (link-local, instance-
  only reachability). Generic `http` kind carries whatever headers the
  Nimbusfile author supplies — cnimbus adds no scheme of its own.

## GitHub Actions (CI)

**Location**: `.github/workflows/ci.yml`.

- Three jobs on `ubuntu-latest`: fmt/vet/test + Thunder-source-sync
  check, lint, 6-way cross-compile matrix. No deploy/publish/release
  step exists today.
- **Auth**: none beyond the implicit `GITHUB_TOKEN` for checkout; no
  secrets referenced.
- Standing product decision: CI stays build-only, never boots an image
  (see project memory `feedback-ci-no-boot-tests`).
