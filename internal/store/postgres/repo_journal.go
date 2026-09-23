package postgres

import (
	"context"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// NextEventSeq returns COALESCE(MAX(event_seq),0)+1 per root, assigned inside the
// reconcile transaction (doc D Sec 3).
func (s *Store) NextEventSeq(ctx context.Context, q Querier, rootID string) (int64, error) {
	var n *int64
	if err := q.QueryRow(ctx,
		`SELECT max(event_seq) FROM index_journal_event WHERE root_id = $1::uuid`, rootID).Scan(&n); err != nil {
		return 0, err
	}
	if n == nil {
		return 1, nil
	}
	return *n + 1, nil
}

// NextIntraGenerationSeq returns MAX(intra_generation_seq)+1 for a
// (root, generation). A newly created generation yields 1; a J6 Journal-repair
// append appends after the existing maximum (doc D Sec 6/8.2).
func (s *Store) NextIntraGenerationSeq(ctx context.Context, q Querier, rootID string, generation int64) (int32, error) {
	var n *int32
	if err := q.QueryRow(ctx,
		`SELECT max(intra_generation_seq) FROM index_journal_event
		  WHERE root_id = $1::uuid AND generation_number = $2`, rootID, generation).Scan(&n); err != nil {
		return 0, err
	}
	if n == nil {
		return 1, nil
	}
	return *n + 1, nil
}

// AppendJournalEvent appends one event with caller-assigned ordering columns.
// The table is append-only (trigger + role policy); events are never updated (C-J3).
func (s *Store) AppendJournalEvent(ctx context.Context, q Querier, e domain.JournalEvent) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_journal_event(
		     root_id, event_seq, generation_number, intra_generation_seq, event_type, resource_id, payload)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7)`,
		e.RootID, e.EventSeq, e.GenerationNumber, e.IntraGenerationSeq, string(e.EventType), e.ResourceID, e.Payload)
	return err
}

// ReadJournal returns per-root events with event_seq > afterSeq ordered by
// event_seq, the authoritative per-root cursor (doc D U1/U2, doc C JC2/JC3).
func (s *Store) ReadJournal(ctx context.Context, q Querier, rootID string, afterSeq int64, limit int) ([]domain.JournalEvent, error) {
	rows, err := q.Query(ctx,
		`SELECT root_id::text, event_seq, event_id, generation_number, intra_generation_seq,
		        event_type, resource_id::text, payload, committed_at
		   FROM index_journal_event
		  WHERE root_id = $1::uuid AND event_seq > $2
		  ORDER BY event_seq
		  LIMIT $3`, rootID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.JournalEvent
	for rows.Next() {
		var (
			e         domain.JournalEvent
			eventType string
		)
		if err := rows.Scan(&e.RootID, &e.EventSeq, &e.EventID, &e.GenerationNumber, &e.IntraGenerationSeq,
			&eventType, &e.ResourceID, &e.Payload, &e.CommittedAt); err != nil {
			return nil, err
		}
		e.EventType = domain.EventType(eventType)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MaxEventSeq returns the per-root journal tail (0 if empty).
func (s *Store) MaxEventSeq(ctx context.Context, q Querier, rootID string) (int64, error) {
	var n *int64
	if err := q.QueryRow(ctx,
		`SELECT max(event_seq) FROM index_journal_event WHERE root_id = $1::uuid`, rootID).Scan(&n); err != nil {
		return 0, err
	}
	if n == nil {
		return 0, nil
	}
	return *n, nil
}
