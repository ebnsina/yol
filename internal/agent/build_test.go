package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// A repository holding several apps builds one directory of it, and that directory has to be
// inside the repository. A build directory pointing elsewhere would hand the machine's own files
// to a build.
func TestTheBuildDirectoryMustBeInsideTheRepository(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}

	dir, err := buildContextDir(workspace, "apps/web")
	if err != nil {
		t.Fatalf("a directory in the repository was refused: %v", err)
	}
	if dir != filepath.Join(workspace, "apps", "web") {
		t.Errorf("dir = %s, want the directory asked for", dir)
	}

	for _, outside := range []string{"../../etc", "/etc", "apps/../../.."} {
		if _, err := buildContextDir(workspace, outside); err == nil {
			t.Errorf("%s was accepted as a directory to build", outside)
		}
	}
}

func TestTheWholeRepositoryIsBuiltWhenNoDirectoryIsNamed(t *testing.T) {
	workspace := t.TempDir()

	dir, err := buildContextDir(workspace, "")
	if err != nil || dir != workspace {
		t.Errorf("dir = %s, err = %v, want the repository itself", dir, err)
	}
}

func TestADirectoryThatIsNotThereIsRefused(t *testing.T) {
	if _, err := buildContextDir(t.TempDir(), "apps/missing"); err == nil {
		t.Error("a directory that is not in the repository was accepted")
	}
}

// A Dockerfile the user wrote wins over anything we would infer, because they know things about
// their app that cannot be worked out from its files.
func TestADockerfileInTheRepositoryIsUsed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := dockerfileIn(dir, "")
	if err != nil {
		t.Fatalf("dockerfileIn: %v", err)
	}
	if path != filepath.Join(dir, "Dockerfile") {
		t.Errorf("path = %q, want the Dockerfile in the repository", path)
	}
}

// Finding no Dockerfile is not a failure: it is how the build decides to work out for itself how
// to build the app.
func TestNoDockerfileIsNotAFailure(t *testing.T) {
	path, err := dockerfileIn(t.TempDir(), "")
	if err != nil {
		t.Fatalf("a repository without a Dockerfile was treated as broken: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want nothing found", path)
	}
}

func TestANamedDockerfileMustBeInTheRepository(t *testing.T) {
	dir := t.TempDir()

	if _, err := dockerfileIn(dir, "docker/Api.Dockerfile"); err == nil {
		t.Error("a Dockerfile that is not in the repository was accepted")
	}
	if _, err := dockerfileIn(dir, "../../etc/passwd"); err == nil {
		t.Error("a path outside the repository was accepted as a Dockerfile")
	}
}

func TestANamedDockerfileIsUsedWhereItIs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(dir, "docker", "Api.Dockerfile")
	if err := os.WriteFile(named, []byte("FROM alpine"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := dockerfileIn(dir, "docker/Api.Dockerfile")
	if err != nil || path != named {
		t.Errorf("path = %q, err = %v, want %s", path, err, named)
	}
}

// The same build has to produce the same command, or its cache is missed and every deploy builds
// from nothing. Maps in Go are deliberately unordered, so this is not free.
func TestBuildArgumentsComeOutInAStableOrder(t *testing.T) {
	values := map[string]string{"NODE_ENV": "production", "API_URL": "https://example.com", "PORT": "3000"}

	first := sortedPairs(values)
	for range 20 {
		next := sortedPairs(values)
		for i := range first {
			if next[i] != first[i] {
				t.Fatalf("the order changed between builds: %v then %v", first, next)
			}
		}
	}
	if first[0] != "API_URL=https://example.com" {
		t.Errorf("first = %q, want the pairs sorted", first[0])
	}
}

func TestACommitIsShortenedForReading(t *testing.T) {
	if got := shortCommit("9f86d081884c7d659a2feaa0c55ad015"); got != "9f86d08" {
		t.Errorf("shortCommit = %q, want 9f86d08", got)
	}
	if got := shortCommit(""); got == "" {
		t.Error("an absent commit produced an empty phrase mid-sentence")
	}
}

func TestRepeatedImagesAreCountedOnce(t *testing.T) {
	// One image carrying several tags is listed once per tag, and removing it twice is an error.
	ids := uniqueLines("sha256:aaa\nsha256:bbb\nsha256:aaa\n\n  sha256:ccc  \n")

	if len(ids) != 3 {
		t.Fatalf("ids = %v, want three distinct images", ids)
	}
	if ids[0] != "sha256:aaa" || ids[2] != "sha256:ccc" {
		t.Errorf("ids = %v, want them in the order listed", ids)
	}
}
