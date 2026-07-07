// Package scan implements App 1's core: walk a directory tree, filter files by
// extension, hash the matching ones, and produce a deterministic (path-sorted)
// set of records. Per-file read errors are collected and surfaced, never
// silently dropped.
package scan

import (
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/manuxio/file-comparer/internal/csvschema"
)

// fsOpen is the file opener. It is a package variable so tests can substitute it
// to simulate read errors in a cross-platform way.
var fsOpen = os.Open

// Options configures a scan.
type Options struct {
	Root           string
	Exts           []string // matched case-insensitively; normalized internally
	Algo           string
	FollowSymlinks bool
	FailFast       bool
	Workers        int // <= 0 means runtime.NumCPU()
}

// FileError records a file that could not be read or hashed.
type FileError struct {
	Path string
	Err  error
}

// Result is the outcome of a scan.
type Result struct {
	Records []csvschema.Record // successfully hashed, sorted by absolute path
	Matched int                // files whose extension matched
	Errored int                // matched files (or paths) that failed
	Skipped int                // symlinks / non-regular files not hashed
	Errors  []FileError        // one per Errored file
}

// NormalizeExt returns ext lowercased and with exactly one leading dot. It
// returns "" for an empty input.
func NormalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// Run walks opts.Root and hashes every file whose extension matches. It returns
// a fatal error only for setup problems (empty/missing root, not a directory,
// no extensions, unsupported algorithm). Per-file read errors are collected in
// Result.Errors and do not abort the scan unless FailFast is set.
func Run(opts Options) (*Result, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("scan root is empty")
	}
	if len(opts.Exts) == 0 {
		return nil, fmt.Errorf("no extensions provided")
	}
	if _, err := csvschema.NewHash(opts.Algo); err != nil {
		return nil, err
	}

	absRoot, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("resolving scan root %q: %w", opts.Root, err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("cannot access scan root %q: %w", absRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root %q is not a directory", absRoot)
	}

	extSet := make(map[string]bool, len(opts.Exts))
	for _, e := range opts.Exts {
		if n := NormalizeExt(e); n != "" {
			extSet[n] = true
		}
	}

	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	var (
		res     = &Result{}
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, workers)
		tripped atomic.Bool // set when FailFast hits its first error
	)

	addErr := func(path string, e error) {
		mu.Lock()
		res.Errored++
		res.Errors = append(res.Errors, FileError{Path: path, Err: e})
		mu.Unlock()
		if opts.FailFast {
			tripped.Store(true)
		}
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if tripped.Load() {
			return fs.SkipAll
		}
		if err != nil {
			// Error accessing this entry itself (e.g. unreadable directory).
			addErr(path, err)
			if tripped.Load() {
				return fs.SkipAll
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir // can't descend; keep going elsewhere
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			if !opts.FollowSymlinks {
				mu.Lock()
				res.Skipped++
				mu.Unlock()
				return nil
			}
			ti, terr := os.Stat(path) // follows the link
			if terr != nil {
				addErr(path, terr)
				if tripped.Load() {
					return fs.SkipAll
				}
				return nil
			}
			if !ti.Mode().IsRegular() {
				mu.Lock()
				res.Skipped++
				mu.Unlock()
				return nil
			}
		case !d.Type().IsRegular():
			// devices, sockets, named pipes, etc.
			mu.Lock()
			res.Skipped++
			mu.Unlock()
			return nil
		}

		if !extSet[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		mu.Lock()
		res.Matched++
		mu.Unlock()

		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, herr := hashFile(p, opts.Algo)
			if herr != nil {
				addErr(p, herr)
				return
			}
			mu.Lock()
			res.Records = append(res.Records, rec)
			mu.Unlock()
		}(path)

		return nil
	})

	wg.Wait()

	// WalkDir returns nil for the SkipAll/SkipDir sentinels, so any non-nil
	// error here is a genuine walk failure.
	if walkErr != nil {
		return nil, fmt.Errorf("walking %q: %w", absRoot, walkErr)
	}

	sort.Slice(res.Records, func(i, j int) bool {
		return res.Records[i].AbsolutePath < res.Records[j].AbsolutePath
	})
	return res, nil
}

// hashFile opens, stats, and hashes a single file, streaming it through the hash
// so memory use stays constant regardless of file size.
func hashFile(path, algo string) (csvschema.Record, error) {
	f, err := fsOpen(path)
	if err != nil {
		return csvschema.Record{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return csvschema.Record{}, err
	}
	h, err := csvschema.NewHash(algo)
	if err != nil {
		return csvschema.Record{}, err
	}
	if _, err := io.Copy(h, f); err != nil {
		return csvschema.Record{}, err
	}
	return csvschema.Record{
		AbsolutePath: path,
		Filename:     filepath.Base(path),
		LastModified: info.ModTime(),
		SizeBytes:    info.Size(),
		Hash:         hex.EncodeToString(h.Sum(nil)),
	}, nil
}
