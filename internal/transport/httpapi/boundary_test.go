package httpapi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
)

// G3-R2: the HTTP transport must not receive any raw writable DB capability.
// This API-level test fails if Deps ever regains a pool/store/exec-typed field.
func TestDepsExposeNoWriteCapability(t *testing.T) {
	rt := reflect.TypeOf(httpapi.Deps{})
	banned := []string{"pgxpool", "postgres.Store", "pgx.Conn", "database/sql", "*sql.DB"}
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		ft := field.Type.String()
		for _, b := range banned {
			if strings.Contains(ft, b) {
				t.Fatalf("httpapi.Deps.%s exposes write-capable type %s (Gate 3 G3-R2)", field.Name, ft)
			}
		}
	}
}
