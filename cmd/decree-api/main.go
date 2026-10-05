// Command decree-api is the HTTP front door of a decree project (SPEC.md).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jtmckay/decree-api/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: it returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("decree-api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultConfigPath, "the config file")
	check := fs.Bool("check", false, "validate the config and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "decree-api: unexpected arguments: %v\n", fs.Args())
		return 2
	}
	if !*check {
		fmt.Fprintln(stderr, "decree-api: serving is not implemented yet; use -check")
		return 2
	}
	return runCheck(*configPath, stdout, stderr)
}

// runCheck is `decree-api -check`: SPEC.md §3, Validation.
func runCheck(path string, stdout, stderr io.Writer) int {
	c, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "config invalid:\n  %v\n", err)
		return 1
	}
	if errs := config.Validate(c, config.Options{}); len(errs) > 0 {
		fmt.Fprintf(stderr, "config invalid: %d error(s)\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(stderr, "  %v\n", e)
		}
		return 1
	}
	fmt.Fprintf(stdout, "config ok: %d endpoints\n", len(c.Endpoints))
	return 0
}
