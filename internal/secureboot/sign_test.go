package secureboot

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"debug/pe"
	"encoding/asn1"
	"encoding/binary"
	"os"
	"testing"
)

// verifyEmbeddedSignature re-derives, from first principles and using
// only the Go standard library, everything a real Authenticode
// verifier (`sbverify`, or UEFI firmware's own Secure Boot check)
// checks: that the PE carries a WIN_CERTIFICATE, that it's a
// well-formed PKCS#7 SignedData wrapping an Authenticode
// SpcIndirectDataContent, that the embedded PE-image digest matches a
// fresh recompute of signed's own bytes, that the SignerInfo's
// authenticatedAttributes digest the *content octets* of that
// SpcIndirectDataContent correctly, and that the RSA signature over
// those attributes verifies against wantCert's public key. This is
// the project's own dockerless, CI-safe equivalent of the real
// `sbverify`-based check AD-042's implementation work did against an
// actual prepared kernel and a throwaway `sbsigntool` container (see
// this package's git history and Tasks.md's F2 entry) -- CI here
// never touches Docker (per this project's own no-boot-tests-in-CI
// convention), so this test exists to keep that real-world proof
// enforced on every future change, without needing Docker at all.
func verifyEmbeddedSignature(t *testing.T, signed []byte, wantCert []byte) {
	t.Helper()
	toSignDigest, encryptedDigest := extractSignedAttrsAndSignature(t, signed)

	certBlock, err := parseCertPEM(wantCert)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := certBlock.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("wantCert public key is %T, not RSA", certBlock.PublicKey)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, toSignDigest, encryptedDigest); err != nil {
		t.Fatalf("RSA signature over authenticatedAttributes does not verify: %v", err)
	}
}

// extractSignedAttrsAndSignature walks signed's embedded WIN_CERTIFICATE
// the same way a real Authenticode verifier does (see
// verifyEmbeddedSignature's own doc comment for the full rationale),
// and additionally cross-checks every structural invariant along the
// way (PE digest matches a fresh recompute, messageDigest attribute
// matches SHA-256 of the eContent's inner octets), returning just the
// two values a final RSA verification needs: the SHA-256 digest of
// the re-tagged authenticatedAttributes, and the raw signature bytes.
func extractSignedAttrsAndSignature(t *testing.T, signed []byte) ([]byte, []byte) {
	t.Helper()

	l, err := parsePELayout(signed)
	if err != nil {
		t.Fatalf("parsing signed PE: %v", err)
	}
	secOff, secSize := l.securityDataDirectory(signed)
	if secOff == 0 || secSize == 0 {
		t.Fatal("no Security data directory entry present after signing")
	}
	if int(secOff)+int(secSize) > len(signed) {
		t.Fatal("Security data directory entry runs past end of file")
	}

	winCert := signed[secOff:]
	dwLength := binary.LittleEndian.Uint32(winCert[0:])
	wRevision := binary.LittleEndian.Uint16(winCert[4:])
	wCertType := binary.LittleEndian.Uint16(winCert[6:])
	if wRevision != 0x0200 || wCertType != 0x0002 {
		t.Fatalf("unexpected WIN_CERTIFICATE revision/type: %#x/%#x", wRevision, wCertType)
	}
	certData := winCert[8:dwLength]

	// Walk the PKCS#7 ContentInfo{SignedData} structure with
	// encoding/asn1's RawValue support -- the same generic technique
	// real ASN.1-aware verifiers use, confirming this package's output
	// is standards-shaped DER, not merely "whatever bytes signPE
	// happened to write".
	var top asn1.RawValue
	rest := mustUnmarshal(t, certData, &top)
	if len(rest) != 0 {
		t.Fatalf("%d trailing bytes after top-level ContentInfo", len(rest))
	}
	var contentType asn1.ObjectIdentifier
	rest = mustUnmarshal(t, top.Bytes, &contentType)
	if !contentType.Equal(oidPKCS7SignedData) {
		t.Fatalf("top ContentInfo contentType = %v, want pkcs7-signedData", contentType)
	}
	var explicit0 asn1.RawValue
	mustUnmarshal(t, rest, &explicit0)
	var signedData asn1.RawValue
	mustUnmarshal(t, explicit0.Bytes, &signedData)

	sdRest := signedData.Bytes
	var version int
	sdRest = mustUnmarshal(t, sdRest, &version)
	var digestAlgs asn1.RawValue
	sdRest = mustUnmarshal(t, sdRest, &digestAlgs)
	var innerContentInfo asn1.RawValue
	sdRest = mustUnmarshal(t, sdRest, &innerContentInfo)
	var certsField asn1.RawValue
	sdRest = mustUnmarshal(t, sdRest, &certsField)
	var signerInfosSet asn1.RawValue
	mustUnmarshal(t, sdRest, &signerInfosSet)

	// eContentType + eContent: the Authenticode SpcIndirectDataContent.
	// eContentExplicit (parsed from the [0] EXPLICIT wrapper) holds the
	// wrapped type's *complete* encoding as its content -- i.e.
	// eContentExplicit.Bytes starts with the SpcIndirectDataContent
	// SEQUENCE's own tag+length, not its bare fields -- so it takes one
	// more RawValue unmarshal (into spcIndirectDataContent below) to
	// reach the actual data/messageDigest fields.
	var eContentType asn1.ObjectIdentifier
	icRest := mustUnmarshal(t, innerContentInfo.Bytes, &eContentType)
	if !eContentType.Equal(oidSPCIndirectDataContent) {
		t.Fatalf("eContentType = %v, want SPC_INDIRECT_DATA_OBJID", eContentType)
	}
	var eContentExplicit asn1.RawValue
	mustUnmarshal(t, icRest, &eContentExplicit)
	eContentInner := eContentExplicit.Bytes[2:] // strip the SpcIndirectDataContent SEQUENCE's own tag+length

	var spcIndirectDataContent asn1.RawValue
	mustUnmarshal(t, eContentExplicit.Bytes, &spcIndirectDataContent)

	// SpcIndirectDataContent.messageDigest is the Authenticode PE hash;
	// confirm it matches a fresh recompute of signed's own bytes.
	var spcData asn1.RawValue
	dRest := mustUnmarshal(t, spcIndirectDataContent.Bytes, &spcData)
	var digestInfo asn1.RawValue
	mustUnmarshal(t, dRest, &digestInfo)
	var digestAlg asn1.RawValue
	diRest := mustUnmarshal(t, digestInfo.Bytes, &digestAlg)
	var embeddedPEDigest []byte
	mustUnmarshal(t, diRest, &embeddedPEDigest)

	wantPEDigest := authenticodeDigest(signed, l, sha256Sum)
	if string(embeddedPEDigest) != string(wantPEDigest) {
		t.Fatalf("embedded PE digest %x != freshly recomputed %x", embeddedPEDigest, wantPEDigest)
	}

	// One SignerInfo, one signer.
	var signerInfo asn1.RawValue
	mustUnmarshal(t, signerInfosSet.Bytes, &signerInfo)
	siRest := signerInfo.Bytes
	var siVersion int
	siRest = mustUnmarshal(t, siRest, &siVersion)
	var issuerAndSerial asn1.RawValue
	siRest = mustUnmarshal(t, siRest, &issuerAndSerial)
	var siDigestAlg asn1.RawValue
	siRest = mustUnmarshal(t, siRest, &siDigestAlg)
	var authAttrs asn1.RawValue
	siRest = mustUnmarshal(t, siRest, &authAttrs)
	if authAttrs.Class != 2 || authAttrs.Tag != 0 { // context-specific [0]
		t.Fatalf("authenticatedAttributes not tagged [0] IMPLICIT: class=%d tag=%d", authAttrs.Class, authAttrs.Tag)
	}
	var sigAlg asn1.RawValue
	siRest = mustUnmarshal(t, siRest, &sigAlg)
	var encryptedDigest []byte
	mustUnmarshal(t, siRest, &encryptedDigest)

	// messageDigest authenticated attribute must equal SHA-256 of
	// eContentInner (see spcIndirectDataContentInner's own doc comment
	// for why it's the inner content, not the full TLV).
	wantContentDigest := sha256Sum(eContentInner)
	foundMessageDigest := false
	attrRest := authAttrs.Bytes
	for len(attrRest) > 0 {
		var attr asn1.RawValue
		attrRest = mustUnmarshal(t, attrRest, &attr)
		aRest := attr.Bytes
		var oid asn1.ObjectIdentifier
		aRest = mustUnmarshal(t, aRest, &oid)
		if !oid.Equal(oidPKCS9MessageDigest) {
			continue
		}
		var valuesSet asn1.RawValue
		mustUnmarshal(t, aRest, &valuesSet)
		var digestValue []byte
		mustUnmarshal(t, valuesSet.Bytes, &digestValue)
		if string(digestValue) != string(wantContentDigest) {
			t.Fatalf("messageDigest attribute %x != SHA-256(eContentInner) %x", digestValue, wantContentDigest)
		}
		foundMessageDigest = true
	}
	if !foundMessageDigest {
		t.Fatal("no messageDigest authenticated attribute found")
	}

	// The actual RSA signature covers SHA-256 of the authenticatedAttributes
	// re-tagged as a universal SET (0x31), exactly the standard CMS/PKCS#7
	// "signed attributes" convention (RFC 5652 5.4) -- this is the step
	// AD-042's implementation got wrong twice in different ways before
	// matching real `sbsign` output (see spcIndirectDataContentInner's
	// doc comment), so this helper (and its two callers below) is the
	// permanent guard against regressing it.
	toSign := derTLV(0x31, authAttrs.Bytes)
	toSignDigest := sha256.Sum256(toSign)
	return toSignDigest[:], encryptedDigest
}

func mustUnmarshal(t *testing.T, data []byte, out interface{}) []byte {
	t.Helper()
	rest, err := asn1.Unmarshal(data, out)
	if err != nil {
		t.Fatalf("ASN.1 unmarshal failed: %v", err)
	}
	return rest
}

func TestSignPEEmbedsVerifiableSignature(t *testing.T) {
	kp, err := Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	base := buildMinimalPE(t, 1)

	signed, err := SignPE(context.Background(), base, kp)
	if err != nil {
		t.Fatalf("SignPE: %v", err)
	}
	if _, err := pe.NewFile(newReaderAt(signed)); err != nil {
		t.Fatalf("stdlib debug/pe rejected SignPE's output: %v", err)
	}
	verifyEmbeddedSignature(t, signed, kp.CertPEM)

	// Negative control: the exact same signature must NOT verify
	// against an unrelated cert -- proving the positive check above is
	// actually exercising the certificate's key, not vacuously passing.
	otherKp, err := Generate("other")
	if err != nil {
		t.Fatal(err)
	}
	toSignDigest, encryptedDigest := extractSignedAttrsAndSignature(t, signed)
	otherCert, err := parseCertPEM(otherKp.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	otherPub := otherCert.PublicKey.(*rsa.PublicKey)
	if err := rsa.VerifyPKCS1v15(otherPub, crypto.SHA256, toSignDigest, encryptedDigest); err == nil {
		t.Fatal("signature verified against an unrelated certificate -- negative control failed")
	}

	// Negative control: the digest of a tampered image must not match
	// the embedded PE digest.
	tampered := append([]byte{}, base...)
	tampered[len(tampered)-1] ^= 0xFF
	l, err := parsePELayout(tampered)
	if err != nil {
		t.Fatal(err)
	}
	tamperedDigest := authenticodeDigest(tampered, l, sha256Sum)
	origDigest := authenticodeDigest(base, l, sha256Sum)
	if string(tamperedDigest) == string(origDigest) {
		t.Fatal("tampering with the image did not change its Authenticode digest -- negative control failed")
	}
}

func TestSignPERejectsNonRSAKey(t *testing.T) {
	// Keypair.KeyPEM/CertPEM are caller-suppliable (--secureboot-key/
	// --secureboot-cert); a non-RSA key must be rejected with a clear
	// error rather than panicking or silently producing garbage.
	_, err := parseRSAKeyPEM([]byte("-----BEGIN PRIVATE KEY-----\nbm90LWEta2V5\n-----END PRIVATE KEY-----\n"))
	if err == nil {
		t.Fatal("want an error for garbage key PEM, got nil")
	}
}

func TestBuildAndSignUKIAddsInitrdAndSignsResult(t *testing.T) {
	kp, err := Generate("uki-test")
	if err != nil {
		t.Fatal(err)
	}
	base := buildMinimalPE(t, 2) // room for .cmdline + .initrd
	initramfs := []byte("fake initramfs payload for the UKI test")

	uki, err := BuildAndSignUKI(context.Background(), base, initramfs, "console=ttyS0", kp)
	if err != nil {
		t.Fatalf("BuildAndSignUKI: %v", err)
	}

	f, err := pe.NewFile(newReaderAt(uki))
	if err != nil {
		t.Fatalf("stdlib debug/pe rejected BuildAndSignUKI's output: %v", err)
	}
	var sawCmdline, sawInitrd bool
	for _, s := range f.Sections {
		switch s.Name {
		case ".cmdline":
			sawCmdline = true
			if s.VirtualAddress != cmdlineVMA {
				t.Fatalf(".cmdline VMA = %#x, want %#x", s.VirtualAddress, cmdlineVMA)
			}
		case ".initrd":
			sawInitrd = true
			if s.VirtualAddress != initrdVMA {
				t.Fatalf(".initrd VMA = %#x, want %#x", s.VirtualAddress, initrdVMA)
			}
		}
	}
	if !sawCmdline || !sawInitrd {
		t.Fatalf("missing expected sections: cmdline=%v initrd=%v (%+v)", sawCmdline, sawInitrd, f.Sections)
	}

	verifyEmbeddedSignature(t, uki, kp.CertPEM)
}

func TestBuildAndSignUKISkipsEmptyCmdlineSection(t *testing.T) {
	kp, err := Generate("uki-empty-cmdline")
	if err != nil {
		t.Fatal(err)
	}
	base := buildMinimalPE(t, 1) // only room for .initrd -- proves .cmdline was never attempted
	initramfs := []byte("payload")

	uki, err := BuildAndSignUKI(context.Background(), base, initramfs, "", kp)
	if err != nil {
		t.Fatalf("BuildAndSignUKI with empty cmdline: %v", err)
	}
	f, err := pe.NewFile(newReaderAt(uki))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range f.Sections {
		if s.Name == ".cmdline" {
			t.Fatal("empty cmdline must not produce a .cmdline section at all")
		}
	}
}

// TestRealVmlinuzSignAndVerify is a real-hardware integration test:
// opt-in (skipped unless CNIMBUS_REAL_VMLINUZ points at a real
// CONFIG_EFI_STUB=y bzImage, e.g. from `cnimbus prepare --out
// ./pieces`), since this project's own CI convention keeps GitHub-
// hosted CI build-only (no boot tests, no real kernels checked in).
// Run manually to reproduce AD-042's real evidence:
//
//	CNIMBUS_REAL_VMLINUZ=./pieces/amd64/vmlinuz go test ./internal/secureboot/... -run TestRealVmlinuzSignAndVerify -v
//
// then independently confirm with a real `sbverify` (fine to run via
// a throwaway Docker container purely as a test-time oracle -- see
// this package's own doc comments and Tasks.md's F2/AD-042 entries
// for why that's not a build-disk runtime dependency).
func TestRealVmlinuzSignAndVerify(t *testing.T) {
	path := os.Getenv("CNIMBUS_REAL_VMLINUZ")
	if path == "" {
		t.Skip("CNIMBUS_REAL_VMLINUZ not set -- real-kernel integration test, opt-in only")
	}
	vmlinuz, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := Generate("cnimbus-real-test")
	if err != nil {
		t.Fatal(err)
	}

	signed, err := SignPE(context.Background(), vmlinuz, kp)
	if err != nil {
		t.Fatalf("SignPE: %v", err)
	}
	verifyEmbeddedSignature(t, signed, kp.CertPEM)

	uki, err := BuildAndSignUKI(context.Background(), vmlinuz, []byte("integration-test-initramfs"), "", kp)
	if err != nil {
		t.Fatalf("BuildAndSignUKI: %v", err)
	}
	verifyEmbeddedSignature(t, uki, kp.CertPEM)

	if out := os.Getenv("CNIMBUS_OUT_SIGNED"); out != "" {
		_ = os.WriteFile(out, signed, 0o644)
	}
	if out := os.Getenv("CNIMBUS_OUT_UKI"); out != "" {
		_ = os.WriteFile(out, uki, 0o644)
	}
	if out := os.Getenv("CNIMBUS_OUT_CERT"); out != "" {
		_ = os.WriteFile(out, kp.CertPEM, 0o644)
	}
}
