package main

// The database/sql drivers this build includes, registered by importing them
// (ADR 0007, ADR 0008, ADR 0011). internal/connect looks them up by name.

import (
	"os"

	_ "github.com/databricks/databricks-sql-go" // "databricks": Databricks SQL warehouses, pure Go
	dbsqllog "github.com/databricks/databricks-sql-go/logger"
	_ "github.com/jackc/pgx/v5/stdlib"      // "pgx": PostgreSQL, pure Go
	"github.com/snowflakedb/gosnowflake/v2" // "snowflake": Snowflake, pure Go
)

// recon reports errors itself, once, so the drivers' own logs to stderr stay quiet
// unless asked for: DATABRICKS_LOG_LEVEL, or tracing=debug in a Snowflake URL.
func init() {
	_ = gosnowflake.GetLogger().SetLogLevel("fatal")
	if os.Getenv("DATABRICKS_LOG_LEVEL") == "" {
		_ = dbsqllog.SetLogLevel("disabled")
	}
}
