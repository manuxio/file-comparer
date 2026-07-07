// Package compare implements App 2's core: diff two checksum manifests (a
// baseline and a current) by absolute path and produce the set of modified,
// added, and deleted files. Unchanged files are omitted.
package compare

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/manuxio/file-comparer/internal/csvschema"
)

// InputError marks a malformed or inconsistent input manifest, as distinct from
// a file-access failure. Callers can use it to choose an exit code.
type InputError struct{ Err error }

func (e *InputError) Error() string { return e.Err.Error() }
func (e *InputError) Unwrap() error { return e.Err }

// Options configures a comparison.
type Options struct {
	BaselinePath        string
	CurrentPath         string
	StripBaselinePrefix string // stripped from baseline paths before matching
	StripCurrentPrefix  string // stripped from current paths before matching
}

// Result summarizes a comparison.
type Result struct {
	Changes  []csvschema.Change // sorted by (status, absolute_path)
	Modified int
	Added    int
	Deleted  int
}

// HasDiff reports whether any difference was found.
func (r *Result) HasDiff() bool { return len(r.Changes) > 0 }

type indexed struct {
	algo   string
	byPath map[string]csvschema.Record
}

func loadManifest(path, stripPrefix string) (*indexed, error) {
	f, err := os.Open(path)
	if err != nil {
		// File-access problem: fatal, not an input-format problem.
		return nil, fmt.Errorf("opening %q: %w", path, err)
	}
	defer f.Close()

	algo, records, err := csvschema.ReadManifest(f)
	if err != nil {
		return nil, &InputError{Err: fmt.Errorf("parsing %q: %w", path, err)}
	}

	byPath := make(map[string]csvschema.Record, len(records))
	for _, r := range records {
		key := r.AbsolutePath
		if stripPrefix != "" {
			key = strings.TrimPrefix(key, stripPrefix)
		}
		if _, dup := byPath[key]; dup {
			return nil, &InputError{Err: fmt.Errorf("duplicate path %q in %q", key, path)}
		}
		byPath[key] = r
	}
	return &indexed{algo: algo, byPath: byPath}, nil
}

// Run loads both manifests and computes their difference.
func Run(opts Options) (*Result, error) {
	base, err := loadManifest(opts.BaselinePath, opts.StripBaselinePrefix)
	if err != nil {
		return nil, err
	}
	cur, err := loadManifest(opts.CurrentPath, opts.StripCurrentPrefix)
	if err != nil {
		return nil, err
	}
	if base.algo != cur.algo {
		return nil, &InputError{Err: fmt.Errorf("hash algorithm mismatch: baseline uses %q, current uses %q", base.algo, cur.algo)}
	}

	keys := make(map[string]struct{}, len(base.byPath)+len(cur.byPath))
	for k := range base.byPath {
		keys[k] = struct{}{}
	}
	for k := range cur.byPath {
		keys[k] = struct{}{}
	}

	res := &Result{}
	for k := range keys {
		b, inBase := base.byPath[k]
		c, inCur := cur.byPath[k]
		switch {
		case inBase && inCur:
			if b.Hash != c.Hash {
				res.Modified++
				bb, cc := b, c
				res.Changes = append(res.Changes, changeFor(csvschema.StatusModified, k, &bb, &cc))
			}
		case inCur:
			res.Added++
			cc := c
			res.Changes = append(res.Changes, changeFor(csvschema.StatusAdded, k, nil, &cc))
		case inBase:
			res.Deleted++
			bb := b
			res.Changes = append(res.Changes, changeFor(csvschema.StatusDeleted, k, &bb, nil))
		}
	}

	sort.Slice(res.Changes, func(i, j int) bool {
		if res.Changes[i].Status != res.Changes[j].Status {
			return res.Changes[i].Status < res.Changes[j].Status
		}
		return res.Changes[i].AbsolutePath < res.Changes[j].AbsolutePath
	})
	return res, nil
}

func changeFor(status, path string, b, c *csvschema.Record) csvschema.Change {
	ch := csvschema.Change{Status: status, AbsolutePath: path}
	if c != nil {
		ch.Filename = c.Filename
		ch.CurrentSha = c.Hash
		ch.CurrentSize = strconv.FormatInt(c.SizeBytes, 10)
		ch.CurrentModified = c.LastModified.UTC().Format(csvschema.TimeFormat)
	}
	if b != nil {
		if ch.Filename == "" {
			ch.Filename = b.Filename
		}
		ch.BaselineSha = b.Hash
		ch.BaselineSize = strconv.FormatInt(b.SizeBytes, 10)
		ch.BaselineModified = b.LastModified.UTC().Format(csvschema.TimeFormat)
	}
	return ch
}
