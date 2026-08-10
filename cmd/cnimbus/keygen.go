package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"cnimbus/internal/secureboot"
)

// runKeygen generates a fresh keypair -- Ed25519 by default (T81 step
// 1's pieces signing), or an RSA+X.509 Secure Boot signing identity
// with --secureboot (F2).
func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "pieces-sign-key.hex", "path to write the hex-encoded Ed25519 private key seed to "+
		"(ignored with --secureboot; see --out-dir instead)")
	securebootMode := fs.Bool("secureboot", false, "generate an RSA-3072 keypair + self-signed X.509 certificate "+
		"for Secure Boot signing (F2) instead of an Ed25519 pieces-signing key")
	outDir := fs.String("out-dir", ".", "(--secureboot only) directory to write secureboot-key.pem/"+
		"secureboot-cert.pem into")
	commonName := fs.String("common-name", "cnimbus", "(--secureboot only) certificate Subject CommonName")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *securebootMode {
		return runKeygenSecureboot(*outDir, *commonName)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating Ed25519 keypair: %w", err)
	}
	seed := priv.Seed()

	if err := writeFileAtomic(*out, []byte(hex.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", *out, err)
	}

	fmt.Printf("wrote private key seed to %s (keep this secret -- never commit it)\n", *out)
	fmt.Printf("public key (pin this with --pieces-verify-key or a Nimbusfile PIECESKEY line):\n%s\n", hex.EncodeToString(pub))
	return nil
}

// runKeygenSecureboot writes a fresh RSA-3072 keypair + self-signed
// X.509 certificate to outDir as secureboot-key.pem/secureboot-cert.pem
// (see internal/secureboot.KeyPEMName/CertPEMName) -- the explicit,
// user-invoked way to pre-generate a Secure Boot signing identity, the
// same role "cnimbus keygen" (Ed25519 mode, above) already plays for
// pieces-sign-key.hex. This is never called automatically by `cnimbus
// build-disk`: --secureboot alone (no explicit --secureboot-key/
// --secureboot-cert) instead calls internal/secureboot.LoadOrGenerate
// against its own default directory, which generates one *the first
// time only* and reuses it on every later build -- see LoadOrGenerate's
// doc comment for why "auto-generate if missing, never regenerate
// once present" is safe as an unattended default in a way "generate
// silently every time" would not be. Running this command explicitly
// is only needed when the caller wants to pick outDir/commonName
// themselves, or wants the generation step to happen as its own
// visible, auditable action rather than implicitly inside a build.
//
// This command REFUSES to overwrite an existing keypair in outDir
// (checked via internal/secureboot.LoadDefault) -- an explicit ask,
// same reasoning as the automatic path: overwriting silently would
// invalidate every signature already produced with the old key,
// against every already-enrolled hypervisor's UEFI db.
func runKeygenSecureboot(outDir, commonName string) error {
	if _, err := secureboot.LoadDefault(outDir); err == nil {
		return fmt.Errorf("%s/%s and %s already exist -- refusing to overwrite an existing Secure Boot "+
			"signing identity (this would invalidate every prior signature against every hypervisor that "+
			"already enrolled the old certificate); remove them yourself first if you really want a new one",
			outDir, secureboot.KeyPEMName, secureboot.CertPEMName)
	}

	kp, err := secureboot.Generate(commonName)
	if err != nil {
		return err
	}
	if err := secureboot.Save(outDir, kp); err != nil {
		return err
	}

	absDir, _ := os.Getwd()
	fmt.Printf("wrote %s/%s (keep this secret -- never commit it) and %s/%s (safe to hand out -- this is what "+
		"gets enrolled into a hypervisor's UEFI db) under %s\n",
		outDir, secureboot.KeyPEMName, outDir, secureboot.CertPEMName, absDir)
	fmt.Printf("use it with: cnimbus build-disk --secureboot --secureboot-key %s/%s --secureboot-cert %s/%s\n",
		outDir, secureboot.KeyPEMName, outDir, secureboot.CertPEMName)
	return nil
}
