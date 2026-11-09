# ADR 0008: The PostgreSQL driver

**Status:** accepted · **Date:** 2026-11-09

## Context

`recon diff` reaches engines through `database/sql` drivers compiled into the binary
(ADR 0007). ADR 0002 asks that each driver come with its own justification: drivers are
recon's largest dependencies, and the release is a static binary for six platforms.

## Decision

**pgx v5, through its `database/sql` adapter (`github.com/jackc/pgx/v5/stdlib`),**
registered as `"pgx"` by a blank import in `cmd/recon/drivers.go`.

- **Pure Go.** It builds with `CGO_ENABLED=0`, so the static releases are unchanged.
- **Maintained and standard.** It is the driver the Go community recommends for
  Postgres; `lib/pq`, the older choice, is in maintenance mode and points to pgx.
- **Connection settings the way Postgres users expect.** pgx reads libpq's environment
  variables (`PGPASSWORD`, `PGSSLMODE`, …), `.pgpass` and service files, and takes the
  same URLs as psql. So the password never needs to be on the command line.
- **Small tree.** pgx v5.12.0 (MIT) brings `pgpassfile`, `pgservicefile`, `puddle`
  (its pool, not used by the adapter but linked), `golang.org/x/sync` and
  `golang.org/x/text`. govulncheck covers them in CI, and Dependabot proposes updates.
- **Only `database/sql` is used.** Nothing outside `cmd/recon/drivers.go` imports pgx;
  the queries return text only (ADR 0004), so no pgx type ever reaches recon's code.

**Tested the way it runs.** In CI, the Postgres job sets `RECON_POSTGRES_URL` and runs
`recon diff` end to end through the driver, against tables created with psql, on every
supported major version.

## Consequences

- The binary grows by about 4 MB.
- pgx v5.12 needs Go 1.25 or later, inside recon's support window (Go 1.26 and 1.27).
- DuckDB's driver needs cgo. It stays out of the default build, as ADR 0002 planned.
