package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/enginetest"
)

// TestEngineDriverPostgres runs recon diff through the pgx driver, the way it runs in
// production: tables are created with psql (RECON_POSTGRES), then compared over a
// connection URL to the same server (RECON_POSTGRES_URL).
func TestEngineDriverPostgres(t *testing.T) {
	t.Parallel()
	url := os.Getenv("RECON_POSTGRES_URL")
	if url == "" {
		t.Skip("RECON_POSTGRES_URL is not set")
	}
	pg := enginetest.Postgres.Open(t)
	left, right := enginetest.Postgres.Table(t, pg, "recon_driver"), enginetest.Postgres.Table(t, pg, "recon_driver")
	ddl := strings.ReplaceAll(ordersDDL, "VARCHAR", "text")
	if err := pg.Exec(t.Context(), fmt.Sprintf(ddl, left, orders(nil))+fmt.Sprintf(ddl, right, orders(rightChanges))); err != nil {
		t.Fatal(err)
	}
	args := func(rightTable string) []string {
		return []string{
			"diff", "--left", url, "--right", url, "--left-table", left, "--right-table", rightTable,
			"--key", "id", "--tolerance", "score=abs:0.01",
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), &defaultDeps, args(right), &stdout, &stderr); code != exitDiffers {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "3 differences (1 only left, 0 only right, 2 changed)") ||
		!strings.Contains(stdout.String(), "changed    id=10  amount: 10.10 → 999.99") {
		t.Errorf("stdout: %s", stdout.String())
	}

	stdout.Reset()
	if code := run(t.Context(), &defaultDeps, args(left), &stdout, &stderr); code != exitOK {
		t.Fatalf("a table against itself: exit code %d, stderr: %s", code, stderr.String())
	}
}
