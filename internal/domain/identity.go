package domain

// SnapshotIdentity is the frozen four-part IO3 identity tuple
// (kind, namespace, version, value); all four enter uniqueness (doc A C-AS1).
//
// The identity that T8/IO3 compare is the identity of the EVALUATED Snapshot
// semantics, finalized by the Kernel after evaluation. For Gate 2 the final
// form is the DETERMINISTIC_DIGEST; a native whole-scope token is provenance
// only and MUST NOT bypass Kernel-derived decision evidence (doc A Sec 3.8,
// doc E Sec 2.4.3).
type SnapshotIdentity struct {
	Kind      SnapshotIdentityKind
	Namespace string
	Version   string
	Value     string
}

// Equal reports whether two identities are the same IO3 identity tuple.
func (i SnapshotIdentity) Equal(other SnapshotIdentity) bool {
	return i.Kind == other.Kind &&
		i.Namespace == other.Namespace &&
		i.Version == other.Version &&
		i.Value == other.Value
}