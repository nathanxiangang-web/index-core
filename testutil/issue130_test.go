package testutil

import "testing"

func TestIssue130TargetGuard(t *testing.T) {
	good := "postgres://tester@127.0.0.1:1234/indexcore_issue130_test?sslmode=disable"
	if issue130Target(good, "owned-disposable-only") != nil {
		t.Fatal("valid purpose target rejected")
	}
	for _, bad := range []struct{ dsn, opt string }{{good, ""}, {"postgres://tester@127.0.0.1:1234/indexcore?sslmode=disable", "owned-disposable-only"}, {"postgres://tester@production/indexcore_issue130_test?sslmode=disable", "owned-disposable-only"}, {"postgres://tester@localhost/indexcore_issue130_test?sslmode=disable&host=production", "owned-disposable-only"}} {
		if issue130Target(bad.dsn, bad.opt) == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}
