package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Source arrives as a compressed archive of a single commit. The archive is written by whoever
// hosts the repository and is therefore not to be trusted with where it unpacks to: an entry may
// name a path outside the directory, or be a link pointing anywhere on the machine. Every entry is
// checked before anything is written.

const sourceFetchTimeout = 5 * time.Minute

// fetchSource downloads the commit being built and expands it into dir.
func (a *Agent) fetchSource(ctx context.Context, req proto.BuildRequest, dir string) error {
	fetchCtx, cancel := context.WithTimeout(ctx, sourceFetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, req.SourceURL, nil)
	if err != nil {
		return fmt.Errorf("agent: fetch source: %w", err)
	}
	// A header rather than the address, so the credential stays out of logs and proxy records.
	if req.SourceToken != "" {
		request.Header.Set("Authorization", "Bearer "+req.SourceToken)
	}

	res, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("agent: fetch source: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("agent: fetch source: the repository answered %d", res.StatusCode)
	}
	return extractTarGz(io.LimitReader(res.Body, maxSourceBytes), dir)
}

// extractTarGz expands an archive into dir, dropping the single directory a repository archive is
// wrapped in so paths match the repository itself.
func extractTarGz(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("agent: read source archive: %w", err)
	}
	defer gz.Close()

	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("agent: read source archive: %w", err)
		}

		name := stripWrapper(header.Name)
		if name == "" {
			continue
		}
		path, err := safePath(dir, name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return fmt.Errorf("agent: expand source: %w", err)
			}

		case tar.TypeReg:
			if err := writeArchiveFile(path, archive, header.FileInfo().Mode()); err != nil {
				return err
			}

		default:
			// Links can point anywhere on the machine, including at the agent's own credential.
			// Nothing needs them to build, so they are left out rather than made safe.
			continue
		}
	}
}

func writeArchiveFile(path string, contents io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("agent: expand source: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return fmt.Errorf("agent: expand source: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, contents); err != nil {
		return fmt.Errorf("agent: expand source: %w", err)
	}
	return nil
}

// stripWrapper removes the top level directory a repository archive wraps everything in, which is
// named after the commit and is not part of the repository.
func stripWrapper(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	_, rest, found := strings.Cut(name, "/")
	if !found {
		return ""
	}
	return strings.Trim(rest, "/")
}

// climbs reports whether a path tries to walk up out of where it is being resolved. Such a path is
// refused rather than cleaned: cleaning it lands somewhere valid, and quietly writing to a
// different file than the one named is worse than saying no.
func climbs(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// safePath resolves an entry inside dir, refusing one that would land anywhere else.
func safePath(dir, name string) (string, error) {
	if climbs(name) {
		return "", fmt.Errorf("agent: the archive contains %s, which points outside the build", name)
	}

	path := filepath.Join(dir, filepath.Clean("/"+name))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("agent: the archive contains %s, which is outside the build", name)
	}
	return path, nil
}
