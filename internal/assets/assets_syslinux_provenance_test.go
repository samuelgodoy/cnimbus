package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestSyslinuxBinariesMatchProvenance pins IsolinuxBin/LdlinuxC32
// against the exact hashes PROVENANCE.md records (verified there
// against a fresh download of upstream syslinux 6.03's own release
// tarball) -- these two files are hand-committed binaries with no
// per-build fetch step of their own to attach a hash check to (unlike
// the kernel/BusyBox/iptables pieces, which are verified at `prepare`
// time), so this is the only thing that would catch either file
// silently drifting from what PROVENANCE.md claims they are.
func TestSyslinuxBinariesMatchProvenance(t *testing.T) {
	const (
		wantIsolinuxBinSHA256 = "c5e4e775a7aada9aa2b227806724c52c66625b88699b3f167b5ec690a7addb91"
		wantLdlinuxC32SHA256  = "5cef9ad0d0ca04097262241686c6c3a7306ab9b9cdf24b9d4ee3b16af01a5af2"
	)
	if got := sha256Hex(IsolinuxBin); got != wantIsolinuxBinSHA256 {
		t.Errorf("isolinux.bin SHA-256 = %s, want %s (see PROVENANCE.md)", got, wantIsolinuxBinSHA256)
	}
	if got := sha256Hex(LdlinuxC32); got != wantLdlinuxC32SHA256 {
		t.Errorf("ldlinux.c32 SHA-256 = %s, want %s (see PROVENANCE.md)", got, wantLdlinuxC32SHA256)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
