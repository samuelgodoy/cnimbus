package pieces

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func genTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestParsePublicKeyHexRoundTrip(t *testing.T) {
	pub, _ := genTestKey(t)
	got, err := ParsePublicKeyHex(hex.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(pub) {
		t.Errorf("ParsePublicKeyHex round-trip mismatch")
	}
}

func TestParsePublicKeyHexRejectsWrongLength(t *testing.T) {
	if _, err := ParsePublicKeyHex(hex.EncodeToString([]byte("too short"))); err == nil {
		t.Fatal("expected an error for a public key of the wrong length")
	}
}

func TestParsePublicKeyHexRejectsInvalidHex(t *testing.T) {
	if _, err := ParsePublicKeyHex("not hex at all zz"); err == nil {
		t.Fatal("expected an error for invalid hex")
	}
}

func TestParsePrivateKeyHexRoundTrip(t *testing.T) {
	_, priv := genTestKey(t)
	seed := priv.Seed()
	got, err := ParsePrivateKeyHex(hex.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(priv) {
		t.Errorf("ParsePrivateKeyHex round-trip mismatch")
	}
}

func TestSignAndVerifyHashes(t *testing.T) {
	pub, priv := genTestKey(t)
	hashData := []byte("abc123  vmlinuz\ndef456  busybox\n")
	sigHex := SignHashes(priv, hashData)
	if err := verifySignature(pub, hashData, sigHex); err != nil {
		t.Fatalf("verifySignature failed against a genuine signature: %v", err)
	}
}

func TestVerifySignatureRejectsTamperedData(t *testing.T) {
	pub, priv := genTestKey(t)
	hashData := []byte("abc123  vmlinuz\n")
	sigHex := SignHashes(priv, hashData)
	tampered := []byte("abc123  vmlinuz-tampered\n")
	if err := verifySignature(pub, tampered, sigHex); err == nil {
		t.Fatal("expected verification to fail against tampered hashData")
	} else if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("error = %v, want errors.Is(err, ErrSignatureInvalid)", err)
	}
}

func TestVerifySignatureRejectsWrongKey(t *testing.T) {
	_, priv := genTestKey(t)
	otherPub, _ := genTestKey(t)
	hashData := []byte("abc123  vmlinuz\n")
	sigHex := SignHashes(priv, hashData)
	if err := verifySignature(otherPub, hashData, sigHex); err == nil {
		t.Fatal("expected verification to fail against the wrong public key")
	}
}

func TestVerifySignatureRejectsMalformedHex(t *testing.T) {
	pub, _ := genTestKey(t)
	if err := verifySignature(pub, []byte("data"), "not valid hex zz"); err == nil {
		t.Fatal("expected an error for malformed signature hex")
	} else if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("error = %v, want errors.Is(err, ErrSignatureInvalid)", err)
	}
}

// End-to-end through Resolve itself, against a local directory source
// (no HTTP/Docker needed): a signed pieces.sha256 verifies with the
// matching public key and is rejected with any other key or with none
// published at all.
func TestResolveVerifiesSignatureAgainstLocalDir(t *testing.T) {
	pub, priv := genTestKey(t)
	vmlinuz, busybox, manifest := []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n")
	root := writeLocalPieces(t, "amd64", vmlinuz, busybox, manifest, true)
	dir := filepath.Join(root, "amd64")

	hashData, err := os.ReadFile(filepath.Join(dir, "pieces.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	sigHex := SignHashes(priv, hashData)
	if err := os.WriteFile(filepath.Join(dir, "pieces.sha256.sig"), []byte(sigHex+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := Resolve(root, "amd64", ResolveOptions{VerifyKey: pub})
	if err != nil {
		t.Fatalf("expected Resolve to accept a genuinely signed pieces.sha256: %v", err)
	}
	if !set.HashesVerified {
		t.Error("HashesVerified should still be true alongside signature verification")
	}

	otherPub, _ := genTestKey(t)
	if _, err := Resolve(root, "amd64", ResolveOptions{VerifyKey: otherPub}); err == nil {
		t.Fatal("expected Resolve to reject a signature that doesn't match the given VerifyKey")
	} else if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("error = %v, want errors.Is(err, ErrSignatureInvalid)", err)
	}

	if _, err := Resolve(root, "amd64", ResolveOptions{VerifyKey: pub}); err != nil {
		t.Fatalf("sanity re-check with the correct key should still pass: %v", err)
	}
}

func TestResolveRefusesVerifyKeyWithNoSignaturePublished(t *testing.T) {
	pub, _ := genTestKey(t)
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), true)
	// pieces.sha256 exists, but no pieces.sha256.sig was ever written.
	if _, err := Resolve(root, "amd64", ResolveOptions{VerifyKey: pub}); err == nil {
		t.Fatal("expected Resolve to refuse a VerifyKey with no pieces.sha256.sig published")
	} else if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("error = %v, want errors.Is(err, ErrSignatureInvalid)", err)
	}
}

func TestResolveRefusesVerifyKeyWithNoHashesAtAll(t *testing.T) {
	pub, _ := genTestKey(t)
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), false)
	if _, err := Resolve(root, "amd64", ResolveOptions{VerifyKey: pub}); err == nil {
		t.Fatal("expected Resolve to refuse a VerifyKey with no pieces.sha256 at all")
	} else if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("error = %v, want errors.Is(err, ErrSignatureInvalid)", err)
	}
}

func TestResolveNoVerifyKeyIgnoresAnySignature(t *testing.T) {
	// Sanity: a source that happens to publish a (garbage) .sig file must
	// not affect a Resolve call that never asked to check it.
	root := writeLocalPieces(t, "amd64", []byte("KERNEL"), []byte("BUSYBOX"), []byte("bin/ls\tbusybox\n"), true)
	dir := filepath.Join(root, "amd64")
	if err := os.WriteFile(filepath.Join(dir, "pieces.sha256.sig"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "amd64", ResolveOptions{}); err != nil {
		t.Fatalf("Resolve with no VerifyKey should ignore an unchecked pieces.sha256.sig: %v", err)
	}
}

// sanity: SignHashes signs pieces.sha256's own bytes format, matching how
// prepare.go's writePiecesHashes actually writes it (sha256sum(1)-style
// lines), not some other encoding.
func TestSignHashesUsesRawFileBytes(t *testing.T) {
	pub, priv := genTestKey(t)
	sum := sha256.Sum256([]byte("KERNEL"))
	hashData := []byte(fmt.Sprintf("%s  vmlinuz\n", hex.EncodeToString(sum[:])))
	sigHex := SignHashes(priv, hashData)
	if strings.TrimSpace(sigHex) == "" {
		t.Fatal("SignHashes returned an empty signature")
	}
	if err := verifySignature(pub, hashData, sigHex); err != nil {
		t.Fatalf("verifySignature failed: %v", err)
	}
}
