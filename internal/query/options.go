package query

// ReadOptions separates independent frozen visibility dimensions (R3-7):
// resource tombstones, deprecated roots, and deleted roots are distinct opt-ins
// and must not be conflated.
type ReadOptions struct {
	IncludeRemoved        bool
	IncludeDeprecatedRoot bool
	IncludeDeletedRoot    bool
}
