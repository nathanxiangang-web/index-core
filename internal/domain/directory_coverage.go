package domain

// DirectoryCoverage is bounded source-presence evidence, not a claim that an
// entire Root is COMPLETE. Persisted in provenance; revalidated under root lock.
type DirectoryCoverage struct {
	Version        int    `json:"version"`
	Path           string `json:"path"`
	Generation     int64  `json:"generation"`
	AdapterDigest  string `json:"adapter_digest"`
	EntryCount     int    `json:"entry_count"`
	TopologyDigest string `json:"topology_digest"`
}
