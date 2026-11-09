package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// signedS3Request is a request to rawURL signed with AWS Signature Version
// 4 for S3 in us-east-1, as an S3 client such as Laravel's signs it, with
// header (name, value pairs) set first. It signs the Host (the URL's host
// and port), every x-amz-* header and Content-Type, so the server refuses
// it if a proxy changes any of them.
func signedS3Request(t *testing.T, method, rawURL, body, user, password string, now time.Time, header ...string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	payload := sha256.Sum256([]byte(body))
	stamp := now.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(payload[:]))

	values := map[string]string{"host": req.URL.Host}
	for name, v := range req.Header {
		if name := strings.ToLower(name); strings.HasPrefix(name, "x-amz-") || name == "content-type" {
			values[name] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + values[name] + "\n")
	}
	signed := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{method, path, canonicalQuery(req.URL.Query()),
		canonicalHeaders.String(), signed, hex.EncodeToString(payload[:])}, "\n")
	scope := day + "/us-east-1/s3/aws4_request"
	hashed := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hashed[:])
	key := []byte("AWS4" + password)
	for _, part := range []string{day, "us-east-1", "s3", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+user+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSHA256(key, toSign)))
	return req
}

// presignedS3URL is rawURL presigned for method with AWS Signature Version
// 4 for S3 in us-east-1, valid for expires from now, as an S3 client such
// as Laravel's temporaryUrl() presigns it: the signature is in the query
// and covers only the Host (the URL's host and port), so anyone may send
// the request, with any body, until it expires.
func presignedS3URL(t *testing.T, method, rawURL, user, password string, now time.Time, expires time.Duration) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	scope := day + "/us-east-1/s3/aws4_request"
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", user+"/"+scope)
	q.Set("X-Amz-Date", stamp)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{method, path, canonicalQuery(q), "host:" + u.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	hashed := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hashed[:])
	key := []byte("AWS4" + password)
	for _, part := range []string{day, "us-east-1", "s3", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(key, toSign)))
	u.RawQuery = canonicalQuery(q)
	return u.String()
}

// canonicalQuery is SigV4's canonical query string: each key and value
// URI-encoded, sorted by key ("policy=" for ?policy).
func canonicalQuery(q url.Values) string {
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, uriEncode(k)+"="+uriEncode(v))
		}
	}
	slices.Sort(pairs)
	return strings.Join(pairs, "&")
}

// uriEncode percent-encodes everything but SigV4's unreserved characters.
func uriEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// readResponse reads res's whole body and closes it.
func readResponse(t *testing.T, res *http.Response) testkit.Response {
	t.Helper()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return testkit.Response{Response: res, Body: string(body)}
}
