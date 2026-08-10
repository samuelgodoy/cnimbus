package compileagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := filepath.Join(t.TempDir(), "extract")
	tests := []struct {
		rel     string
		wantErr bool
	}{
		{"normal/file.txt", false},
		{"a/b/c", false},
		{"../escape", true},
		{"../../etc/passwd", true},
		{"a/../../escape", true},
		{"./a/./b", false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, err := safeJoin(base, tt.rel)
			if tt.wantErr {
				if err == nil {
					t.Errorf("safeJoin(%q, %q) = %q, <nil>, want an error", base, tt.rel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeJoin(%q, %q): unexpected error: %v", base, tt.rel, err)
			}
			if !strings.HasPrefix(got, base) {
				t.Errorf("safeJoin result %q does not stay under base %q", got, base)
			}
		})
	}
}

func TestVerifyTarballSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sha256("hello")
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	if err := verifyTarballSHA256(path, want); err != nil {
		t.Errorf("expected the matching hash to verify: %v", err)
	}
	// Case-insensitive: the same hash, uppercased, should still verify.
	if err := verifyTarballSHA256(path, strings.ToUpper(want)); err != nil {
		t.Errorf("expected case-insensitive comparison to verify: %v", err)
	}
	if err := verifyTarballSHA256(path, "0000000000000000000000000000000000000000000000000000000000000000"[:64]); err == nil {
		t.Error("expected a mismatched hash to fail verification")
	}
}
