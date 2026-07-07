// Command checksum (App 1) recursively hashes every file under a root directory
// whose extension matches an allow-list, writing a deterministic CSV manifest.
//
// Exit codes:
//
//	0  completed; every matched file hashed successfully
//	1  fatal config/setup error; no manifest produced
//	2  manifest produced, but one or more files could not be read
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/manuxio/file-comparer/internal/csvschema"
	"github.com/manuxio/file-comparer/internal/scan"
)

// stringList collects repeated --ext flags.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("checksum", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var exts stringList
	root := fs.String("root", envOr("SCAN_ROOT", "/mnt/data"), "directory tree to scan")
	fs.Var(&exts, "ext", "file extension to include (repeatable), e.g. --ext .php")
	output := fs.String("output", os.Getenv("OUTPUT"), "output CSV path (required)")
	algo := fs.String("algo", envOr("ALGO", csvschema.DefaultAlgo), "hash algorithm: "+csvschema.SupportedAlgos())
	follow := fs.Bool("follow-symlinks", envBool("FOLLOW_SYMLINKS"), "follow symlinks to regular files")
	failFast := fs.Bool("fail-fast", envBool("FAIL_FAST"), "abort on the first unreadable file")
	maxDepth := fs.Int("max-depth", envInt("MAX_DEPTH", 0), "max directory levels below root to descend (0 = unlimited; root entries are depth 1)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	// Extensions: repeated flags win; otherwise EXTENSIONS (comma-separated).
	extList := []string(exts)
	if len(extList) == 0 {
		if env := os.Getenv("EXTENSIONS"); env != "" {
			extList = strings.Split(env, ",")
		}
	}

	if *output == "" {
		fmt.Fprintln(stderr, "error: no output file (set --output or OUTPUT)")
		return 1
	}
	if len(extList) == 0 {
		fmt.Fprintln(stderr, "error: no extensions provided (set --ext or EXTENSIONS)")
		return 1
	}
	if !csvschema.IsSupportedAlgo(*algo) {
		fmt.Fprintf(stderr, "error: unsupported algorithm %q (supported: %s)\n", *algo, csvschema.SupportedAlgos())
		return 1
	}
	if *maxDepth < 0 {
		fmt.Fprintf(stderr, "error: --max-depth must be >= 0 (got %d)\n", *maxDepth)
		return 1
	}

	start := time.Now()
	res, err := scan.Run(scan.Options{
		Root:           *root,
		Exts:           extList,
		Algo:           *algo,
		FollowSymlinks: *follow,
		FailFast:       *failFast,
		MaxDepth:       *maxDepth,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if err := writeManifest(*output, *algo, res); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	// Surface every per-file read error explicitly.
	for _, fe := range res.Errors {
		fmt.Fprintf(stderr, "read error: %s: %v\n", fe.Path, fe.Err)
	}
	// Surface directories not fully scanned because of the depth limit, so
	// limited coverage is never silent.
	for _, p := range res.DepthPruned {
		fmt.Fprintf(stderr, "depth-limit: not descended (max-depth=%d): %s\n", *maxDepth, p)
	}

	depth := "unlimited"
	if *maxDepth > 0 {
		depth = strconv.Itoa(*maxDepth)
	}
	fmt.Fprintf(stderr, "checksum: matched=%d hashed=%d errored=%d skipped=%d max-depth=%s depth-pruned=%d elapsed=%s output=%s\n",
		res.Matched, len(res.Records), res.Errored, res.Skipped, depth, len(res.DepthPruned),
		time.Since(start).Round(time.Millisecond), *output)

	if res.Errored > 0 {
		fmt.Fprintf(stderr, "completed with %d read error(s); manifest is incomplete\n", res.Errored)
		return 2
	}
	return 0
}

func writeManifest(path, algo string, res *scan.Result) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create output directory %q: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create output file %q: %w", path, err)
	}
	if err := csvschema.WriteManifest(f, algo, res.Records); err != nil {
		f.Close()
		return fmt.Errorf("writing manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing output: %w", err)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
