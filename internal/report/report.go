// Package report writes the outcome of a comparison for people (text) and for programs
// (JSON). Values are shown as the canonical encoding defines them, made readable: NULL as
// null, strings unescaped, floats in shortest decimal form, timestamps in microseconds
// since 1970.
package report

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/diff"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// Status is the verdict of a comparison.
type Status string

// The verdicts.
const (
	Equal      Status = "equal"      // proven equal
	Differs    Status = "differs"    // at least one difference found
	Incomplete Status = "incomplete" // no difference found, but a limit left ranges undecided
)

// Report is a comparison to write.
type Report struct {
	Left, Right string         // how each side is named, such as its table
	Columns     []canon.Column // the schema compared, key first
	Result      *diff.Result
	Elapsed     time.Duration
}

// Status returns the verdict.
func (r *Report) Status() Status {
	switch {
	case len(r.Result.Differences) > 0:
		return Differs
	case r.Result.Equal():
		return Equal
	}
	return Incomplete
}

// Summary counts the differences by kind and by column.
type Summary struct {
	Differences int            `json:"differences"`
	OnlyLeft    int            `json:"only_left"`
	OnlyRight   int            `json:"only_right"`
	Changed     int            `json:"changed"`
	Columns     map[string]int `json:"changed_columns"` // differences per changed column
}

// Summary counts the differences found.
func (r *Report) Summary() Summary {
	s := Summary{Differences: len(r.Result.Differences), Columns: make(map[string]int)}
	for i := range r.Result.Differences {
		d := &r.Result.Differences[i]
		switch d.Kind {
		case diff.OnlyLeft:
			s.OnlyLeft++
		case diff.OnlyRight:
			s.OnlyRight++
		case diff.Changed:
			s.Changed++
			for _, c := range d.Columns {
				s.Columns[c]++
			}
		}
	}
	return s
}

// Text writes a summary line, the counts by column, any undecided ranges, and up to
// maxRows differences (all of them when maxRows is negative).
func Text(w io.Writer, r *Report, maxRows int) error {
	tw := &textWriter{w: w}
	res, sum := r.Result, r.Summary()

	tw.printf("status: %s · %s\n", r.Status(), strings.Join(r.headline(&sum), " · "))
	if len(sum.Columns) > 0 {
		names := slices.SortedFunc(maps.Keys(sum.Columns), func(a, b string) int {
			return cmp.Or(cmp.Compare(sum.Columns[b], sum.Columns[a]), cmp.Compare(a, b))
		})
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = n + " " + group(int64(sum.Columns[n]))
		}
		tw.printf("changed columns: %s\n", strings.Join(parts, ", "))
	}
	if res.Truncated {
		tw.printf("stopped at %s differences: more were found and not reported\n", group(int64(len(res.Differences))))
	}
	if len(res.Unresolved) > 0 {
		var rows int64
		for _, u := range res.Unresolved {
			rows += u.LeftRows + u.RightRows
		}
		tw.printf("undecided: %s key ranges (%s rows) disagree but were not compared row by row; raise the limits to resolve them\n",
			group(int64(len(res.Unresolved))), group(rows))
	}

	if len(res.Differences) > 0 {
		tw.printf("\n")
	}
	for i := range res.Differences {
		if maxRows >= 0 && i == maxRows {
			tw.printf("… %s more\n", group(int64(len(res.Differences)-maxRows)))
			break
		}
		r.writeDifference(tw, &res.Differences[i])
	}
	return tw.err
}

// writeDifference writes one line: the kind, the key, and each changed column's values.
func (r *Report) writeDifference(tw *textWriter, d *diff.Difference) {
	tw.printf("%-10s %s=%d", d.Kind, r.Columns[0].Name, d.Key)
	for _, name := range d.Columns {
		c := slices.IndexFunc(r.Columns, func(col canon.Column) bool { return col.Name == name })
		tw.printf("  %s: %s → %s", name, display(r.Columns[c], d.Left[c]), display(r.Columns[c], d.Right[c]))
	}
	tw.printf("\n")
}

func (r *Report) headline(sum *Summary) []string {
	res := r.Result
	parts := []string{fmt.Sprintf("%s left, %s right rows", group(res.Stats.LeftRows), group(res.Stats.RightRows))}
	if sum.Differences > 0 {
		parts = append(parts, fmt.Sprintf("%s differences (%s only left, %s only right, %s changed)",
			group(int64(sum.Differences)), group(int64(sum.OnlyLeft)), group(int64(sum.OnlyRight)), group(int64(sum.Changed))))
	}
	parts = append(parts,
		group(res.Stats.Queries)+" queries",
		group(res.Stats.RowsFetched)+" rows fetched",
		r.Elapsed.Round(time.Millisecond).String())
	return parts
}

type textWriter struct {
	w   io.Writer
	err error
}

func (t *textWriter) printf(format string, args ...any) {
	if t.err == nil {
		_, t.err = fmt.Fprintf(t.w, format, args...)
	}
}

// display renders a comparison text for people: NULL, a quoted string, or the value.
func display(col canon.Column, text string) string {
	v := value(col, text)
	if v == nil {
		return "NULL"
	}
	if col.Type == canon.TypeString {
		return strconv.Quote(*v)
	}
	return *v
}

// value decodes a comparison text: nil for NULL, strings unescaped, and floats in their
// shortest decimal form, which reads back as the same float.
func value(col canon.Column, text string) *string {
	if text == `\N` {
		return nil
	}
	switch {
	case col.Type == canon.TypeString:
		text = unescape(text)
	case col.Type == canon.TypeFloat:
		if f, err := tolerance.ParseExact(text); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			text = strconv.FormatFloat(f, 'g', -1, 64)
		}
	}
	return &text
}

// unescape reverses the canonical escaping of a string: \\ and \|.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// group writes n with thousands separators.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// The JSON document. Field names are part of recon's interface: change them only with a
// new "version".
type (
	jsonReport struct {
		Version     int              `json:"version"`
		Status      Status           `json:"status"`
		Complete    bool             `json:"complete"`
		Left        jsonSide         `json:"left"`
		Right       jsonSide         `json:"right"`
		Key         string           `json:"key"`
		Columns     []string         `json:"columns"`
		Summary     Summary          `json:"summary"`
		Truncated   bool             `json:"truncated"`
		Differences []jsonDifference `json:"differences"`
		Unresolved  []jsonUnresolved `json:"unresolved"`
		Stats       jsonStats        `json:"stats"`
	}
	jsonSide struct {
		Name string `json:"name"`
		Rows int64  `json:"rows"`
	}
	jsonDifference struct {
		Key     int64              `json:"key"`
		Kind    string             `json:"kind"`
		Columns []string           `json:"columns,omitempty"`
		Left    map[string]*string `json:"left,omitempty"`
		Right   map[string]*string `json:"right,omitempty"`
	}
	jsonUnresolved struct {
		Lo        int64 `json:"lo"`
		Hi        int64 `json:"hi"`
		LeftRows  int64 `json:"left_rows"`
		RightRows int64 `json:"right_rows"`
	}
	jsonStats struct {
		Rounds      int     `json:"rounds"`
		Queries     int64   `json:"queries"`
		RowsFetched int64   `json:"rows_fetched"`
		Seconds     float64 `json:"seconds"`
	}
)

// JSON writes the whole report as one JSON document. Values are strings, or null for
// NULL, keyed by column name.
func JSON(w io.Writer, r *Report) error {
	res := r.Result
	names := make([]string, len(r.Columns))
	for i, c := range r.Columns {
		names[i] = c.Name
	}
	doc := jsonReport{
		Version:     1,
		Status:      r.Status(),
		Complete:    res.Complete(),
		Left:        jsonSide{Name: r.Left, Rows: res.Stats.LeftRows},
		Right:       jsonSide{Name: r.Right, Rows: res.Stats.RightRows},
		Key:         names[0],
		Columns:     names,
		Summary:     r.Summary(),
		Truncated:   res.Truncated,
		Differences: make([]jsonDifference, len(res.Differences)),
		Unresolved:  make([]jsonUnresolved, len(res.Unresolved)),
		Stats: jsonStats{
			Rounds:      res.Stats.Rounds,
			Queries:     res.Stats.Queries,
			RowsFetched: res.Stats.RowsFetched,
			Seconds:     r.Elapsed.Seconds(),
		},
	}
	for i := range res.Differences {
		d := &res.Differences[i]
		doc.Differences[i] = jsonDifference{
			Key: d.Key, Kind: d.Kind.String(), Columns: d.Columns,
			Left: r.values(d.Left), Right: r.values(d.Right),
		}
	}
	for i, u := range res.Unresolved {
		doc.Unresolved[i] = jsonUnresolved{Lo: u.Lo, Hi: u.Hi, LeftRows: u.LeftRows, RightRows: u.RightRows}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func (r *Report) values(texts []string) map[string]*string {
	if texts == nil {
		return nil
	}
	out := make(map[string]*string, len(texts))
	for i, t := range texts {
		out[r.Columns[i].Name] = value(r.Columns[i], t)
	}
	return out
}
