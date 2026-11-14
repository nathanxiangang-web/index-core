package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrRuntimePrivileges = errors.New("runtime database privileges missing")

// CheckRuntimePrivileges is a read-only deployment preflight for the configured
// writer account, not the migration owner. Check once before startup, not per
// cycle. OFF still needs Root registration and covered-reservation completion.
// Keep the explicit grant recipes in scripts/sql in sync (real-role tests).
func CheckRuntimePrivileges(ctx context.Context, q Querier, rolling bool) error {
	var usage bool
	if err := q.QueryRow(ctx, `SELECT has_schema_privilege(current_user,'public','USAGE')`).Scan(&usage); err != nil {
		return err
	}
	if !usage {
		return fmt.Errorf("%w: public.USAGE", ErrRuntimePrivileges)
	}
	tables, privileges := []string{}, []string{}
	add := func(names, perms string) {
		for _, name := range strings.Fields(names) {
			for _, p := range strings.Fields(perms) {
				tables = append(tables, "public."+name)
				privileges = append(privileges, p)
			}
		}
	}
	add("schema_migrations", "SELECT")
	add(`index_generation index_snapshot_entry index_identity_evidence_observation
 index_applied_snapshot index_journal_event index_reconcile_result`, "SELECT INSERT")
	add(`index_root index_canonical_resource index_identity_evidence_current
 index_snapshot index_admission index_root_config index_root_adapter_config
 index_scope_watch_state index_dirty_scope_work index_mirror_root_budget`, "SELECT INSERT UPDATE")
	if rolling {
		add("index_mirror_budget", "SELECT UPDATE")
		add("index_mirror_watch_binding", "SELECT INSERT UPDATE")
	}
	rows, err := q.Query(ctx, `SELECT t||'.'||p FROM unnest($1::text[],$2::text[]) AS required(t,p)
 WHERE NOT has_table_privilege(current_user,t,p) ORDER BY t,p`, tables, privileges)
	if err != nil {
		return err
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return err
		}
		missing = append(missing, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s; apply reviewed runtime grants, then run doctor as the runtime account", ErrRuntimePrivileges, strings.Join(missing, ", "))
	}
	return nil
}
