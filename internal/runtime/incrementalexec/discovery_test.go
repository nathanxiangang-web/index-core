package incrementalexec

import (
	"strings"
	"testing"
)

func TestDiscoveryScopeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		prefix, parent string
		input          []string
		count          int
		bad            bool
	}{
		{"", "/cloud", []string{"/cloud/a"}, 0, false},
		{"/cloud", "/cloud-other", []string{"/cloud-other/a"}, 0, false},
		{"/cloud", "/cloud", []string{"/cloud/a", "/cloud/a", "/cloud/b"}, 2, false},
		{"/", "/", []string{"/a"}, 1, false},
		{"/cloud", "/cloud", []string{"/cloud/../bad"}, 0, true},
		{"/cloud", "/cloud", []string{"/other/a"}, 0, true},
		{"/cloud", "/cloud", []string{"/cloud/a/b"}, 0, true},
		{"/cloud", "/cloud", []string{"/cloud"}, 0, true},
		{"/cloud", "/cloud/" + strings.Repeat("a/", 7) + "a", []string{"/cloud/" + strings.Repeat("a/", 8) + "a"}, 0, false},
	} {
		got, err := discoveryChildren(tc.prefix, tc.parent, tc.input)
		if (err != nil) != tc.bad || len(got) != tc.count {
			t.Fatalf("case %+v: children=%v err=%v", tc, got, err)
		}
	}
	if _, err := discoveryChildren("/cloud", "/cloud", make([]string, 1001)); err == nil {
		t.Fatal("uncapped children")
	}
}
