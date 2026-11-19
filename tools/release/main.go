// Command release builds the platform archives and their SHA-256 checksums.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var targets = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"}
var appNames = []string{"openplexer", "openplexer-hub", "openplexer-verify"}
var versionPattern = regexp.MustCompile(`^v[0-9][a-zA-Z0-9.+-]*$`)

func main() {
	version := flag.String("version", "", "Release tag to embed in the binaries")
	out := flag.String("out", "dist/release", "Output directory for archives and SHA256SUMS")
	target := flag.String("target", "", "Build only this GOOS/GOARCH pair")
	flag.Parse()
	if err := release(*version, *out, *target); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func release(version, out, target string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid version %q, use a tag such as v0.1.0", version)
	}
	selected := targets
	if target != "" {
		if !slices.Contains(targets, target) {
			return fmt.Errorf("unsupported target %q", target)
		}
		selected = []string{target}
	}

	mtime, err := releaseTime()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(out, ".build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	bin := filepath.Join(stage, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		return err
	}

	var sums strings.Builder
	var archives []string
	for _, target := range selected {
		goos, goarch, _ := strings.Cut(target, "/")
		args := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X main.version=" + version, "-o", bin + string(os.PathSeparator)}
		for _, app := range appNames {
			args = append(args, "./apps/"+app)
		}
		cmd := exec.Command("go", args...)
		cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch, "GOAMD64=v1", "GOARM64=v8.0", "GOEXPERIMENT=")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", target, err)
		}

		suffix, extension := "", ".tar.gz"
		writeArchive := writeTarGz
		if goos == "windows" {
			suffix, extension = ".exe", ".zip"
			writeArchive = writeZip
		}
		var files []archiveFile
		for _, app := range appNames {
			name := app + suffix
			files = append(files, archiveFile{name, filepath.Join(bin, name), 0755})
		}
		for _, doc := range []string{"LICENSE", "NOTICE", "README.md"} {
			files = append(files, archiveFile{doc, doc, 0644})
		}

		name := fmt.Sprintf("openplexer_%s_%s_%s%s", version, goos, goarch, extension)
		f, err := os.Create(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		writeErr := writeArchive(io.MultiWriter(f, hash), files, mtime)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		fmt.Fprintf(&sums, "%x  %s\n", hash.Sum(nil), name)
		archives = append(archives, name)
	}

	if err := os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	for _, name := range append(archives, "SHA256SUMS") {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(out, name)); err != nil {
			return err
		}
	}
	return nil
}

func releaseTime() (time.Time, error) {
	epoch := os.Getenv("SOURCE_DATE_EPOCH")
	if epoch == "" {
		output, err := exec.Command("git", "log", "-1", "--format=%ct", "HEAD").Output()
		if err != nil {
			return time.Time{}, fmt.Errorf("read HEAD commit time: %w", err)
		}
		epoch = strings.TrimSpace(string(output))
	}
	// ZIP extended timestamps store seconds since the epoch in 32 bits.
	seconds, err := strconv.ParseUint(epoch, 10, 32)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid release timestamp %q: %w", epoch, err)
	}
	return time.Unix(int64(seconds), 0).UTC(), nil
}

type archiveFile struct {
	name, path string
	mode       os.FileMode
}

func (file archiveFile) copyTo(dst io.Writer) error {
	src, err := os.Open(file.path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(dst, src)
	return err
}

func writeTarGz(dst io.Writer, files []archiveFile, mtime time.Time) (err error) {
	gz := gzip.NewWriter(dst)
	gz.ModTime = mtime
	archive := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, archive.Close(), gz.Close()) }()
	for _, file := range files {
		info, err := os.Stat(file.path)
		if err != nil {
			return err
		}
		header := tar.Header{
			Name: file.name, Mode: int64(file.mode), Size: info.Size(),
			ModTime: mtime, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
		}
		if err := archive.WriteHeader(&header); err != nil {
			return err
		}
		if err := file.copyTo(archive); err != nil {
			return err
		}
	}
	return nil
}

func writeZip(dst io.Writer, files []archiveFile, mtime time.Time) (err error) {
	archive := zip.NewWriter(dst)
	defer func() { err = errors.Join(err, archive.Close()) }()
	for _, file := range files {
		header := zip.FileHeader{Name: file.name, Method: zip.Deflate, Modified: mtime}
		header.SetMode(file.mode)
		if mtime.Year() < 1980 {
			// DOS dates start at 1980. Keep the real time in the extended field.
			header.Modified = time.Time{}
			header.ModifiedDate = 1<<5 | 1 // January 1, 1980.
			header.Extra = []byte{0x55, 0x54, 5, 0, 1}
			header.Extra = binary.LittleEndian.AppendUint32(header.Extra, uint32(mtime.Unix()))
		}
		entry, err := archive.CreateHeader(&header)
		if err != nil {
			return err
		}
		if err := file.copyTo(entry); err != nil {
			return err
		}
	}
	return nil
}
