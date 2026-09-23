package postgres

// nullableString returns a database-friendly nullable text value for any typed
// string pointer (enums are persisted as constrained text).
func nullableString[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}