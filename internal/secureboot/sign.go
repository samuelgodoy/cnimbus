package secureboot

import (
	"context"
)

// SignPE signs pe (a PE32+/PE32+ EFI application -- either a raw
// EFI-stub vmlinuz, or an already-assembled UKI) with kp, returning
// the signed bytes. This is the direct EFI-stub-kernel signing step F2
// asks for: a real bzImage/Image built with CONFIG_EFI_STUB=y is
// itself a valid PE32+ application, so no UKI assembly is required
// just to make it Secure-Boot-signable.
//
// AD-042: this used to shell out to `sbsign` inside a throwaway Docker
// image (see git history before AD-042 for the removed
// internal/assets/data/Dockerfile.secureboot and dockerrun-based
// runInSigner) -- a real violation of `cnimbus build-disk`'s own
// documented "never touches Docker, never touches a compiler" (see
// README.md) invariant. Signing is now done entirely in Go: see
// authenticode.go's signPE for the Authenticode/PKCS#7 construction,
// and pecoff.go for the raw PE-byte manipulation it and
// BuildAndSignUKI below both build on. ctx is accepted (and, today,
// unused) purely so this signature stays compatible with its previous
// Docker-backed shape and any future caller that wants a
// context.Context to plumb through -- pure-Go ASN.1/RSA work here has
// nothing to cancel.
func SignPE(_ context.Context, pe []byte, kp Keypair) ([]byte, error) {
	return signPE(pe, kp)
}

// BuildAndSignUKI assembles a Unified Kernel Image from vmlinuz
// (already CONFIG_EFI_STUB=y, itself a valid PE application),
// initramfs (stage-1, see internal/rootfs.BuildImages's Stage1), and
// cmdline (the kernel command line -- see BuildAndSignUKI's own doc
// comment in cmd/cnimbus/build.go for why this project's
// CONFIG_CMDLINE_OVERRIDE=y makes this section informational rather
// than load-bearing for THIS kernel config specifically), then signs
// the result with kp.
//
// AD-042: section assembly used to be `objcopy --add-section` inside
// the same removed Docker signer image SignPE's doc comment above
// describes; it's now appendSection (pecoff.go), a hand-rolled
// PE-section-appender built directly against the public PE/COFF
// spec. Section VMAs/ordering/skip-if-empty behavior are unchanged
// from the Docker-based implementation -- see the constants below and
// their doc comment, carried over verbatim from the real objcopy bugs
// (overlapping VMAs, a silently-dropped empty .cmdline section) F2
// found and fixed against a real prepared amd64 kernel; those fixes
// are encoded here as appendSection's own explicit-VMA-required,
// skip-on-empty-input behavior instead of objcopy flags.
//
// Section VMAs are explicit fixed constants (cmdlineVMA/initrdVMA
// below), NOT automatic placement -- a first version of the
// Docker/objcopy implementation this replaced omitted
// --change-section-vma entirely, on the (wrong) assumption that plain
// `--add-section` places each new section after the existing ones. A
// real run instead produced `.initrd` at VMA 0, directly overlapping
// vmlinuz's own `.setup` section at VMA 0x1000 (confirmed with
// `objdump -h` against the real output -- see F2's Tasks.md entry for
// the exact before/after section table) -- objcopy's own default for
// a brand-new section is VMA 0, full stop, not "wherever the previous
// highest section ends". The fix places new sections at fixed,
// generously-spaced high addresses (16MiB and 64MiB) that can't
// collide with a bzImage's own sections, which this project's real
// builds top out well under 4MiB into (verified against the same real
// objdump run: .setup/.text/.data all land under VMA 0x360000).
const (
	cmdlineVMA = 0x1000000 // 16MiB
	initrdVMA  = 0x4000000 // 64MiB -- ample headroom past .cmdline for any real initramfs size
)

func BuildAndSignUKI(_ context.Context, vmlinuz, initramfs []byte, cmdline string, kp Keypair) ([]byte, error) {
	stub := vmlinuz
	var err error

	// A zero-length section is a real objcopy no-op (confirmed via a
	// real `objdump -h`: the section doesn't appear in the output at
	// all when its input file is empty) -- appendSection reproduces
	// that exact behavior by having this caller skip the step outright
	// rather than ever calling it with empty data, so `.cmdline` is
	// either a real, present section or genuinely absent, never a
	// silently-dropped one. cnimbus's own build-disk caller always
	// passes cmdline="" today (see this function's own doc comment:
	// CONFIG_CMDLINE_OVERRIDE=y makes this section inert for this
	// kernel config regardless), so this branch exists for any
	// future/other caller that does pass a real one.
	if cmdline != "" {
		stub, err = appendSection(stub, ".cmdline", []byte(cmdline), cmdlineVMA)
		if err != nil {
			return nil, err
		}
	}

	stub, err = appendSection(stub, ".initrd", initramfs, initrdVMA)
	if err != nil {
		return nil, err
	}

	return signPE(stub, kp)
}
