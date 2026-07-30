package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// archiveOf builds a repository archive, wrapped in the commit-named directory a real one has.
func archiveOf(t *testing.T, entries []*tar.Header, bodies []string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)

	for i, header := range entries {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if bodies[i] != "" {
			if _, err := writer.Write([]byte(bodies[i])); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close compression: %v", err)
	}
	return buffer.Bytes()
}

func file(name, body string) (*tar.Header, string) {
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}, body
}

// The directory a repository archive wraps everything in is named after the commit and is not part
// of the repository, so paths have to match what the user sees in their editor.
func TestTheDirectoryTheArchiveIsWrappedInIsDropped(t *testing.T) {
	header, body := file("owner-repo-abc123/cmd/app/main.go", "package main")
	dir := t.TempDir()

	if err := extractTarGz(bytes.NewReader(archiveOf(t, []*tar.Header{header}, []string{body})), dir); err != nil {
		t.Fatalf("expand: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "cmd", "app", "main.go")); err != nil {
		t.Errorf("the file is not where the repository has it: %v", err)
	}
}

// The archive comes from wherever the repository is hosted, so an entry naming a path outside the
// build must not be written. Anything else would let an archive overwrite files on the machine.
func TestAnArchiveCannotWriteOutsideTheBuild(t *testing.T) {
	header, body := file("owner-repo-abc123/../../escaped", "anywhere")
	dir := t.TempDir()

	err := extractTarGz(bytes.NewReader(archiveOf(t, []*tar.Header{header}, []string{body})), dir)
	if err == nil {
		t.Fatal("an entry pointing outside the build was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escaped")); statErr == nil {
		t.Error("a file was written outside the build")
	}
}

// A link in the archive could point at anything on the machine, including the agent's own
// credential. Nothing needs one to build, so they are left out.
func TestLinksInAnArchiveAreLeftOut(t *testing.T) {
	dir := t.TempDir()
	entries := []*tar.Header{
		{Name: "owner-repo-abc123/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/yol/credential"},
		{Name: "owner-repo-abc123/main.go", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
	}

	if err := extractTarGz(bytes.NewReader(archiveOf(t, entries, []string{"", "main"})), dir); err != nil {
		t.Fatalf("expand: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Error("a link was written into the build")
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Errorf("the rest of the archive was not expanded: %v", err)
	}
}

func TestExecutablePermissionsSurvive(t *testing.T) {
	dir := t.TempDir()
	header := &tar.Header{Name: "owner-repo-abc123/build.sh", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2}

	if err := extractTarGz(bytes.NewReader(archiveOf(t, []*tar.Header{header}, []string{"ok"})), dir); err != nil {
		t.Fatalf("expand: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "build.sh"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("mode = %v, want the file still runnable", info.Mode())
	}
}

func TestSomethingThatIsNotAnArchiveIsRefused(t *testing.T) {
	if err := extractTarGz(bytes.NewReader([]byte("this is not an archive")), t.TempDir()); err == nil {
		t.Error("something that is not an archive was accepted")
	}
}
