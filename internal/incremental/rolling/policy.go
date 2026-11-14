// Package rolling defines background policy, never canonical truth or source I/O.
package rolling

import (
	"errors"
	"time"
)

type Policy struct {
	SharePercent int
	Spacing      time.Duration
	Lease        time.Duration
}

func (p Policy) Validate() error {
	if p.SharePercent < 1 || p.SharePercent > 50 || p.Spacing < time.Millisecond || p.Spacing > time.Minute || p.Lease < time.Hour || p.Lease > 7*24*time.Hour {
		return errors.New("rolling mirror requires share1..50, spacing1ms..1m and lease1h..7d")
	}
	return nil
}

// AdmissionSpacing is an estimated eight-call scan cost, not an HTTP counter.
func (p Policy) AdmissionSpacing() time.Duration {
	return p.Spacing * 8 * 100 / time.Duration(p.SharePercent)
}
