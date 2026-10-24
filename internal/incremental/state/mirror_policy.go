package state

import (
	"errors"
	"time"
)

// MirrorDiscoveryStats count operational decisions, not source facts. The first
// eight categories partition child inputs; enrollment/merged counts overlap.
type MirrorDiscoveryStats struct {
	NewChildren, ChangedChildren, VerifiedReused, AlreadyQueued                int
	UnverifiedChildren, ProtectedChildren, UncertainChildren, ExplicitChildren int
	WatchEnrolled, SignalsMerged                                               int
}

func ValidateMirrorRevisit(interval time.Duration) error {
	if interval != 0 && (interval < 30*time.Second || interval > time.Hour || interval%time.Second != 0) {
		return errors.New("mirror revisit must be zero (disabled) or whole seconds within [30s,1h]")
	}
	return nil
}
