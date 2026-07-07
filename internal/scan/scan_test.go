package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sha256("abc")
const sha256abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFiltersByExtensionCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.php", "abc")
	writeFile(t, dir, "b.js", "abc")
	writeFile(t, dir, "c.txt", "abc")     // excluded
	writeFile(t, dir, "sub/d.PHP", "abc") // matched despite upper-case ext
	writeFile(t, dir, "sub/e.md", "abc")  // excluded

	res, err := Run(Options{Root: dir, Exts: []string{"php", ".JS"}, Algo: "sha256"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Matched != 3 {
		t.Fatalf("matched = %d, want 3", res.Matched)
	}
	if len(res.Records) != 3 {
		t.Fatalf("hashed = %d, want 3", len(res.Records))
	}
	if res.Errored != 0 {
		t.Fatalf("errored = %d, want 0", res.Errored)
	}
	for _, r := range res.Records {
		if r.Hash != sha256abc {
			t.Fatalf("%s: hash = %s, want %s", r.AbsolutePath, r.Hash, sha256abc)
		}
	}
}

func TestScanOutputIsDeterministicAndSorted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z.php", "a.php", "m.php", "sub/b.php"} {
		writeFile(t, dir, name, "abc")
	}

	first, err := Run(Options{Root: dir, Exts: []string{".php"}, Algo: "sha256"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(Options{Root: dir, Exts: []string{".php"}, Algo: "sha256"})
	if err != nil {
		t.Fatal(err)
	}

	if len(first.Records) != len(second.Records) {
		t.Fatalf("record count differs between runs: %d vs %d", len(first.Records), len(second.Records))
	}
	for i := range first.Records {
		if first.Records[i].AbsolutePath != second.Records[i].AbsolutePath {
			t.Fatalf("run order differs at %d: %q vs %q", i, first.Records[i].AbsolutePath, second.Records[i].AbsolutePath)
		}
		if i > 0 && first.Records[i-1].AbsolutePath > first.Records[i].AbsolutePath {
			t.Fatalf("records not sorted: %q before %q", first.Records[i-1].AbsolutePath, first.Records[i].AbsolutePath)
		}
	}
}

func TestScanSurfacesReadErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.php", "abc")
	writeFile(t, dir, "boom.php", "abc")

	orig := fsOpen
	fsOpen = func(name string) (*os.File, error) {
		if strings.Contains(filepath.Base(name), "boom") {
			return nil, fmt.Errorf("simulated read error")
		}
		return orig(name)
	}
	defer func() { fsOpen = orig }()

	res, err := Run(Options{Root: dir, Exts: []string{".php"}, Algo: "sha256", Workers: 1})
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if res.Errored != 1 || len(res.Errors) != 1 {
		t.Fatalf("errored = %d (errors=%d), want 1", res.Errored, len(res.Errors))
	}
	if len(res.Records) != 1 || res.Records[0].Filename != "ok.php" {
		t.Fatalf("want only ok.php hashed, got %+v", res.Records)
	}
	if !strings.Contains(res.Errors[0].Path, "boom.php") {
		t.Fatalf("error path = %q, want it to mention boom.php", res.Errors[0].Path)
	}
}

func TestScanMaxDepth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.php", "abc")            // depth 1
	writeFile(t, dir, "sub/b.php", "abc")        // depth 2
	writeFile(t, dir, "sub/deeper/c.php", "abc") // depth 3

	// MaxDepth 1: only root's direct entries; sub is pruned.
	res, err := Run(Options{Root: dir, Exts: []string{".php"}, Algo: "sha256", MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0].Filename != "a.php" {
		t.Fatalf("MaxDepth=1: want only a.php, got %v", filenames(res))
	}
	if len(res.DepthPruned) != 1 || filepath.Base(res.DepthPruned[0]) != "sub" {
		t.Fatalf("MaxDepth=1: want sub pruned, got %v", res.DepthPruned)
	}

	// MaxDepth 2: a.php + sub/b.php; sub/deeper is pruned.
	res, err = Run(Options{Root: dir, Exts: []string{".php"}, Algo: "sha256", MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("MaxDepth=2: want 2 files, got %v", filenames(res))
	}
	if len(res.DepthPruned) != 1 || filepath.Base(res.DepthPruned[0]) != "deeper" {
		t.Fatalf("MaxDepth=2: want deeper pruned, got %v", res.DepthPruned)
	}

	// MaxDepth 0 (unlimited): all three, nothing pruned.
	res, err = Run(Options{Root: dir, Exts: []string{".php"}, Algo: "sha256", MaxDepth: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 3 || len(res.DepthPruned) != 0 {
		t.Fatalf("MaxDepth=0: want 3 files and 0 pruned, got %v pruned=%v", filenames(res), res.DepthPruned)
	}
}

func filenames(res *Result) []string {
	out := make([]string, len(res.Records))
	for i, r := range res.Records {
		out[i] = r.Filename
	}
	return out
}

func TestRunRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]Options{
		"empty root":    {Root: "", Exts: []string{".php"}, Algo: "sha256"},
		"no extensions": {Root: dir, Exts: nil, Algo: "sha256"},
		"bad algo":      {Root: dir, Exts: []string{".php"}, Algo: "md5"},
		"missing root":  {Root: filepath.Join(dir, "nope"), Exts: []string{".php"}, Algo: "sha256"},
	}
	for name, opts := range cases {
		if _, err := Run(opts); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
