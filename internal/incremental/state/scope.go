package state

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidScopeKey is returned when a scope key violates the P3 storage
// contract. Invalid scope keys must fail before any SQL mutation.
var ErrInvalidScopeKey = errors.New("invalid scope key")

// ValidateScopeKey enforces the P3 Sec 5 scope-key storage contract:
//
//	/             valid root scope
//	/a/b          valid
//	/a/           invalid (trailing slash)
//	//a           invalid (empty component)
//	/a/../b       invalid (dot component)
//	"\x.."        invalid (backslash separator)
func ValidateScopeKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidScopeKey)
	}
	if key == "/" {
		return nil
	}
	if !strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: %q must be root-absolute", ErrInvalidScopeKey, key)
	}
	if strings.HasSuffix(key, "/") {
		return fmt.Errorf("%w: %q must not have a trailing slash", ErrInvalidScopeKey, key)
	}
	if strings.Contains(key, `\`) {
		return fmt.Errorf("%w: %q must not use a backslash separator", ErrInvalidScopeKey, key)
	}
	for _, seg := range strings.Split(key[1:], "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: %q has an empty component", ErrInvalidScopeKey, key)
		case ".", "..":
			return fmt.Errorf("%w: %q has a %q component", ErrInvalidScopeKey, key, seg)
		}
	}
	return nil
}
