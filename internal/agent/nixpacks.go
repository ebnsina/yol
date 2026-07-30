package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// An app without a Dockerfile still has to be turned into an image, which needs a tool that can
// work out how. It is installed on first use rather than during setup, so a server only ever
// deploying apps that carry their own Dockerfile never downloads it.
//
// The version is pinned and the download is checked against a known digest. This runs on the
// customer's machine as root, so "whatever the latest release happens to be" is not good enough:
// a compromised release would otherwise be installed automatically across every server.

const (
	nixpacksVersion = "1.41.0"
	nixpacksTimeout = 3 * time.Minute
)

// nixpacksDigests are the archives this build will accept, by processor. Adding a processor means
// adding its digest, so an unrecognised one refuses to install rather than trusting a download.
var nixpacksDigests = map[string]struct{ arch, digest string }{
	"amd64": {"x86_64", "0f55de7874507b9cf7502113120bd96f2ab6979f78d10eaf2eb2ade9207b3af6"},
	"arm64": {"aarch64", "912bd02dd2bb6f9c3a9ed965fe8a68b4aa318dc7a2546e2eca6f2806a894ba39"},
}

// ensureNixpacks returns the path to the tool, installing it if this machine does not have it yet.
func (a *Agent) ensureNixpacks(ctx context.Context) (string, error) {
	path := filepath.Join(a.cfg.StateDir, "bin", "nixpacks-"+nixpacksVersion)
	if info, err := os.Stat(path); err == nil && info.Mode()&0o100 != 0 {
		return path, nil
	}

	release, ok := nixpacksDigests[runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("agent: no build tool is published for %s processors", runtime.GOARCH)
	}

	slog.Info("installing the tool that builds apps without a Dockerfile", "version", nixpacksVersion)
	if err := a.downloadNixpacks(ctx, release.arch, release.digest, path); err != nil {
		return "", err
	}
	return path, nil
}

// downloadNixpacks fetches the pinned release and installs it only once its digest matches.
func (a *Agent) downloadNixpacks(ctx context.Context, arch, digest, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("agent: prepare %s: %w", filepath.Dir(path), err)
	}

	name := fmt.Sprintf("nixpacks-v%s-%s-unknown-linux-musl.tar.gz", nixpacksVersion, arch)
	url := "https://github.com/railwayapp/nixpacks/releases/download/v" + nixpacksVersion + "/" + name

	fetchCtx, cancel := context.WithTimeout(ctx, nixpacksTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("agent: install the build tool: %w", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent: install the build tool: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("agent: install the build tool: the download answered %d", res.StatusCode)
	}

	// Written beside its destination and moved into place only once verified, so an interrupted
	// download cannot leave something half installed that a later build would try to run.
	staged := path + ".incoming"
	defer os.Remove(staged)

	if err := stageNixpacks(res.Body, staged, digest); err != nil {
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("agent: install the build tool: %w", err)
	}
	return nil
}

// stageNixpacks writes the tool out of the archive and refuses it unless the archive is exactly
// the one this build expects.
func stageNixpacks(body io.Reader, staged, digest string) error {
	archive, err := io.ReadAll(io.LimitReader(body, maxSourceBytes))
	if err != nil {
		return fmt.Errorf("agent: install the build tool: %w", err)
	}

	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return fmt.Errorf("agent: the build tool download did not match what was expected: %s", got)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("agent: install the build tool: %w", err)
	}
	defer gz.Close()

	tarball := tar.NewReader(gz)
	for {
		header, err := tarball.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("agent: the build tool download did not contain the tool")
		}
		if err != nil {
			return fmt.Errorf("agent: install the build tool: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "nixpacks" {
			continue
		}
		return writeArchiveFile(staged, tarball, 0o755)
	}
}
