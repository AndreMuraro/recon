package dialect

import (
	"errors"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
)

func TestColumnsSQL(t *testing.T) {
	t.Parallel()
	for _, d := range dialects {
		for _, tt := range []struct {
			table []string
			want  []string
		}{
			{[]string{"t"}, []string{"table_schema = current_schema()"}},
			{[]string{"s", "t"}, []string{"table_schema = " + d.StringLiteral("s")}},
			{[]string{"c", "s", "t"}, []string{"table_catalog = " + d.StringLiteral("c")}},
		} {
			q, err := ColumnsSQL(d, tt.table)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range append(tt.want, "table_name = "+d.StringLiteral("t")) {
				if !strings.Contains(q, w) {
					t.Errorf("%s %v: %q lacks %q", d.Name(), tt.table, q, w)
				}
			}
			if strings.Contains(strings.ToUpper(q), "ORDER BY") {
				t.Errorf("%s: %q has an ORDER BY", d.Name(), q)
			}
		}
		for _, bad := range [][]string{nil, {"a", "b", "c", "d"}, {""}, {"s", "t\x00"}} {
			if _, err := ColumnsSQL(d, bad); !errors.Is(err, ErrTableName) {
				t.Errorf("%s %q: got %v, want %v", d.Name(), bad, err, ErrTableName)
			}
		}
	}
}

func TestStringLiteral(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    Dialect
		in   string
		want string
	}{
		{DuckDB{}, `it's a \ path`, `'it''s a \ path'`},
		{Postgres{}, `it's a \ path`, `E'it\'s a \\ path'`},
		{Postgres{}, "plain", "E'plain'"},
		{Snowflake{}, `it's a \ path`, `'it\'s a \\ path'`},
		{Databricks{}, `it's a \ path`, `'it\'s a \\ path'`},
	}
	for _, tt := range tests {
		if got := tt.d.StringLiteral(tt.in); got != tt.want {
			t.Errorf("%s.StringLiteral(%q) = %s, want %s", tt.d.Name(), tt.in, got, tt.want)
		}
	}
}

func TestColumnType(t *testing.T) {
	t.Parallel()
	type want struct {
		typ   canon.Type
		scale int32
	}
	dec := func(p, s int32) ColumnInfo { return ColumnInfo{Precision: p, Scale: s, HasScale: true} }
	tests := []struct {
		d    Dialect
		dt   string
		info ColumnInfo
		want want // TypeInvalid: unsupported
	}{
		{DuckDB{}, "BIGINT", ColumnInfo{}, want{canon.TypeInt, 0}},
		{DuckDB{}, "UINTEGER", ColumnInfo{}, want{canon.TypeInt, 0}},
		{DuckDB{}, "UBIGINT", ColumnInfo{}, want{}},
		{DuckDB{}, "HUGEINT", ColumnInfo{}, want{}},
		{DuckDB{}, "DECIMAL(18,2)", dec(18, 2), want{canon.TypeDecimal, 2}},
		{DuckDB{}, "FLOAT", ColumnInfo{}, want{canon.TypeFloat, 0}},
		{DuckDB{}, "TIMESTAMP WITH TIME ZONE", ColumnInfo{}, want{canon.TypeTimestamp, 0}},
		{DuckDB{}, "TIMESTAMP_MS", ColumnInfo{}, want{canon.TypeTimestamp, 0}},
		{DuckDB{}, "TIMESTAMP_NS", ColumnInfo{}, want{}},
		{DuckDB{}, "VARCHAR", ColumnInfo{}, want{canon.TypeString, 0}},
		{DuckDB{}, "UUID", ColumnInfo{}, want{}},
		{DuckDB{}, "INTEGER[]", ColumnInfo{}, want{}},
		{Postgres{}, "integer", ColumnInfo{}, want{canon.TypeInt, 0}},
		{Postgres{}, "numeric", dec(12, 3), want{canon.TypeDecimal, 3}},
		{Postgres{}, "numeric", ColumnInfo{}, want{}}, // no declared scale
		{Postgres{}, "numeric", dec(50, 2), want{}},   // beyond DECIMAL(38)
		{Postgres{}, "double precision", ColumnInfo{}, want{canon.TypeFloat, 0}},
		{Postgres{}, "timestamp without time zone", ColumnInfo{}, want{canon.TypeTimestamp, 0}},
		{Postgres{}, "character", ColumnInfo{}, want{canon.TypeString, 0}},
		{Postgres{}, "jsonb", ColumnInfo{}, want{}},
		{Postgres{}, "ARRAY", ColumnInfo{}, want{}},
		{Snowflake{}, "NUMBER", dec(38, 0), want{canon.TypeInt, 0}},
		{Snowflake{}, "NUMBER", dec(18, 4), want{canon.TypeDecimal, 4}},
		{Snowflake{}, "FLOAT", ColumnInfo{}, want{canon.TypeFloat, 0}},
		{Snowflake{}, "TEXT", ColumnInfo{}, want{canon.TypeString, 0}},
		{Snowflake{}, "TIMESTAMP_TZ", ColumnInfo{}, want{canon.TypeTimestamp, 0}},
		{Snowflake{}, "VARIANT", ColumnInfo{}, want{}},
		{Snowflake{}, "TIME", ColumnInfo{}, want{}},
		{Databricks{}, "INT", ColumnInfo{}, want{canon.TypeInt, 0}},
		{Databricks{}, "DECIMAL", dec(10, 2), want{canon.TypeDecimal, 2}},
		{Databricks{}, "TIMESTAMP_NTZ", ColumnInfo{}, want{canon.TypeTimestamp, 0}},
		{Databricks{}, "STRING", ColumnInfo{}, want{canon.TypeString, 0}},
		{Databricks{}, "MAP", ColumnInfo{}, want{}},
		{Databricks{}, "BINARY", ColumnInfo{}, want{}},
	}
	for _, tt := range tests {
		info := tt.info
		info.Name, info.DataType = "c", tt.dt
		typ, scale, err := tt.d.ColumnType(&info)
		if tt.want.typ == canon.TypeInvalid {
			if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), tt.dt) {
				t.Errorf("%s %s: got %v, want an unsupported-type error naming the type", tt.d.Name(), tt.dt, err)
			}
			continue
		}
		if err != nil || typ != tt.want.typ || scale != tt.want.scale {
			t.Errorf("%s %s: got %v %d %v, want %v %d", tt.d.Name(), tt.dt, typ, scale, err, tt.want.typ, tt.want.scale)
		}
	}
}
