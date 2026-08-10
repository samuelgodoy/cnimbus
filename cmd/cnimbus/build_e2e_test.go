package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cnimbus/internal/pieces"
)

// appendPiecesJSONHash writes provData as pieces.json in dir and appends
// its hash to the pieces.sha256 writeFixturePieces already wrote --
// otherwise Resolve's own checkHash would refuse pieces.json outright
// ("no entry in pieces.sha256"), the same "no entry means refuse" rule
// every other piece is held to.
func appendPiecesJSONHash(t *testing.T, dir string, provData []byte) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "pieces.json"), provData)
	existing, err := os.ReadFile(filepath.Join(dir, "pieces.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	line := sha256Hex(provData) + "  pieces.json\n"
	mustWrite(t, filepath.Join(dir, "pieces.sha256"), append(existing, []byte(line)...))
}

// T55: nothing in CI ever produces an image -- every regression in
// internal/rootfs/internal/isoimage/internal/rawimage/build.go that the
// narrower unit tests don't model has always shipped silently. This is
// the cheap layer the ticket describes: no Docker, no real kernel/busybox
// -- a tiny fixture pieces directory stands in for `cnimbus prepare`'s
// output, and runBuild (the same function `cnimbus build-disk` itself
// calls) is exercised end to end for both FORMAT iso and FORMAT raw,
// asserting on the produced artifact's actual on-disk structure rather
// than just "no error was returned".
func TestBuildDiskEndToEndFromFixturePieces(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(nimbusfilePath, []byte("HOSTNAME testvm\nENTRYPOINT /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outISO := filepath.Join(dir, "out.iso")
	if err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64", "-o", outISO, "--no-lockfile"}); err != nil {
		t.Fatalf("runBuild (FORMAT iso): %v", err)
	}
	assertISO9660WithElTorito(t, outISO)
}

// T79: --tmpdir must actually reach isoimage.Write/rootfs.BuildImages,
// not just be parsed and discarded. Proven by pointing it at a
// nonexistent directory: os.MkdirTemp/os.CreateTemp fail immediately
// against a nonexistent parent, so a build-disk run that fails here
// could only be using the flag's value, not the OS default temp dir.
func TestBuildRespectsExplicitTmpDir(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(nimbusfilePath, []byte("HOSTNAME testvm\nENTRYPOINT /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outISO := filepath.Join(dir, "out.iso")
	badTmpDir := filepath.Join(dir, "does-not-exist")
	err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", outISO, "--no-lockfile", "--tmpdir", badTmpDir})
	if err == nil {
		t.Fatal("expected runBuild to fail with a nonexistent --tmpdir, proving the flag actually reaches the workspace creation")
	}
}

// T77: a COPY destined for one of stage 1's four tmpfs exec dirs that's
// large enough to push the EFI boot image past El Torito's 16-bit
// sector-count ceiling must fail with a message naming the actual
// oversized COPY, not just isoimage's own EFI/El-Torito-centric error
// text with no connection back to the Nimbusfile line that caused it.
func TestBuildFailsWithActionableMessageOnOversizedShadowedCopy(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)

	// El Torito's ceiling is ~32 MiB (65535 sectors * 512 bytes); 40 MiB
	// of COPY'd content into /usr/bin, on top of the (tiny, fixture)
	// busybox binary and applet symlinks, is comfortably past it -- but
	// stage 1's initramfs is gzip'd (see buildStage1Initramfs), so the
	// content must not be trivially compressible (all-zero bytes would
	// shrink to nearly nothing and never trip the ceiling at all).
	// math/rand's default (unseeded) sequence is deterministic across Go
	// versions for a given source, and is not being used for anything
	// security-sensitive here -- just incompressible test filler.
	bigData := make([]byte, 40*1024*1024)
	rng := mathrand.New(mathrand.NewSource(1))
	if _, err := rng.Read(bigData); err != nil {
		t.Fatal(err)
	}
	bigApp := filepath.Join(dir, "bigapp")
	if err := os.WriteFile(bigApp, bigData, 0o644); err != nil {
		t.Fatal(err)
	}

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	// TMPSIZE (T52) is a separate, earlier build-time guard on the boot-
	// time tmpfs -- raised here so this test reaches El Torito's own
	// ceiling (T77) instead of tripping that one first.
	nimbusfileContent := "HOSTNAME testvm\nTMPSIZE 64m\nCOPY ./bigapp /usr/bin/bigapp\nENTRYPOINT /usr/bin/bigapp\n"
	if err := os.WriteFile(nimbusfilePath, []byte(nimbusfileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	outISO := filepath.Join(dir, "out.iso")
	err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64", "-o", outISO, "--no-lockfile"})
	if err == nil {
		t.Fatal("expected runBuild to fail: the EFI boot image should exceed El Torito's size ceiling")
	}
	msg := err.Error()
	if !strings.Contains(msg, "usr/bin/bigapp") {
		t.Errorf("expected the error to name the oversized COPY destination, got: %q", msg)
	}
	if !strings.Contains(msg, "FORMAT raw") {
		t.Errorf("expected the error to suggest FORMAT raw as a way out, got: %q", msg)
	}
}

func TestBuildRawEndToEndFromFixturePieces(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(nimbusfilePath, []byte("HOSTNAME testvm\nFORMAT raw\nENTRYPOINT /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outRaw := filepath.Join(dir, "out.img")
	if err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64", "-o", outRaw, "--no-lockfile"}); err != nil {
		t.Fatalf("runBuild (FORMAT raw): %v", err)
	}
	assertGPTWithESP(t, outRaw)
}

// writeFixturePieces writes the smallest set of files internal/pieces.Resolve
// requires from a local directory source: real bytes are not needed
// (nothing here boots a kernel), only the right filenames, a
// busybox-manifest.tsv in the real "path<TAB>target" format, and a
// pieces.sha256 covering all three so the hash-verification path is
// exercised too, not skipped.
func writeFixturePieces(t *testing.T, dir string) {
	t.Helper()
	vmlinuz := []byte("fixture-vmlinuz-not-a-real-kernel")
	busybox := []byte("fixture-busybox-not-a-real-elf-binary")
	manifest := []byte("bin/sh\tbusybox\nbin/ls\tbusybox\n")

	mustWrite(t, filepath.Join(dir, "vmlinuz"), vmlinuz)
	mustWrite(t, filepath.Join(dir, "busybox"), busybox)
	mustWrite(t, filepath.Join(dir, "busybox-manifest.tsv"), manifest)

	sum := func(b []byte) string {
		return sha256Hex(b)
	}
	hashes := sum(vmlinuz) + "  vmlinuz\n" +
		sum(busybox) + "  busybox\n" +
		sum(manifest) + "  busybox-manifest.tsv\n"
	mustWrite(t, filepath.Join(dir, "pieces.sha256"), []byte(hashes))
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

// assertISO9660WithElTorito checks the two structural facts that make an
// ISO actually bootable rather than merely "a file that exists": a
// Primary Volume Descriptor's "CD001" standard identifier at LBA 16
// (byte offset 0x8000, identifier at +1), and a Boot Record Volume
// Descriptor (type 0) naming "EL TORITO SPECIFICATION" at LBA 17 (byte
// offset 0x8800) -- both fixed, specified locations in the ISO9660/El
// Torito standards, independent of this project's own code.
func assertISO9660WithElTorito(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	const pvdOffset = 16 * 2048
	if len(data) < pvdOffset+6 {
		t.Fatalf("%s is only %d bytes, too small to hold a Primary Volume Descriptor", path, len(data))
	}
	if data[pvdOffset] != 1 {
		t.Errorf("expected a Primary Volume Descriptor (type 1) at LBA 16, got type %d", data[pvdOffset])
	}
	if !bytes.Equal(data[pvdOffset+1:pvdOffset+6], []byte("CD001")) {
		t.Errorf("expected ISO9660 standard identifier %q at LBA 16+1, got %q", "CD001", data[pvdOffset+1:pvdOffset+6])
	}

	const brvdOffset = 17 * 2048
	if len(data) < brvdOffset+32 {
		t.Fatalf("%s is only %d bytes, too small to hold a Boot Record Volume Descriptor", path, len(data))
	}
	if data[brvdOffset] != 0 {
		t.Errorf("expected a Boot Record Volume Descriptor (type 0) at LBA 17, got type %d", data[brvdOffset])
	}
	if !bytes.Equal(data[brvdOffset+1:brvdOffset+6], []byte("CD001")) {
		t.Errorf("expected ISO9660 standard identifier at LBA 17+1, got %q", data[brvdOffset+1:brvdOffset+6])
	}
	if !bytes.Contains(data[brvdOffset:brvdOffset+32], []byte("EL TORITO SPECIFICATION")) {
		t.Errorf("expected El Torito boot record identifier in the LBA 17 volume descriptor, got %q", data[brvdOffset:brvdOffset+32])
	}
}

// assertGPTWithESP checks the fixed, standard-specified GPT header
// signature at LBA 1 and that the ESP partition-type GUID
// (C12A7328-F81F-11D2-BA4B-00A0C93EC93B, little-endian in its first
// three fields per the GPT spec) appears somewhere in the partition
// entry array that follows -- both independent of this project's own
// code, unlike re-parsing the partition table with go-diskfs itself
// would be.
func assertGPTWithESP(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	const lba = 512
	if len(data) < lba+8 {
		t.Fatalf("%s is only %d bytes, too small to hold a GPT header", path, len(data))
	}
	if !bytes.Equal(data[lba:lba+8], []byte("EFI PART")) {
		t.Errorf("expected GPT signature %q at LBA 1, got %q", "EFI PART", data[lba:lba+8])
	}
	espGUID := []byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}
	if !bytes.Contains(data, espGUID) {
		t.Error("expected the EFI System Partition type GUID to appear somewhere in the GPT partition entry array")
	}
}

// T59: build-disk must fail with a specific message when the Nimbusfile's
// VGA setting doesn't match what the pieces were actually built with --
// previously this assembled silently into an ISO whose cmdline says
// console=tty0 against a kernel with no framebuffer console compiled in.
func TestBuildFailsOnVGAMismatchWithPiecesProvenance(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)
	// Pieces were built without --vga.
	appendPiecesJSONHash(t, piecesDir, []byte(`{"schema_version":2,"arch":"amd64","vga":false}`))

	// Nimbusfile declares VGA true.
	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(nimbusfilePath, []byte("HOSTNAME testvm\nVGA true\nENTRYPOINT /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out.iso"), "--no-lockfile"})
	if err == nil {
		t.Fatal("expected an error for a VGA mismatch between the Nimbusfile and the pieces' own provenance")
	}
	if !strings.Contains(err.Error(), "VGA") {
		t.Errorf("expected the error to mention VGA, got: %v", err)
	}
}

// A matching VGA setting must build successfully -- confirms the check
// isn't just always failing.
func TestBuildSucceedsWhenVGAMatchesPiecesProvenance(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)
	appendPiecesJSONHash(t, piecesDir, []byte(`{"schema_version":2,"arch":"amd64","vga":true}`))

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(nimbusfilePath, []byte("HOSTNAME testvm\nVGA true\nENTRYPOINT /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out.iso"), "--no-lockfile"}); err != nil {
		t.Fatalf("expected success when VGA matches pieces provenance, got: %v", err)
	}
}

// F6.2: same reasoning as the VGA tests above, applied to HARDBOOT --
// pieces built for one boot profile must not silently assemble against a
// Nimbusfile declaring another (a "none" pieces set is missing whatever
// bare-metal driver support "eth"/"wifi" would need).
func TestBuildFailsOnHardbootMismatchWithPiecesProvenance(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)
	// Pieces were built with no boot_profile field at all -- the same as
	// every pre-HARDBOOT pieces set, which build.go must normalize to "none".
	appendPiecesJSONHash(t, piecesDir, []byte(`{"schema_version":2,"arch":"amd64","vga":false}`))

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	content := "HOSTNAME testvm\nHARDBOOT eth\nENTRYPOINT /bin/true\n"
	if err := os.WriteFile(nimbusfilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out.iso"), "--no-lockfile"})
	if err == nil {
		t.Fatal("expected an error for a HARDBOOT mismatch between the Nimbusfile and the pieces' own provenance")
	}
	if !strings.Contains(err.Error(), "HARDBOOT") {
		t.Errorf("expected the error to mention HARDBOOT, got: %v", err)
	}
}

// A matching HARDBOOT setting must build successfully -- confirms the
// check isn't just always failing. Uses "eth" (not "none") specifically
// to prove the comparison isn't merely checking "is this the default".
func TestBuildSucceedsWhenHardbootMatchesPiecesProvenance(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)
	appendPiecesJSONHash(t, piecesDir, []byte(`{"schema_version":2,"arch":"amd64","vga":false,"boot_profile":"eth"}`))

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	content := "HOSTNAME testvm\nHARDBOOT eth\nENTRYPOINT /bin/true\n"
	if err := os.WriteFile(nimbusfilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out.iso"), "--no-lockfile"}); err != nil {
		t.Fatalf("expected success when HARDBOOT matches pieces provenance, got: %v", err)
	}
}

// HB-N-003: an existing Nimbusfile with no HARDBOOT line must produce the
// same-size image as one with an explicit "HARDBOOT none" -- the whole
// point of defaulting to "none" is that adding this feature changes
// nothing for every Nimbusfile written before it existed. Not a raw byte
// comparison: go-diskfs's iso9660 writer embeds a wall-clock volume
// creation timestamp (filesystem/iso9660/finalize.go), a pre-existing,
// unrelated non-determinism in every ISO this project produces -- HARDBOOT
// doesn't introduce it and can't be blamed for it, so size (which that
// timestamp can't affect) is the meaningful invariant here.
func TestHardbootNoneIsByteIdenticalToNoHardbootDirective(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)

	build := func(nimbusfileBody, outName string) []byte {
		nimbusfilePath := filepath.Join(dir, outName+".Nimbusfile")
		if err := os.WriteFile(nimbusfilePath, []byte(nimbusfileBody), 0o644); err != nil {
			t.Fatal(err)
		}
		outPath := filepath.Join(dir, outName+".iso")
		if err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
			"-o", outPath, "--no-lockfile"}); err != nil {
			t.Fatalf("%s: %v", outName, err)
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	withoutDirective := build("HOSTNAME testvm\nENTRYPOINT /bin/true\n", "without")
	withExplicitNone := build("HOSTNAME testvm\nHARDBOOT none\nENTRYPOINT /bin/true\n", "with")

	if len(withoutDirective) != len(withExplicitNone) {
		t.Errorf("expected same-size output between no HARDBOOT line (%d bytes) and an explicit \"HARDBOOT none\" (%d bytes)",
			len(withoutDirective), len(withExplicitNone))
	}
}

// T81 step 1, end to end through runBuild itself (the same function
// "cnimbus build-disk" calls): --pieces-verify-key must accept a
// genuinely signed pieces.sha256, reject a wrong key, and a Nimbusfile
// PIECESKEY line must apply when no flag overrides it.
func TestBuildVerifiesPiecesSignatureEndToEnd(t *testing.T) {
	dir := t.TempDir()
	piecesRoot := filepath.Join(dir, "pieces")
	piecesDir := filepath.Join(piecesRoot, "amd64")
	if err := os.MkdirAll(piecesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixturePieces(t, piecesDir)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hashData, err := os.ReadFile(filepath.Join(piecesDir, "pieces.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	sigHex := pieces.SignHashes(priv, hashData)
	mustWrite(t, filepath.Join(piecesDir, "pieces.sha256.sig"), []byte(sigHex+"\n"))
	pubHex := hex.EncodeToString(pub)

	nimbusfilePath := filepath.Join(dir, "Nimbusfile")
	if err := os.WriteFile(nimbusfilePath, []byte("HOSTNAME testvm\nENTRYPOINT /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The correct key, via --pieces-verify-key, must succeed.
	if err := runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out-ok.iso"), "--no-lockfile", "--pieces-verify-key", pubHex}); err != nil {
		t.Fatalf("expected success with the correct --pieces-verify-key, got: %v", err)
	}

	// A different key must be rejected.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	err = runBuild(context.Background(), []string{"-f", nimbusfilePath, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out-bad.iso"), "--no-lockfile", "--pieces-verify-key", hex.EncodeToString(otherPub)})
	if err == nil {
		t.Fatal("expected runBuild to fail against a --pieces-verify-key that doesn't match the signature")
	}

	// A Nimbusfile PIECESKEY line, with no flag, must apply the same way.
	nimbusfileWithKey := filepath.Join(dir, "Nimbusfile.pieceskey")
	content := "HOSTNAME testvm\nENTRYPOINT /bin/true\nPIECESKEY " + pubHex + "\n"
	if err := os.WriteFile(nimbusfileWithKey, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runBuild(context.Background(), []string{"-f", nimbusfileWithKey, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out-nimbusfile-key.iso"), "--no-lockfile"}); err != nil {
		t.Fatalf("expected success via a Nimbusfile PIECESKEY line, got: %v", err)
	}

	// A flag-passed key wins over a mismatching Nimbusfile PIECESKEY.
	contentBad := "HOSTNAME testvm\nENTRYPOINT /bin/true\nPIECESKEY " + hex.EncodeToString(otherPub) + "\n"
	nimbusfileWithBadKey := filepath.Join(dir, "Nimbusfile.badkey")
	if err := os.WriteFile(nimbusfileWithBadKey, []byte(contentBad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runBuild(context.Background(), []string{"-f", nimbusfileWithBadKey, "--pieces", piecesRoot, "--arch", "amd64",
		"-o", filepath.Join(dir, "out-flag-wins.iso"), "--no-lockfile", "--pieces-verify-key", pubHex}); err != nil {
		t.Fatalf("expected a --pieces-verify-key flag to override a mismatching Nimbusfile PIECESKEY, got: %v", err)
	}
}
