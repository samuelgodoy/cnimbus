// Package kernelinfo resolves a symbolic or explicit kernel version
// against the official kernel.org release index and returns a
// downloadable, (when possible) signature-verifiable source tarball.
package kernelinfo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

// IndexURL is the kernel.org release index. Overridable via the
// CNIMBUS_KERNEL_INDEX_URL env var (used by tests and offline mirrors).
const defaultIndexURL = "https://www.kernel.org/releases.json"

// ErrUpstreamFetch (T50) wraps a genuine network/transport failure
// reaching kernel.org's release index -- distinct from a malformed
// response (a real bug in the index itself or this parser, not
// something retrying helps with). This is the one sentinel a CI
// pipeline can retry on: "kernel.org is briefly unreachable" versus
// "a signature didn't match" (compileagent.ErrVerification) or "a hash
// didn't match" (pieces.ErrHashMismatch), neither of which is ever safe
// to retry as-is.
var ErrUpstreamFetch = errors.New("could not reach kernel.org")

// Release is one entry of kernel.org/releases.json.
type Release struct {
	Moniker  string `json:"moniker"`
	Version  string `json:"version"`
	IsEOL    bool   `json:"iseol"`
	Released struct {
		Timestamp int64  `json:"timestamp"`
		ISODate   string `json:"isodate"`
	} `json:"released"`
	Source string `json:"source"`
	PGP    string `json:"pgp"`
}

type releaseIndex struct {
	Releases []Release `json:"releases"`
}

// Resolved is the outcome of resolving a version spec: the concrete
// kernel version, its source tarball URL, and whether that URL came
// from the verified kernel.org index or a best-effort fallback.
type Resolved struct {
	Version  string
	Source   string
	PGP      string
	Moniker  string
	Fallback bool // true if the URL was guessed rather than confirmed by the index
}

// indexURL returns the release index URL, honoring the override env var.
func indexURL() string {
	if u := os.Getenv("CNIMBUS_KERNEL_INDEX_URL"); u != "" {
		return u
	}
	return defaultIndexURL
}

func fetchIndex() (*releaseIndex, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(indexURL())
	if err != nil {
		return nil, fmt.Errorf("fetching kernel release index: %w: %w", ErrUpstreamFetch, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching kernel release index: HTTP %s: %w", resp.Status, ErrUpstreamFetch)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading kernel release index: %w", err)
	}
	var idx releaseIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("parsing kernel release index: %w", err)
	}
	return &idx, nil
}

// Resolve turns a version spec into a concrete, downloadable kernel
// release. Recognized specs:
//
//	"latest-stable"   -> the release.json entry with moniker "stable"
//	"latest-longterm" -> the newest release.json entry with moniker "longterm"
//	anything else     -> treated as an explicit version, e.g. "6.9.4"
func Resolve(versionSpec string) (*Resolved, error) {
	idx, err := fetchIndex()
	if err != nil {
		return nil, err
	}

	switch versionSpec {
	case "latest-stable":
		for _, r := range idx.Releases {
			if r.Moniker == "stable" {
				return &Resolved{Version: r.Version, Source: r.Source, PGP: r.PGP, Moniker: r.Moniker}, nil
			}
		}
		return nil, fmt.Errorf("kernel release index has no entry with moniker \"stable\"")

	case "latest-longterm":
		var longterm []Release
		for _, r := range idx.Releases {
			if r.Moniker == "longterm" && !r.IsEOL {
				longterm = append(longterm, r)
			}
		}
		if len(longterm) == 0 {
			return nil, fmt.Errorf("kernel release index has no active entry with moniker \"longterm\"")
		}
		sort.Slice(longterm, func(i, j int) bool {
			return longterm[i].Released.Timestamp > longterm[j].Released.Timestamp
		})
		r := longterm[0]
		return &Resolved{Version: r.Version, Source: r.Source, PGP: r.PGP, Moniker: r.Moniker}, nil

	default:
		for _, r := range idx.Releases {
			if r.Version == versionSpec {
				return &Resolved{Version: r.Version, Source: r.Source, PGP: r.PGP, Moniker: r.Moniker}, nil
			}
		}
		// Not in the (small) live index — fall back to kernel.org's
		// stable tarball path convention. Signature verification (see
		// VerifyTarball) still runs against this guessed .tar.sign URL
		// when the fallback path is taken, exactly as it does for an
		// index-resolved release.
		return fallbackRelease(versionSpec)
	}
}

func fallbackRelease(version string) (*Resolved, error) {
	major, err := majorSeries(version)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("https://cdn.kernel.org/pub/linux/kernel/v%s.x/linux-%s.tar.xz", major, version)
	return &Resolved{
		Version:  version,
		Source:   url,
		PGP:      url[:len(url)-len(".tar.xz")] + ".tar.sign",
		Moniker:  "explicit",
		Fallback: true,
	}, nil
}

func majorSeries(version string) (string, error) {
	for i, c := range version {
		if c == '.' {
			return version[:i], nil
		}
	}
	return "", fmt.Errorf("cannot determine major series from version %q", version)
}
