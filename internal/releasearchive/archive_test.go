package releasearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArchiveIsDeterministicAndCanonical(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input")
	if err := os.WriteFile(input, []byte("binary bytes\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1_700_000_000, 0).UTC()
	first := filepath.Join(directory, "first.tar.gz")
	second := filepath.Join(directory, "second.tar.gz")
	for _, output := range []string{first, second} {
		if err := WriteArchive(input, output, "bb", stamp); err != nil {
			t.Fatal(err)
		}
	}
	firstBytes, _ := os.ReadFile(first)
	secondBytes, _ := os.ReadFile(second)
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("identical inputs produced different archives")
	}

	compressed, err := gzip.NewReader(bytes.NewReader(firstBytes))
	if err != nil {
		t.Fatal(err)
	}
	if !compressed.ModTime.Equal(stamp) || compressed.OS != 255 {
		t.Fatalf("gzip header time=%s os=%d", compressed.ModTime, compressed.OS)
	}
	archive := tar.NewReader(compressed)
	header, err := archive.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "bb" || header.Mode != 0o755 || header.Uid != 0 || header.Gid != 0 || !header.ModTime.Equal(stamp) || header.Format != tar.FormatUSTAR {
		t.Fatalf("header=%+v", header)
	}
	content := new(bytes.Buffer)
	if _, err := content.ReadFrom(archive); err != nil {
		t.Fatal(err)
	}
	if content.String() != "binary bytes\n" {
		t.Fatalf("content=%q", content.String())
	}
}

func TestWriteArchiveWithNoticeIncludesCanonicalNotice(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input")
	notice := filepath.Join(directory, "notice")
	if err := os.WriteFile(input, []byte("binary\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notice, []byte("license\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "archive.tar.gz")
	stamp := time.Unix(1_700_000_000, 0).UTC()
	if err := WriteArchiveWithNotice(input, output, "bb", notice, stamp); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(compressed)
	first, err := archive.Next()
	if err != nil || first.Name != "bb" || first.Mode != 0o755 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	if _, err := io.Copy(io.Discard, archive); err != nil {
		t.Fatal(err)
	}
	second, err := archive.Next()
	if err != nil || second.Name != "THIRD_PARTY_NOTICES.md" || second.Mode != 0o644 || !second.ModTime.Equal(stamp) {
		t.Fatalf("second=%+v error=%v", second, err)
	}
	content := new(bytes.Buffer)
	if _, err := content.ReadFrom(archive); err != nil || content.String() != "license\n" {
		t.Fatalf("notice=%q error=%v", content.String(), err)
	}
}

func TestChecksumsAreSortedAndVerified(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "z.tar.gz")
	second := filepath.Join(directory, "a.tar.gz")
	if err := os.WriteFile(first, []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(directory, "checksums.txt")
	if err := WriteChecksums(manifest, []string{first, second}); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(manifest)
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "  a.tar.gz") || !strings.HasSuffix(lines[1], "  z.tar.gz") {
		t.Fatalf("manifest=%q", text)
	}
	if err := VerifyChecksums(manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyChecksums(manifest); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("verify changed archive err=%v", err)
	}
}
