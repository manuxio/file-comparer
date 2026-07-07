package csvschema

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	// Use a whole-second UTC time so RFC3339 round-trips exactly.
	mod := time.Date(2026, 7, 7, 3, 10, 0, 0, time.UTC)
	in := []Record{
		{AbsolutePath: "/mnt/data/a.php", Filename: "a.php", LastModified: mod, SizeBytes: 3, Hash: "aaa"},
		{AbsolutePath: "/mnt/data/b.js", Filename: "b.js", LastModified: mod, SizeBytes: 42, Hash: "bbb"},
	}

	var buf bytes.Buffer
	if err := WriteManifest(&buf, "sha256", in); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	algo, out, err := ReadManifest(&buf)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if algo != "sha256" {
		t.Fatalf("algo = %q, want sha256", algo)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d records, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].AbsolutePath != in[i].AbsolutePath ||
			out[i].Filename != in[i].Filename ||
			out[i].SizeBytes != in[i].SizeBytes ||
			out[i].Hash != in[i].Hash ||
			!out[i].LastModified.Equal(in[i].LastModified) {
			t.Fatalf("record %d round-trip mismatch:\n got  %+v\n want %+v", i, out[i], in[i])
		}
	}
}

func TestManifestHeaderTracksAlgo(t *testing.T) {
	if got := ManifestHeader("sha512"); got[len(got)-1] != "sha512" {
		t.Fatalf("last column = %q, want sha512", got[len(got)-1])
	}
}

func TestParseManifestHeaderErrors(t *testing.T) {
	tests := map[string][]string{
		"too few columns": {"absolute_path", "filename", "sha256"},
		"wrong column":    {"path", "filename", "last_modified", "size_bytes", "sha256"},
		"unknown algo":    {"absolute_path", "filename", "last_modified", "size_bytes", "md5"},
	}
	for name, header := range tests {
		if _, err := ParseManifestHeader(header); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestReadManifestRejectsMalformedRow(t *testing.T) {
	data := strings.Join([]string{
		"absolute_path,filename,last_modified,size_bytes,sha256",
		"/mnt/data/a.php,a.php,not-a-time,3,aaa",
	}, "\n")
	if _, _, err := ReadManifest(strings.NewReader(data)); err == nil {
		t.Fatal("expected error on bad timestamp, got nil")
	}
}

func TestSupportedAlgos(t *testing.T) {
	if !IsSupportedAlgo("sha256") || !IsSupportedAlgo("sha512") {
		t.Fatal("sha256/sha512 should be supported")
	}
	if IsSupportedAlgo("md5") {
		t.Fatal("md5 should not be offered for security use")
	}
}
