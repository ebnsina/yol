// Command fake-github answers as GitHub does, for the parts a deploy uses.
//
// It exists so the whole path — a push arriving, a token being minted, code being fetched, an image
// being built on a real server, traffic moving to it — can be proven without a repository, an
// installation, or a network. Nothing in the control plane or the agent behaves differently: the
// address of GitHub is configuration, and this is what the harness points it at.
//
// Two commits are served, "one" and "two", differing only in what the app answers with. That is
// enough to tell whether traffic actually moved.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// versionFile is how the script says which commit the branch points at now. A file rather than an
// endpoint, so moving it looks like somebody pushing rather than like a test poking at a mock.
var versionFile string

func main() {
	addr := flag.String("addr", ":8099", "where to listen")
	flag.StringVar(&versionFile, "version-file", "", "file naming which commit the branch points at")
	flag.Parse()

	mux := http.NewServeMux()

	// A token for one installation. Any request is answered: what is being proven is the deploy,
	// not GitHub's own checks.
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"token":      "ghs_stand_in_token",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id":      42,
			"account": map[string]any{"login": "harness"},
		})
	})

	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"total_count": 1,
			"repositories": []map[string]any{{
				"id":             987,
				"full_name":      "harness/shop",
				"private":        true,
				"default_branch": "main",
			}},
		})
	})

	// The head of a branch. Which commit that is comes from a file the script writes, so a second
	// deploy builds something different.
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{ref}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"sha": currentCommit()})
	})

	// The code itself, as an archive of one commit, wrapped in a directory named after it exactly
	// as a real archive is.
	mux.HandleFunc("GET /repos/{owner}/{repo}/tarball/{sha}", func(w http.ResponseWriter, r *http.Request) {
		sha := r.PathValue("sha")
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			// A build with no credential must fail rather than quietly succeed on nothing.
			http.Error(w, `{"message":"Requires authentication"}`, http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/gzip")
		if err := writeArchive(w, sha); err != nil {
			log.Printf("could not write the archive: %v", err)
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("nothing here answers %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})

	log.Printf("standing in for GitHub on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// currentCommit is which commit the branch points at. Changed by writing the file, which is how the
// script asks for a second, different version.
func currentCommit() string {
	if versionFile != "" {
		if contents, err := os.ReadFile(versionFile); err == nil {
			if strings.TrimSpace(string(contents)) == "two" {
				return "2222222222222222222222222222222222222222"
			}
		}
	}
	return "1111111111111111111111111111111111111111"
}

// writeArchive builds the repository: a Dockerfile and the page the app serves. Deliberately tiny,
// because the point is the deploy rather than the build, and busybox already has a web server.
func writeArchive(w http.ResponseWriter, sha string) error {
	answer := "version-one"
	if strings.HasPrefix(sha, "2") {
		answer = "version-two"
	}

	dockerfile := `FROM alpine:3.20
RUN mkdir -p /site
COPY index.html /site/index.html
EXPOSE 3000
CMD ["httpd", "-f", "-p", "3000", "-h", "/site"]
`

	gz := gzip.NewWriter(w)
	defer gz.Close()
	archive := tar.NewWriter(gz)
	defer archive.Close()

	// The directory a real archive wraps everything in, named after the commit.
	prefix := fmt.Sprintf("harness-shop-%s/", sha[:7])
	for name, body := range map[string]string{
		"Dockerfile": dockerfile,
		"index.html": answer + "\n",
	} {
		if err := archive.WriteHeader(&tar.Header{
			Name:     prefix + name,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(body)),
		}); err != nil {
			return err
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
