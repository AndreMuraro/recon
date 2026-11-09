// Command recon proves that a rewritten table matches the original, across warehouses,
// without moving the data. See https://github.com/AndreMuraro/recon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
)

// version is set at release time with -ldflags "-X main.version=...". Builds made with
// go install fall back to the module version recorded in the binary.
var version = ""

// Exit codes. Codes 1 and 3 are results, not errors, that CI jobs act on.
const (
	exitOK         = 0
	exitDiffers    = 1 // the tables differ
	exitError      = 2
	exitIncomplete = 3 // no difference found, but a limit left part of the tables uncompared
)

const usage = `recon proves that a rewritten table matches the original, across warehouses.

Usage:
  recon <command> [flags]

Commands:
  diff      compare a table on one engine with a table on another
  version   print the version
  help      show this help
`

func main() {
	// An interrupt cancels the queries in flight instead of leaving them running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, &defaultDeps, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main without the process: it takes arguments, output streams and the outside
// world's dependencies, and returns the exit code, so the command line is tested
// in-process.
func run(ctx context.Context, d *deps, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitError
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "diff":
		return runDiff(ctx, d, rest, stdout, stderr)
	case "version":
		return runVersion(rest, stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "recon: unknown command %q\n\n%s", cmd, usage)
		return exitError
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "recon version: unexpected argument %q\n", fs.Arg(0))
		return exitError
	}
	fmt.Fprintf(stdout, "recon %s\n", resolveVersion())
	return exitOK
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
