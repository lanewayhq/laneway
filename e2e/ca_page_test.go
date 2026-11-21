package e2e

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"image"
	"image/draw"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"howett.net/plist"
)

// plainGet sends GET path over plain HTTP to d, for host, without following
// redirects, and returns the response with its body read.
func plainGet(t *testing.T, d *testkit.Daemon, host, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddr()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(body)
}

func TestTheCAPageIsServedOverPlainHTTPOnTheDashboardDomain(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "ca-http.example.test")

	res, body := plainGet(t, d, "laneway.ca-http.example.test", "/ca")

	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d (%s), want the CA page:\n%s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	for _, want := range []string{"iPhone", "Certificate Trust Settings", "Android", "Firefox"} {
		if !strings.Contains(body, want) {
			t.Errorf("the CA page doesn't mention %q:\n%s", want, body)
		}
	}
	for _, r := range []struct{ host, path string }{
		{"laneway.ca-http.example.test", "/"},
		{"laneway.ca-http.example.test", "/cart"},
		{"laneway.ca-http.example.test", "/cab"},
		{"shop.ca-http.example.test", "/ca"},
	} {
		if res, _ := plainGet(t, d, r.host, r.path); res.StatusCode != http.StatusPermanentRedirect {
			t.Errorf("http://%s%s: status %d, want a redirect to HTTPS", r.host, r.path, res.StatusCode)
		}
	}
}

// rootCA is Laneway's root CA certificate as d keeps it on disk.
func rootCA(t *testing.T, d *testkit.Daemon) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(d.Paths.CARootCert())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("%s holds no PEM block", d.Paths.CARootCert())
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// caLink is the path of the CA page's link whose text is text.
func caLink(t *testing.T, page, text string) string {
	t.Helper()
	m := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*>` + regexp.QuoteMeta(text) + `</a>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the CA page has no %q link:\n%s", text, page)
	}
	return m[1]
}

func TestTheCAPageOffersLanewaysRootCertificate(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "ca-cert.example.test")
	dash := "laneway.ca-cert.example.test"
	_, page := plainGet(t, d, dash, "/ca")
	link := caLink(t, page, "Download the certificate")

	plain, body := plainGet(t, d, dash, link)
	secure := d.Get("https://" + dash + link)

	for _, r := range []struct {
		over string
		res  *http.Response
		body string
	}{{"plain HTTP", plain, body}, {"HTTPS", secure.Response, secure.Body}} {
		if r.res.StatusCode != http.StatusOK || r.res.Header.Get("Content-Type") != "application/x-x509-ca-cert" {
			t.Fatalf("over %s: status %d (%s), want a CA certificate", r.over, r.res.StatusCode, r.res.Header.Get("Content-Type"))
		}
		cert, err := x509.ParseCertificate([]byte(r.body))
		if err != nil {
			t.Fatalf("over %s: the download isn't a DER certificate: %v", r.over, err)
		}
		if !cert.IsCA || !bytes.Equal(cert.Raw, rootCA(t, d).Raw) {
			t.Fatalf("over %s: got %q, want Laneway's root CA", r.over, cert.Subject)
		}
	}
}

func TestTheCAPageOffersAnIOSProfileHoldingTheRootCertificate(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "ca-profile.example.test")
	dash := "laneway.ca-profile.example.test"
	_, page := plainGet(t, d, dash, "/ca")

	res, body := plainGet(t, d, dash, caLink(t, page, "Download the profile"))

	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/x-apple-aspen-config" {
		t.Fatalf("status %d (%s), want a configuration profile:\n%s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	var profile struct {
		PayloadType    string
		PayloadContent []struct {
			PayloadType    string
			PayloadContent []byte
		}
	}
	if _, err := plist.Unmarshal([]byte(body), &profile); err != nil {
		t.Fatalf("the profile isn't a property list: %v\n%s", err, body)
	}
	if profile.PayloadType != "Configuration" || len(profile.PayloadContent) != 1 {
		t.Fatalf("want a Configuration with one payload, got %+v", profile)
	}
	payload := profile.PayloadContent[0]
	if payload.PayloadType != "com.apple.security.root" || !bytes.Equal(payload.PayloadContent, rootCA(t, d).Raw) {
		t.Fatalf("the profile's payload is a %q, want Laneway's root CA as com.apple.security.root", payload.PayloadType)
	}
}

func TestNoCAPageResponseHoldsAPrivateKey(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "ca-key.example.test")
	dash := "laneway.ca-key.example.test"
	key, err := os.ReadFile(filepath.Join(filepath.Dir(d.Paths.CARootCert()), "root.key"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(key)
	if block == nil {
		t.Fatal("root.key holds no PEM block")
	}
	_, page := plainGet(t, d, dash, "/ca")
	paths := []string{"/ca", "/ca/qr.svg"}
	for _, m := range regexp.MustCompile(`(?:href|src)="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		paths = append(paths, m[1])
	}

	for _, p := range paths {
		_, plainBody := plainGet(t, d, dash, p)
		for over, body := range map[string]string{"plain HTTP": plainBody, "HTTPS": d.Get("https://" + dash + p).Body} {
			if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, string(block.Bytes)) ||
				strings.Contains(body, base64.StdEncoding.EncodeToString(block.Bytes)) {
				t.Errorf("%s over %s holds the CA's private key", p, over)
			}
		}
	}
}

func TestTheDaemonStatusNamesTheRootDomainAndTheCAPage(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "ca-status.example.test")

	info, err := d.Client().Daemon(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if info.RootDomain != "ca-status.example.test" || info.CAPage != "http://laneway.ca-status.example.test/ca" {
		t.Fatalf("rootDomain %q, caPage %q; want the Root Domain and its plain-HTTP CA page", info.RootDomain, info.CAPage)
	}
}

// decodeQR reads the text of the QR code in svg, as the CA page's QR code
// draws it: a viewBox of whole modules and one path of dark runs, each
// "M<x> <y>h<n>v1h-<n>z".
func decodeQR(t *testing.T, svg string) string {
	t.Helper()
	box := regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`).FindStringSubmatch(svg)
	if box == nil {
		t.Fatalf("no viewBox in the QR code:\n%s", svg)
	}
	size, _ := strconv.Atoi(box[1])
	const scale = 8
	img := image.NewGray(image.Rect(0, 0, size*scale, size*scale))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	for _, m := range regexp.MustCompile(`M(\d+) (\d+)h(\d+)v1h-\d+z`).FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		n, _ := strconv.Atoi(m[3])
		draw.Draw(img, image.Rect(x*scale, y*scale, (x+n)*scale, (y+1)*scale), image.Black, image.Point{}, draw.Src)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatalf("the QR code doesn't decode: %v\n%s", err, svg)
	}
	return res.GetText()
}

func TestTheDashboardsQRCodeOpensTheCAPage(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "ca-qr.example.test")

	res := d.Get("https://laneway.ca-qr.example.test/ca/qr.svg")

	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("status %d (%s), want an SVG:\n%s", res.StatusCode, res.Header.Get("Content-Type"), res.Body)
	}
	if got := decodeQR(t, res.Body); got != "http://laneway.ca-qr.example.test/ca" {
		t.Fatalf("the QR code opens %q, want the CA page over plain HTTP", got)
	}
}

func TestThereIsNoQRCodeWhileTheRootDomainIsLocalhost(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "ca-qr-localhost")

	if res := d.Get(dash + "/ca/qr.svg"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404: no other device reaches localhost", res.StatusCode)
	}
}
