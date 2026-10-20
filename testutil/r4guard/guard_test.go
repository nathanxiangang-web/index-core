package r4guard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0R4GuardRefusesMissingAuthorization(t *testing.T) {
	t.Setenv("INDEXCORE_R4_OPT_IN", "")
	t.Setenv("INDEXCORE_TEST_DATABASE_URL", "postgres://never-connect.invalid/production")
	if pool, err := Open(context.Background()); err == nil || pool != nil {
		t.Fatal("unsafe reset access")
	}
}

func TestP0R4GuardRejectsAmbiguousDSNsBeforeConnecting(t *testing.T) {
	a := Attestation{Host: "indexcore-r4-125-000000000000-db", Container: strings.Repeat("0", 64), Nonce: strings.Repeat("1", 64), System: "12345", OID: 1}
	b, _ := json.Marshal(a)
	f := filepath.Join(t.TempDir(), "proof.json")
	if os.WriteFile(f, b, 0600) != nil {
		t.Fatal("fixture write")
	}
	t.Setenv("INDEXCORE_R4_OPT_IN", "isolated-issue-125")
	t.Setenv("INDEXCORE_R4_ATTESTATION", f)
	for _, dsn := range []string{"", "postgres://r4:dummy@live.invalid:5432/" + Database + "?sslmode=disable", "postgres://r4:dummy@" + a.Host + ":5432/production?sslmode=disable", "postgres://other:dummy@" + a.Host + ":5432/" + Database + "?sslmode=disable", "postgres://r4:dummy@" + a.Host + ":5432/" + Database + "?sslmode=require", "postgres://r4:dummy@" + a.Host + ",live.invalid:5432/" + Database + "?sslmode=disable"} {
		t.Setenv("INDEXCORE_TEST_DATABASE_URL", dsn)
		if pool, err := Open(context.Background()); err == nil || pool != nil {
			t.Fatal("unverified DSN accepted")
		}
	}
}

func TestP0R4GuardRejectsServerIdentityMismatch(t *testing.T) {
	if os.Getenv("INDEXCORE_R4_OPT_IN") != "isolated-issue-125" {
		t.Skip("NOT_RUN: real instance mismatch needs isolated attestation")
	}
	data, err := os.ReadFile(os.Getenv("INDEXCORE_R4_ATTESTATION"))
	if err != nil {
		t.Fatal("proof unavailable")
	}
	var original Attestation
	if json.Unmarshal(data, &original) != nil {
		t.Fatal("proof invalid")
	}
	for _, field := range []string{"nonce", "container", "system", "oid"} {
		t.Run(field, func(t *testing.T) {
			a := original
			switch field {
			case "nonce":
				a.Nonce = strings.Repeat("0", 64)
			case "container":
				a.Container = strings.Repeat("0", 64)
			case "system":
				a.System = "1"
			case "oid":
				a.OID++
			}
			b, _ := json.Marshal(a)
			f := filepath.Join(t.TempDir(), "mismatch.json")
			if os.WriteFile(f, b, 0600) != nil {
				t.Fatal("fixture write")
			}
			t.Setenv("INDEXCORE_R4_ATTESTATION", f)
			if pool, err := Open(context.Background()); err == nil || pool != nil {
				t.Fatal("SQL identity fence bypassed")
			}
		})
	}
}
func TestP0R4GuardRefusesUnverifiedAttestation(t *testing.T) {
	t.Setenv("INDEXCORE_R4_OPT_IN", "isolated-issue-125")
	t.Setenv("INDEXCORE_R4_ATTESTATION", t.TempDir())
	if pool, err := Open(context.Background()); err == nil || pool != nil {
		t.Fatal("directory accepted as proof")
	}
}
