package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// G3-R4(5): the adapter config write boundary rejects plaintext provider
// secrets; only environment-variable references (suffix "_env") are allowed.
func TestValidateAdapterConfigSecretFree(t *testing.T) {
	rejected := [][]byte{
		[]byte(`{"base_url":"http://x","password":"hunter2"}`),
		[]byte(`{"token":"abc"}`),
		[]byte(`{"credentials":"c"}`),
		[]byte(`{"api-key":"k"}`),
		[]byte(`{"nested":{"private_key":"p"}}`),
	}
	for _, cfg := range rejected {
		if err := postgres.ValidateAdapterConfigSecretFree(cfg); !errors.Is(err, postgres.ErrPlaintextSecret) {
			t.Errorf("config %s must be rejected as plaintext secret, got %v", cfg, err)
		}
	}
	accepted := [][]byte{
		[]byte(`{"base_url":"http://x","username_env":"U","password_env":"P","token_env":"T"}`),
		[]byte(`{"remote":"local","path":"/data"}`),
		nil,
	}
	for _, cfg := range accepted {
		if err := postgres.ValidateAdapterConfigSecretFree(cfg); err != nil {
			t.Errorf("config %s must be accepted, got %v", cfg, err)
		}
	}
}

// G3-R4(5): a plaintext secret cannot be persisted through the Store write
// boundary used by `root adapter set --config`.
func TestUpsertAdapterConfigRejectsPlaintextSecret(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "b1000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("root: %v", err)
	}
	err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{
		CollectorKind: "alist", Config: []byte(`{"base_url":"http://x","password":"plaintext"}`),
	})
	if !errors.Is(err, postgres.ErrPlaintextSecret) {
		t.Fatalf("plaintext secret must be rejected at the write boundary, got %v", err)
	}
	var stored []byte
	scanErr := st.Pool().QueryRow(context.Background(),
		`SELECT config FROM index_root_adapter_config WHERE root_id=$1::uuid`, rootID).Scan(&stored)
	if scanErr == nil {
		t.Fatalf("rejected adapter config must not be persisted, got %s", stored)
	}
}
