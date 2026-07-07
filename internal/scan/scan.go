// Package scan implements App 1's core: walk a directory tree, filter files by
// extension, hash the matching ones, and produce a deterministic (path-sorted)
// set of records. Per-file read errors are collected and surfaced, never
// silently dropped.
//
// Both stages are parallel: a pool of directory workers reads directories
// concurrently (important on high-latency or high-parallelism storage), and a
// separate pool of hash workers streams matching files through the hash. The two
// are decoupled by a bounded channel so slow hashing applies backpressure to
// traversal instead of letting an unbounded file backlog grow in memory.
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

// readBufferSize is the reusable per-hash-worker read buffer. A large buffer
// cuts read syscall overhead on big files; 1 MiB is close to optimal across
// storage types and is cheap (workers × 1 MiB total).
const readBufferSize = 1 << 20

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
	Workers        int // hash concurrency; <= 0 means runtime.NumCPU()
	DirWorkers     int // directory-read concurrency; <= 0 means Workers
	// MaxDepth limits how many directory levels below Root are descended.
	// Root's direct entries are depth 1. 0 (the default) means unlimited.
	// Directories deeper than the limit are pruned and reported in
	// Result.DepthPruned rather than silently skipped.
	MaxDepth int
}

// FileError records a file that could not be read or hashed.
type FileError struct {
	Path string
	Err  error
}

// Result is the outcome of a scan.
type Result struct {
	Records     []csvschema.Record // successfully hashed, sorted by absolute path
	Matched     int                // files whose extension matched
	Errored     int                // matched files (or paths) that failed
	Skipped     int                // symlinks / non-regular files not hashed
	Errors      []FileError        // one per Errored file
	DepthPruned []string           // directories not descended due to MaxDepth
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

type dirJob struct {
	path  string
	depth int
}

// Run walks opts.Root and hashes every file whose extension matches. It returns
// a fatal error only for setup problems (empty/missing root, not a directory,
// no extensions, unsupported algorithm). Per-file read errors are collected in
// Result.Errors and do not abort the scan unless FailFast is set.
//
// Symlink cycles cannot cause infinite recursion: directory entries are
// classified by their lstat type and symlinks are never descended (with
// FollowSymlinks a symlink is stat'd and hashed only if it resolves to a regular
// file — its target directory is never re-walked). Only real directories are
// traversed, and a normal filesystem's directory graph is a tree (no directory
// hardlinks), so it has no cycles. MaxDepth is an additional bound for exotic
// cases such as a real cycle created by bind/loop mounts.
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
	dirWorkers := opts.DirWorkers
	if dirWorkers <= 0 {
		dirWorkers = workers
	}

	var (
		res       = &Result{}
		mu        sync.Mutex
		tripped   atomic.Bool
		done      = make(chan struct{})
		closeOnce sync.Once
	)
	trip := func() {
		tripped.Store(true)
		closeOnce.Do(func() { close(done) })
	}
	addErr := func(path string, e error) {
		mu.Lock()
		res.Errored++
		res.Errors = append(res.Errors, FileError{Path: path, Err: e})
		mu.Unlock()
		if opts.FailFast {
			trip()
		}
	}
	addSkip := func() {
		mu.Lock()
		res.Skipped++
		mu.Unlock()
	}
	addPruned := func(p string) {
		mu.Lock()
		res.DepthPruned = append(res.DepthPruned, p)
		mu.Unlock()
	}

	// Hash pipeline: hash workers pull file paths off fileCh. The buffer gives
	// traversal room to run ahead while bounding the in-flight file backlog.
	fileCh := make(chan string, workers*64)
	var hashWG sync.WaitGroup
	hashWG.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer hashWG.Done()
			buf := make([]byte, readBufferSize)
			for p := range fileCh {
				if tripped.Load() {
					continue // drain without hashing so senders never block
				}
				rec, herr := hashFile(p, opts.Algo, buf)
				if herr != nil {
					addErr(p, herr)
					continue
				}
				mu.Lock()
				res.Records = append(res.Records, rec)
				mu.Unlock()
			}
		}()
	}

	sendFile := func(p string) {
		mu.Lock()
		res.Matched++
		mu.Unlock()
		select {
		case fileCh <- p:
		case <-done: // FailFast tripped; stop feeding
		}
	}

	// Traversal work stack (LIFO keeps the queue small — depth-first-ish).
	// remaining counts directories discovered but not yet fully processed.
	var (
		qmu       sync.Mutex
		qcond     = sync.NewCond(&qmu)
		stack     []dirJob
		remaining int
	)
	push := func(j dirJob) {
		qmu.Lock()
		stack = append(stack, j)
		remaining++
		qcond.Signal()
		qmu.Unlock()
	}

	processDir := func(j dirJob) {
		if tripped.Load() {
			return
		}
		entries, rerr := os.ReadDir(j.path)
		if rerr != nil {
			addErr(j.path, rerr) // unreadable directory — surfaced, not fatal
			return
		}
		for _, e := range entries {
			if tripped.Load() {
				return
			}
			full := filepath.Join(j.path, e.Name())
			mode, merr := entryMode(e)
			if merr != nil {
				addErr(full, merr)
				continue
			}
			switch {
			case mode.IsDir():
				childDepth := j.depth + 1
				if opts.MaxDepth > 0 && childDepth >= opts.MaxDepth {
					addPruned(full)
					continue
				}
				push(dirJob{path: full, depth: childDepth})
			case mode&fs.ModeSymlink != 0:
				if !opts.FollowSymlinks {
					addSkip()
					continue
				}
				ti, terr := os.Stat(full) // follows the link
				if terr != nil {
					addErr(full, terr)
					continue
				}
				if !ti.Mode().IsRegular() {
					addSkip() // never descend a symlinked directory
					continue
				}
				if extSet[strings.ToLower(filepath.Ext(full))] {
					sendFile(full)
				}
			case mode.IsRegular():
				if extSet[strings.ToLower(filepath.Ext(full))] {
					sendFile(full)
				}
			default:
				addSkip() // device, socket, pipe, irregular
			}
		}
	}

	push(dirJob{path: absRoot, depth: 0})

	var dirWG sync.WaitGroup
	dirWG.Add(dirWorkers)
	for i := 0; i < dirWorkers; i++ {
		go func() {
			defer dirWG.Done()
			for {
				qmu.Lock()
				for len(stack) == 0 && remaining > 0 {
					qcond.Wait()
				}
				if len(stack) == 0 { // remaining == 0: all directories done
					qmu.Unlock()
					return
				}
				j := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				qmu.Unlock()

				processDir(j)

				qmu.Lock()
				remaining--
				if remaining == 0 {
					qcond.Broadcast() // wake the others so they exit
				}
				qmu.Unlock()
			}
		}()
	}

	// Close the file channel once traversal is fully drained, so hash workers
	// finish after the last file has been queued.
	go func() {
		dirWG.Wait()
		close(fileCh)
	}()
	hashWG.Wait()

	sort.Slice(res.Records, func(i, j int) bool {
		return res.Records[i].AbsolutePath < res.Records[j].AbsolutePath
	})
	sort.Strings(res.DepthPruned)
	return res, nil
}

// entryMode returns the file mode of a directory entry, resolving the type via
// lstat only when the readdir type is ambiguous (e.g. DT_UNKNOWN on some
// filesystems). It never follows symlinks.
func entryMode(e fs.DirEntry) (fs.FileMode, error) {
	t := e.Type()
	if t.IsDir() || t.IsRegular() || t&fs.ModeSymlink != 0 {
		return t, nil
	}
	info, err := e.Info()
	if err != nil {
		return 0, err
	}
	return info.Mode(), nil
}

// hashFile opens, stats, and hashes a single file, streaming it through the hash
// with the supplied reusable buffer so memory use stays constant regardless of
// file size.
func hashFile(path, algo string, buf []byte) (csvschema.Record, error) {
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
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
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
