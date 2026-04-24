// Package version holds build-time identifiers baked into the binary and
// utilities for comparing them against the latest GitHub release.
//
// The binary's version is set via -ldflags from the Makefile; it matches the
// git tag verbatim (e.g. "v0.1.0"). Local/untagged builds look like
// "v0.1.0-5-g1234abc-dirty" or "v0.0.0-dev". Any value missing the vX.Y.Z
// shape is treated as a development build when checking for updates.
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Info captures what's baked in at build time.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Module    string `json:"module,omitempty"`
}

// current is populated once by Set() at startup. Package-level for easy
// read access from collectors/metrics without threading state through.
var current Info

// Set stashes the build-time info. Called from main() once the -ldflags
// variables are available.
func Set(info Info) {
	if info.GoVersion == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			info.GoVersion = bi.GoVersion
			if info.Module == "" {
				info.Module = bi.Main.Path
			}
		}
	}
	current = info
}

// Get returns the build-time info.
func Get() Info { return current }

// GitHubRepo derives "owner/repo" from the module path (github.com/O/R/...).
// Returns "" if the module isn't hosted on GitHub.
func GitHubRepo() string {
	m := current.Module
	if m == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			m = bi.Main.Path
		}
	}
	const prefix = "github.com/"
	if !strings.HasPrefix(m, prefix) {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(m, prefix), "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// Release is the subset of GitHub's release payload we care about.
type Release struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

// FetchLatest GETs https://api.github.com/repos/<repo>/releases/latest.
// Unauthenticated (60 req/hr per IP rate limit — ample for hourly checks).
func FetchLatest(ctx context.Context, repo string) (*Release, error) {
	if repo == "" {
		return nil, fmt.Errorf("no github repo configured")
	}
	url := "https://api.github.com/repos/" + repo + "/releases/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "clicktrics/"+current.Version)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &r, nil
}

// Status describes the relationship between the running version and the
// upstream latest tag.
type Status int

const (
	// StatusUnknown: couldn't determine (network failure, bad version string, ...)
	StatusUnknown Status = iota
	// StatusDev: running a non-tagged or dirty local build — no comparison.
	StatusDev
	// StatusCurrent: running ≥ the latest upstream tag.
	StatusCurrent
	// StatusOutdated: upstream has a newer tagged release.
	StatusOutdated
)

// String gives a short human label for the status.
func (s Status) String() string {
	switch s {
	case StatusDev:
		return "dev-build"
	case StatusCurrent:
		return "up-to-date"
	case StatusOutdated:
		return "UPDATE-AVAILABLE"
	default:
		return "unknown"
	}
}

// Compare returns whether current is older than latest. Non-semver strings
// (e.g. "v0.0.0-dev" or a commit hash) map to StatusDev. Pre-release / dirty
// suffixes after the patch number are ignored for the comparison.
func Compare(current, latest string) Status {
	cMaj, cMin, cPat, cOK := parseSemver(current)
	lMaj, lMin, lPat, lOK := parseSemver(latest)
	if !cOK {
		return StatusDev
	}
	if !lOK {
		return StatusUnknown
	}
	switch {
	case lMaj > cMaj:
		return StatusOutdated
	case lMaj < cMaj:
		return StatusCurrent
	case lMin > cMin:
		return StatusOutdated
	case lMin < cMin:
		return StatusCurrent
	case lPat > cPat:
		return StatusOutdated
	default:
		return StatusCurrent
	}
}

// parseSemver extracts Major.Minor.Patch from a string like "v1.2.3" or
// "v1.2.3-rc1". Anything after the patch (pre-release, build metadata,
// `-N-g<hash>-dirty` from git describe) is discarded. Non-matches → false.
func parseSemver(s string) (maj, min, pat int, ok bool) {
	s = strings.TrimPrefix(s, "v")
	// Truncate at first non-digit/dot after the 3rd field.
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	// parts[2] may be "3", "3-rc1", "3-5-g1234abc-dirty" etc.
	patStr := parts[2]
	for i, r := range patStr {
		if r < '0' || r > '9' {
			patStr = patStr[:i]
			break
		}
	}
	var err error
	if maj, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if min, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if patStr == "" {
		return 0, 0, 0, false
	}
	if pat, err = strconv.Atoi(patStr); err != nil {
		return 0, 0, 0, false
	}
	return maj, min, pat, true
}
