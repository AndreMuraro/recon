// Package connect opens a connection to an engine from a URL: the URL's scheme picks the
// SQL dialect and the database/sql driver (ADR 0007, ADR 0011). Drivers are compiled in
// by build; a build without an engine's driver says so instead of failing obscurely.
package connect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/AndreMuraro/recon/internal/dialect"
	"github.com/AndreMuraro/recon/internal/diff"
)

// Errors.
var (
	ErrURL      = errors.New("invalid connection URL")
	ErrNoDriver = errors.New("driver not in this build")
)

// Conn is an open connection to one engine.
type Conn struct {
	Engine  string
	Dialect dialect.Dialect
	Querier diff.Querier
	Close   func() error
}

// Opener opens a connection. Open is the production one; tests substitute their own.
type Opener func(ctx context.Context, rawURL string) (*Conn, error)

// engine is a URL scheme recon understands.
type engine struct {
	dialect dialect.Dialect
	driver  string // database/sql driver name
	// dsn returns the driver's data source name. getenv reads credentials the URL leaves
	// out, from the variables each engine's own tools use.
	dsn func(u *url.URL, raw string, getenv func(string) string) (string, error)
}

func passThrough(_ *url.URL, raw string, _ func(string) string) (string, error) { return raw, nil }

var engines = map[string]engine{
	// pgx reads PGPASSWORD and .pgpass itself.
	"postgres":   {dialect.Postgres{}, "pgx", passThrough},
	"postgresql": {dialect.Postgres{}, "pgx", passThrough},
	// duckdb:///abs/path.duckdb or duckdb:relative/path.duckdb; the query string is
	// passed to the driver as its options.
	"duckdb": {dialect.DuckDB{}, "duckdb", func(u *url.URL, _ string, _ func(string) string) (string, error) {
		path := u.Opaque
		if path == "" {
			path = u.Path
		}
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		return path, nil
	}},
	"snowflake":  {dialect.Snowflake{}, "snowflake", snowflakeDSN},
	"databricks": {dialect.Databricks{}, "databricks", databricksDSN},
}

// snowflakeDSN turns snowflake://user@account/database/schema?warehouse=… into
// gosnowflake's user:password@account/database/schema?warehouse=…. The password comes
// from SNOWFLAKE_PASSWORD unless the URL has one or names another authenticator
// (externalbrowser, SNOWFLAKE_JWT, …).
func snowflakeDSN(u *url.URL, _ string, getenv func(string) string) (string, error) {
	if u.User == nil || u.User.Username() == "" || u.Host == "" {
		return "", fmt.Errorf("%w: a Snowflake URL is snowflake://user@account/database/schema?warehouse=…", ErrURL)
	}
	user := u.User
	if _, hasPassword := user.Password(); !hasPassword && u.Query().Get("authenticator") == "" {
		pw := getenv("SNOWFLAKE_PASSWORD")
		if pw == "" {
			return "", fmt.Errorf("%w: set SNOWFLAKE_PASSWORD, or an authenticator in the URL", ErrURL)
		}
		user = url.UserPassword(user.Username(), pw)
	}
	// gosnowflake splits the DSN at the last '@' and query-unescapes each part, so every
	// part is query-escaped, as its own Config.DSN does.
	var b strings.Builder
	b.WriteString(url.QueryEscape(user.Username()))
	if pw, ok := user.Password(); ok {
		b.WriteString(":" + url.QueryEscape(pw))
	}
	b.WriteString("@" + u.Host)
	for _, part := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if part != "" {
			b.WriteString("/" + url.QueryEscape(part))
		}
	}
	if u.RawQuery != "" {
		b.WriteString("?" + u.RawQuery)
	}
	return b.String(), nil
}

// databricksDSN turns databricks://host/sql/1.0/warehouses/id?catalog=… into
// databricks-sql-go's token:TOKEN@host:443/sql/1.0/warehouses/id?catalog=…. The token
// comes from DATABRICKS_TOKEN unless the URL has one or names another authType (OAuth).
func databricksDSN(u *url.URL, _ string, getenv func(string) string) (string, error) {
	if u.Hostname() == "" || u.Path == "" || u.Path == "/" {
		return "", fmt.Errorf("%w: a Databricks URL is databricks://host/sql/1.0/warehouses/id, the warehouse's HTTP path", ErrURL)
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}
	user := u.User
	if _, hasToken := user.Password(); !hasToken && u.Query().Get("authType") == "" {
		token := getenv("DATABRICKS_TOKEN")
		if token == "" {
			return "", fmt.Errorf("%w: set DATABRICKS_TOKEN, or an authType in the URL", ErrURL)
		}
		user = url.UserPassword("token", token)
	}
	return strings.TrimPrefix((&url.URL{User: user, Host: host, Path: u.Path, RawQuery: u.RawQuery}).String(), "//"), nil
}

// Schemes lists the URL schemes recon understands.
func Schemes() []string {
	out := make([]string, 0, len(engines))
	for s := range engines {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Parse checks rawURL and returns its engine's dialect, without connecting.
func Parse(rawURL string) (dialect.Dialect, error) {
	e, _, err := parse(rawURL)
	if err != nil {
		return nil, err
	}
	return e.dialect, nil
}

func parse(rawURL string) (engine, *url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		// url.Error repeats the URL, password included.
		return engine{}, nil, fmt.Errorf("%w: it doesn't parse as a URL", ErrURL)
	}
	e, ok := engines[strings.ToLower(u.Scheme)]
	if !ok {
		return engine{}, nil, fmt.Errorf("%w: %s: the scheme must be one of %s", ErrURL, Redact(rawURL), strings.Join(Schemes(), ", "))
	}
	return e, u, nil
}

// Open connects through the engine's database/sql driver and checks the connection.
func Open(ctx context.Context, rawURL string) (*Conn, error) {
	e, u, err := parse(rawURL)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(sql.Drivers(), e.driver) {
		return nil, fmt.Errorf("%w: %s needs the %s driver, which this build of recon doesn't include (ADR 0007)",
			ErrNoDriver, e.dialect.Name(), e.driver)
	}
	dsn, err := e.dsn(u, rawURL, os.Getenv)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(e.driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Redact(rawURL), err)
	}
	if perr := db.PingContext(ctx); perr != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to %s: %w", Redact(rawURL), perr)
	}
	return &Conn{Engine: e.dialect.Name(), Dialect: e.dialect, Querier: diff.NewDB(db), Close: db.Close}, nil
}

// Redact returns rawURL with any password replaced, for messages and reports.
func Redact(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparsable URL)"
	}
	return u.Redacted()
}

// ParseTable splits a table name into its parts at dots outside double quotes. Inside
// quotes, a doubled quote stands for one. Parts are kept as written: engines store
// unquoted names in their own case, so give them as stored.
func ParseTable(name string) ([]string, error) {
	var (
		parts  []string
		cur    strings.Builder
		quoted bool // inside a quoted part
		closed bool // the current part was quoted and has ended
	)
	bad := func(why string) ([]string, error) {
		return nil, fmt.Errorf("%w %q: %s", dialect.ErrTableName, name, why)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case quoted && c == '"' && i+1 < len(name) && name[i+1] == '"':
			cur.WriteByte('"')
			i++
		case quoted && c == '"':
			quoted, closed = false, true
		case quoted:
			cur.WriteByte(c)
		case c == '.':
			parts = append(parts, cur.String())
			cur.Reset()
			closed = false
		case c == '"' && cur.Len() == 0 && !closed:
			quoted = true
		case c == '"' || closed:
			return bad("a quote may only enclose a whole part")
		default:
			cur.WriteByte(c)
		}
	}
	if quoted {
		return bad("unterminated quote")
	}
	parts = append(parts, cur.String())
	if slices.Contains(parts, "") {
		return bad("empty part")
	}
	return parts, nil
}
