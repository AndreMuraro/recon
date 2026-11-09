# ADR 0007: The diff command

**Status:** accepted · **Date:** 2026-11-09

## Context

The pieces of a comparison exist: schema matching (ADR 0006), the bisection search (ADR
0004) and reports. The command line puts them in front of people, and in front of CI jobs,
which act on exit codes. It also has to reach the engines, which means database drivers.
Those are the heaviest dependencies recon will have, and DuckDB's needs cgo (ADR 0002).

## Decision

**One command, flags only:**

```console
recon diff --left URL --right URL --table NAME --key COLUMN \
    [--left-table NAME] [--right-table NAME] [--exclude COLS] [--tolerance COL=abs:X] \
    [--format text|json] [--output FILE] [--max-differences N] [--max-rows-fetched N]
```

- **The URL's scheme picks the engine:** its SQL dialect and its `database/sql` driver
  (`postgres://…`, `duckdb:///path`). The table is a separate flag, not part of the URL:
  a driver's own URL syntax stays untouched, and the table name is parsed one way for
  every engine (`schema.table`, with double quotes around parts that contain dots).
- **Passwords don't go on the command line.** Each driver's own environment variables
  (`PGPASSWORD` for Postgres) are the documented way. A URL that does carry one is
  redacted in every message and report.
- **Exit codes are results:** 0 equal, 1 differs, 3 incomplete (no difference found,
  but a limit left ranges uncompared, so equality isn't proven), 2 error. A CI job can
  tell "the tables differ" from "recon failed" without parsing output.
- **Reports** go to standard output, or with `--output` to a file, with the summary line
  still on standard output. Text is for people; JSON (versioned) for programs.
- **An interrupt cancels the queries in flight** through the context, rather than leaving
  them running on the warehouse.

**Drivers are compiled in, and a build without one says so.** `internal/connect` maps
schemes to drivers by name and checks `sql.Drivers()` before opening. A missing driver
is an error that names it, not a failed connection. Drivers are added in their own change,
with their own justification (ADR 0002): pgx, which is pure Go, in the default static
build; duckdb-go, which needs cgo, in a separate per-platform build.

**Everything outside the process is injected.** `run` takes the opener, the clock and
the file creator, so the whole command, flags to exit code, is tested in-process against
the engines' CLIs (package enginetest), including a DuckDB table against a Postgres one.

## Consequences

- Until the drivers land, `recon diff` validates its flags and fails at connection with
  a clear message; the tested path runs through the engine CLIs instead.
- `flag` keeps single-dash and double-dash forms equivalent and gives no shell
  completion. That stays acceptable while recon has two commands (ADR 0002).
