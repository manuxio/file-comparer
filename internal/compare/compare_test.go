package compare

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manuxio/file-comparer/internal/csvschema"
)

var testMod = time.Date(2026, 7, 7, 3, 0, 0, 0, time.UTC)

func writeManifest(t *testing.T, path, algo string, records []csvschema.Record) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := csvschema.WriteManifest(f, algo, records); err != nil {
		t.Fatal(err)
	}
}

func rec(path, hash string, size int64) csvschema.Record {
	return csvschema.Record{
		AbsolutePath: path,
		Filename:     filepath.Base(path),
		LastModified: testMod,
		SizeBytes:    size,
		Hash:         hash,
	}
}

func TestCompareDetectsModifiedAddedDeleted(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "baseline.csv")
	cur := filepath.Join(dir, "current.csv")

	writeManifest(t, base, "sha256", []csvschema.Record{
		rec("/mnt/data/a.php", "aaa", 3),    // will be modified
		rec("/mnt/data/gone.php", "ggg", 3), // deleted
		rec("/mnt/data/same.php", "sss", 3), // unchanged
	})
	writeManifest(t, cur, "sha256", []csvschema.Record{
		rec("/mnt/data/a.php", "AAA", 4),    // modified (hash + size changed)
		rec("/mnt/data/new.php", "nnn", 3),  // added
		rec("/mnt/data/same.php", "sss", 3), // unchanged -> omitted
	})

	res, err := Run(Options{BaselinePath: base, CurrentPath: cur})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Modified != 1 || res.Added != 1 || res.Deleted != 1 {
		t.Fatalf("mod/add/del = %d/%d/%d, want 1/1/1", res.Modified, res.Added, res.Deleted)
	}
	if len(res.Changes) != 3 {
		t.Fatalf("changes = %d, want 3 (unchanged omitted)", len(res.Changes))
	}
	// Sorted by (status, path): ADDED, DELETED, MODIFIED.
	wantOrder := []struct{ status, path string }{
		{csvschema.StatusAdded, "/mnt/data/new.php"},
		{csvschema.StatusDeleted, "/mnt/data/gone.php"},
		{csvschema.StatusModified, "/mnt/data/a.php"},
	}
	for i, w := range wantOrder {
		if res.Changes[i].Status != w.status || res.Changes[i].AbsolutePath != w.path {
			t.Fatalf("change %d = (%s, %s), want (%s, %s)", i,
				res.Changes[i].Status, res.Changes[i].AbsolutePath, w.status, w.path)
		}
	}
}

func TestCompareStripPrefixMakesPathsComparable(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "baseline.csv")
	cur := filepath.Join(dir, "current.csv")

	// Same logical file, captured at different mount points, same content.
	writeManifest(t, base, "sha256", []csvschema.Record{rec("/mnt/backup/x.php", "hhh", 3)})
	writeManifest(t, cur, "sha256", []csvschema.Record{rec("/mnt/data/x.php", "hhh", 3)})

	res, err := Run(Options{
		BaselinePath:        base,
		CurrentPath:         cur,
		StripBaselinePrefix: "/mnt/backup",
		StripCurrentPrefix:  "/mnt/data",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.HasDiff() {
		t.Fatalf("expected no diff after prefix stripping, got %+v", res.Changes)
	}
}

func TestCompareRejectsDuplicatePath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "baseline.csv")
	cur := filepath.Join(dir, "current.csv")
	writeManifest(t, base, "sha256", []csvschema.Record{rec("/mnt/data/a.php", "aaa", 3), rec("/mnt/data/a.php", "bbb", 3)})
	writeManifest(t, cur, "sha256", []csvschema.Record{rec("/mnt/data/a.php", "aaa", 3)})

	_, err := Run(Options{BaselinePath: base, CurrentPath: cur})
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InputError for duplicate path, got %v", err)
	}
}

func TestCompareRejectsAlgoMismatch(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "baseline.csv")
	cur := filepath.Join(dir, "current.csv")
	writeManifest(t, base, "sha256", []csvschema.Record{rec("/mnt/data/a.php", "aaa", 3)})
	writeManifest(t, cur, "sha512", []csvschema.Record{rec("/mnt/data/a.php", "aaa", 3)})

	_, err := Run(Options{BaselinePath: base, CurrentPath: cur})
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InputError for algo mismatch, got %v", err)
	}
}

func TestCompareMissingFileIsFatalNotInputError(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "current.csv")
	writeManifest(t, cur, "sha256", []csvschema.Record{rec("/mnt/data/a.php", "aaa", 3)})

	_, err := Run(Options{BaselinePath: filepath.Join(dir, "nope.csv"), CurrentPath: cur})
	if err == nil {
		t.Fatal("expected error for missing baseline")
	}
	var ie *InputError
	if errors.As(err, &ie) {
		t.Fatalf("missing file should be fatal, not InputError: %v", err)
	}
}
