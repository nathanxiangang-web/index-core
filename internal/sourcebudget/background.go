package sourcebudget

import (
	"context"
	"errors"
	"time"
)

type backgroundKey struct{}

func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}
func isBackground(ctx context.Context) bool { v, _ := ctx.Value(backgroundKey{}).(bool); return v }

type HTTPEvent struct {
	StartedAt, FinishedAt                 time.Time
	Status                                int
	Background, Failed, Timeout, Canceled bool
}

// Options are applied only during construction, before concurrent use.
type Option func(*Manager) error

func WithBackgroundShare(percent int) Option {
	return func(m *Manager) error {
		if percent < 1 || percent > 50 {
			return errors.New("background HTTP share must be1..50 percent")
		}
		m.backgroundPercent = percent
		return nil
	}
}

// WithObserver reports actual RoundTrip attempts, never planned reservations.
// It runs outside the policy mutex. The caller must be concurrency-safe and
// bounded; errors, paths, URLs, headers and credentials are never passed to it.
func WithObserver(f func(HTTPEvent)) Option {
	return func(m *Manager) error { m.observer = f; return nil }
}
