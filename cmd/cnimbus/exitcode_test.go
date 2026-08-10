package main

import (
	"errors"
	"fmt"
	"testing"

	"cnimbus/internal/compileagent"
	"cnimbus/internal/dockerrun"
	"cnimbus/internal/kernelinfo"
	"cnimbus/internal/pieces"
)

// T50: every failure used to exit 1 regardless of cause. exitCodeFor
// must distinguish the four sentinel-wrapped classes via errors.Is,
// through however many layers of %w-wrapping sit between the raising
// package and main.
func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"generic", errors.New("boom"), exitGeneric},
		{"docker unavailable, unwrapped", dockerrun.ErrUnavailable, exitMissingHostDependency},
		{"docker unavailable, wrapped", fmt.Errorf("resolving kernel version: %w", dockerrun.ErrUnavailable), exitMissingHostDependency},
		{"pgp verification failure, wrapped", fmt.Errorf("building thunder: %w", compileagent.ErrVerification), exitVerificationFailure},
		{"pieces hash mismatch, wrapped", fmt.Errorf("resolving pieces: %w", pieces.ErrHashMismatch), exitVerificationFailure},
		{"pieces signature invalid, wrapped", fmt.Errorf("resolving pieces: %w", pieces.ErrSignatureInvalid), exitVerificationFailure},
		{"kernel.org unreachable, unwrapped", kernelinfo.ErrUpstreamFetch, exitUpstreamFetchFailure},
		{"kernel.org unreachable, wrapped", fmt.Errorf("resolving kernel version: %w", kernelinfo.ErrUpstreamFetch), exitUpstreamFetchFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
