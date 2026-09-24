package hintapi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/transport/hintapi"
)

// P9: the Hint transport must hold only the narrow P8 Ingester plus
// logger/token config. It must never regain a direct writable DB/execution
// capability.
func TestP9HintDepsExposeNoWriteCapability(t *testing.T) {
	rt := reflect.TypeOf(hintapi.Deps{})
	banned := []string{
		"pgxpool", "postgres.Store", "pgx.Conn", "database/sql", "*sql.DB",
		"scan.Service", "incrementalexec", "incrementalorch", "Coordinator",
	}
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		ft := field.Type.String()
		for _, b := range banned {
			if strings.Contains(ft, b) {
				t.Fatalf("hintapi.Deps.%s exposes write-capable type %s", field.Name, ft)
			}
		}
	}
}
