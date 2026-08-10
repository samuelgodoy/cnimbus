# Codebase Concerns

Genuinely open risks and fragile areas as of today -- not a running log
of concerns already resolved (see `.specs/project/STATE.md` for the
decision history behind anything that used to be listed here).

## Fragile areas

**Kconfig fragment additions** (`internal/assets/data/kconfig/*.fragment`):
- Symbol dependency chains are frequently invisible from a symbol's own
  Kconfig stanza (a `depends on` line can look satisfied while an
  enclosing `menuconfig ... if ... endif` block adds an invisible
  requirement). `olddefconfig`/`allnoconfig` drops an unsatisfied
  symbol silently rather than failing.
- Mitigation in place: `verifyFragmentsApplied`
  (`internal/compileagent/kernel.go`) checks presence *and* value after
  `olddefconfig`, and fails the build by name if anything requested
  didn't survive.
- Any new fragment change still needs a real `cnimbus prepare --arch
  <arch>` run before it's considered done -- a first attempt at a new
  symbol is wrong more often than not.

**`internal/dockerrun`** (`dockerrun.go`): zero automated test coverage
(see TESTING.md). Every change here (argv construction, capability
list, volume-ownership fixup) is validated by hand against a real
Docker daemon, not `go test`.

## Genuinely open gaps

- **WiFi real-radio association** (`HARDBOOT wifi`) is implemented and
  verified by direct image inspection, but has never associated with a
  real access point -- no WiFi hardware available in this dev
  environment.
- **`riscv64` as a Nimbusfile guest architecture.** The CLI itself
  cross-compiles and runs on riscv64; `prepare`/`build-disk` still hard-
  reject any `ARCH` other than amd64/arm64. Adding it is a materially
  larger change than the CLI's own riscv64 support (new kconfig
  fragments, kernel/BusyBox piece selection) -- not scoped yet.
- **macOS and Windows-on-Arm host validation** for the CLI binary itself
  -- cross-compiles clean, not yet run on real hardware of either kind.

---
_Update this file when a concern is genuinely resolved (remove it) or a
new one is found -- not on a schedule._
