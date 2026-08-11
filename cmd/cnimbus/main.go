// cnimbus is a single standalone binary with two very different modes:
//
//   - `cnimbus prepare` is the only command that touches Docker: it
//     compiles the kernel + BusyBox inside a throwaway Linux container
//     (Thunder runs there -- see internal/compileagent) and exports
//     the prebuilt "pieces" (see internal/pieces).
//   - `cnimbus build-disk` never touches Docker: pure Go, it fetches those
//     pieces and assembles a bootable ISO.
//
// Splitting these into subcommands of one binary, rather than two
// separate binaries, keeps distribution simple while preserving the
// property that actually assembling an image needs nothing but `cnimbus`
// itself.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"cnimbus/internal/compileagent"
	"cnimbus/internal/dockerrun"
	"cnimbus/internal/kernelinfo"
	"cnimbus/internal/pieces"
)

// Exit codes (T50): every failure used to exit 1 regardless of cause,
// so a CI pipeline couldn't tell "kernel.org is briefly unreachable,
// worth retrying" from "a signature didn't match, never retry this".
// Usage errors (bad flags, unknown subcommand) already exit 2 via
// flag.ExitOnError / the default case below, unchanged by this table.
const (
	exitGeneric               = 1
	exitUsage                 = 2
	exitMissingHostDependency = 3
	exitVerificationFailure   = 4
	exitUpstreamFetchFailure  = 5
)

// exitCodeFor maps a failure to its exit code by matching sentinel
// errors declared in the packages that actually raise them
// (dockerrun.ErrUnavailable, compileagent.ErrVerification,
// pieces.ErrHashMismatch, kernelinfo.ErrUpstreamFetch) -- errors.Is, not
// string matching, so this keeps working through any amount of
// %w-wrapping added between the call site and here.
func exitCodeFor(err error) int {
	switch {
	case errors.Is(err, dockerrun.ErrUnavailable):
		return exitMissingHostDependency
	case errors.Is(err, compileagent.ErrVerification), errors.Is(err, pieces.ErrHashMismatch), errors.Is(err, pieces.ErrSignatureInvalid):
		return exitVerificationFailure
	case errors.Is(err, kernelinfo.ErrUpstreamFetch):
		return exitUpstreamFetchFailure
	default:
		return exitGeneric
	}
}

// version is overwritten at build time via:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// see BUILD.md's "Embedding a version string" section.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	// Only `prepare` needs a cancellable context (it's the one command
	// that runs a long-lived Docker container -- see runPrepare's own doc
	// comment). signal.NotifyContext intercepts SIGINT/SIGTERM itself,
	// which is what turns Ctrl-C during a 20-minute kernel build from an
	// immediate, deferred-functions-skipped process kill (Go's default
	// disposition) into an ordinary canceled-context error return: the
	// same defer os.RemoveAll(...) calls that already existed throughout
	// cmd/cnimbus and internal/dockerrun now actually run on this path
	// too, because the process is no longer killed out from under them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(os.Args[2:])
	case "prepare":
		err = runPrepare(ctx, os.Args[2:])
	case "build-disk":
		err = runBuild(ctx, os.Args[2:])
	case "kv-serve":
		err = runKVServe(os.Args[2:])
	case "validate":
		err = runValidate(os.Args[2:])
	case "clean":
		err = runClean(os.Args[2:])
	case "keygen":
		err = runKeygen(os.Args[2:])
	case "run":
		err = runRun(os.Args[2:])
	case "version":
		fmt.Println(version)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "cnimbus: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "cnimbus: %v\n", err)
		os.Exit(exitCodeFor(err))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cnimbus -- build a minimal Linux VM image from a Nimbusfile.

Usage:
  cnimbus init                    write an example Nimbusfile to the current directory
  cnimbus prepare [flags]         (needs Docker) compile kernel+busybox, export pieces
  cnimbus build-disk [flags]       (no Docker) fetch pieces, assemble the image
  cnimbus kv-serve [flags]        host side of AGENT: serve a JSON file's live content
  cnimbus validate [flags]        check a Nimbusfile without building anything (syntax,
                                  COPY/ADD sources exist, COPY/ADD ELF binaries match ARCH)
  cnimbus clean [flags]           remove prepare's Docker volumes/images (and optionally ./pieces)
  cnimbus keygen [flags]          generate an Ed25519 keypair for signing pieces.sha256 (see
                                  --pieces-sign-key/--pieces-verify-key below)
  cnimbus run [flags] <image>     boot a built image locally via QEMU (default), VirtualBox,
                                  VMware, or Hyper-V (--backend); prints manual QEMU
                                  instructions if qemu-system-<arch> isn't installed
  cnimbus version                 print the build version

The Nimbusfile and these flags are not two ways of doing the same thing --
they answer different questions, and complement each other:

  the Nimbusfile  describes the IMAGE: which kernel and BusyBox, which
                architecture, what goes inside it, what runs at boot.
                Commit it -- it's what makes a build reproducible.
  flags         describe THIS INVOCATION: which Nimbusfile to read, where
                pieces come from, where the output goes. Machine-specific,
                nothing you'd commit.

Where both can express the same setting (KERNEL, BUSYBOX, ARCH, VGA,
HARDBOOT), one rule applies everywhere: the Nimbusfile declares it, and a
flag actually passed on the command line overrides it -- including turning
something back off (--vga=false against a Nimbusfile saying "VGA true").

Prepare flags:
  -f <path>        Nimbusfile to read (default "Nimbusfile"; works with none present)
  --kernel <ver>   overrides KERNEL: "latest-stable", "latest-longterm", or explicit
  --busybox <ver>  overrides BUSYBOX: explicit version, or "latest"
  --arch <arch>    overrides ARCH: amd64 or arm64 (default "amd64" if neither is set)
  --vga[=false]    overrides VGA: enable a real VGA/framebuffer console for console=tty0
                    (off by default; turn on to see boot output in a GUI hypervisor's
                    own display window, e.g. VirtualBox)
  --hardboot <p>   overrides HARDBOOT: "none" (default), "eth", "wifi", or "eth+wifi"
                    -- each single value is exclusive to its own driver family;
                    "eth+wifi" is the only value that builds both
  --out <dir>      where to write vmlinuz/busybox/busybox-manifest.tsv/pieces.sha256 (default "./pieces")
  --insecure-skip-kernel-verify   skip PGP verification of the downloaded kernel tarball
                    (fetched from kernel.org, checked against known kernel.org signer
                    keys fetched live via WKD -- see internal/compileagent.VerifyKernelTarball).
                    Only for a trusted offline mirror without a matching .tar.sign.
  --pieces-sign-key <path>   path to a hex-encoded Ed25519 private key seed (see
                    "cnimbus keygen"); signs the produced pieces.sha256, writing
                    pieces.sha256.sig alongside it, so build-disk can authenticate
                    these pieces via --pieces-verify-key, not just check their
                    integrity.

Build-iso flags:
  -f <path>       Nimbusfile to read (default "Nimbusfile"; required -- it defines the image)
  --arch <arch>   overrides ARCH: which arch-namespaced pieces to assemble
  --pieces <src>  where to fetch prebuilt kernel/busybox pieces from: a local
                  directory, or an http(s):// URL prefix. Defaults to "./pieces"
                  when it exists; can also be set via the CNIMBUS_PIECES env var.
                  Verified against a pieces.sha256 alongside it when present.
  --pieces-insecure-http   allow a plain http:// --pieces source (refused by
                  default -- see internal/pieces.Resolve)
  --pieces-cache-dir <dir>   local cache for http(s) --pieces sources (default: an
                  OS-conventional per-user cache dir); avoids re-downloading
                  vmlinuz/busybox when the source's pieces.sha256 is unchanged
  --no-pieces-cache   disable the pieces cache entirely
  --pieces-verify-key <hex-pubkey>   an Ed25519 public key (see "cnimbus keygen"); if set,
                  refuses to build unless the pieces source published a pieces.sha256.sig
                  that verifies against it -- authenticity, not just the hash check above
                  (a Nimbusfile PIECESKEY line sets this too; a flag passed here wins)
  --build-arg <NAME>=<VALUE>   set an ARG directive's value; repeatable
  --no-lockfile   skip writing <output>.lock (resolved pieces/image hashes for this build)
  -o <path>       output image path (default "<hostname>.iso", or "<hostname>.img"
                  for FORMAT raw). The image *type* is the Nimbusfile's FORMAT; this
                  is only where the file lands.
  --secureboot    sign the shipped EFI-stub kernel with sbsign (F2), so a Secure
                  Boot-enabled firmware whose db carries the matching certificate
                  will load it (and refuse anything else). Needs Docker. Opt-in --
                  never added to a build that didn't ask for it, same as --uefi/HARDBOOT
  --uki           assemble+sign a Unified Kernel Image (kernel+initramfs+cmdline
                  merged into one PE, systemd-stub style) instead of two separate
                  EFI-boot-image files. Implies --secureboot. Needs Docker
  --secureboot-key/--secureboot-cert <path>   bring your own PEM-encoded RSA
                  key/X.509 cert for --secureboot/--uki (mirrors --ovmf-code/
                  --ovmf-vars); must be given together. Omit both to auto-generate
                  a keypair once under --secureboot-dir and reuse it on every later
                  build (never silently regenerated -- see "cnimbus keygen --secureboot"
                  to pre-generate one explicitly instead)
  --secureboot-dir <dir>   where the auto-generated keypair above is stored/reused
                  (default "./secureboot"); ignored when --secureboot-key/-cert are given

Validate flags:
  -f <path>       Nimbusfile to check (default "Nimbusfile")
  --arch <arch>   overrides ARCH for the ELF-architecture check on COPY/ADD binaries
  --build-arg <NAME>=<VALUE>   set an ARG directive's value; repeatable

Clean flags:
  --pieces        also remove the pieces output directory (see --pieces-dir)
  --pieces-dir <dir>   which directory --pieces removes (default "./pieces")
  --dry-run       print what would be removed without removing it

Keygen flags:
  --out <path>    where to write the hex-encoded Ed25519 private key seed
                  (default "pieces-sign-key.hex"); the matching public key is
                  only ever printed, never written to disk
  --secureboot    generate an RSA-3072 + self-signed X.509 Secure Boot signing
                  identity (F2) instead of an Ed25519 pieces-signing key
  --out-dir <dir>   (--secureboot only) directory to write secureboot-key.pem/
                  secureboot-cert.pem into (default ".")
  --common-name <name>   (--secureboot only) certificate Subject CommonName
                  (default "cnimbus")

Run flags:
  --arch <arch>   image architecture: amd64 or arm64 (default "amd64")
  --uefi          boot via UEFI/OVMF instead of BIOS (amd64 only)
  --ovmf-code/--ovmf-vars <path>   OVMF firmware paths (auto-detected if omitted)
  --mem <MB>      guest RAM (default 512)
  --hostfwd <host>:<guest>   TCP port to forward (default "8080:8080")
  --backend <name>   "qemu" (default), "vbox", "vmware", or "hyperv"
  --vm-name <name>   VM name to create (default "cnimbus-run")
  --vm-keep       don't delete the VM after this command exits

KV-serve flags:
  --file <path>   JSON file to serve (default "kv.json"); edit and save it any
                  time, no restart needed -- guests running AGENT pick up the
                  change on their next poll
  --addr <addr>   address to listen on (default ":9999")

Everything else about the image is Nimbusfile-only, by design: it describes
the artifact, not how you invoked the build. Run "cnimbus init" to see every
directive with commented examples, or the README for the full reference --
HOSTNAME, DHCP/IP, NTP, FORMAT (iso/raw), USER, VOLUME, ENV, FIREWALL/FIREWALL6,
COPY/ADD, ENTRYPOINT/CMD, SERVICE, AGENT, PIECESKEY all live there, not as flags.

Exit codes:
  1   generic failure
  2   usage error (bad flags, unknown subcommand)
  3   missing host dependency (e.g. docker not installed/reachable)
  4   verification/integrity failure (PGP signature or pieces hash mismatch --
      never safe to retry as-is)
  5   upstream fetch failure (e.g. kernel.org unreachable -- safe to retry,
      unlike 4)
`)
}
