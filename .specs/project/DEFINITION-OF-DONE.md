# Definition of Done

**Baseline:** 2026-08-06

A change is *done* when its evidence gate is satisfied — not when it
compiles, and not when its unit tests pass. This document exists because
this project has repeatedly merged correct-looking, well-tested changes
that had never been executed, and then discovered on the next real boot
that they were wrong.

## Change classes and their gates

Classify the change by **what can silently break**, not by diff size.

### Class A — Pure host-side logic
*Parsers, flag handling, error classification, argv construction, file
writers that never reach a guest.*

Gate:
- [ ] `go build ./...` and `go vet ./...` clean
- [ ] `go test ./...` green (in Docker on Windows — see STACK.md)
- [ ] New behavior has a test that fails without the change
- [ ] Malformed input produces an actionable error naming the directive/flag

**Evidence level: E2.** No real execution required — nothing here can
fail in a way a test can't model.

### Class B — Build orchestration
*Anything touching `internal/dockerrun`, the builder Dockerfile, Thunder,
or the `prepare` pipeline.*

Gate: everything in Class A, plus:
- [ ] A **real `cnimbus prepare` run** completed, exit 0
- [ ] The run log excerpt showing the changed behavior is recorded in the ticket
- [ ] `pieces.json` inspected and correct for the change

**Evidence level: E3.** Non-negotiable. `internal/dockerrun` has zero
test coverage and every bug found in it so far was found this way.

### Class C — Kernel configuration
*Any `.fragment` edit, any `verifyFragmentsApplied` change.*

Gate: everything in Class B, plus:
- [ ] Every requested symbol confirmed present **with the requested value**
      in the resolved `.config` after `olddefconfig`
- [ ] For a new symbol: its full dependency chain was traced, including
      enclosing `menuconfig`/`if` blocks, before the first build attempt
- [ ] Cross-arch impact stated explicitly: does this symbol exist on both
      amd64 and arm64, and is it in the right fragment?

**Evidence level: E3.** Budget for at least one failed build — the
project's history says the first attempt is wrong more often than right.

### Class D — Guest boot path
*Stage 1 `/init`, `rcS`, inittab, supervisor scripts, firewall script,
mounts, networking, anything that executes inside the guest.*

Gate: everything in Class C, plus:
- [ ] A **real boot** on at least one hypervisor, with a captured serial log
- [ ] The specific log line proving the new behavior is quoted in the ticket
- [ ] A negative control where meaningful (what it looked like before, or
      what the failure case produces)

**Evidence level: E4.** This is the class that round 2 skipped ~40 times.

### Class E — Cross-platform claim
*Any change whose correctness differs per host OS, guest arch, or
hypervisor — including anything in the `run` backends.*

Gate: everything in Class D, plus:
- [ ] Demonstrated on **every platform the change claims to support**
- [ ] Platforms *not* covered are explicitly named as unverified in the
      ticket and in README's support matrix

**Evidence level: E5.** A change that touches four backends and is
tested on one is not done — it is one-quarter done, and the other three
quarters have historically been broken (see REQUIREMENTS traceability
for T20, T21, T95).

### Class F — Secret handling
*Anything that introduces, stores, transports, or logs a credential —
AGENT tokens, the new WiFi PSK, signing keys.*

Gate: everything in Class D, plus:
- [ ] The secret's on-disk mode verified **in the produced image**, not
      in the source that generates it
- [ ] Confirmed absent from: build logs, `/proc/<pid>/cmdline`, and any
      world-readable path
- [ ] A documented path exists to supply it *without* committing it to
      the Nimbusfile
- [ ] Rotation/replacement is possible without rebuilding, or the
      limitation is documented

**Evidence level: E4** with the checks performed against the real image.

## The honesty rule

If a gate cannot be satisfied, the change may still merge — but the
ticket must state **exactly which gate is unmet and why**, in the same
place someone would look to find out whether it works.

"Implemented, not boot-tested" is an acceptable outcome. "Implemented"
with no qualifier, when it was never booted, is not — that is the
failure mode this whole document exists to prevent.

Reference phrasing this project already uses well:

> **Falta**: not exercised end-to-end (no real Ctrl-C during an actual
> `prepare` run was performed in this session) — verified at the
> compile/type level only.

That sentence is worth more than a green checkmark.

## Applying a gate to an existing, already-merged change

The v1.0 backlog's Section A exists precisely to retro-apply Class D and
E gates to work that merged under Class A discipline. Retro-verification
is normal and expected; the failure is leaving it untracked.
