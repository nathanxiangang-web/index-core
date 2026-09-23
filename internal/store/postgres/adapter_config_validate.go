package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrPlaintextSecret is returned when an adapter config embeds a plaintext
// provider credential instead of an environment-variable reference.
var ErrPlaintextSecret = errors.New("adapter config must not contain plaintext secrets")

// ValidateAdapterConfigSecretFree rejects adapter configs that embed plaintext
// credentials. Provider secrets may only be referenced by environment-variable
// NAME (e.g. password_env / token_env) and are resolved at runtime; they must
// never be persisted into index_root_adapter_config (G3-R2.8). This is enforced
// at the Store write boundary so no caller (CLI or otherwise) can bypass it.
func ValidateAdapterConfigSecretFree(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("adapter config must be valid JSON: %w", err)
	}
	return rejectPlaintextSecrets(v)
}

func rejectPlaintextSecrets(v any) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isPlaintextSecretKey(k) {
				return fmt.Errorf("%w: key %q; reference it from the environment instead (e.g. %q)", ErrPlaintextSecret, k, k+"_env")
			}
			if err := rejectPlaintextSecrets(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := rejectPlaintextSecrets(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// isPlaintextSecretKey reports whether a config key names a secret. Keys that
// reference the environment (normalized suffix "env", e.g. password_env) are
// references rather than secrets and are allowed.
func isPlaintextSecretKey(k string) bool {
	norm := strings.ToLower(k)
	norm = strings.NewReplacer("_", "", "-", "", ".", "").Replace(norm)
	if strings.HasSuffix(norm, "env") {
		return false
	}
	for _, s := range []string{"password", "passwd", "token", "secret", "credential", "apikey", "privatekey"} {
		if strings.Contains(norm, s) {
			return true
		}
	}
	return false
}
