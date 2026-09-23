package journal

import (
	"errors"
	"sort"
)

// ErrNonZeroFloorWithoutCheckpoint is returned when a projection replay is asked
// to start from a non-zero floor without a matching checkpoint. A non-zero floor
// is only ever checkpoint resume, never a full rebuild (doc D Sec 9, JD11).
var ErrNonZeroFloorWithoutCheckpoint = errors.New("non-zero replay floor requires a matching checkpoint")

// Event is the minimal per-root journal view used for projection replay.
type Event struct {
	RootID             string
	EventSeq           int64
	GenerationNumber   int64
	IntraGenerationSeq int32
}

// CursorVector is the per-root consumption cursor {root_id: last_seen_event_seq}.
// There is no canonical cross-root order; only per-root event_seq is ordered
// (doc D U1/U2, doc C JC2/JC7).
type CursorVector map[string]int64

// After returns the last-seen event_seq for a root (0 if none), i.e. the next
// read uses event_seq > After(root).
func (c CursorVector) After(rootID string) int64 {
	if c == nil {
		return 0
	}
	return c[rootID]
}

// Checkpoint is a persisted projection state paired with the exact per-root
// cursors it represents (doc D Sec 9.5).
type Checkpoint struct {
	Cursors CursorVector
}

// ResolveReplayFloor validates a requested replay floor. Floor 0 means a true
// rebuild from the Journal origin. A non-zero floor is only permitted as a
// checkpoint resume where a checkpoint exists and its cursor immediately
// precedes the floor (doc D Sec 9.5/9.6).
func ResolveReplayFloor(rootID string, requestedFloor int64, cp *Checkpoint) (int64, error) {
	if requestedFloor <= 0 {
		return 0, nil
	}
	if cp == nil {
		return 0, ErrNonZeroFloorWithoutCheckpoint
	}
	last, ok := cp.Cursors[rootID]
	if !ok || requestedFloor != last+1 {
		return 0, ErrNonZeroFloorWithoutCheckpoint
	}
	return requestedFloor, nil
}

// Less orders events by (generation_number, intra_generation_seq, event_seq), the
// deterministic application order within a root.
func Less(a, b Event) bool {
	if a.GenerationNumber != b.GenerationNumber {
		return a.GenerationNumber < b.GenerationNumber
	}
	if a.IntraGenerationSeq != b.IntraGenerationSeq {
		return a.IntraGenerationSeq < b.IntraGenerationSeq
	}
	return a.EventSeq < b.EventSeq
}

// InApplyOrder returns a copy of events sorted by the deterministic apply order.
func InApplyOrder(events []Event) []Event {
	out := make([]Event, len(events))
	copy(out, events)
	sort.SliceStable(out, func(i, j int) bool { return Less(out[i], out[j]) })
	return out
}
