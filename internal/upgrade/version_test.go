package upgrade

import "testing"

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		major int
	}{
		{"v0.2.30", true, 0},
		{"0.2.30", true, 0},
		{" v1.2.3 ", true, 1},
		{"v1.2.3-rc1", true, 1},
		{"v1.2.3+build.5", true, 1},
		{"dev", false, 0},
		{"v1.2", false, 0},
		{"v1.2.3.4", false, 0},
		{"vx.y.z", false, 0},
		{"", false, 0},
	}
	for _, c := range cases {
		s, ok := parseSemver(c.in)
		if ok != c.ok {
			t.Errorf("parseSemver(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && s.major != c.major {
			t.Errorf("parseSemver(%q) major = %d, want %d", c.in, s.major, c.major)
		}
	}
}

func TestIsRelease(t *testing.T) {
	if isRelease("dev") {
		t.Error("dev should not be a release")
	}
	if !isRelease("0.2.30") {
		t.Error("0.2.30 should be a release")
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current, candidate string
		want               bool
	}{
		// The two version forms goreleaser produces must compare equal.
		{"0.2.30", "v0.2.30", false},
		{"v0.2.30", "0.2.30", false},
		{"0.2.30", "v0.2.31", true},
		{"0.2.30", "v0.3.0", true},
		{"0.2.30", "v1.0.0", true},
		{"0.3.0", "v0.2.99", false},
		{"1.0.0", "v0.9.9", false},
		{"0.2.30", "v0.2.29", false},
		// dev / unparseable never compares as upgradeable.
		{"dev", "v0.3.0", false},
		{"0.2.30", "", false},
		{"0.2.30", "garbage", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.candidate); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.candidate, got, c.want)
		}
	}
}

func TestDisplay(t *testing.T) {
	if got := display("0.2.30"); got != "v0.2.30" {
		t.Errorf("display(0.2.30) = %q, want v0.2.30", got)
	}
	if got := display("v0.2.30"); got != "v0.2.30" {
		t.Errorf("display(v0.2.30) = %q, want v0.2.30", got)
	}
	if got := display("dev"); got != "dev" {
		t.Errorf("display(dev) = %q, want dev", got)
	}
}
