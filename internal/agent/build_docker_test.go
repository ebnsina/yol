package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/proto"
)

// These build real images with the Docker daemon on this machine, which takes minutes and pulls
// from the network. Run them with YOL_BUILD_TESTS=1, and see dev/verify-phase2.sh for the same
// path exercised on a server rather than a laptop.

func skipUnlessBuildTests(t *testing.T) {
	t.Helper()

	if os.Getenv("YOL_BUILD_TESTS") != "1" {
		t.Skip("set YOL_BUILD_TESTS=1 to build real images")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker on this machine")
	}
}

// serveRepository hands out the files as a repository archive would, wrapper directory included.
func serveRepository(t *testing.T, files map[string]string) string {
	t.Helper()

	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)

	for name, body := range files {
		header := &tar.Header{
			Name:     "owner-repo-abcdef1/" + name,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(body)),
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The credential belongs in a header, so a request without one is refused here too.
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/tarball/abcdef1"
}

func buildingAgent(t *testing.T) *Agent {
	t.Helper()

	control, err := url.Parse("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}

	// A server may mount its temporary directory so that nothing there can run, and the agent
	// installs a tool it then runs. Real installations keep state under /var/lib, so this only
	// needs saying in a test.
	stateDir := os.Getenv("YOL_TEST_STATE_DIR")
	if stateDir == "" {
		stateDir = t.TempDir()
	}
	return New(&config.Agent{StateDir: stateDir, ControlPlaneURL: control}, nil)
}

func removeImage(t *testing.T, ref string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "--force", ref).Run() })
}

// A Dockerfile the user wrote is what gets built, exactly as they wrote it.
func TestABuildFromADockerfileProducesARunnableImage(t *testing.T) {
	skipUnlessBuildTests(t)

	source := serveRepository(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nCOPY greeting .\nCMD [\"cat\",\"greeting\"]\n",
		"greeting":   "built on the server",
	})

	agent := buildingAgent(t)
	ref := "yol-test/dockerfile:abcdef1"
	removeImage(t, ref)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result := agent.runBuild(ctx, proto.BuildRequest{
		DeploymentID: "11111111-1111-1111-1111-111111111111",
		ServiceID:    "22222222-2222-2222-2222-222222222222",
		SourceURL:    source,
		SourceToken:  "test-token",
		CommitSHA:    "abcdef1234567890",
		ImageRef:     ref,
		Labels:       map[string]string{proto.LabelService: "22222222-2222-2222-2222-222222222222"},
	})

	if !result.Succeeded {
		t.Fatalf("the build failed: %s", result.Reason)
	}
	if result.Builder != proto.BuilderDockerfile {
		t.Errorf("builder = %s, want the Dockerfile in the repository to be used", result.Builder)
	}

	out, err := exec.Command("docker", "run", "--rm", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("the image does not run: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "built on the server") {
		t.Errorf("the image printed %q, want what the repository contained", strings.TrimSpace(string(out)))
	}
}

// An app with no Dockerfile still has to become an image, worked out from its files alone.
func TestABuildWithNoDockerfileIsWorkedOut(t *testing.T) {
	skipUnlessBuildTests(t)

	// The tool that works a build out is installed for the machine the agent runs on, and agents
	// run on servers. Cross compile this test and run it there rather than on a laptop.
	if runtime.GOOS != "linux" {
		t.Skip("this path is built for servers; run it on one")
	}

	source := serveRepository(t, map[string]string{
		"go.mod":  "module example.com/probe\n\ngo 1.22\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"worked out\") }\n",
	})

	agent := buildingAgent(t)
	ref := "yol-test/inferred:abcdef1"
	removeImage(t, ref)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	result := agent.runBuild(ctx, proto.BuildRequest{
		DeploymentID: "33333333-3333-3333-3333-333333333333",
		ServiceID:    "44444444-4444-4444-4444-444444444444",
		SourceURL:    source,
		SourceToken:  "test-token",
		CommitSHA:    "abcdef1234567890",
		ImageRef:     ref,
	})

	if !result.Succeeded {
		t.Fatalf("the build failed: %s", result.Reason)
	}
	if result.Builder != proto.BuilderNixpacks {
		t.Errorf("builder = %s, want the build to have been worked out", result.Builder)
	}

	out, err := exec.Command("docker", "run", "--rm", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("the image does not run: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "worked out") {
		t.Errorf("the image printed %q, want the app's own output", strings.TrimSpace(string(out)))
	}
}

// A build runs inside a builder carrying real limits, because it shares a machine with the site it
// is being deployed for.
func TestBuildsRunInsideALimitedBuilder(t *testing.T) {
	skipUnlessBuildTests(t)

	agent := buildingAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := agent.ensureBuilder(ctx, proto.BuildRequest{
		MemoryLimitBytes: 2 << 30,
		CPUPercent:       50,
	}); err != nil {
		t.Fatalf("could not prepare a builder: %v", err)
	}

	limits, err := agent.builderLimits(ctx)
	if err != nil {
		t.Fatalf("could not read the builder's limits: %v", err)
	}
	if limits != "2147483648 50000" {
		t.Errorf("limits = %q, want the memory and processor limits asked for", limits)
	}
}

// The source is fetched with the credential in a header. Without it the repository refuses, and a
// build that reported success on an empty directory would be worse than one that failed.
func TestABuildWithoutTheCredentialFails(t *testing.T) {
	skipUnlessBuildTests(t)

	source := serveRepository(t, map[string]string{"Dockerfile": "FROM alpine:3.20\n"})
	agent := buildingAgent(t)

	result := agent.runBuild(context.Background(), proto.BuildRequest{
		DeploymentID: "55555555-5555-5555-5555-555555555555",
		SourceURL:    source,
		ImageRef:     "yol-test/refused:abcdef1",
	})

	if result.Succeeded {
		t.Error("a build with no credential for the repository reported success")
	}
	if result.Reason == "" {
		t.Error("the failure carried no reason a user could act on")
	}
}
