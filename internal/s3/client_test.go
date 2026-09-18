package s3

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is a minimal in-memory S3 server: ListObjectsV2 (delimiter,
// max-keys, continuation tokens, encoding-type=url), HEAD/GET (Range),
// PUT (incl. server-side copy), DELETE — with an independent SigV4
// verifier that recomputes the signature from the received wire bytes.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string]fakeObject
	accessKey string
	secretKey string
	bucket   string
	srv      *httptest.Server
}

type fakeObject struct {
	data  []byte
	mtime time.Time
}

func newFakeS3(t *testing.T, accessKey, secretKey string) (*fakeS3, *Client) {
	t.Helper()
	f := &fakeS3{
		objects:   make(map[string]fakeObject),
		accessKey: accessKey,
		secretKey: secretKey,
		bucket:    "media",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	c := New(f.srv.URL, f.bucket, "us-east-1", accessKey, secretKey)
	return f, c
}

func (f *fakeS3) seed(key string, data string, mtime time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{data: []byte(data), mtime: mtime}
}

// ---- SigV4 verification (independent of how the request was built) ----

func (f *fakeS3) verify(r *http.Request) error {
	if f.accessKey == "" {
		return nil
	}
	if authz := r.Header.Get("Authorization"); authz != "" {
		fields := map[string]string{}
		for _, part := range strings.Split(strings.TrimPrefix(authz, sigAlgo+" "), ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			fields[k] = v
		}
		cred := strings.Split(fields["Credential"], "/")
		if len(cred) != 5 || cred[0] != f.accessKey {
			return fmt.Errorf("credential %q", fields["Credential"])
		}
		date, region := cred[1], cred[2]
		var headers [][2]string
		for _, name := range strings.Split(fields["SignedHeaders"], ";") {
			if name == "host" {
				headers = append(headers, [2]string{"host", r.Host})
			} else {
				headers = append(headers, [2]string{name, r.Header.Get(name)})
			}
		}
		creq := canonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
			headers, r.Header.Get("x-amz-content-sha256"))
		sts := strings.Join([]string{sigAlgo, r.Header.Get("x-amz-date"),
			date + "/" + region + "/s3/aws4_request", sha256Hex(creq)}, "\n")
		want := hex.EncodeToString(hmacSHA256(signingKey(f.secretKey, date, region), sts))
		if want != fields["Signature"] {
			return fmt.Errorf("signature mismatch: got %s want %s", fields["Signature"], want)
		}
		return nil
	}
	// Presigned: everything but X-Amz-Signature joins the canonical query.
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") == "" {
		return errors.New("request carries no authentication")
	}
	cred := strings.Split(q.Get("X-Amz-Credential"), "/")
	if len(cred) != 5 || cred[0] != f.accessKey {
		return fmt.Errorf("presign credential %q", q.Get("X-Amz-Credential"))
	}
	var pairs []queryPair
	for k, vs := range q {
		if k == "X-Amz-Signature" {
			continue
		}
		for _, v := range vs {
			pairs = append(pairs, queryPair{k, v})
		}
	}
	creq := canonicalRequest(r.Method, r.URL.EscapedPath(), renderQuery(pairs),
		[][2]string{{"host", r.Host}}, unsignedPayload)
	sts := strings.Join([]string{sigAlgo, q.Get("X-Amz-Date"),
		cred[1] + "/" + cred[2] + "/s3/aws4_request", sha256Hex(creq)}, "\n")
	want := hex.EncodeToString(hmacSHA256(signingKey(f.secretKey, cred[1], cred[2]), sts))
	if want != q.Get("X-Amz-Signature") {
		return fmt.Errorf("presign signature mismatch: got %s want %s", q.Get("X-Amz-Signature"), want)
	}
	return nil
}

// ---- S3 protocol surface ----

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if err := f.verify(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.URL.Path == "/"+f.bucket {
		f.serveList(w, r)
		return
	}
	// r.URL.Path is already percent-decoded by net/http.
	key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
	switch r.Method {
	case http.MethodHead:
		f.mu.Lock()
		obj, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
		w.Header().Set("Last-Modified", obj.mtime.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f.mu.Lock()
		obj, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		data := obj.data
		if rng := r.Header.Get("Range"); rng != "" {
			a, b, ok := parseRange(rng, int64(len(data)))
			if !ok {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(int(b-a+1)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[a : b+1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	case http.MethodPut:
		if src := r.Header.Get("x-amz-copy-source"); src != "" {
			srcKey, err := url.PathUnescape(strings.TrimPrefix(src, "/"+f.bucket+"/"))
			if err != nil {
				http.Error(w, "bad escape", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			obj, ok := f.objects[srcKey]
			if ok {
				f.objects[key] = obj
			}
			f.mu.Unlock()
			if !ok {
				http.Error(w, "NoSuchKey", http.StatusNotFound)
				return
			}
			w.Write([]byte(`<CopyObjectResult><ETag>"x"</ETag></CopyObjectResult>`))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.objects[key] = fakeObject{data: body, mtime: time.Now()}
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func parseRange(h string, size int64) (int64, int64, bool) {
	var a, b int64
	if _, err := fmt.Sscanf(h, "bytes=%d-%d", &a, &b); err != nil {
		return 0, 0, false
	}
	if a >= size || b >= size || a > b {
		return 0, 0, false
	}
	return a, b, true
}

func (f *fakeS3) serveList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	maxKeys := 1000
	if mk := q.Get("max-keys"); mk != "" {
		maxKeys, _ = strconv.Atoi(mk)
	}
	token := q.Get("continuation-token")

	f.mu.Lock()
	keys := make([]string, 0, len(f.objects))
	for k := range f.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	var contents []string
	var prefixes []string
	seen := map[string]bool{}
	emitted := 0
	truncated := false
	next := ""
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if token != "" && k <= token {
			continue
		}
		item := k
		isPrefix := false
		if delimiter != "" {
			if i := strings.Index(k[len(prefix):], delimiter); i >= 0 {
				item = k[:len(prefix)+i+1]
				isPrefix = true
			}
		}
		if seen[item] {
			continue
		}
		seen[item] = true
		if emitted == maxKeys {
			truncated = true
			break
		}
		emitted++
		next = item
		f.mu.Lock()
		obj := f.objects[k]
		f.mu.Unlock()
		if isPrefix {
			prefixes = append(prefixes, item)
		} else {
			contents = append(contents, fmt.Sprintf(
				`<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>`,
				awsEscape(item, false), obj.mtime.UTC().Format(time.RFC3339), len(obj.data)))
		}
	}
	b.WriteString(fmt.Sprintf(`<IsTruncated>%t</IsTruncated>`, truncated))
	if truncated {
		b.WriteString(fmt.Sprintf("<NextContinuationToken>%s</NextContinuationToken>", awsEscape(next, false)))
	}
	b.WriteString(strings.Join(contents, ""))
	for _, p := range prefixes {
		b.WriteString(fmt.Sprintf("<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", awsEscape(p, false)))
	}
	b.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, b.String())
}

// ---- tests ----

func seedStore(t *testing.T) (*fakeS3, *Client) {
	f, c := newFakeS3(t, "AKEXAMPLE", "SKEXAMPLE")
	mod := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f.seed("videos/hello.txt", "0123456789", mod)
	// Pathological names: space, unicode, '#', '%', '+'.
	f.seed("videos/weird #1%+.txt", "weird", mod)
	f.seed("videos/空 目录/深 file.bin", "deep", mod)
	f.seed("docs/readme.md", "read me", mod)
	f.seed("root.txt", "r", mod)
	return f, c
}

func entryKeys(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}

func TestListRootAndSubdirs(t *testing.T) {
	_, c := seedStore(t)
	ctx := context.Background()

	root, err := c.List(ctx, "")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	want := []string{"docs/", "root.txt", "videos/"}
	if got := entryKeys(root); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("root = %v, want %v", got, want)
	}
	for _, e := range root {
		if e.Key == "root.txt" && (e.IsDir || e.Size != 1) {
			t.Errorf("root.txt = %+v", e)
		}
		if (e.Key == "docs/" || e.Key == "videos/") && !e.IsDir {
			t.Errorf("%s should be a dir", e.Key)
		}
	}

	videos, err := c.List(ctx, "videos/")
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	want = []string{"videos/hello.txt", "videos/weird #1%+.txt", "videos/空 目录/"}
	if got := entryKeys(videos); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("videos = %v, want %v", got, want)
	}
	hello, err := c.Head(ctx, "videos/hello.txt")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if hello.Size != 10 || hello.ModTime.IsZero() {
		t.Errorf("hello = %+v", hello)
	}
}

func TestHeadAndPrefixChecks(t *testing.T) {
	_, c := seedStore(t)
	ctx := context.Background()

	if _, err := c.Head(ctx, "nope.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("head missing = %v, want ErrNotExist", err)
	}
	if ok, _ := c.PrefixHasKeys(ctx, "videos/"); !ok {
		t.Error("videos/ should have keys")
	}
	if ok, _ := c.PrefixHasKeys(ctx, "nope/"); ok {
		t.Error("nope/ should not have keys")
	}
}

func TestListPagination(t *testing.T) {
	f, c := seedStore(t)
	f.seed("p/3.txt", "3", time.Now())
	f.seed("p/1.txt", "1", time.Now())
	f.seed("p/2.txt", "2", time.Now())
	ctx := context.Background()

	var got []string
	token := ""
	for {
		res, err := c.listPage(ctx, "p/", token, 1, true)
		if err != nil {
			t.Fatalf("listPage: %v", err)
		}
		for _, e := range res.files {
			got = append(got, e.Key)
		}
		if !res.truncated {
			break
		}
		token = res.nextToken
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"p/1.txt", "p/2.txt", "p/3.txt"}) {
		t.Errorf("paged keys = %v", got)
	}
}

func TestMutations(t *testing.T) {
	_, c := seedStore(t)
	ctx := context.Background()

	if err := c.Put(ctx, "newdir/", 0, nil); err != nil {
		t.Fatalf("mkdir marker: %v", err)
	}
	if err := c.Put(ctx, "newdir/file.bin", 4, strings.NewReader("abcd")); err != nil {
		t.Fatalf("put: %v", err)
	}
	e, err := c.Head(ctx, "newdir/file.bin")
	if err != nil || e.Size != 4 {
		t.Fatalf("after put: %+v %v", e, err)
	}
	if err := c.Copy(ctx, "newdir/file.bin", "newdir/renamed.bin"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if e, err := c.Head(ctx, "newdir/renamed.bin"); err != nil || e.Size != 4 {
		t.Fatalf("after copy: %+v %v", e, err)
	}
	if err := c.Delete(ctx, "newdir/file.bin"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Head(ctx, "newdir/file.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after delete = %v", err)
	}
	if ok, _ := c.PrefixHasKeys(ctx, "newdir/"); !ok {
		t.Error("dir contents must survive a single-key delete")
	}
}

func TestPresignGetServesRange(t *testing.T) {
	_, c := seedStore(t)
	u := c.PresignGet("videos/weird #1%+.txt", time.Hour)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=1-3") // unsigned header — must be allowed
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "eir" {
		t.Errorf("presigned GET: %d %q", resp.StatusCode, body)
	}
}

func TestAuthRejected(t *testing.T) {
	_, c := seedStore(t)
	bad := New(c.Endpoint, c.Bucket, "us-east-1", "AKEXAMPLE", "wrong-secret")
	if _, err := bad.List(context.Background(), ""); !errors.Is(err, os.ErrPermission) {
		t.Errorf("want ErrPermission, got %v", err)
	}
}

func TestAnonymousAccess(t *testing.T) {
	f, _ := newFakeS3(t, "", "")
	f.seed("pub.txt", "open", time.Now())
	c := New(f.srv.URL, f.bucket, "us-east-1", "", "")
	entries, err := c.List(context.Background(), "")
	if err != nil || len(entries) != 1 || entries[0].Key != "pub.txt" {
		t.Fatalf("anonymous list: %v %+v", err, entries)
	}
	if u := c.PresignGet("pub.txt", time.Hour); strings.Contains(u, "X-Amz-Signature") {
		t.Errorf("anonymous presign should be a plain URL: %s", u)
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in                 string
		endpoint, bucket   string
		region             string
		wantErr            bool
	}{
		{"https://minio.example:9000/media", "https://minio.example:9000", "media", "us-east-1", false},
		{"https://s3.eu-west-1.amazonaws.com/mybucket", "https://s3.eu-west-1.amazonaws.com", "mybucket", "eu-west-1", false},
		{"https://s3.us-east-1.amazonaws.com.cn/cn-bucket", "https://s3.us-east-1.amazonaws.com.cn", "cn-bucket", "us-east-1", false},
		{"https://s3.amazonaws.com/b", "https://s3.amazonaws.com", "b", "us-east-1", false},
		{"http://127.0.0.1:9000/b?region=eu-central-1", "http://127.0.0.1:9000", "b", "eu-central-1", false},
		{"https://example.com", "", "", "", true},
		{"", "", "", "", true},
	}
	for _, tc := range cases {
		endpoint, bucket, region, err := ParseTarget(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTarget(%q) = no error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTarget(%q): %v", tc.in, err)
			continue
		}
		if endpoint != tc.endpoint || bucket != tc.bucket || region != tc.region {
			t.Errorf("ParseTarget(%q) = %q %q %q, want %q %q %q",
				tc.in, endpoint, bucket, region, tc.endpoint, tc.bucket, tc.region)
		}
	}
}
