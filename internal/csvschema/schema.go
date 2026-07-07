// Package csvschema is the single source of truth for the CSV formats shared by
// the checksum (App 1) and csvdiff (App 2) apps. Both apps import it; neither
// hand-rolls headers or field ordering. Changing a format means changing it here.
package csvschema

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/csv"
	"fmt"
	"hash"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultAlgo is used when no hash algorithm is specified.
const DefaultAlgo = "sha256"

// TimeFormat is the canonical last_modified encoding: RFC3339, always in UTC.
const TimeFormat = time.RFC3339

// fixedManifestColumns are the leading columns of a checksum manifest. The final
// column is named after the hash algorithm (e.g. "sha256"), so a full manifest
// header is fixedManifestColumns followed by the algorithm name.
var fixedManifestColumns = []string{"absolute_path", "filename", "last_modified", "size_bytes"}

// algos maps a supported algorithm name to its hash constructor.
var algos = map[string]func() hash.Hash{
	"sha256": sha256.New,
	"sha512": sha512.New,
}

// NewHash returns a fresh hash.Hash for algo, or an error if it is unsupported.
func NewHash(algo string) (hash.Hash, error) {
	ctor, ok := algos[algo]
	if !ok {
		return nil, fmt.Errorf("unsupported hash algorithm %q (supported: %s)", algo, SupportedAlgos())
	}
	return ctor(), nil
}

// IsSupportedAlgo reports whether algo is a known hash algorithm.
func IsSupportedAlgo(algo string) bool {
	_, ok := algos[algo]
	return ok
}

// SupportedAlgos returns the supported algorithm names, sorted and comma-joined.
func SupportedAlgos() string {
	names := make([]string, 0, len(algos))
	for n := range algos {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Record is one row of a checksum manifest: a single hashed file.
type Record struct {
	AbsolutePath string
	Filename     string // base name including extension
	LastModified time.Time
	SizeBytes    int64
	Hash         string // lowercase hex
}

// ManifestHeader returns the CSV header for a manifest produced with algo.
func ManifestHeader(algo string) []string {
	h := make([]string, 0, len(fixedManifestColumns)+1)
	h = append(h, fixedManifestColumns...)
	return append(h, algo)
}

// Row encodes a Record as CSV fields.
func (r Record) Row() []string {
	return []string{
		r.AbsolutePath,
		r.Filename,
		r.LastModified.UTC().Format(TimeFormat),
		strconv.FormatInt(r.SizeBytes, 10),
		r.Hash,
	}
}

// ParseManifestHeader validates a manifest header and returns the hash algorithm
// named by its final column.
func ParseManifestHeader(header []string) (algo string, err error) {
	want := len(fixedManifestColumns) + 1
	if len(header) != want {
		return "", fmt.Errorf("manifest header: expected %d columns, got %d (%v)", want, len(header), header)
	}
	for i, col := range fixedManifestColumns {
		if header[i] != col {
			return "", fmt.Errorf("manifest header column %d: expected %q, got %q", i+1, col, header[i])
		}
	}
	algo = header[len(header)-1]
	if !IsSupportedAlgo(algo) {
		return "", fmt.Errorf("manifest hash column %q is not a supported algorithm (%s)", algo, SupportedAlgos())
	}
	return algo, nil
}

// ParseRecord decodes one manifest data row.
func ParseRecord(row []string) (Record, error) {
	want := len(fixedManifestColumns) + 1
	if len(row) != want {
		return Record{}, fmt.Errorf("expected %d fields, got %d", want, len(row))
	}
	mod, err := time.Parse(TimeFormat, row[2])
	if err != nil {
		return Record{}, fmt.Errorf("last_modified %q: %w", row[2], err)
	}
	size, err := strconv.ParseInt(row[3], 10, 64)
	if err != nil {
		return Record{}, fmt.Errorf("size_bytes %q: %w", row[3], err)
	}
	return Record{
		AbsolutePath: row[0],
		Filename:     row[1],
		LastModified: mod,
		SizeBytes:    size,
		Hash:         row[4],
	}, nil
}

// WriteManifest writes the header followed by records as CSV to w.
func WriteManifest(w io.Writer, algo string, records []Record) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(ManifestHeader(algo)); err != nil {
		return err
	}
	for _, r := range records {
		if err := cw.Write(r.Row()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ReadManifest reads all records from r, returning the manifest's hash algorithm
// and its records.
func ReadManifest(r io.Reader) (algo string, records []Record, err error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = len(fixedManifestColumns) + 1

	header, err := cr.Read()
	if err != nil {
		return "", nil, fmt.Errorf("reading header: %w", err)
	}
	algo, err = ParseManifestHeader(header)
	if err != nil {
		return "", nil, err
	}
	for {
		row, rerr := cr.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", nil, rerr
		}
		rec, perr := ParseRecord(row)
		if perr != nil {
			return "", nil, perr
		}
		records = append(records, rec)
	}
	return algo, records, nil
}

// Change statuses used by csvdiff output.
const (
	StatusModified = "MODIFIED" // present in both, hash differs
	StatusAdded    = "ADDED"    // present only in current
	StatusDeleted  = "DELETED"  // present only in baseline
)

// Change is one row of a csvdiff output. Size/modified fields are strings so the
// side that is absent (ADDED has no baseline, DELETED has no current) is empty.
type Change struct {
	Status           string
	AbsolutePath     string
	Filename         string
	BaselineSha      string
	CurrentSha       string
	BaselineSize     string
	CurrentSize      string
	BaselineModified string
	CurrentModified  string
}

// ChangeHeader returns the CSV header for a csvdiff output.
func ChangeHeader() []string {
	return []string{
		"status", "absolute_path", "filename",
		"baseline_sha", "current_sha",
		"baseline_size", "current_size",
		"baseline_modified", "current_modified",
	}
}

// Row encodes a Change as CSV fields.
func (c Change) Row() []string {
	return []string{
		c.Status, c.AbsolutePath, c.Filename,
		c.BaselineSha, c.CurrentSha,
		c.BaselineSize, c.CurrentSize,
		c.BaselineModified, c.CurrentModified,
	}
}

// WriteChanges writes the header followed by change rows as CSV to w.
func WriteChanges(w io.Writer, changes []Change) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(ChangeHeader()); err != nil {
		return err
	}
	for _, c := range changes {
		if err := cw.Write(c.Row()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
