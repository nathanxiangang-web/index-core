package incrementalruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
)

type issue144SQLState string

func (e issue144SQLState) Error() string    { return "SECRET_COOKIE_AND_DATABASE_URL" }
func (e issue144SQLState) SQLState() string { return string(e) }

func TestIssue144CycleFailureFieldsAreFiniteAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		stop                        incrementalexec.StopReason
		class                       state.ErrorClass
		err                         error
		family, sqlstate, wantClass string
	}{
		{"internal", incrementalexec.StopInternalItemFailure, state.ErrorInternal, fmt.Errorf("%w: SECRET_COOKIE_AND_DATABASE_URL", incrementalexec.ErrScannerFailed), "SCANNER_FAILURE", "UNKNOWN", "INTERNAL"},
		{"store", incrementalexec.StopSystemicError, state.ErrorInternal, issue144SQLState("42P01"), "UNCLASSIFIED", "42P01", "INTERNAL"},
		{"completion", incrementalexec.StopSystemicError, state.ErrorInternal, fmt.Errorf("%w: SECRET_COOKIE_AND_DATABASE_URL", incrementalexec.ErrCompletionFailed), "COMPLETION_FAILURE", "UNKNOWN", "INTERNAL"},
		{"cancel", incrementalexec.StopContextCancelled, state.ErrorInternal, context.Canceled, "CONTEXT", "UNKNOWN", "INTERNAL"},
		{"stale", incrementalexec.StopStaleSelection, state.ErrorTransientProvider, incrementalexec.ErrStaleSelection, "STALE_SELECTION", "UNKNOWN", "TRANSIENT_PROVIDER"},
		{"malicious", incrementalexec.StopReason("SECRET_COOKIE_AND_DATABASE_URL"), state.ErrorClass("SECRET_COOKIE_AND_DATABASE_URL"), issue144SQLState("SECRET_COOKIE_AND_DATABASE_URL"), "UNCLASSIFIED", "UNKNOWN", "UNKNOWN"},
		{"unknown", incrementalexec.StopSystemicError, state.ErrorInternal, errors.New("SECRET_COOKIE_AND_DATABASE_URL"), "UNCLASSIFIED", "UNKNOWN", "INTERNAL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := incrementalorch.Result{Executor: incrementalexec.CycleResult{StopReason: tc.stop, Last: incrementalexec.Result{RootID: "PRIVATE_ROOT", ScopeKey: "/PRIVATE_FILENAME", SnapshotID: "PRIVATE_SNAPSHOT", ClaimedSignalSeq: 19, FailureClass: &tc.class}}}
			var log bytes.Buffer
			slog.New(slog.NewJSONHandler(&log, nil)).Error("incremental_cycle_failed", cycleFailureFields(res, tc.err)...)
			for _, secret := range []string{"SECRET_COOKIE_AND_DATABASE_URL", "PRIVATE_ROOT", "PRIVATE_FILENAME", "PRIVATE_SNAPSHOT"} {
				if strings.Contains(log.String(), secret) {
					t.Fatal("sensitive diagnostic leaked")
				}
			}
			var row map[string]any
			if e := json.Unmarshal(log.Bytes(), &row); e != nil {
				t.Fatal(e)
			}
			if row["error_family"] != tc.family || row["sql_state"] != tc.sqlstate || row["failure_class"] != tc.wantClass {
				t.Fatal("wrong typed diagnostic", row)
			}
			if len(row["root_fingerprint"].(string)) != 20 || row["claimed_signal_seq"] != float64(19) {
				t.Fatal("correlation lost", row)
			}
		})
	}
}
