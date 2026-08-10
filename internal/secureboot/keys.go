// Package secureboot implements F2: signing the EFI-stub kernel (and,
// optionally, a Unified Kernel Image assembled from it) so a Secure
// Boot-enabled firmware whose "db" carries this project's cert will
// load it and refuse anything else.
//
// This is a deliberately separate trust mechanism from
// internal/pieces' pieces.sha256 signing (T81 step 1): that Ed25519
// key proves *cnimbus's own build pipeline* produced a given
// vmlinuz/busybox/iptables set (authenticity of the "ready pieces"
// this tool consumes). This package's RSA key+cert instead proves,
// to a *third party's UEFI firmware*, that whoever holds the private
// key approved this exact PE binary being executed at boot --
// `sbsign`/Secure Boot has no concept of Ed25519 (UEFI's signature
// database only recognizes RSA/X.509 as of this writing), so the two
// cannot share a keypair even in principle.
package secureboot

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// KeyPEMName and CertPEMName are the two files a Keypair is always
// saved/loaded as -- one directory, two fixed filenames, the same
// "generate once, reuse" convention cmd/cnimbus/keygen.go already
// established for pieces-sign-key.hex (see LoadOrGenerate's own doc
// comment for the full reasoning). KeyPEMName is never world-readable
// (see SaveKeypair); CertPEMName is meant to be handed out (enrolled
// into a hypervisor's UEFI db, checked into version control if the
// project wants a stable, sharable Secure Boot identity) so it stays
// 0o644.
const (
	KeyPEMName  = "secureboot-key.pem"
	CertPEMName = "secureboot-cert.pem"
)

// rsaKeyBits is 3072, not 2048: this key has no PGP/kernel.org-style
// long-tail compatibility requirement to weigh against strength (the
// only consumer is sbsign/UEFI firmware, both of which handle RSA-3072
// natively), and 3072 is the current NIST/BSI floor recommendation for
// a key meant to stay trusted for years inside a hypervisor's NVRAM,
// not just one build.
const rsaKeyBits = 3072

// certValidity is 10 years: long enough that a real deployment doesn't
// need a cert-rotation story before this ticket's own milestone
// (measured boot / UKI) is even done, short enough that a compromised
// or lost key doesn't stay trusted by every already-enrolled VM
// forever. cnimbus itself never re-checks this expiry -- that's
// firmware's job at boot time, the same way it already checks
// Microsoft's own db certs.
const certValidity = 10 * 365 * 24 * time.Hour

// Keypair is an RSA private key plus its self-signed X.509 certificate,
// both PEM-encoded exactly as sbsign expects them on its own
// --key/--cert flags (no intermediate DER/PKCS12 conversion needed).
type Keypair struct {
	KeyPEM  []byte // PKCS#8 "PRIVATE KEY" PEM block
	CertPEM []byte // "CERTIFICATE" PEM block
}

// Generate creates a fresh RSA-3072 keypair and a self-signed X.509
// certificate around it, commonName identifying whoever/whatever this
// signing identity represents (e.g. "cnimbus <hostname>" -- see
// cmd/cnimbus/keygen.go's --secureboot mode, the only caller that
// picks this). Callers decide entirely on their own whether/when to
// call this at all: see LoadOrGenerate's doc comment for why cnimbus's
// build pipeline itself never calls Generate silently.
func Generate(commonName string) (Keypair, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return Keypair{}, fmt.Errorf("generating RSA-%d key: %w", rsaKeyBits, err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Keypair{}, fmt.Errorf("generating certificate serial number: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"cnimbus"}},
		NotBefore:             now.Add(-1 * time.Hour), // small clock-skew margin, same reasoning TLS CAs use
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed root of trust for this one signing identity, not a real CA hierarchy
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Keypair{}, fmt.Errorf("self-signing certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Keypair{}, fmt.Errorf("marshaling private key: %w", err)
	}

	return Keypair{
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

// Save writes kp to dir as KeyPEMName (0o600 -- a private key, never
// world-readable, mirroring cmd/cnimbus/keygen.go's pieces-sign-key.hex
// permissions) and CertPEMName (0o644 -- a certificate is meant to be
// handed out: enrolled into a hypervisor's db, or checked into version
// control). Both writes go through writeFileAtomic-equivalent
// create-then-rename so a process kill mid-write can never leave a
// truncated-but-present key file that a later LoadOrGenerate would
// (wrongly) treat as "already generated, reuse it" -- see
// atomicWriteFile's own doc comment.
func Save(dir string, kp Keypair) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomicWriteFile(filepath.Join(dir, KeyPEMName), kp.KeyPEM, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", KeyPEMName, err)
	}
	if err := atomicWriteFile(filepath.Join(dir, CertPEMName), kp.CertPEM, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", CertPEMName, err)
	}
	return nil
}

// Load reads a keypair back from explicit key/cert paths -- the
// "bring your own certificate" path (--secureboot-key/--secureboot-cert
// on `cnimbus build-disk`, mirroring how --ovmf-code/--ovmf-vars on
// `cnimbus run` let a caller point at their own firmware instead of
// cnimbus resolving one itself; see cmd/cnimbus/run.go's resolveOVMF).
// No validation beyond "the files exist and are readable" -- sbsign
// itself is what actually parses/validates them, at signing time, with
// a far more complete error message than this package could produce
// from a superficial re-parse.
func Load(keyPath, certPath string) (Keypair, error) {
	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 -- caller-supplied path, same trust model as --ovmf-code
	if err != nil {
		return Keypair{}, fmt.Errorf("reading --secureboot-key %s: %w", keyPath, err)
	}
	certPEM, err := os.ReadFile(certPath) // #nosec G304 -- caller-supplied path, same trust model as --ovmf-code
	if err != nil {
		return Keypair{}, fmt.Errorf("reading --secureboot-cert %s: %w", certPath, err)
	}
	return Keypair{KeyPEM: keyPEM, CertPEM: certPEM}, nil
}

// ErrNoDefaultKeypair is returned by LoadDefault when dir holds neither
// KeyPEMName nor CertPEMName yet -- LoadOrGenerate's signal to generate
// a fresh one rather than fail outright.
var ErrNoDefaultKeypair = errors.New("no secureboot keypair found")

// LoadDefault reads dir/KeyPEMName and dir/CertPEMName, wrapping
// ErrNoDefaultKeypair if neither exists yet (a fresh project) --
// distinct from any other read error (e.g. one file present but the
// other missing or corrupt, a real problem LoadOrGenerate must not
// paper over by regenerating and silently invalidating whichever half
// did exist).
func LoadDefault(dir string) (Keypair, error) {
	keyPath := filepath.Join(dir, KeyPEMName)
	certPath := filepath.Join(dir, CertPEMName)
	_, keyErr := os.Stat(keyPath)
	_, certErr := os.Stat(certPath)
	if os.IsNotExist(keyErr) && os.IsNotExist(certErr) {
		return Keypair{}, ErrNoDefaultKeypair
	}
	return Load(keyPath, certPath)
}

// LoadOrGenerate implements the actual default-vs-bring-your-own
// decision `cnimbus build-disk --secureboot` makes (see
// cmd/cnimbus/build.go): if dir already holds a saved keypair (from
// either a previous auto-generated run, or a manual "cnimbus keygen
// --secureboot --out-dir dir"), it's loaded and reused -- a key that
// invalidated every prior signature on every previous re-run would
// make --secureboot actively unsafe to leave on by default. Only when
// dir is genuinely empty (ErrNoDefaultKeypair) does this generate a
// fresh keypair and save it, so the *next* build reuses this one too.
//
// This is the fallback path only -- a caller that passes
// --secureboot-key/--secureboot-cert explicitly never reaches this at
// all (see Load above); LoadOrGenerate exists purely so --secureboot
// alone, with no other flag, still does something safe and repeatable
// by default.
func LoadOrGenerate(dir, commonName string) (kp Keypair, generated bool, err error) {
	kp, err = LoadDefault(dir)
	if err == nil {
		return kp, false, nil
	}
	if !errors.Is(err, ErrNoDefaultKeypair) {
		return Keypair{}, false, err
	}
	kp, err = Generate(commonName)
	if err != nil {
		return Keypair{}, false, err
	}
	if err := Save(dir, kp); err != nil {
		return Keypair{}, false, fmt.Errorf("saving newly generated secureboot keypair: %w", err)
	}
	return kp, true, nil
}

// atomicWriteFile mirrors cmd/cnimbus's own writeFileAtomic (T47):
// write to a same-directory temp file, fsync, then rename over the
// final name, so a process kill (or a full disk) mid-write can never
// leave a truncated-but-present key/cert file behind. Duplicated here
// rather than imported because writeFileAtomic lives in package main
// (cmd/cnimbus), and this package is imported *by* cmd/cnimbus --
// importing it back would be a cycle.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName) // best-effort cleanup on any failure path below
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	success = true
	return nil
}
