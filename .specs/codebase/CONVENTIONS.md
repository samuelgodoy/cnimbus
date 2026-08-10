# Code Conventions

## Naming

**Packages**: short, lowercase, single-word, purpose-named —
`nimbusfile`, `rootfs`, `pieces`, `dockerrun`, `isoimage`, `rawimage`,
`compileagent`, `kernelinfo`, `agentruntime`. No `pkg/`/`utils/` grab bags.

**Files**: lowercase, no hyphens, named after the thing they implement —
`stage1.go`, `squashfsroot.go`, `frompieces.go`, `cpio.go`,
`atomicwrite.go`, `run_vmware.go`. Tests are always co-located
`<name>_test.go`, same package (white-box). OS-specific files use Go
build-constraint filename suffixes: `vboxguest_linux.go`/`_other.go`,
`vmware_linux_amd64.go`/`.s`/`vmware_other.go`.

**Functions**: `verbNoun` (`runBuild`, `resolveCopies`, `buildThunder`).
One canonical entry point per package: `nimbusfile.Parse`,
`pieces.Resolve`, `isoimage.Write`, `dockerrun.Run`. Private helpers are
named precisely for what they do, even if long:
`describeOversizedShadowedCopies`, `ensureVolumeOwnedByInvokingUser`.

**Types**: PascalCase nouns mirroring the domain concept across package
boundaries **on purpose**, not shared via import —
`nimbusfile.Volume`/`rootfs.Volume` are near-identical but independently
defined, with doc comments explaining why ("keeps this package usable
standalone").

**Constants**: grouped `const` blocks with one explanatory comment for
the whole block (e.g. `main.go`'s exit-code constants). A zero value is
sometimes deliberately reused as "unset" rather than adding a bool/
pointer (documented inline, e.g. `CopyOp.Chmod uint32`).

**Ticket references**: non-trivial changes carry an inline `// T<N>:`
comment explaining *why* the code looks the way it does — often
narrating a previously-tried, reverted approach. `Tasks.md` tracks these
by number.

## Error handling

Sentinel errors + `%w` wrapping + `errors.Is` matching, consistently:

```go
var ErrHashMismatch = errors.New("pieces hash verification failed")
// ...
case errors.Is(err, pieces.ErrHashMismatch):
    return exitVerificationFailure
```

Every wrap is `fmt.Errorf("...: %w", ..., sentinelErr)` with the
sentinel last. Distinct sentinels map to distinct process exit codes
(2 usage, 3 missing host dependency, 4 verification failure — never
safe to retry, 5 upstream fetch failure — safe to retry). Error strings
are long-form and actionable, often naming the fix inline.

## Adding a Nimbusfile directive

One `case "DIRECTIVE":` arm in `(*Nimbusfile).apply()`
(`internal/nimbusfile/nimbusfile.go`): validate `rest`, return a
descriptive error naming the directive on failure, assign into the
struct (append for repeatable directives). Every directive gets a
one-line entry in the file's package doc comment, which doubles as
reference documentation.

## Adding a CLI flag

Each subcommand builds its own `flag.NewFlagSet`, with long inline help
strings (often explaining *why* a default is what it is). Where a
setting exists both in the Nimbusfile and as a flag, an explicitly
*passed* flag always wins — checked via `fs.Visit` populating a
`passed map[string]bool`, never by comparing against a flag's zero
value. `main.go`'s `usage()` is a manually-maintained heredoc kept in
sync with every subcommand.

## Comments

Dense, not sparse — the most distinctive convention here. Long-form
prose explaining **why**, frequently including: the history of a bug
and a previously-tried/reverted fix, empirical verification notes
("verified empirically against a real VMware Player VM"), explicit
rejection of alternatives considered, and cross-references to other
files/functions by name. Package doc comments are substantial —
several paragraphs of design rationale, not a one-liner.

## Test naming

`TestXxx` describing the exact behavior, often naming the mechanism:
`TestBuildFailsWithActionableMessageOnOversizedShadowedCopy`. Table-
driven for multi-case behavior. `EndToEnd` suffix for tests that run a
real build via the actual CLI entry function; `Live` suffix for tests
that hit a real external network service. Every non-trivial test
carries the same `T<N>:` ticket-reference comment style as production
code.
