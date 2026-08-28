package releasearchive

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WriteArchive packages one executable with fully specified tar and gzip
// metadata so the result does not depend on the host tar implementation.
func WriteArchive(inputPath, outputPath, entryName string, modTime time.Time) error {
	return WriteArchiveWithNotice(inputPath, outputPath, entryName, "", modTime)
}

// WriteArchiveWithNotice packages the executable and, when provided, the
// distribution notice required by linked third-party dependencies.
func WriteArchiveWithNotice(inputPath, outputPath, entryName, noticePath string, modTime time.Time) error {
	if entryName == "" || filepath.Base(entryName) != entryName || entryName == "." {
		return fmt.Errorf("archive entry name must be a base name: %q", entryName)
	}
	input, err := openArchiveInput(inputPath)
	if err != nil {
		return err
	}
	defer input.Close()
	var notice *os.File
	if noticePath != "" {
		notice, err = openArchiveInput(noticePath)
		if err != nil {
			return err
		}
		defer notice.Close()
	}

	stamp := modTime.UTC().Truncate(time.Second)
	return writeAtomic(outputPath, 0o644, func(output *os.File) error {
		compressed, err := gzip.NewWriterLevel(output, gzip.BestCompression)
		if err != nil {
			return fmt.Errorf("create gzip writer: %w", err)
		}
		compressed.Header.ModTime = stamp
		compressed.Header.OS = 255
		archive := tar.NewWriter(compressed)
		if err := writeArchiveEntry(archive, input, entryName, 0o755, stamp); err != nil {
			return err
		}
		if notice != nil {
			if err := writeArchiveEntry(archive, notice, "THIRD_PARTY_NOTICES.md", 0o644, stamp); err != nil {
				return err
			}
		}
		if err := archive.Close(); err != nil {
			return fmt.Errorf("close tar writer: %w", err)
		}
		if err := compressed.Close(); err != nil {
			return fmt.Errorf("close gzip writer: %w", err)
		}
		return nil
	})
}

func openArchiveInput(path string) (*os.File, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open archive input: %w", err)
	}
	info, err := input.Stat()
	if err != nil {
		input.Close()
		return nil, fmt.Errorf("inspect archive input: %w", err)
	}
	if !info.Mode().IsRegular() {
		input.Close()
		return nil, fmt.Errorf("archive input is not a regular file: %s", path)
	}
	return input, nil
}

func writeArchiveEntry(archive *tar.Writer, input *os.File, name string, mode int64, stamp time.Time) error {
	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect archive input: %w", err)
	}
	header := &tar.Header{
		Name: name, Mode: mode, Uid: 0, Gid: 0, Size: info.Size(), ModTime: stamp,
		Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
	}
	if err := archive.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	if _, err := io.Copy(archive, input); err != nil {
		return fmt.Errorf("write tar content: %w", err)
	}
	return nil
}

// WriteChecksums writes the conventional two-space SHA-256 manifest ordered by
// archive base name.
func WriteChecksums(outputPath string, archivePaths []string) error {
	if len(archivePaths) == 0 {
		return errors.New("at least one archive is required")
	}
	paths := append([]string(nil), archivePaths...)
	sort.Slice(paths, func(i, j int) bool { return filepath.Base(paths[i]) < filepath.Base(paths[j]) })
	return writeAtomic(outputPath, 0o644, func(output *os.File) error {
		for _, path := range paths {
			digest, err := fileDigest(path)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(output, "%s  %s\n", digest, filepath.Base(path)); err != nil {
				return fmt.Errorf("write checksum manifest: %w", err)
			}
		}
		return nil
	})
}

// VerifyChecksums verifies files beside the manifest and rejects path-bearing
// entries so a manifest cannot escape its release directory.
func VerifyChecksums(manifestPath string) error {
	manifest, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("open checksum manifest: %w", err)
	}
	defer manifest.Close()
	directory := filepath.Dir(manifestPath)
	entries := 0
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return fmt.Errorf("invalid checksum entry: %q", scanner.Text())
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return fmt.Errorf("invalid checksum digest for %q", fields[1])
		}
		if filepath.Base(fields[1]) != fields[1] || fields[1] == "." {
			return fmt.Errorf("checksum entry must be a base name: %q", fields[1])
		}
		actual, err := fileDigest(filepath.Join(directory, fields[1]))
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, fields[0]) {
			return fmt.Errorf("checksum mismatch for %s", fields[1])
		}
		entries++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read checksum manifest: %w", err)
	}
	if entries == 0 {
		return errors.New("checksum manifest is empty")
	}
	return nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open checksum input %s: %w", path, err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash checksum input %s: %w", path, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeAtomic(path string, mode os.FileMode, write func(*os.File) error) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".release-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set output mode: %w", err)
	}
	if err = write(temporary); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace output: %w", err)
	}
	return nil
}
