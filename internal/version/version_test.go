package version

import "testing"

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in           string
		maj, min, p  int
		ok           bool
	}{
		{"v0.1.0", 0, 1, 0, true},
		{"0.1.0", 0, 1, 0, true},
		{"v1.2.3", 1, 2, 3, true},
		{"v1.2.3-rc1", 1, 2, 3, true},
		{"v0.1.0-5-g1234abc-dirty", 0, 1, 0, true},
		{"v10.20.300", 10, 20, 300, true},
		{"dev", 0, 0, 0, false},
		{"v0.0.0-dev", 0, 0, 0, true},
		{"not-semver", 0, 0, 0, false},
		{"1.2", 0, 0, 0, false},
		{"", 0, 0, 0, false},
	}
	for _, c := range cases {
		maj, min, pat, ok := parseSemver(c.in)
		if ok != c.ok {
			t.Errorf("parseSemver(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (maj != c.maj || min != c.min || pat != c.p) {
			t.Errorf("parseSemver(%q) = (%d,%d,%d), want (%d,%d,%d)",
				c.in, maj, min, pat, c.maj, c.min, c.p)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		current, latest string
		want            Status
	}{
		{"v0.1.0", "v0.1.0", StatusCurrent},
		{"v0.1.0", "v0.1.1", StatusOutdated},
		{"v0.1.0", "v0.2.0", StatusOutdated},
		{"v0.1.0", "v1.0.0", StatusOutdated},
		{"v1.0.0", "v0.9.9", StatusCurrent},   // ahead of latest (dev/pre-release build)
		{"v0.1.1", "v0.1.0", StatusCurrent},
		{"v0.1.0-rc1", "v0.1.0", StatusCurrent},  // rc1 patch == release patch; we don't distinguish pre-release
		{"dev", "v0.1.0", StatusDev},
		{"v0.1.0", "not-a-tag", StatusUnknown},
		{"v0.0.0-dev", "v0.1.0", StatusOutdated}, // 0.0.0 parses; 0.0.0 < 0.1.0
	}
	for _, c := range cases {
		got := Compare(c.current, c.latest)
		if got != c.want {
			t.Errorf("Compare(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestGitHubRepo(t *testing.T) {
	// Set current.Module to test the derivation directly. When Module is
	// empty the function falls back to runtime/debug.ReadBuildInfo(), so
	// we only test cases where Module is explicitly populated.
	orig := current.Module
	defer func() { current.Module = orig }()

	cases := map[string]string{
		"github.com/features-not-bugs/clicktrics":    "features-not-bugs/clicktrics",
		"github.com/features-not-bugs/clicktrics/v2": "features-not-bugs/clicktrics",
		"gitlab.com/foo/bar":                         "",
		"github.com/onlyone":                         "", // missing repo component
	}
	for mod, want := range cases {
		current.Module = mod
		if got := GitHubRepo(); got != want {
			t.Errorf("GitHubRepo(mod=%q) = %q, want %q", mod, got, want)
		}
	}
}
