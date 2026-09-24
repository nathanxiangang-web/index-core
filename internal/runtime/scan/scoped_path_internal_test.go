package scan

import "testing"

// TestCanonicalScopePathRejectsTraversal proves a scope can never escape the
// root: "." / ".." / empty components are rejected outright.
func TestCanonicalScopePathRejectsTraversal(t *testing.T) {
	rejected := []string{
		"..", "../other", "/../etc", "a/../../b", "a/..", "./x", "/a/./b", "a//b",
	}
	for _, in := range rejected {
		if got, err := canonicalScopePath(in); err == nil {
			t.Fatalf("scope %q must be rejected, got %q", in, got)
		}
	}

	accepted := map[string]string{
		"":      "",
		"/":     "",
		"sub":   "/sub",
		"/sub":  "/sub",
		"a/b":   "/a/b",
		"/a/b/": "/a/b",
	}
	for in, want := range accepted {
		got, err := canonicalScopePath(in)
		if err != nil || got != want {
			t.Fatalf("canonicalScopePath(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
}

// TestScopeAPIPathContainment proves the resolved provider path stays inside the
// root even if a caller bypasses canonicalScopePath.
func TestScopeAPIPathContainment(t *testing.T) {
	cases := []struct {
		root, rel, want string
		wantErr         bool
	}{
		{"/library", "", "/library", false},
		{"/library", "/sub", "/library/sub", false},
		{"/", "/downloads", "/downloads", false},
		{"", "/downloads", "/downloads", false},
		{"/library", "/../other", "", true},
		{"/library", "/sub/../../escape", "", true},
		{"../root", "/x", "", true},
	}
	for _, c := range cases {
		got, err := scopeAPIPath(c.root, c.rel)
		if c.wantErr {
			if err == nil {
				t.Fatalf("scopeAPIPath(%q,%q) must fail, got %q", c.root, c.rel, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("scopeAPIPath(%q,%q) = (%q,%v), want %q", c.root, c.rel, got, err, c.want)
		}
	}
}
