package secureboot

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGenerateProducesValidSelfSignedCert(t *testing.T) {
	kp, err := Generate("cnimbus-test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	block, _ := pem.Decode(kp.CertPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("CertPEM did not decode to a CERTIFICATE PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing generated certificate: %v", err)
	}
	if cert.Subject.CommonName != "cnimbus-test" {
		t.Errorf("CommonName = %q, want %q", cert.Subject.CommonName, "cnimbus-test")
	}
	// Self-signed: the certificate's own signature must verify against
	// its own embedded public key. CheckSignature (not CheckSignatureFrom
	// -- that one also enforces CA-chain policy checks irrelevant to a
	// single self-signed Secure Boot leaf cert) is the direct check for
	// "this exact cert's signature bytes match its own TBS content".
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		t.Errorf("certificate signature does not verify against its own public key: %v", err)
	}

	keyBlock, _ := pem.Decode(kp.KeyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("KeyPEM did not decode to a PRIVATE KEY PEM block")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kp, err := Generate("roundtrip")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := Save(dir, kp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := LoadDefault(dir)
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if string(got.KeyPEM) != string(kp.KeyPEM) || string(got.CertPEM) != string(kp.CertPEM) {
		t.Error("LoadDefault did not return the exact bytes Save wrote")
	}

	// Private key must never be world-readable -- Windows/NTFS has no
	// POSIX permission bits at all (os.Stat there always reports
	// 0666/0444 based solely on the read-only attribute, never a real
	// owner/group/other split), so this check only means anything on a
	// real POSIX filesystem.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, KeyPEMName))
		if err != nil {
			t.Fatalf("stat %s: %v", KeyPEMName, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %o, want no group/other permission bits", KeyPEMName, info.Mode().Perm())
		}
	}
}

func TestLoadDefaultMissingReturnsErrNoDefaultKeypair(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadDefault(dir)
	if err == nil {
		t.Fatal("expected an error for an empty directory")
	}
	if !errors.Is(err, ErrNoDefaultKeypair) {
		t.Errorf("got %v, want ErrNoDefaultKeypair", err)
	}
}

func TestLoadOrGenerateGeneratesOnceThenReuses(t *testing.T) {
	dir := t.TempDir()

	kp1, generated1, err := LoadOrGenerate(dir, "cnimbus")
	if err != nil {
		t.Fatalf("first LoadOrGenerate: %v", err)
	}
	if !generated1 {
		t.Fatal("expected the first call against an empty directory to generate a fresh keypair")
	}

	kp2, generated2, err := LoadOrGenerate(dir, "cnimbus")
	if err != nil {
		t.Fatalf("second LoadOrGenerate: %v", err)
	}
	if generated2 {
		t.Fatal("second call regenerated a keypair instead of reusing the one the first call saved -- " +
			"this would invalidate every signature already produced with the old key")
	}
	if string(kp1.KeyPEM) != string(kp2.KeyPEM) || string(kp1.CertPEM) != string(kp2.CertPEM) {
		t.Error("reused keypair bytes differ from the originally generated ones")
	}
}
