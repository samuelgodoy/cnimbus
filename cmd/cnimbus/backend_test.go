package main

import "testing"

// F3 (was T100): every backend's own name() must match the map key it's
// registered under in backends -- runRun's dispatch (run.go) looks the
// flag value up in that map, so a mismatch here would silently make a
// backend unreachable (or reachable only under the wrong --backend
// value) without any compiler error to catch it.
func TestBackendsMapKeyMatchesOwnName(t *testing.T) {
	for key, b := range backends {
		if b.name() != key {
			t.Errorf("backends[%q].name() = %q, want %q", key, b.name(), key)
		}
	}
	for _, want := range []string{"qemu", "vbox", "vmware", "hyperv"} {
		if _, ok := backends[want]; !ok {
			t.Errorf("expected a %q entry in backends", want)
		}
	}
}

func TestEffectiveUEFI(t *testing.T) {
	tests := []struct {
		isISO, uefi, want bool
	}{
		{isISO: true, uefi: false, want: false},
		{isISO: true, uefi: true, want: true},
		// FORMAT raw (GPT+ESP only) is always forced to UEFI regardless
		// of what --uefi says.
		{isISO: false, uefi: false, want: true},
		{isISO: false, uefi: true, want: true},
	}
	for _, tt := range tests {
		if got := effectiveUEFI(tt.isISO, tt.uefi); got != tt.want {
			t.Errorf("effectiveUEFI(isISO=%v, uefi=%v) = %v, want %v", tt.isISO, tt.uefi, got, tt.want)
		}
	}
}

func TestSplitHostfwd(t *testing.T) {
	hostPort, guestPort, ok := splitHostfwd("8080:80")
	if !ok || hostPort != "8080" || guestPort != "80" {
		t.Errorf("splitHostfwd(8080:80) = (%q, %q, %v), want (8080, 80, true)", hostPort, guestPort, ok)
	}
	if _, _, ok := splitHostfwd("not-a-hostfwd"); ok {
		t.Error("expected ok=false for a string with no colon")
	}
}

// F3 (was T100): the shared cleanup guard must match vbox's own
// pre-refactor semantics exactly -- a running VM (started) is never torn
// down regardless of --vm-keep, --vm-keep alone (not yet started) only
// ever prints the "left in place" message without deleting, and the
// plain failure case (neither started nor kept) is the only one that
// actually calls remove.
func TestCleanupUnlessStartedOrKept(t *testing.T) {
	tests := []struct {
		name           string
		started, keep  bool
		wantRemoveCall bool
	}{
		{"failed setup, not kept -> removed", false, false, true},
		{"failed setup, kept -> left in place, not removed", false, true, false},
		{"started, not kept -> left running, not removed", true, false, false},
		{"started and kept -> left running, not removed", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			removed := false
			cleanupUnlessStartedOrKept(tt.started, tt.keep, "kept-message", func() { removed = true })
			if removed != tt.wantRemoveCall {
				t.Errorf("started=%v keep=%v: remove called = %v, want %v", tt.started, tt.keep, removed, tt.wantRemoveCall)
			}
		})
	}
}
