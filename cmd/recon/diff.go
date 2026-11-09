package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AndreMuraro/recon/internal/connect"
	"github.com/AndreMuraro/recon/internal/dialect"
	"github.com/AndreMuraro/recon/internal/diff"
	"github.com/AndreMuraro/recon/internal/report"
	"github.com/AndreMuraro/recon/internal/schema"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

const diffUsage = `Usage: recon diff --left URL --right URL --table NAME --key COLUMN [flags]

Compares a table on one engine with a table on another, where they are, and reports
the rows that differ. Columns are paired by name, ignoring case.

Exit codes: 0 the tables are equal, 1 they differ, 3 no difference was found but a
limit left part of the tables uncompared, 2 an error.

Connection URLs:
  postgres://user@host:5432/db?sslmode=require   (the password from PGPASSWORD)
  duckdb:///path/to/file.duckdb

Flags:
`

// diffFlags are the parsed flags of recon diff.
type diffFlags struct {
	left, right                  string
	table, leftTable, rightTable string
	key                          string
	exclude                      listFlag
	tolerances                   listFlag
	format, output               string
	show                         int
	maxDifferences               int
	maxRowsFetched               int64
	concurrency                  int
	timeout                      time.Duration
}

// listFlag is a flag that may be repeated, each value also split at commas.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for s := range strings.SplitSeq(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// parseTolerances reads --tolerance values: column=spec, where spec is abs:X, rel:X or
// both joined by a plus sign, abs:X+rel:Y.
func parseTolerances(specs []string) (map[string]tolerance.Tolerance, error) {
	out := make(map[string]tolerance.Tolerance, len(specs))
	for _, spec := range specs {
		col, bounds, ok := strings.Cut(spec, "=")
		if !ok || col == "" {
			return nil, fmt.Errorf("--tolerance %q: want column=abs:X, column=rel:X or column=abs:X+rel:Y", spec)
		}
		var tol tolerance.Tolerance
		for b := range strings.SplitSeq(bounds, "+") {
			kind, value, _ := strings.Cut(b, ":")
			v, err := strconv.ParseFloat(value, 64)
			switch {
			case err != nil:
				return nil, fmt.Errorf("--tolerance %q: %q is not a number", spec, value)
			case kind == "abs":
				tol.Abs = v
			case kind == "rel":
				tol.Rel = v
			default:
				return nil, fmt.Errorf("--tolerance %q: %q must be abs or rel", spec, kind)
			}
		}
		if err := tol.Validate(); err != nil {
			return nil, fmt.Errorf("--tolerance %q: %w", spec, err)
		}
		if tol.IsExact() {
			return nil, fmt.Errorf("--tolerance %q: a zero tolerance is the default; leave it out", spec)
		}
		if _, dup := out[col]; dup {
			return nil, fmt.Errorf("--tolerance: %q is given twice", col)
		}
		out[col] = tol
	}
	return out, nil
}

func newDiffFlags(stderr io.Writer) (*flag.FlagSet, *diffFlags) {
	f := &diffFlags{}
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, diffUsage)
		fs.PrintDefaults()
	}
	fs.StringVar(&f.left, "left", "", "connection `URL` of the left engine")
	fs.StringVar(&f.right, "right", "", "connection `URL` of the right engine")
	fs.StringVar(&f.table, "table", "", "the table on both sides, as table, schema.table or catalog.schema.table")
	fs.StringVar(&f.leftTable, "left-table", "", "the table on the left, when its name differs")
	fs.StringVar(&f.rightTable, "right-table", "", "the table on the right, when its name differs")
	fs.StringVar(&f.key, "key", "", "the key `column`, an integer unique on each side")
	fs.Var(&f.exclude, "exclude", "`columns` not to compare (repeatable, or comma-separated)")
	fs.Var(&f.tolerances, "tolerance", "a float column's tolerance: `column=abs:X`, column=rel:X or column=abs:X+rel:Y (repeatable)")
	fs.StringVar(&f.format, "format", "text", "report `format`: text or json")
	fs.StringVar(&f.output, "output", "-", "write the report to this `file` instead of standard output")
	fs.IntVar(&f.show, "show", 50, "differences listed in a text report; -1 for all")
	fs.IntVar(&f.maxDifferences, "max-differences", 10_000, "stop after this many differences")
	fs.Int64Var(&f.maxRowsFetched, "max-rows-fetched", 100_000, "fetch at most this many rows, both sides together")
	fs.IntVar(&f.concurrency, "concurrency", 4, "key ranges searched at once")
	fs.DurationVar(&f.timeout, "timeout", 0, "give up after this `duration`, such as 10m (0: no limit)")
	return fs, f
}

// check validates the flags, returning the table name on each side.
func (f *diffFlags) check() (left, right []string, tols map[string]tolerance.Tolerance, err error) {
	var missing []string
	for _, r := range []struct{ name, value string }{{"--left", f.left}, {"--right", f.right}, {"--key", f.key}} {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	lt, rt := cmp.Or(f.leftTable, f.table), cmp.Or(f.rightTable, f.table)
	if lt == "" || rt == "" {
		missing = append(missing, "--table (or --left-table and --right-table)")
	}
	if len(missing) > 0 {
		return nil, nil, nil, fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	if f.format != "text" && f.format != "json" {
		return nil, nil, nil, fmt.Errorf("--format %q: want text or json", f.format)
	}
	if f.maxDifferences < 1 || f.maxRowsFetched < 1 || f.concurrency < 1 {
		return nil, nil, nil, errors.New("--max-differences, --max-rows-fetched and --concurrency must be positive")
	}
	if left, err = connect.ParseTable(lt); err != nil {
		return nil, nil, nil, err
	}
	if right, err = connect.ParseTable(rt); err != nil {
		return nil, nil, nil, err
	}
	for _, u := range []string{f.left, f.right} {
		if _, perr := connect.Parse(u); perr != nil {
			return nil, nil, nil, perr
		}
	}
	tols, err = parseTolerances(f.tolerances)
	return left, right, tols, err
}

func runDiff(ctx context.Context, d *deps, args []string, stdout, stderr io.Writer) int {
	fs, f := newDiffFlags(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "recon diff: unexpected argument %q\n", fs.Arg(0))
		return exitError
	}
	leftTable, rightTable, tols, err := f.check()
	if err != nil {
		fmt.Fprintf(stderr, "recon diff: %v\n\nRun recon diff -h for help.\n", err)
		return exitError
	}
	if f.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}

	start := d.now()
	rep, err := compare(ctx, d, f, leftTable, rightTable, tols)
	if err != nil {
		fmt.Fprintf(stderr, "recon diff: %v\n", err)
		return exitError
	}
	rep.Elapsed = d.now().Sub(start)

	if err = writeReport(d, f, rep, stdout); err != nil {
		fmt.Fprintf(stderr, "recon diff: writing the report: %v\n", err)
		return exitError
	}
	switch rep.Status() {
	case report.Equal:
		return exitOK
	case report.Differs:
		return exitDiffers
	case report.Incomplete:
		return exitIncomplete
	}
	return exitError
}

// side is one table, connected and read.
type side struct {
	conn  *connect.Conn
	table *schema.Table
}

func open(ctx context.Context, d *deps, rawURL string, table []string) (*side, error) {
	conn, err := d.open(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	t, err := schema.Introspect(ctx, conn.Querier, conn.Dialect, table)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &side{conn: conn, table: t}, nil
}

func (s *side) name() string { return s.conn.Engine + ":" + strings.Join(s.table.Name, ".") }

func compare(ctx context.Context, d *deps, f *diffFlags, lt, rt []string, tols map[string]tolerance.Tolerance) (*report.Report, error) {
	left, err := open(ctx, d, f.left, lt)
	if err != nil {
		return nil, fmt.Errorf("left: %w", err)
	}
	defer func() { _ = left.conn.Close() }()
	right, err := open(ctx, d, f.right, rt)
	if err != nil {
		return nil, fmt.Errorf("right: %w", err)
	}
	defer func() { _ = right.conn.Close() }()

	plan, err := schema.Match(left.table, right.table, left.conn.Dialect, right.conn.Dialect,
		&schema.Options{Key: f.key, Exclude: f.exclude, Tolerances: tols})
	if err != nil {
		return nil, err
	}
	ls, err := dialect.NewSegmenter(left.conn.Dialect, lt, plan.Encoder, plan.Left)
	if err != nil {
		return nil, err
	}
	rs, err := dialect.NewSegmenter(right.conn.Dialect, rt, plan.Encoder, plan.Right)
	if err != nil {
		return nil, err
	}
	res, err := diff.Diff(ctx, plan.Encoder,
		diff.NewSQLSide(left.conn.Querier, ls), diff.NewSQLSide(right.conn.Querier, rs),
		diff.Options{MaxDifferences: f.maxDifferences, MaxRowsFetched: f.maxRowsFetched, Concurrency: f.concurrency})
	if err != nil {
		return nil, err
	}
	return &report.Report{Left: left.name(), Right: right.name(), Columns: plan.Encoder.Columns(), Result: res}, nil
}

// writeReport writes the report to --output, and when that is a file, the summary line
// to stdout as well.
func writeReport(d *deps, f *diffFlags, rep *report.Report, stdout io.Writer) (err error) {
	w := stdout
	if f.output != "-" {
		file, cerr := d.create(f.output)
		if cerr != nil {
			return cerr
		}
		defer func() {
			if cerr := file.Close(); err == nil {
				err = cerr
			}
		}()
		w = file
		if serr := report.Text(stdout, rep, 0); serr != nil {
			return serr
		}
	}
	if f.format == "json" {
		return report.JSON(w, rep)
	}
	return report.Text(w, rep, f.show)
}

// deps are what recon diff takes from the outside world, so tests can replace them.
type deps struct {
	open   connect.Opener
	now    func() time.Time
	create func(name string) (io.WriteCloser, error)
}

var defaultDeps = deps{
	open: connect.Open,
	now:  time.Now,
	create: func(name string) (io.WriteCloser, error) {
		return os.Create(name) //nolint:gosec // the path is the user's own --output
	},
}
