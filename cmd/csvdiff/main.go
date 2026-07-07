// Command csvdiff (App 2) compares two checksum manifests (a baseline and a
// current) and writes a CSV of the files that were modified, added, or deleted.
//
// Exit codes:
//
//	0  comparison succeeded (0 unless --fail-on-diff and differences exist)
//	1  fatal error (missing/unreadable input file, bad flags, write failure)
//	2  an input CSV failed schema validation
//	3  success and differences found (only when --fail-on-diff is set)
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/manuxio/file-comparer/internal/compare"
	"github.com/manuxio/file-comparer/internal/csvschema"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("csvdiff", flag.ContinueOnError)
	fs.SetOutput(stderr)

	baseline := fs.String("baseline", os.Getenv("BASELINE_CSV"), "baseline (last good backup) manifest CSV (required)")
	current := fs.String("current", os.Getenv("CURRENT_CSV"), "current (suspect) manifest CSV (required)")
	output := fs.String("output", os.Getenv("OUTPUT"), "output changes CSV (required)")
	stripBase := fs.String("strip-baseline-prefix", os.Getenv("STRIP_BASELINE_PREFIX"), "path prefix stripped from baseline paths before matching")
	stripCur := fs.String("strip-current-prefix", os.Getenv("STRIP_CURRENT_PREFIX"), "path prefix stripped from current paths before matching")
	failOnDiff := fs.Bool("fail-on-diff", envBool("FAIL_ON_DIFF"), "exit 3 when any difference is found")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	if *baseline == "" || *current == "" {
		fmt.Fprintln(stderr, "error: both --baseline and --current are required")
		return 1
	}
	if *output == "" {
		fmt.Fprintln(stderr, "error: no output file (set --output or OUTPUT)")
		return 1
	}

	res, err := compare.Run(compare.Options{
		BaselinePath:        *baseline,
		CurrentPath:         *current,
		StripBaselinePrefix: *stripBase,
		StripCurrentPrefix:  *stripCur,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		var ie *compare.InputError
		if errors.As(err, &ie) {
			return 2
		}
		return 1
	}

	if err := writeChanges(*output, res); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "csvdiff: modified=%d added=%d deleted=%d total=%d output=%s\n",
		res.Modified, res.Added, res.Deleted, len(res.Changes), *output)

	if *failOnDiff && res.HasDiff() {
		return 3
	}
	return 0
}

func writeChanges(path string, res *compare.Result) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create output directory %q: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create output file %q: %w", path, err)
	}
	if err := csvschema.WriteChanges(f, res.Changes); err != nil {
		f.Close()
		return fmt.Errorf("writing changes: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing output: %w", err)
	}
	return nil
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
