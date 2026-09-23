package journal

import (
	"errors"
	"testing"
)

func TestCursorVectorAfter(t *testing.T) {
	var c CursorVector
	if c.After("r1") != 0 {
		t.Fatal("missing root cursor must read from the root origin (0)")
	}
	c = CursorVector{"r1": 7}
	if c.After("r1") != 7 || c.After("r2") != 0 {
		t.Fatal("cursor vector must be per-root independent")
	}
}

func TestResolveReplayFloor(t *testing.T) {
	if f, err := ResolveReplayFloor("r1", 0, nil); err != nil || f != 0 {
		t.Fatalf("floor 0 is a full rebuild from origin, got %d err=%v", f, err)
	}
	if _, err := ResolveReplayFloor("r1", 5, nil); !errors.Is(err, ErrNonZeroFloorWithoutCheckpoint) {
		t.Fatal("non-zero floor without checkpoint must be rejected (JD11)")
	}
	cp := &Checkpoint{Cursors: CursorVector{"r1": 4}}
	if f, err := ResolveReplayFloor("r1", 5, cp); err != nil || f != 5 {
		t.Fatalf("matching checkpoint cursor 4 must allow floor 5, got %d err=%v", f, err)
	}
	if _, err := ResolveReplayFloor("r1", 6, cp); !errors.Is(err, ErrNonZeroFloorWithoutCheckpoint) {
		t.Fatal("floor not equal to checkpoint cursor+1 must be rejected")
	}
}

func TestInApplyOrder(t *testing.T) {
	events := []Event{
		{RootID: "r", EventSeq: 3, GenerationNumber: 2, IntraGenerationSeq: 1},
		{RootID: "r", EventSeq: 1, GenerationNumber: 1, IntraGenerationSeq: 1},
		{RootID: "r", EventSeq: 2, GenerationNumber: 1, IntraGenerationSeq: 2},
	}
	got := InApplyOrder(events)
	want := []int64{1, 2, 3}
	for i, e := range got {
		if e.EventSeq != want[i] {
			t.Fatalf("apply order mismatch at %d: got seq %d want %d", i, e.EventSeq, want[i])
		}
	}
}
