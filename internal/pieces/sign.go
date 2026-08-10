package pieces

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
)

// sigFileName is written by `cnimbus prepare --pieces-sign-key` alongside
// pieces.sha256: a detached Ed25519 signature over pieces.sha256's exact
// bytes, hex-encoded (same convention as pieces.sha256 itself, so both
// files are plain text and independently inspectable). Its absence is
// never an error on its own -- only Resolve, when handed a verify key,
// treats a missing signature as refusing to trust the pieces (see
// Resolve's own VerifyKeyHex handling).
const sigFileName = "pieces.sha256.sig"

// ErrSignatureInvalid is T81 step 1's authenticity sentinel, distinct
// from ErrHashMismatch: a hash match only proves the fetched bytes match
// what pieces.sha256 claims (transport integrity); a signature match
// proves pieces.sha256 itself was produced by whoever holds the private
// key named by --pieces-verify-key (authenticity) -- the hop T81's own
// ticket text says was previously missing entirely. Wrapped the same way
// ErrHashMismatch is, so cnimbus's exit-code table (T50) maps it to the
// same "verification/integrity failure, never safe to retry as-is" exit
// code via errors.Is.
var ErrSignatureInvalid = errors.New("pieces signature verification failed")

// ParsePublicKeyHex decodes a hex-encoded Ed25519 public key (64 hex
// characters -- the 32 raw bytes ed25519.PublicKey requires), as passed
// via --pieces-verify-key or a Nimbusfile's PIECESKEY directive. Kept
// here (not in cmd/cnimbus) so both the flag and the directive validate
// a fingerprint identically, at the point it's declared rather than only
// once it's actually used to verify something.
func ParsePublicKeyHex(s string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid hex-encoded Ed25519 public key %q: %w", s, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("Ed25519 public key %q is %d bytes, want %d", s, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivateKeyHex decodes a hex-encoded Ed25519 private key seed (64
// hex characters -- the 32-byte seed ed25519.NewKeyFromSeed expects, not
// the 64-byte expanded form ed25519.GenerateKey also returns), as read
// from the file --pieces-sign-key names.
func ParsePrivateKeyHex(s string) (ed25519.PrivateKey, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid hex-encoded Ed25519 private key seed: %w", err)
	}
	if len(raw) != ed25519.SeedSize {
		return nil, fmt.Errorf("Ed25519 private key seed is %d bytes, want %d", len(raw), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

// SignHashes signs pieces.sha256's exact bytes, returning the hex-encoded
// detached signature to write as pieces.sha256.sig.
func SignHashes(key ed25519.PrivateKey, hashData []byte) string {
	return hex.EncodeToString(ed25519.Sign(key, hashData))
}

// verifySignature checks sigHex (as fetched from pieces.sha256.sig)
// against hashData (pieces.sha256's exact bytes) using pubKey.
func verifySignature(pubKey ed25519.PublicKey, hashData []byte, sigHex string) error {
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("%w: %s is not valid hex: %v", ErrSignatureInvalid, sigFileName, err)
	}
	if !ed25519.Verify(pubKey, hashData, sig) {
		return fmt.Errorf("%w: %s does not verify against the given --pieces-verify-key -- "+
			"either pieces.sha256 was not signed by the holder of that key, or it was tampered with "+
			"after signing", ErrSignatureInvalid, sigFileName)
	}
	return nil
}
