package e2e

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// The vite Process's slow tier: a real Laravel app with laravel-vite-plugin,
// run by real PHP, Composer, Node, npm and Vite, its database in SQLite.
//
//	PORTBY_SLOW=1 \
//	PORTBY_PHP=/path/to/php \
//	PORTBY_COMPOSER=/path/to/composer.phar \
//	PORTBY_LARAVEL_APP=/path/to/a/laravel/app \
//	go test ./e2e -run TestARealLaravelAppLoadsItsAssets
//
// node and npm come from PATH. npm installs laravel-vite-plugin 3 and Vite 8
// offline, from $PORTBY_NPM_CACHE or the user's ~/.npm, so they must have
// been installed once.

const viteBlade = `<!DOCTYPE html>
<html>
<head>@vite(['resources/css/app.css', 'resources/js/app.js'])</head>
<body>the portby vite page</body>
</html>
`

func TestARealLaravelAppLoadsItsAssetsAndLiveReloadsThroughViteDotItsDomain(t *testing.T) {
	if os.Getenv("PORTBY_SLOW") == "" {
		t.Skip("slow tier: set PORTBY_SLOW=1 to run a real Laravel app with Vite")
	}
	php, composer, app := os.Getenv("PORTBY_PHP"), os.Getenv("PORTBY_COMPOSER"), os.Getenv("PORTBY_LARAVEL_APP")
	if php == "" || composer == "" || app == "" {
		t.Skip("set PORTBY_PHP, PORTBY_COMPOSER and PORTBY_LARAVEL_APP to run a real Laravel app")
	}
	t.Parallel()
	d := testkit.StartDaemon(t)
	realLaravelTools(t, d, php, composer)
	realNodeTools(t, d)
	project := "lara-vite-real"
	domain := project + ".localhost"
	repo := realLaravelViteRepo(t, project, app)

	d.MustRunCLI(repo.Dir, "link", "--yes")
	if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
		t.Fatalf("setup is %+v, want Ready", setup)
	}

	// The first page (held while the Worktree starts) names Vite at
	// vite.<domain> on the proxy, from the public/hot Vite wrote.
	_, port, _ := net.SplitHostPort(d.ProxyAddr())
	vite := "https://vite." + domain + ":" + port
	page := d.Get("https://" + domain + "/portby-vite")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("GET /portby-vite = %d:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, `src="`+vite+`/@vite/client"`)
	assertContains(t, page.Body, `href="`+vite+`/resources/css/app.css"`)
	assertFile(t, filepath.Join(repo.Dir, "public", "hot"), vite)

	// Assets load over HTTPS, and the page's origin may use them.
	css := viteGet(t, d, vite+"/resources/css/app.css", "https://"+domain)
	assertContains(t, css.Body, "color: teal")
	if got := css.Header.Get("Access-Control-Allow-Origin"); got != "https://"+domain {
		t.Errorf("Access-Control-Allow-Origin = %q, want the page's origin", got)
	}
	client := viteGet(t, d, vite+"/@vite/client", "https://"+domain).Body
	assertContains(t, client, `const socketProtocol = "wss"`)
	assertContains(t, client, `const hmrPort = `+port+`;`)
	assertContains(t, client, `"vite.`+domain+`"`)
	token := regexp.MustCompile(`const wsToken = "([^"]+)"`).FindStringSubmatch(client)
	if token == nil {
		t.Fatalf("no wsToken in @vite/client:\n%s", client)
	}

	// Live reload: Vite's HMR WebSocket through the proxy.
	ws := openViteHMR(t, d, "vite."+domain, port, token[1])
	ws.waitFor(t, `"type":"connected"`)
	writeFile(t, filepath.Join(repo.Dir, "resources", "css", "app.css"), ".portby { color: tomato; }\n")
	ws.waitFor(t, `"path":"/resources/css/app.css"`)
	writeFile(t, filepath.Join(repo.Dir, "resources", "views", "portby-vite.blade.php"), strings.Replace(viteBlade, "page", "edited page", 1))
	ws.waitFor(t, `"type":"full-reload"`)
}

// realNodeTools puts node and an npm on the daemon's PATH that installs
// offline from the user's npm cache.
func realNodeTools(t *testing.T, d *testkit.Daemon) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("no npm on PATH")
	}
	if npm, err = filepath.EvalSymlinks(npm); err != nil {
		t.Fatal(err)
	}
	cache := os.Getenv("PORTBY_NPM_CACHE")
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(home, ".npm")
	}
	if err := os.Symlink(node, filepath.Join(d.Bin, "node")); err != nil {
		t.Fatal(err)
	}
	testkit.WriteExecutable(t, d.Bin, "npm", "npm_config_cache="+shellQuoted(cache)+
		" npm_config_offline=true npm_config_audit=false npm_config_fund=false exec node "+shellQuoted(npm)+" \"$@\"\n")
}

// realLaravelViteRepo is realLaravelRepo on SQLite, using laravel-vite-plugin
// with a page that loads its assets through @vite.
func realLaravelViteRepo(t *testing.T, name, app string) *gitfixture.Repo {
	t.Helper()
	repo := realLaravelRepo(t, name, app)
	example := strings.Replace(readFile(t, filepath.Join(repo.Dir, ".env.example")), "DB_CONNECTION=mysql", "DB_CONNECTION=sqlite", 1)
	return repo.
		WriteFile(".env.example", example).
		WriteFile("package.json", `{"private": true, "type": "module", "devDependencies": {"laravel-vite-plugin": "^3.1", "vite": "^8.0.0"}}`+"\n").
		WriteFile("vite.config.js", `import { defineConfig } from 'vite';
import laravel from 'laravel-vite-plugin';

export default defineConfig({
    plugins: [laravel({ input: ['resources/css/app.css', 'resources/js/app.js'], refresh: true })],
});
`).
		WriteFile("resources/css/app.css", ".portby { color: teal; }\n").
		WriteFile("resources/js/app.js", "document.body.dataset.portby = 'js';\n").
		WriteFile("resources/views/portby-vite.blade.php", viteBlade).
		WriteFile("routes/web.php", "<?php\n\nuse Illuminate\\Support\\Facades\\Route;\n\nRoute::view('/portby-vite', 'portby-vite');\n").
		Commit("use laravel-vite-plugin")
}

// viteGet GETs url as a page at origin loading a module from it does.
func viteGet(t *testing.T, d *testkit.Daemon, url, origin string) testkit.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	res, err := d.HTTPS().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d:\n%s", url, res.StatusCode, body)
	}
	return testkit.Response{Response: res, Body: string(body)}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// viteHMR is a WebSocket to Vite's HMR server, as @vite/client opens it.
type viteHMR struct {
	conn *tls.Conn
	br   *bufio.Reader
}

func openViteHMR(t *testing.T, d *testkit.Daemon, host, port, token string) *viteHMR {
	t.Helper()
	conn, err := tls.Dial("tcp", d.ProxyAddr(), &tls.Config{RootCAs: d.CAPool(), ServerName: host, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "GET /?token=%s HTTP/1.1\r\nHost: %s:%s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: vite-hmr\r\n\r\n",
		token, host, port)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("the HMR WebSocket upgrade: status %d: %s", resp.StatusCode, body)
	}
	return &viteHMR{conn: conn, br: br}
}

// waitFor reads messages until one contains want.
func (ws *viteHMR) waitFor(t *testing.T, want string) {
	t.Helper()
	ws.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var seen []string
	for {
		msg, err := ws.read()
		if err != nil {
			t.Fatalf("waiting for a message with %s: %v; got %q", want, err, seen)
		}
		if strings.Contains(msg, want) {
			return
		}
		seen = append(seen, msg)
	}
}

// read reads one unmasked frame from the server and returns its payload.
func (ws *viteHMR) read() (string, error) {
	var head [2]byte
	if _, err := io.ReadFull(ws.br, head[:]); err != nil {
		return "", err
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(ws.br, b[:]); err != nil {
			return "", err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(ws.br, b[:]); err != nil {
			return "", err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(ws.br, payload); err != nil {
		return "", err
	}
	if head[0]&0x0f == 0x8 {
		return "", fmt.Errorf("closed by Vite: %q", payload)
	}
	return string(payload), nil
}
