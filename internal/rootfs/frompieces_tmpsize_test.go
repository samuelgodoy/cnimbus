package rootfs

import (
	"strings"
	"testing"
)

// T52: parseTmpfsSizeBytes/checkShadowedFilesFitTmpfs are the build-time
// half of T51's fix -- an over-budget COPY/ADD into one of stage 1's
// four exec-dir tmpfs mounts must fail build-disk up front, not only at
// boot when the tmpfs actually fills up.

func TestParseTmpfsSizeBytes(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 32 * 1024 * 1024, false}, // defaultTmpfsSize
		{"32m", 32 * 1024 * 1024, false},
		{"1g", 1024 * 1024 * 1024, false},
		{"512k", 512 * 1024, false},
		{"100", 100, false},
		{"0", 0, true},
		{"-5m", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseTmpfsSizeBytes(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseTmpfsSizeBytes(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTmpfsSizeBytes(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseTmpfsSizeBytes(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestCheckShadowedFilesFitTmpfsRejectsOversizedDirectory(t *testing.T) {
	shadowed := []ExtraFile{
		{Path: "/usr/bin/myapp", Perm: 0o755, Data: make([]byte, 40*1024*1024)}, // 40 MiB into a 32 MiB default tmpfs
	}
	err := checkShadowedFilesFitTmpfs(shadowed, []byte("busybox"), "")
	if err == nil {
		t.Fatal("expected an error for a COPY exceeding the default 32m tmpfs")
	}
}

func TestCheckShadowedFilesFitTmpfsHonorsTMPSIZEOverride(t *testing.T) {
	shadowed := []ExtraFile{
		{Path: "/usr/bin/myapp", Perm: 0o755, Data: make([]byte, 40*1024*1024)},
	}
	if err := checkShadowedFilesFitTmpfs(shadowed, []byte("busybox"), "64m"); err != nil {
		t.Errorf("expected TMPSIZE 64m to accommodate a 40 MiB file: %v", err)
	}
}

func TestCheckShadowedFilesFitTmpfsChecksDirectoriesIndependently(t *testing.T) {
	// A large file in usr/bin must not be masked by headroom in bin/.
	shadowed := []ExtraFile{
		{Path: "/bin/small", Perm: 0o755, Data: []byte("x")},
		{Path: "/usr/bin/big", Perm: 0o755, Data: make([]byte, 40*1024*1024)},
	}
	err := checkShadowedFilesFitTmpfs(shadowed, nil, "")
	if err == nil {
		t.Fatal("expected an error: usr/bin exceeds the default tmpfs size on its own")
	}
}

func TestBuildStage1InitUsesTmpfsSizeOverride(t *testing.T) {
	script := buildStage1Init(nil, nil, "128m")
	if !strings.Contains(script,"size=128m tmpfs /mnt/root/bin") {
		t.Errorf("expected the tmpfs mount lines to use the overridden size: %q", script)
	}
	if strings.Contains(script,"size=32m") {
		t.Errorf("did not expect the default size to appear when TMPSIZE overrides it: %q", script)
	}
}

func TestBuildStage1InitDefaultsTmpfsSizeWhenEmpty(t *testing.T) {
	script := buildStage1Init(nil, nil, "")
	if !strings.Contains(script,"size=32m tmpfs /mnt/root/bin") {
		t.Errorf("expected the default tmpfs size when no override is given: %q", script)
	}
}
