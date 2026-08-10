package assets

import (
	"strings"
	"testing"
)

// Real V3 boot-validation (2026-08-06) found `cnimbus prepare --arch
// arm64` failing outright: the builder Dockerfile pinned bc=1.07.1-4,
// but Debian trixie's arm64 archive only ever rebuilt that source
// version as the binNMU 1.07.1-4+b1, never the bare 1.07.1-4 amd64
// resolved. This guards against silently returning to one
// hardcoded bc= version that only works for a single TARGETARCH.
func TestDockerfilePinsBcPerArch(t *testing.T) {
	content := string(ForgeDockerfile)

	if !strings.Contains(content, "ARG TARGETARCH") {
		t.Fatal("expected the builder Dockerfile to declare ARG TARGETARCH before branching the bc pin")
	}
	if !strings.Contains(content, `amd64) BC_VERSION=1.07.1-4 ;;`) {
		t.Error("expected an amd64 bc version case matching the archive version verified for amd64")
	}
	if !strings.Contains(content, `arm64) BC_VERSION=1.07.1-4+b1 ;;`) {
		t.Error("expected an arm64 bc version case matching the archive version verified for arm64 (the +b1 binNMU)")
	}
	if strings.Contains(content, "bc=1.07.1-4 \\") {
		t.Error("found a hardcoded single-arch bc= pin -- this breaks the other architecture's apt-get install outright")
	}
}
