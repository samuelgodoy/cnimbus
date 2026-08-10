package kernelinfo

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testIndex = `{
  "releases": [
    {"moniker": "mainline", "version": "7.2-rc5", "iseol": false, "released": {"timestamp": 1785102348, "isodate": "2026-07-26"}, "source": "https://git.kernel.org/torvalds/t/linux-7.2-rc5.tar.gz", "pgp": null},
    {"moniker": "stable", "version": "7.1.5", "iseol": false, "released": {"timestamp": 1784903251, "isodate": "2026-07-24"}, "source": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.5.tar.xz", "pgp": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.5.tar.sign"},
    {"moniker": "longterm", "version": "6.18.41", "iseol": false, "released": {"timestamp": 1785409559, "isodate": "2026-07-30"}, "source": "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.41.tar.xz", "pgp": "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.41.tar.sign"},
    {"moniker": "longterm", "version": "6.12.100", "iseol": false, "released": {"timestamp": 1780000000, "isodate": "2026-05-01"}, "source": "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.12.100.tar.xz", "pgp": "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.12.100.tar.sign"},
    {"moniker": "longterm", "version": "5.10.999", "iseol": true, "released": {"timestamp": 1700000000, "isodate": "2023-11-14"}, "source": "https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.999.tar.xz", "pgp": "https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.999.tar.sign"}
  ]
}`

func withTestIndex(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CNIMBUS_KERNEL_INDEX_URL", srv.URL)
}

func TestResolveLatestStable(t *testing.T) {
	withTestIndex(t, testIndex)
	r, err := Resolve("latest-stable")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "7.1.5" {
		t.Errorf("Version = %q, want 7.1.5", r.Version)
	}
	if r.Source != "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.5.tar.xz" {
		t.Errorf("Source = %q", r.Source)
	}
	if r.PGP == "" {
		t.Error("expected a PGP sign URL for the stable release")
	}
	if r.Fallback {
		t.Error("an index-resolved release should not be marked Fallback")
	}
}

func TestResolveLatestLongtermPicksNewestNonEOL(t *testing.T) {
	withTestIndex(t, testIndex)
	r, err := Resolve("latest-longterm")
	if err != nil {
		t.Fatal(err)
	}
	// 6.18.41 has a newer timestamp than 6.12.100, and 5.10.999 is EOL
	// so it must never be picked regardless of timestamp.
	if r.Version != "6.18.41" {
		t.Errorf("Version = %q, want 6.18.41 (newest non-EOL longterm)", r.Version)
	}
}

func TestResolveExplicitVersionFromIndex(t *testing.T) {
	withTestIndex(t, testIndex)
	r, err := Resolve("6.12.100")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "6.12.100" || r.Fallback {
		t.Errorf("Resolved = %+v, want exact index match, not a fallback", r)
	}
}

func TestResolveExplicitVersionNotInIndexFallsBack(t *testing.T) {
	withTestIndex(t, testIndex)
	r, err := Resolve("6.1.999")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Fallback {
		t.Error("expected Fallback=true for a version absent from the index")
	}
	want := "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.1.999.tar.xz"
	if r.Source != want {
		t.Errorf("Source = %q, want %q", r.Source, want)
	}
	wantPGP := "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.1.999.tar.sign"
	if r.PGP != wantPGP {
		t.Errorf("PGP = %q, want %q", r.PGP, wantPGP)
	}
}

func TestResolveNoStableInIndex(t *testing.T) {
	withTestIndex(t, `{"releases": [{"moniker": "mainline", "version": "1.0", "released": {"timestamp": 1, "isodate": "x"}, "source": "s"}]}`)
	if _, err := Resolve("latest-stable"); err == nil {
		t.Fatal("expected error when the index has no \"stable\" entry")
	}
}

func TestResolveNoActiveLongterm(t *testing.T) {
	withTestIndex(t, `{"releases": [{"moniker": "longterm", "version": "1.0", "iseol": true, "released": {"timestamp": 1, "isodate": "x"}, "source": "s"}]}`)
	if _, err := Resolve("latest-longterm"); err == nil {
		t.Fatal("expected error when every longterm entry is EOL")
	}
}

func TestResolveIndexUnreachable(t *testing.T) {
	t.Setenv("CNIMBUS_KERNEL_INDEX_URL", "http://127.0.0.1:1/does-not-exist")
	_, err := Resolve("latest-stable")
	if err == nil {
		t.Fatal("expected error when the index endpoint is unreachable")
	}
	// T50: a genuine transport failure must be recognizable via
	// errors.Is(err, ErrUpstreamFetch) so cnimbus's own exit-code table
	// can tell "kernel.org is briefly unreachable, safe to retry" apart
	// from a verification/integrity failure, which never is.
	if !errors.Is(err, ErrUpstreamFetch) {
		t.Errorf("expected errors.Is(err, ErrUpstreamFetch), got %v", err)
	}
}

func TestResolveIndexBadHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("CNIMBUS_KERNEL_INDEX_URL", srv.URL)
	_, err := Resolve("latest-stable")
	if err == nil {
		t.Fatal("expected error for a non-200 index response")
	}
	if !errors.Is(err, ErrUpstreamFetch) {
		t.Errorf("expected errors.Is(err, ErrUpstreamFetch), got %v", err)
	}
}

func TestResolveIndexBadJSON(t *testing.T) {
	withTestIndex(t, "not json")
	if _, err := Resolve("latest-stable"); err == nil {
		t.Fatal("expected error for malformed JSON index")
	}
}

func TestFallbackVersionWithoutDotFails(t *testing.T) {
	withTestIndex(t, testIndex)
	if _, err := Resolve("noversionformat"); err == nil {
		t.Fatal("expected error resolving a version with no major.minor separator")
	} else if !strings.Contains(err.Error(), "major series") {
		t.Errorf("error = %v, want a major-series-related message", err)
	}
}
