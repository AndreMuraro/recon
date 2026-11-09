package main

// The database/sql drivers this build includes, registered by importing them
// (ADR 0007, ADR 0008). internal/connect looks them up by name.
import (
	_ "github.com/jackc/pgx/v5/stdlib" // "pgx": PostgreSQL, pure Go
)
