package containertest

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/lanewayhq/laneway/internal/container"
)

// ObjectStore is a Server for a fake object storage Engine, such as RustFS:
// a little of S3's HTTP API, path-style, kept in the container's Volume
// (see objectStoreOf), shared with the Bucket commands
// (ObjectStoreCommands):
//
//	GET /health/ready                        200 (no signature needed)
//	GET /                                    ListAllMyBucketsResult
//	HEAD, PUT, DELETE /<bucket>              DELETE needs x-rustfs-force-delete unless it's empty
//	GET /<bucket>                            ListBucketResult (prefix= narrows it)
//	GET, PUT, DELETE /<bucket>?tagging
//	HEAD, GET, PUT, DELETE /<bucket>/<key>
//
// Every request but the health check must be signed: it needs an
// Authorization header or an X-Amz-Signature query parameter, which the fake
// doesn't check further. Errors are S3's XML errors with their codes.
func ObjectStore(ln net.Listener, spec container.Spec, data *Volume) {
	store := objectStoreOf(data)
	http.Serve(ln, http.HandlerFunc(store.serveHTTP))
}

// objectStore is the fake object storage server's data: its Buckets, by
// name. It is shared by the container's Server and its commands.
type objectStore struct {
	mu      sync.Mutex
	buckets map[string]*fakeBucket
}

// fakeBucket is one Bucket in the fake object store. Later fields (its
// policy, its CORS origins) record what the Bucket commands configure.
type fakeBucket struct {
	objects map[string]fakeObject
	tags    map[string]string
}

// fakeObject is one object in a fakeBucket.
type fakeObject struct {
	body        []byte
	contentType string
}

// objectStoreOf is the container's object store, kept in its Volume so it
// survives the container stopping and starting again.
func objectStoreOf(data *Volume) *objectStore {
	return data.Shared("objectstore", func() any {
		return &objectStore{buckets: map[string]*fakeBucket{}}
	}).(*objectStore)
}

// bucketName is what RustFS accepts as a Bucket name (spike
// lanewayhq/plugins#13): 3 to 63 lowercase letters, digits and -, starting
// and ending with a letter or digit, and not starting with xn--.
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func validBucketName(name string) bool {
	return bucketName.MatchString(name) && !strings.HasPrefix(name, "xn--")
}

// s3Error is S3's error body.
type s3Error struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeS3Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	xml.NewEncoder(w).Encode(s3Error{Code: code, Message: message})
}

// signed reports whether r carries a signature, header-signed or
// presigned.
func signed(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || r.URL.Query().Get("X-Amz-Signature") != ""
}

func (s *objectStore) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health/ready" {
		return
	}
	if !signed(r) {
		writeS3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied.")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case bucket == "":
		s.serveService(w, r)
	case key == "":
		s.serveBucket(w, r, bucket)
	default:
		s.serveObject(w, r, bucket, key)
	}
}

// serveService answers a request for the server itself: ListBuckets.
func (s *objectStore) serveService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed.")
		return
	}
	type bucket struct {
		Name string `xml:"Name"`
	}
	var list struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Buckets []bucket `xml:"Buckets>Bucket"`
	}
	for _, name := range slices.Sorted(maps.Keys(s.buckets)) {
		list.Buckets = append(list.Buckets, bucket{Name: name})
	}
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(list)
}

// serveBucket answers a request for the Bucket called name.
func (s *objectStore) serveBucket(w http.ResponseWriter, r *http.Request, name string) {
	b := s.buckets[name]
	if r.Method == http.MethodPut && !r.URL.Query().Has("tagging") {
		switch {
		case !validBucketName(name):
			writeS3Error(w, http.StatusBadRequest, "InvalidBucketName", "The specified bucket is not valid.")
		case b != nil:
			writeS3Error(w, http.StatusConflict, "BucketAlreadyOwnedByYou", "Your previous request to create the named bucket succeeded and you already own it.")
		default:
			s.buckets[name] = &fakeBucket{objects: map[string]fakeObject{}, tags: map[string]string{}}
		}
		return
	}
	if b == nil {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	if r.URL.Query().Has("tagging") {
		b.serveTagging(w, r)
		return
	}
	switch r.Method {
	case http.MethodHead:
	case http.MethodDelete:
		if len(b.objects) > 0 && r.Header.Get("x-rustfs-force-delete") != "true" {
			writeS3Error(w, http.StatusConflict, "BucketNotEmpty", "The bucket you tried to delete is not empty")
			return
		}
		delete(s.buckets, name)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		type object struct {
			Key  string `xml:"Key"`
			Size int    `xml:"Size"`
		}
		list := struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Name     string   `xml:"Name"`
			Contents []object `xml:"Contents"`
		}{Name: name}
		prefix := r.URL.Query().Get("prefix")
		for _, key := range slices.Sorted(maps.Keys(b.objects)) {
			if strings.HasPrefix(key, prefix) {
				list.Contents = append(list.Contents, object{Key: key, Size: len(b.objects[key].body)})
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		xml.NewEncoder(w).Encode(list)
	default:
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed.")
	}
}

// tagging is S3's Tagging document.
type tagging struct {
	XMLName xml.Name `xml:"Tagging"`
	Tags    []struct {
		Key   string `xml:"Key"`
		Value string `xml:"Value"`
	} `xml:"TagSet>Tag"`
}

func (b *fakeBucket) serveTagging(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if len(b.tags) == 0 {
			writeS3Error(w, http.StatusNotFound, "NoSuchTagSet", "The TagSet does not exist")
			return
		}
		var t tagging
		for _, k := range slices.Sorted(maps.Keys(b.tags)) {
			t.Tags = append(t.Tags, struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			}{k, b.tags[k]})
		}
		w.Header().Set("Content-Type", "application/xml")
		xml.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t tagging
		if err := xml.NewDecoder(r.Body).Decode(&t); err != nil {
			writeS3Error(w, http.StatusBadRequest, "MalformedXML", err.Error())
			return
		}
		b.tags = map[string]string{}
		for _, tag := range t.Tags {
			b.tags[tag.Key] = tag.Value
		}
	case http.MethodDelete:
		b.tags = map[string]string{}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed.")
	}
}

// serveObject answers a request for the object key in the Bucket called
// bucket.
func (s *objectStore) serveObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	b := s.buckets[bucket]
	if b == nil {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeS3Error(w, http.StatusBadRequest, "IncompleteBody", err.Error())
			return
		}
		b.objects[key] = fakeObject{body: body, contentType: r.Header.Get("Content-Type")}
	case http.MethodGet, http.MethodHead:
		o, ok := b.objects[key]
		if !ok {
			writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		if o.contentType != "" {
			w.Header().Set("Content-Type", o.contentType)
		}
		if r.Method == http.MethodGet {
			w.Write(o.body)
		}
	case http.MethodDelete:
		delete(b.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed.")
	}
}

// objectScript is one of the Bucket commands of an object storage Engine
// manifest (RustFS's, or the objects test Engine's) as the fake recognises
// it: sh -c '<script>' sh <args>..., with args arguments and a script
// containing marker, done as run.
type objectScript struct {
	name   string
	args   int
	marker string
	run    func(s *objectStore, args []string) container.Output
}

// objectScripts are the Bucket commands the fake answers, matched in order
// by their number of arguments and a marker in their script, which is what
// the RustFS spike's scripts (lanewayhq/plugins#13) do:
//
//	list:   sh -c '… curl … / … <Name> …' (no arguments)
//	create: sh -c '… curl -X DELETE … x-rustfs-force-delete …; curl -X PUT …' sh <bucket>
//	drop:   sh -c '… curl -X DELETE -H 'x-rustfs-force-delete: true' …' sh <bucket>
//	exists: sh -c '… curl -I …/<bucket>; curl …/<bucket>?tagging …' sh <bucket>
var objectScripts = []objectScript{
	{"list", 0, "<Name>", (*objectStore).listScript},
	{"create", 1, "-X PUT", (*objectStore).createScript},
	{"drop", 1, "x-rustfs-force-delete", (*objectStore).dropScript},
	{"exists", 1, "?tagging", (*objectStore).existsScript},
}

// ObjectStoreCommands answers the Bucket commands of an object storage
// Engine run in a fake object store container (see objectScripts), as the
// scripts would against the server in the container (ObjectStore). A
// script fails as curl's does, exiting 1 with S3's error on stderr.
func ObjectStoreCommands(_ context.Context, spec container.Spec, data *Volume, cmd []string) container.Output {
	if cmd[0] != "sh" {
		return container.Output{ExitCode: 127, Stderr: fmt.Appendf(nil, "%s: command not found\n", cmd[0])}
	}
	var args []string
	if len(cmd) > 4 {
		args = cmd[4:]
	}
	if len(cmd) >= 3 && cmd[1] == "-c" {
		for _, script := range objectScripts {
			if len(args) == script.args && strings.Contains(cmd[2], script.marker) {
				return script.run(objectStoreOf(data), args)
			}
		}
	}
	return container.Output{ExitCode: 2, Stderr: fmt.Appendf(nil, "the fake object store doesn't know the script %q\n", cmd)}
}

// unfinished reports whether b is a copy a clone command left unfinished,
// tagged laneway-clone, which isn't a Bucket yet.
func (b *fakeBucket) unfinished() bool {
	_, ok := b.tags["laneway-clone"]
	return ok
}

func (s *objectStore) listScript(_ []string) container.Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out strings.Builder
	for _, name := range slices.Sorted(maps.Keys(s.buckets)) {
		out.WriteString(name + "\n")
	}
	return container.Output{Stdout: []byte(out.String())}
}

func (s *objectStore) existsScript(args []string) container.Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.buckets[args[0]]; b != nil && !b.unfinished() {
		return container.Output{Stdout: []byte(args[0] + "\n")}
	}
	return container.Output{}
}

func (s *objectStore) createScript(args []string) container.Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := args[0]
	if !validBucketName(name) {
		return s3Failure("InvalidBucketName", "The specified bucket is not valid.")
	}
	// exists said no, so a Bucket already here is a leftover.
	s.buckets[name] = &fakeBucket{objects: map[string]fakeObject{}, tags: map[string]string{}}
	return container.Output{}
}

func (s *objectStore) dropScript(args []string) container.Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.buckets, args[0])
	return container.Output{}
}

// s3Failure is how the scripts fail on an S3 error: curl exits 1 with the
// error's XML on stderr.
func s3Failure(code, message string) container.Output {
	body, _ := xml.Marshal(s3Error{Code: code, Message: message})
	return container.Output{ExitCode: 1, Stderr: append(body, '\n')}
}
