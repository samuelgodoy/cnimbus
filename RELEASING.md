# Releasing

cNimbus uses [SemVer](https://semver.org/): `vMAJOR.MINOR.PATCH` (e.g.
`v0.3.1`), with an optional pre-release suffix (`v0.3.1-rc1`). While the
project is pre-1.0 (`v0.x.y`), the SemVer spec itself says anything can
change between minor versions -- so within `v0.x`, treat MINOR the way
you'd otherwise treat MAJOR (a Nimbusfile-breaking or pieces.json-schema-
breaking change), and PATCH for everything else. Once `v1.0.0` ships,
switch to the standard reading: MAJOR for a breaking change to the
Nimbusfile directive set, the `pieces.json` schema, or a documented CLI
flag; MINOR for a new directive, flag, or backend added without breaking
an existing one; PATCH for a bug fix.

**What "release" means here.** Only the `cnimbus` CLI binary itself is
released -- see [`.specs/project/STATE.md`](.specs/project/STATE.md)
AD-003: cNimbus stays distroless and self-build-only, and that decision
covers "pieces" (kernel/BusyBox/iptables builds), not the CLI tool a user
runs to produce their own. Publishing `cnimbus` binaries is the normal
way any Go CLI ships.

## Cutting a release

1. Update [`CHANGELOG.md`](CHANGELOG.md): rename `## [Unreleased]` to
   `## [vX.Y.Z] - YYYY-MM-DD` and start a fresh empty `## [Unreleased]`
   above it.
2. Commit that change on `main`.
3. Tag it and push the tag:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

4. Pushing the tag triggers
   [`.github/workflows/release.yml`](.github/workflows/release.yml): it
   cross-compiles `cnimbus` for the same targets
   [`ci.yml`](.github/workflows/ci.yml)'s `build-all-targets` job already
   validates build for -- as of 2026-08-07, seven cells: windows/amd64,
   windows/arm64, linux/amd64, linux/arm64, linux/riscv64, darwin/amd64,
   darwin/arm64 (see `.specs/project/STATE.md` AD-006 and AD-031 --
   windows/riscv64 and darwin/riscv64 are excluded since they aren't real
   Go ports). The two workflows' matrices are kept in sync by hand; a
   target added to one must be added to the other. Each build stamps
   `main.version` with the tag itself via `-ldflags -X`, and the workflow
   publishes a GitHub Release with each platform's archive attached
   (`.zip` for Windows, `.tar.gz` otherwise), alongside `LICENSE` and
   `NOTICE`.
5. Verify the release: the downloaded binary (named
   `cnimbus-<arch>-<os>[.exe]`, e.g. `cnimbus-amd64-linux`) should print
   exactly the tag you pushed when run with `version`.

There is deliberately no automation for steps 1-3 -- deciding *when* to
cut a release, and writing the changelog entry a human will read, aren't
things to automate away.

## Pre-flight checklist for cutting `v0.1.0` (the first real tag)

Nothing below runs automatically -- it's the exact sequence to run by
hand, once, when actually ready to cut the first real release. No step
here pushes anything on its own; each command is copy-pasteable but
deliberate.

1. Confirm `main` is clean and CI is green on its latest commit:

   ```bash
   git status
   git log --oneline -1
   gh run list --branch main --limit 1
   ```

2. Update [`CHANGELOG.md`](CHANGELOG.md): rename `## [Unreleased]` to
   `## [v0.1.0] - YYYY-MM-DD` (today's date) and add a fresh empty
   `## [Unreleased]` above it. Commit that on `main`:

   ```bash
   git add CHANGELOG.md
   git commit -m "Prepare CHANGELOG for v0.1.0"
   git push origin main
   ```

3. Tag and push the tag -- this is the step that actually triggers
   `release.yml` and publishes a public GitHub Release:

   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

4. Verify afterward:

   ```bash
   gh run list --workflow=release.yml --limit 1
   gh run watch   # follow the run started by the tag push
   gh release view v0.1.0
   ```

   Confirm the run succeeded and the release page has all seven archives
   (`cnimbus-amd64-windows-v0.1.0.zip`, `cnimbus-arm64-windows-v0.1.0.zip`,
   `cnimbus-amd64-linux-v0.1.0.tar.gz`, `cnimbus-arm64-linux-v0.1.0.tar.gz`,
   `cnimbus-riscv64-linux-v0.1.0.tar.gz`,
   `cnimbus-amd64-darwin-v0.1.0.tar.gz`,
   `cnimbus-arm64-darwin-v0.1.0.tar.gz`), each containing `LICENSE`,
   `NOTICE`, and a binary named `cnimbus-<arch>-<os>[.exe]`. Download at
   least one archive and run that binary's `version` subcommand to
   confirm it prints exactly `v0.1.0`.

## If a release build fails

The tag has already been pushed and the release title is already claimed
by the failed run. Delete the GitHub Release draft (if one was partially
created) and the tag (`git push --delete origin vX.Y.Z` -- ask before
force-pushing or deleting anything shared if you're not the one who
pushed it), fix the issue, and re-tag from a clean `main` once CI is
green there.
