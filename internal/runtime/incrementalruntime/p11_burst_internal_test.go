package incrementalruntime

import (
	"testing"
	"time"
)

var p11Base = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)

func TestP11BurstFirstCycleStartsImmediately(t *testing.T) {
	r := &Runtime{}
	if d := r.burstWaitBeforeStart(p11Base); d != 0 {
		t.Fatalf("first cycle must start immediately, got %s", d)
	}
}

func TestP11BurstIdleSatisfiesCooldown(t *testing.T) {
	// Four contiguous cycles already ran, then a full post-cycle idle passed.
	r := &Runtime{burstCount: MaxConsecutiveCyclesPerBurst, lastCycleFinishedAt: p11Base}
	if d := r.burstWaitBeforeStart(p11Base.Add(BurstCooldown)); d != 0 {
		t.Fatalf("post-cycle idle >= cooldown must start immediately with no extra sleep, got %s", d)
	}
	if r.burstCount != 0 {
		t.Fatalf("satisfied idle must reset the burst, got %d", r.burstCount)
	}
}

func TestP11BurstFifthContiguousWaitsRemaining(t *testing.T) {
	r := &Runtime{burstCount: MaxConsecutiveCyclesPerBurst, lastCycleFinishedAt: p11Base}
	got := r.burstWaitBeforeStart(p11Base.Add(200 * time.Millisecond))
	if got != 800*time.Millisecond {
		t.Fatalf("fifth contiguous cycle must wait only the remaining cooldown, got %s", got)
	}
}

func TestP11LongCycleDoesNotCountAsCooldown(t *testing.T) {
	// The fourth cycle itself ran 5s, but post-cycle idle is only 10ms, so the
	// cooldown budget is still essentially full.
	r := &Runtime{burstCount: MaxConsecutiveCyclesPerBurst, lastCycleFinishedAt: p11Base}
	got := r.burstWaitBeforeStart(p11Base.Add(10 * time.Millisecond))
	if got < 900*time.Millisecond {
		t.Fatalf("time spent inside a long cycle must not satisfy the post-cycle cooldown, got %s", got)
	}
}

func TestP11BurstUnderCapStartsImmediately(t *testing.T) {
	r := &Runtime{burstCount: MaxConsecutiveCyclesPerBurst - 1, lastCycleFinishedAt: p11Base}
	if d := r.burstWaitBeforeStart(p11Base.Add(time.Millisecond)); d != 0 {
		t.Fatalf("cycle below the cap must start immediately, got %s", d)
	}
}

func TestP11MarkCycleFinishedStampsAndCounts(t *testing.T) {
	r := &Runtime{}
	r.markCycleFinished(p11Base)
	if r.burstCount != 1 || !r.lastCycleFinishedAt.Equal(p11Base) {
		t.Fatalf("markCycleFinished = count %d finished %s", r.burstCount, r.lastCycleFinishedAt)
	}
}
