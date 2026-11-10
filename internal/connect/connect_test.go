package connect

import (
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/snowflakedb/gosnowflake/v2"

	"github.com/AndreMuraro/recon/internal/dialect"
)

func TestParseTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{"orders", []string{"orders"}},
		{"public.orders", []string{"public", "orders"}},
		{"main.finance.voyage_pnl", []string{"main", "finance", "voyage_pnl"}},
		{`"My Schema"."a.b"`, []string{"My Schema", "a.b"}},
		{`"say ""hi"""`, []string{`say "hi"`}},
		{`s."T"`, []string{"s", "T"}},
	}
	for _, tt := range tests {
		got, err := ParseTable(tt.in)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("ParseTable(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "a..b", ".a", "a.", `"open`, `"a"b`, `a"b"`, `""`} {
		if _, err := ParseTable(bad); !errors.Is(err, dialect.ErrTableName) {
			t.Errorf("ParseTable(%q): got %v, want %v", bad, err, dialect.ErrTableName)
		}
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	for url, want := range map[string]string{
		"postgres://u@h/db":      "postgres",
		"postgresql://u@h/db":    "postgres",
		"POSTGRES://u@h/db":      "postgres",
		"duckdb:///tmp/x.duckdb": "duckdb",
		"duckdb:local.duckdb":    "duckdb",
	} {
		d, err := Parse(url)
		if err != nil || d.Name() != want {
			t.Errorf("Parse(%q) = %v, %v; want %s", url, d, err, want)
		}
	}
	_, err := Parse("mysql://user:hunter2@h/db")
	if !errors.Is(err, ErrURL) || !strings.Contains(err.Error(), "databricks, duckdb, postgres, postgresql, snowflake") {
		t.Errorf("unknown scheme: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error shows the password: %v", err)
	}
	if _, err := Parse("postgres://u:hunter2@h:port/db"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("unparsable URL: %v", err)
	}
}

func TestDuckDBDataSourceName(t *testing.T) {
	t.Parallel()
	for url, want := range map[string]string{
		"duckdb:///tmp/x.duckdb":               "/tmp/x.duckdb",
		"duckdb:rel/x.duckdb":                  "rel/x.duckdb",
		"duckdb:///tmp/x.duckdb?access_mode=r": "/tmp/x.duckdb?access_mode=r",
	} {
		e, u, err := parse(url)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.dsn(u, url, noEnv); err != nil || got != want {
			t.Errorf("%s: DSN %q, want %q", url, got, want)
		}
	}
}

func TestOpenWithoutDriver(t *testing.T) {
	t.Parallel()
	if slices.Contains(sql.Drivers(), "pgx") {
		t.Skip("this build includes the pgx driver")
	}
	_, err := Open(t.Context(), "postgres://u:hunter2@localhost/db")
	if !errors.Is(err, ErrNoDriver) || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("got %v, want %v without the password", err, ErrNoDriver)
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	if got := Redact("postgres://u:hunter2@h/db"); strings.Contains(got, "hunter2") || !strings.Contains(got, "u:xxxxx@h") {
		t.Errorf("Redact = %q", got)
	}
}

func noEnv(string) string { return "" }

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestSnowflakeDSN(t *testing.T) {
	t.Parallel()
	const pw = "p@ss+w:rd%/?" //nolint:gosec // a made-up password, to test escaping
	tests := []struct {
		url, password, want string
	}{
		{
			url:      "snowflake://ETL@myorg-acct/ANALYTICS/FINANCE?warehouse=WH",
			password: pw,
			want:     "ETL:" + url.QueryEscape(pw) + "@myorg-acct/ANALYTICS/FINANCE?warehouse=WH",
		},
		{
			url:  "snowflake://ETL@myorg-acct/ANALYTICS?authenticator=externalbrowser",
			want: "ETL@myorg-acct/ANALYTICS?authenticator=externalbrowser",
		},
		{
			// The URL's password wins over the environment's.
			url:      "snowflake://ETL:in%20url@acct", //nolint:gosec // a made-up password
			password: "ignored",
			want:     "ETL:in+url@acct",
		},
	}
	for _, tt := range tests {
		e, u, err := parse(tt.url)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.dsn(u, tt.url, env(map[string]string{"SNOWFLAKE_PASSWORD": tt.password}))
		if err != nil || got != tt.want {
			t.Errorf("%s: %q, %v; want %q", tt.url, got, err, tt.want)
			continue
		}
		// gosnowflake reads back the same user and password.
		cfg, err := gosnowflake.ParseDSN(got)
		if err != nil {
			t.Errorf("gosnowflake can't parse %q: %v", got, err)
			continue
		}
		if cfg.User != "ETL" || (tt.password == pw && cfg.Password != pw) {
			t.Errorf("gosnowflake read user %q, password %q", cfg.User, cfg.Password)
		}
	}
	for _, bad := range []string{"snowflake://acct/db", "snowflake://ETL@acct/db"} {
		e, u, err := parse(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.dsn(u, bad, noEnv); !errors.Is(err, ErrURL) {
			t.Errorf("%s: got %v, want %v", bad, err, ErrURL)
		}
	}
}

func TestDatabricksDSN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url, token, want string
	}{
		{
			url:   "databricks://adb-1.2.azuredatabricks.net/sql/1.0/warehouses/abc?catalog=main",
			token: "dapi123",
			want:  "token:dapi123@adb-1.2.azuredatabricks.net:443/sql/1.0/warehouses/abc?catalog=main",
		},
		{
			url:  "databricks://host:8443/sql/1.0/warehouses/abc?authType=OauthU2M",
			want: "host:8443/sql/1.0/warehouses/abc?authType=OauthU2M",
		},
	}
	for _, tt := range tests {
		e, u, err := parse(tt.url)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.dsn(u, tt.url, env(map[string]string{"DATABRICKS_TOKEN": tt.token}))
		if err != nil || got != tt.want {
			t.Errorf("%s: %q, %v; want %q", tt.url, got, err, tt.want)
		}
	}
	for _, bad := range []string{"databricks://host", "databricks://host/sql/1.0/warehouses/abc"} {
		e, u, err := parse(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.dsn(u, bad, noEnv); !errors.Is(err, ErrURL) {
			t.Errorf("%s: got %v, want %v", bad, err, ErrURL)
		}
	}
}
