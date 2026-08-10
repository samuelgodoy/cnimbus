// Authenticode PE signing, pure Go, stdlib-only (AD-042). Replaces
// the earlier Docker-based `sbsign` call: signing a PE binary the way
// Secure Boot firmware (and `sbverify`) actually check it means
// embedding a PKCS#7 SignedData structure -- carrying a
// SpcIndirectDataContent (the Authenticode-specific "what got
// signed") -- as a WIN_CERTIFICATE entry appended past the end of the
// image and referenced from the Optional Header's Security data
// directory. Both the PKCS#7/CMS SignedData shape (RFC 2315) and the
// Authenticode-specific SpcIndirectDataContent/SpcPeImageData/SpcLink
// types on top of it (Microsoft's public "Windows Authenticode
// Portable Executable Signature Format" spec) are fixed, publicly
// documented ASN.1 structures -- not reverse-engineered guesswork.
//
// crypto/x509 has no built-in "sign this PKCS#7 SignedData" call (Go
// deliberately never shipped general PKCS#7 support -- see
// golang.org/issue/15625), so the structure is hand-built here with
// encoding/asn1, the same way crypto/x509 itself hand-builds
// Certificate/TBSCertificate ASN.1 internally. Where a piece is a
// plain universal-tag value (an OBJECT IDENTIFIER, an INTEGER, an
// OCTET STRING, a stdlib-shaped SEQUENCE like pkix.AlgorithmIdentifier)
// asn1.Marshal is used directly; the handful of context-specific
// IMPLICIT/EXPLICIT tags PKCS#7/Authenticode requires (the SignedData
// content's [0] EXPLICIT wrapper, SignerInfo's [0] IMPLICIT
// authenticatedAttributes, the certificates [0] IMPLICIT SET, SpcLink's
// [2] EXPLICIT file choice, SpcString's [0] IMPLICIT unicode choice)
// are built with the small derTLV/setOf helpers below, since Go's
// asn1.Marshal struct tags can't express "IMPLICIT SET" cleanly for a
// bespoke struct without fighting the encoder.
package secureboot

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
)

const cryptoSHA256 = crypto.SHA256

// Object identifiers this file needs. Every one is a fixed, publicly
// registered OID (PKCS#7/PKCS#9 from RFC 2315/2985, SPC_* from
// Microsoft's Authenticode spec) -- none are invented here.
var (
	oidSPCIndirectDataContent = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 4}  // SPC_INDIRECT_DATA_OBJID
	oidSPCPEImageData         = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 15} // SPC_PE_IMAGE_DATAOBJ
	oidPKCS7SignedData        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidPKCS9ContentType       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidPKCS9MessageDigest     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSHA256                 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSAEncryption          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
)

// derTLV builds one DER tag-length-value with an explicit, already-
// computed tag byte (so callers can pick context-specific/implicit/
// constructed bits directly, e.g. 0xA0 for "[0] IMPLICIT constructed")
// and DER's own length encoding (short form under 128 bytes, long
// form -- 0x80|numLenBytes followed by the big-endian length -- above
// that; no other length form is legal DER).
func derTLV(tag byte, content []byte) []byte {
	out := []byte{tag}
	n := len(content)
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	default:
		var lenBytes []byte
		for x := n; x > 0; x >>= 8 {
			lenBytes = append([]byte{byte(x)}, lenBytes...)
		}
		out = append(out, byte(0x80|len(lenBytes)))
		out = append(out, lenBytes...)
	}
	return append(out, content...)
}

func setOf(elements ...[]byte) []byte {
	var content []byte
	for _, e := range elements {
		content = append(content, e...)
	}
	return derTLV(0x31, content) // universal SET (constructed), tag 17
}

func mustMarshal(v interface{}) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		// Every call site below marshals a fixed Go type (OID, *big.Int,
		// []byte, AlgorithmIdentifier) that asn1.Marshal always accepts --
		// a failure here would be a programming error, not a runtime
		// condition callers should handle.
		panic(fmt.Sprintf("secureboot: internal ASN.1 marshal error: %v", err))
	}
	return b
}

// algorithmIdentifierDER encodes AlgorithmIdentifier{oid, NULL} --
// the conventional form for both a digest algorithm (SHA-256) and a
// signature algorithm (rsaEncryption) here, matching what every real
// Authenticode signature (and X.509 certificate) carries.
func algorithmIdentifierDER(oid asn1.ObjectIdentifier) []byte {
	return mustMarshal(pkix.AlgorithmIdentifier{Algorithm: oid, Parameters: asn1.NullRawValue})
}

// spcPEImageDataFlagsAndFile is the fixed, byte-for-byte content of
// SpcPeImageData's two fields (flags SpcPeImageFlags, file SpcLink)
// used here for every signature: a zero-valued flags BIT STRING,
// followed by file encoded as the industry-standard
// "<<<Obsolete>>>" placeholder SpcLink (a BMPSTRING/UTF-16BE literal)
// real-world Authenticode signers -- `sbsign` among them -- have used
// for decades whenever there's no real catalog/page-hash file to
// reference. Confirmed byte-for-byte against a real `sbsign --key
// ...  --cert ...` run over this project's own real vmlinuz (AD-042):
// an earlier version of this file hand-built a structurally-valid but
// differently-tagged SpcLink (a "file [2] EXPLICIT SpcString"
// alternative, per one published reading of the Microsoft spec) --
// asn1parse accepted it as well-formed DER, but `sbverify` rejected
// the signature outright. Diffing against sbsign's own real output
// showed the field sbsigntool (and, by extension, sbverify's
// decoder) actually expects is exactly this fixed blob, tagged [0]
// rather than [2] -- reproduced verbatim here rather than re-derived
// from the spec a second time, since real interop with existing
// Secure Boot tooling (sbverify, and by the same code path, real UEFI
// firmware's own signature-list tooling) matters more than which
// competing reading of a decades-old, inconsistently-implemented spec
// is "more correct". Content is otherwise immaterial: nothing at
// verification time reads this sub-field back out for meaning, only
// for shape.
var spcPEImageDataFlagsAndFile = []byte{
	0x03, 0x01, 0x00, // BIT STRING, 1 content byte, 0 unused bits -- flags, zero-valued
	0xa0, 0x20, // [0] constructed, len 32 -- SpcLink "file" field
	0xa2, 0x1e, // [2] constructed, len 30
	0x80, 0x1c, // [0] primitive, len 28 -- BMPSTRING content follows (UTF-16BE "<<<Obsolete>>>")
	0x00, 0x3c, 0x00, 0x3c, 0x00, 0x3c, 0x00, 0x4f, 0x00, 0x62, 0x00, 0x73,
	0x00, 0x6f, 0x00, 0x6c, 0x00, 0x65, 0x00, 0x74, 0x00, 0x65, 0x00, 0x3e,
	0x00, 0x3e, 0x00, 0x3e,
}

// spcIndirectDataContentInner builds the *content* of the
// Authenticode-specific SpcIndirectDataContent SEQUENCE { data
// SpcAttributeTypeAndOptionalValue, messageDigest DigestInfo } --
// i.e. everything that goes inside that SEQUENCE's own tag+length,
// but not the tag+length themselves. peDigest is the Authenticode
// PE-image hash (see authenticodeDigest in pecoff.go). data's value
// is an SpcPeImageData SEQUENCE built from spcPEImageDataFlagsAndFile
// above.
//
// This is deliberately exposed as the *inner* content rather than the
// full wrapped SEQUENCE: the SignerInfo messageDigest attribute
// (built in signPE below) is the SHA-256 of exactly these inner
// bytes, NOT of the full SpcIndirectDataContent TLV (tag+length
// included) -- confirmed empirically against a real `sbsign`-produced
// signature (AD-042): an earlier version of this code hashed the full
// wrapped TLV, which is self-consistent (this package's own signature
// still verified against its own certificate) but didn't match what
// `sbverify`/GnuTLS's PKCS7 implementation independently recomputes,
// so cross-tool verification failed outright despite the signature
// being internally coherent. Diffing this package's real output
// against `sbsign`'s for the same key/cert/kernel (byte-identical
// SpcIndirectDataContent, deliberately compared with an unrelated
// signingTime to rule out coincidence) showed sbsign's own embedded
// messageDigest attribute value is SHA-256 of the SEQUENCE's content
// octets alone.
func spcIndirectDataContentInner(peDigest []byte) []byte {
	spcPEImageData := derTLV(0x30, spcPEImageDataFlagsAndFile)
	data := derTLV(0x30, append(mustMarshal(oidSPCPEImageData), spcPEImageData...))
	digestInfo := derTLV(0x30, append(algorithmIdentifierDER(oidSHA256), mustMarshal(peDigest)...))
	return append(data, digestInfo...)
}

// signPE embeds a fresh Authenticode/PKCS#7 signature into pe (a
// PE32/PE32+ image), using kp's RSA private key and self-signed
// certificate as the signing identity, and returns the resulting
// bytes. This is SignPE's actual implementation (SignPE in sign.go is
// now a thin ctx-accepting wrapper for API compatibility with the
// call sites in cmd/cnimbus/build.go).
func signPE(pe []byte, kp Keypair) ([]byte, error) {
	l, err := parsePELayout(pe)
	if err != nil {
		return nil, fmt.Errorf("parsing PE for signing: %w", err)
	}
	cert, key, err := parseKeypair(kp)
	if err != nil {
		return nil, err
	}

	peDigest := authenticodeDigest(pe, l, sha256Sum)
	eContentInner := spcIndirectDataContentInner(peDigest)
	eContent := derTLV(0x30, eContentInner)
	// The messageDigest attribute below is SHA-256 of eContentInner
	// (the SpcIndirectDataContent SEQUENCE's content octets), NOT of
	// eContent (the same bytes plus their own tag+length) -- see
	// spcIndirectDataContentInner's doc comment for how that was
	// confirmed against a real sbsign-produced signature.
	contentDigest := sha256Sum(eContentInner)

	contentTypeAttr := derTLV(0x30, append(mustMarshal(oidPKCS9ContentType), setOf(mustMarshal(oidSPCIndirectDataContent))...))
	messageDigestAttr := derTLV(0x30, append(mustMarshal(oidPKCS9MessageDigest), setOf(mustMarshal(contentDigest))...))
	authAttrsContent := concatAll(contentTypeAttr, messageDigestAttr)

	// Per PKCS#7/CMS: the signature covers the DER encoding of the
	// attributes re-tagged as a plain universal SET (tag 0x31), NOT the
	// [0] IMPLICIT SET (tag 0xA0) form SignerInfo itself carries them
	// in -- the single most common real-world PKCS#7 signing bug, and
	// exactly what this project's own real sbverify-based verification
	// (see sign_test.go) exists to catch if gotten wrong.
	toSign := derTLV(0x31, authAttrsContent)
	toSignDigest := sha256Sum(toSign)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, cryptoSHA256, toSignDigest)
	if err != nil {
		return nil, fmt.Errorf("RSA-signing authenticated attributes: %w", err)
	}

	digestAlgDER := algorithmIdentifierDER(oidSHA256)
	sigAlgDER := algorithmIdentifierDER(oidRSAEncryption)
	issuerAndSerial := derTLV(0x30, append(append([]byte{}, cert.RawIssuer...), mustMarshal(cert.SerialNumber)...))
	authAttrsForSignerInfo := derTLV(0xA0, authAttrsContent)

	signerInfo := derTLV(0x30, concatAll(
		derTLV(0x02, []byte{1}), // version 1
		issuerAndSerial,
		digestAlgDER,
		authAttrsForSignerInfo,
		sigAlgDER,
		mustMarshal(sig), // OCTET STRING
	))

	signedDataContentInfo := derTLV(0x30, concatAll(
		mustMarshal(oidSPCIndirectDataContent),
		derTLV(0xA0, eContent),
	))
	certificatesSet := derTLV(0xA0, cert.Raw) // [0] IMPLICIT SET OF Certificate, one entry

	signedData := derTLV(0x30, concatAll(
		derTLV(0x02, []byte{1}), // version 1
		setOf(digestAlgDER),
		signedDataContentInfo,
		certificatesSet,
		setOf(signerInfo),
	))

	topContentInfo := derTLV(0x30, concatAll(
		mustMarshal(oidPKCS7SignedData),
		derTLV(0xA0, signedData),
	))

	return embedWinCertificate(pe, l, topContentInfo)
}

func concatAll(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func parseKeypair(kp Keypair) (*x509.Certificate, *rsa.PrivateKey, error) {
	cert, err := parseCertPEM(kp.CertPEM)
	if err != nil {
		return nil, nil, err
	}
	key, err := parseRSAKeyPEM(kp.KeyPEM)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("secureboot: cert PEM has no CERTIFICATE block")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseRSAKeyPEM reads back the PKCS#8 "PRIVATE KEY" PEM block
// Keypair.Save/Generate always produce (see keys.go's Generate) --
// the only format this package itself ever writes, though PKCS#1
// "RSA PRIVATE KEY" is also accepted for a bring-your-own key/cert
// pair supplied via --secureboot-key/--secureboot-cert.
func parseRSAKeyPEM(keyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("secureboot: key PEM has no PEM block")
	}
	switch block.Type {
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PKCS#8 private key: %w", err)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("secureboot: private key is %T, not RSA -- sbsign-equivalent Authenticode signing needs RSA", key)
		}
		return rsaKey, nil
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("secureboot: unrecognized key PEM block type %q", block.Type)
	}
}

// embedWinCertificate appends certData (the DER-encoded PKCS#7
// SignedData ContentInfo) to pe as a WIN_CERTIFICATE entry, and
// points the Optional Header's Security data directory at it --
// replacing any WIN_CERTIFICATE table pe already carried (re-signing
// drops the previous signature rather than appending a second one,
// matching sbsign's own --output semantics of producing one signed
// copy from an unsigned input).
//
// WIN_CERTIFICATE layout (Microsoft PE/COFF spec, "Attribute
// Certificate Table"): a 4-byte dwLength (this entry's own header +
// content, NOT including the 8-byte-alignment padding that follows
// it -- confirmed empirically against sbverify, see sign_test.go),
// 2-byte wRevision (0x0200, WIN_CERT_REVISION_2_0), 2-byte wCertType
// (0x0002, WIN_CERT_TYPE_PKCS_SIGNED_DATA), then the certData bytes
// themselves, then zero-padding up to the next 8-byte boundary. The
// Security directory's own Size field DOES include that padding (the
// full on-disk span of the (only) attribute certificate table entry).
func embedWinCertificate(pe []byte, l peLayout, certData []byte) ([]byte, error) {
	// Drop any pre-existing certificate table before appending a fresh
	// one -- its file offset (if present) is exactly where the "real"
	// image data ends, same as authenticodeDigest already assumes.
	secOff, _ := l.securityDataDirectory(pe)
	base := pe
	if secOff != 0 && int(secOff) < len(pe) {
		base = pe[:secOff]
	}

	winCertLen := 8 + len(certData)
	out := make([]byte, len(base))
	copy(out, base)

	hdr := make([]byte, 8, 8+len(certData))
	binary.LittleEndian.PutUint32(hdr[0:], uint32(winCertLen))
	binary.LittleEndian.PutUint16(hdr[4:], 0x0200)
	binary.LittleEndian.PutUint16(hdr[6:], 0x0002)
	entry := append(hdr, certData...)

	certOffset := len(out)
	out = append(out, entry...)
	paddedLen := align(len(entry), 8)
	if pad := paddedLen - len(entry); pad > 0 {
		out = append(out, make([]byte, pad)...)
	}

	l.setSecurityDataDirectory(out, uint32(certOffset), uint32(paddedLen))
	// The Security directory entry itself was just rewritten, and it
	// lies before certOffset (inside the optional header), so
	// authenticodeDigest/checksum offsets from `l` are still valid --
	// nothing between the start of the file and certOffset moved.
	recomputeChecksum(out, l)
	return out, nil
}
