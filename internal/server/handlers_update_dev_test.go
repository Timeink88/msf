package server

import "testing"

func TestDevUpdateSuppressed(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"untagged dev build vs newer release", "0.1.0-dev", "v0.6.3", true},
		{"dev build of the released version", "0.6.3-dev", "v0.6.3", false},
		{"release build never suppressed", "0.6.3", "v0.6.3", false},
		{"local suffix contains release", "0.6.3-cn.20260906", "v0.6.3", false},
		{"empty latest", "0.1.0-dev", "", false},
		{"empty current", "", "v0.6.3", false},
		{"case insensitive", "0.1.0-Dev", "V0.6.3", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := devUpdateSuppressed(tc.current, tc.latest); got != tc.want {
				t.Fatalf("devUpdateSuppressed(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

// The self-update gate for dev builds must stay closed: a dev build never
// reports an update as available, even against a newer release.
func TestVersionDifferentSuppressesDevBuilds(t *testing.T) {
	if versionDifferent("0.1.0-dev", "v0.6.3") {
		t.Fatal("dev build must not be offered a self-update")
	}
	if !versionDifferent("0.6.3", "v0.6.4") {
		t.Fatal("release build must see a newer release as different")
	}
	if versionDifferent("0.6.3-cn.20260906", "v0.6.3") {
		t.Fatal("local suffix containing the release version must count as same")
	}
}
