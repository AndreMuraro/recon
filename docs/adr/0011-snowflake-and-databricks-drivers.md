# ADR 0011: The Snowflake and Databricks drivers

**Status:** accepted · **Date:** 2026-11-09

## Context

With their dialects in place (ADR 0010), `recon diff` needs drivers to reach Snowflake and
Databricks. ADR 0002 asks each driver to justify itself, and these two are far heavier
than pgx (ADR 0008).

## Decision

**gosnowflake v2 and databricks-sql-go**, the vendors' own Go drivers, registered through
`database/sql` in `cmd/recon/drivers.go`, like pgx. Nothing else imports them.

- **Pure Go.** Every release target still builds with `CGO_ENABLED=0`: Linux, macOS and
  Windows on amd64 and arm64, checked before this change was made.
- **The vendors maintain them,** and they track their services' authentication: key pair,
  SSO and OAuth for Snowflake; personal access tokens and OAuth for Databricks.
- **Credentials come from the environment, like pgx's.** A URL without a password takes it
  from `SNOWFLAKE_PASSWORD` or `DATABRICKS_TOKEN`, unless it names another authenticator
  (`?authenticator=externalbrowser`, `?authType=OauthU2M`). recon builds each driver's
  connection string from the URL: query-escaped parts for gosnowflake, whose parser
  unescapes them, and an explicit port 443 for databricks-sql-go, which requires one.
  Tests check the strings with gosnowflake's own parser, including a password with
  `@ + : % / ?` in it.
- **One error, not three.** Both drivers log failures to stderr as well as returning
  them. recon silences their logs, unless `DATABRICKS_LOG_LEVEL` or Snowflake's
  `tracing=` parameter asks for them, so a failure is reported once, with the URL's
  password redacted.

## Consequences

- **The binary grows from 15 MB to 37 MB, and `go.mod` from 5 to 85 indirect
  dependencies,** mostly the AWS, Azure and Google Cloud SDKs gosnowflake uses for stages
  and Apache Arrow, which both use for results. A build without them would need build
  tags per engine; that's worth doing only if someone needs a smaller binary.
- govulncheck in CI now covers a much larger tree; Dependabot keeps it current.
- **CI can't connect to either service.** What is tested: the dialects' SQL (ADR 0010),
  the connection strings against the drivers' parsers, the drivers' registration, and
  that connection errors never show a credential. A real run against each service is a
  manual check before a release, like the Snowflake conformance script.
