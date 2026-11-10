package main

import (
	"database/sql"
	"slices"
	"testing"
)

// TestDriversRegistered checks that this build registers the driver of every engine
// internal/connect can open, under the name it looks up.
func TestDriversRegistered(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pgx", "snowflake", "databricks"} {
		if !slices.Contains(sql.Drivers(), name) {
			t.Errorf("driver %q is not registered; registered: %v", name, sql.Drivers())
		}
	}
}
