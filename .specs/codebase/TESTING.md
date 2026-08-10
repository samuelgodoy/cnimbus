# Testing Infrastructure

## Test Frameworks

- **Unit/Integration**: stdlib `testing`, no testify/gomock. Table-driven
  is the dominant idiom (`tests := []struct{...}{...}` + `t.Run`).
- **E2E**: no separate framework — `cmd/cnimbus/build_e2e_test.go` calls
  the real `runBuild` function (the same one `cnimbus build-disk` itself
  calls) against fixture pieces and inspects the produced artifact's
  actual bytes.
- **Coverage tool**: none configured.

## Test Organization

- **Location**: always co-located `<package>/<file>_test.go`, same
  package (white-box). No separate `tests/`/`spec/` directory for unit
  tests (a top-level `test/` exists but was empty at last check).
- **Naming**: `TestXxx` describing the exact behavior, e.g.
  `TestBuildFailsWithActionableMessageOnOversizedShadowedCopy`. `EndToEnd`
  suffix for real-build tests, `Live` suffix for tests hitting a real
  external service.
- **Structure**: large files are sometimes split by concern rather than
  kept as one monolith — e.g. `frompieces_test.go` vs.
  `frompieces_tmpsize_test.go` vs. `squashfsroot_tmpdir_test.go`.

## Testing Patterns

### Unit tests
Table-driven, `t.Helper()` in shared setup, `t.Fatalf` when the test
can't meaningfully continue, `t.Errorf` when it can (so one table-driven
run reports every failing case).

### Integration/E2E tests
`build_e2e_test.go`'s `writeFixturePieces` stands in for what
`cnimbus prepare` would have produced (fake but correctly-shaped
`vmlinuz`/`busybox` bytes, real-format manifest + `pieces.sha256`).
`TestBuildDiskEndToEndFromFixturePieces`/`TestBuildRawEndToEndFromFixturePieces`
assert on real on-disk structure (ISO9660 `CD001` signature + El Torito
boot record at their fixed LBAs; GPT `EFI PART` signature + ESP GUID).
Further e2e tests lock in specific regressions end to end:
`TestBuildRespectsExplicitTmpDir`,
`TestBuildFailsWithActionableMessageOnOversizedShadowedCopy`,
`TestBuildFailsOnVGAMismatchWithPiecesProvenance`,
`TestBuildVerifiesPiecesSignatureEndToEnd`.

### Live-network tests
`internal/compileagent/verify_test.go`'s `TestVerifyKernelTarballLive`
downloads a real kernel.org release tarball (~150MB) and verifies its
real PGP signature via a live WKD fetch — no mocking, by design ("the
whole point of this feature is that it talks to the real kernel.org").
Double-gated: skipped under `testing.Short()` **and** requires
`CNIMBUS_TEST_NETWORK=1`.

## Test Execution

- CI: `go test -short ./...` on `ubuntu-latest` (native, no Docker
  needed there).
- Local, on this Windows dev machine: `go test` must run inside a
  `golang` Docker container, not natively (antimalware deletes freshly-
  compiled Go test binaries before they execute) — see project memory
  `feedback-run-go-tests-via-docker-on-windows`.
- `-short` skips the live kernel.org integration test.

## Known Test Coverage Gaps (observed, not exhaustive)

- **`internal/dockerrun/dockerrun.go`** — zero test files. Everything
  here shells out to the real `docker` CLI (argument construction, the
  volume-ownership fixup, capability list, sorted-env-var ordering) with
  no automated coverage at all.
- **`cmd/thunder/main.go`** — no co-located test; only exercised
  indirectly by the "Thunder source sync check" (verifies the embedded
  *copy* matches, not that its logic behaves correctly).
- **`internal/agentruntime/agentruntime.go`** — no test file.
- **`cmd/cnimbusagent`'s hypervisor-specific backends**
  (`vboxguest_linux.go`, `virtioserial.go`, `vmware_linux_amd64.go`/`.s`)
  — untested by anything CI runs; only `http.go` has a test file. These
  need a real hypervisor's guest-integration channel to exercise
  meaningfully.
- **Structural**: nothing in CI ever boots a produced image — every
  regression the e2e byte-structure checks don't model has always
  shipped silently until caught by hand. This is the exact gap Section
  A of `Tasks.md` (boot-validation debt) exists to close.
