package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestFilesInTheStaticDirAreServedByPortbyAndEverythingElseByTheProcess(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "static-files").
		WriteFile("portby.toml", fmt.Sprintf("[processes.web]\ncommand = %q\nprimary = true\nstatic_dir = \"public\"\n",
			server+" --name web")).
		WriteFile("public/css/app.css", "body { color: teal }\n").
		WriteFile("public/index.php", "<?php // the front controller\n").
		WriteFile("storage/app/public/avatar.txt", "an uploaded file\n").
		WriteFile("secret.txt", "outside public\n").
		Commit("a Project with public files")
	// As `php artisan storage:link` makes it.
	if err := os.Symlink("../storage/app/public", filepath.Join(repo.Dir, "public", "storage")); err != nil {
		t.Fatal(err)
	}
	d.MustRunCLI(repo.Dir, "link")

	css := d.Get("https://static-files.localhost/css/app.css")
	if css.StatusCode != http.StatusOK || css.Body != "body { color: teal }\n" {
		t.Fatalf("GET /css/app.css = %d %q, want the file", css.StatusCode, css.Body)
	}
	if ct := css.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
	if avatar := d.Get("https://static-files.localhost/storage/avatar.txt"); avatar.Body != "an uploaded file\n" {
		t.Errorf("GET /storage/avatar.txt through the storage link = %d %q, want the file", avatar.StatusCode, avatar.Body)
	}

	// The front controller, directories and missing files are the app's.
	for _, path := range []string{"/", "/index.php", "/css/", "/css", "/missing.css", "/users/1"} {
		if got := d.GetServer("https://static-files.localhost" + path); got.Name != "web" || got.Path != path {
			t.Errorf("GET %s reached %+v, want the web Process", path, got)
		}
	}
	// Nothing outside the static dir is served from disk.
	for _, path := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/storage/../../secret.txt"} {
		if res := d.Get("https://static-files.localhost" + path); strings.Contains(res.Body, "outside public") {
			t.Errorf("GET %s = %q, a file outside public/", path, res.Body)
		}
	}
	// Only GET and HEAD are served from disk.
	resp, err := d.HTTPS().Post("https://static-files.localhost/css/app.css", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"name":"web"`) {
		t.Errorf("POST /css/app.css = %d %q, want the web Process", resp.StatusCode, body)
	}
}
