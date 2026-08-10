package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// BuildLock records exactly what went into one build-disk run: the
// Nimbusfile's own content hash, the pieces source and each fetched
// file's hash, and the resulting image's hash. "KERNEL latest-stable"
// (or any moving-target version) means the same Nimbusfile can produce
// a different image on a different day -- this is the record of
// *which* day's answer actually got built, so a later rebuild (or
// someone else's machine) can confirm whether they got the same bits,
// not just the same Nimbusfile.
type BuildLock struct {
	CnimbusVersion       string `json:"cnimbus_version"`
	BuiltAt              string `json:"built_at"` // RFC3339
	Nimbusfile           string `json:"nimbusfile"`
	NimbusfileSHA256     string `json:"nimbusfile_sha256"`
	Arch                 string `json:"arch"`
	Format               string `json:"format"`
	PiecesSource         string `json:"pieces_source"`
	VmlinuzSHA256        string `json:"vmlinuz_sha256"`
	BusyboxSHA256        string `json:"busybox_sha256"`
	ManifestSHA256       string `json:"busybox_manifest_sha256"`
	// IptablesSHA256 is empty when no FIREWALL directive is present (no
	// iptables binary -- bundled or COPY'd -- is injected into the image
	// at all in that case) or the bundled binary predates this feature
	// (see pieces.Set.Iptables's own doc comment).
	IptablesSHA256 string `json:"iptables_sha256,omitempty"`
	// SupplicantSHA256 (F6.4) is empty unless the pieces source was built
	// with HARDBOOT wifi (see pieces.Set.Supplicant's own doc comment) --
	// same optional-piece treatment as IptablesSHA256 above.
	SupplicantSHA256 string `json:"supplicant_sha256,omitempty"`
	// AgentSHA256 is empty unless a Nimbusfile AGENT directive is
	// present -- this hashes cmd/cnimbusagent, the one root-privileged
	// binary every AGENT kind now injects into the image (see
	// internal/assets), previously unrecorded despite being exactly as
	// security-relevant as vmlinuz/busybox above.
	AgentSHA256          string `json:"agent_sha256,omitempty"`
	PiecesHashesVerified bool   `json:"pieces_hashes_verified"`
	OutputImage          string `json:"output_image"`
	OutputImageSHA256    string `json:"output_image_sha256"`
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sha256File hashes path by streaming it through io.Copy (T58) rather
// than reading the whole thing into a []byte first: the output image is
// exactly the one artifact in this pipeline with no natural size
// ceiling (a FORMAT raw image with a multi-GiB VOLUME), so hashing it
// for the lockfile at the very last step of an otherwise-successful
// build was also the single largest transient allocation build-disk ever
// made -- an OOM kill here is the worst possible moment to fail.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only handle; nothing meaningful to do with a close error here
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeLockfile writes path as pretty-printed JSON, via writeFileAtomic
// (T47) so an interrupted write can't leave a truncated .lock file.
func writeLockfile(path string, lock BuildLock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

func buildLockfilePath(outImagePath string) string {
	return outImagePath + ".lock"
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
