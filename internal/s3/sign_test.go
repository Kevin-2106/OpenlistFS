package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSigV4DocsVector replays the worked example from the AWS SigV4
// documentation: GET /test.txt on examplebucket with a Range header,
// signed at 2013-05-24T00:00:00Z in us-east-1.
func TestSigV4DocsVector(t *testing.T) {
	accessKey := "AKIAIOSFODNN7EXAMPLE"
	secretKey := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	stamp := "20130524T000000Z"
	headers := [][2]string{
		{"host", "examplebucket.s3.amazonaws.com"},
		{"range", "bytes=0-9"},
		{"x-amz-content-sha256", emptyPayload},
		{"x-amz-date", stamp},
	}
	creq := canonicalRequest("GET", "/test.txt", "", headers, emptyPayload)
	if got, want := sha256Hex(creq), "7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972"; got != want {
		t.Fatalf("canonical request hash = %s, want %s\ncreq:\n%s", got, want, creq)
	}
	authz := authorization(accessKey, secretKey, "us-east-1", "GET", "/test.txt", "", headers, emptyPayload, stamp)
	for _, want := range []string{
		"Credential=" + accessKey + "/20130524/us-east-1/s3/aws4_request",
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date",
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
	} {
		if !strings.Contains(authz, want) {
			t.Fatalf("authorization %q\n  missing %q", authz, want)
		}
	}
}

func TestAwsEscape(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"a/b", true, "a%2Fb"},
		{"a/b", false, "a/b"},
		{"a b", true, "a%20b"},
		{"a+b", true, "a%2Bb"},
		{"a#b", true, "a%23b"},
		{"a%b", true, "a%25b"},
		{"Az09-._~", true, "Az09-._~"},
		{"空 目录", false, "%E7%A9%BA%20%E7%9B%AE%E5%BD%95"},
	}
	for _, tc := range cases {
		if got := awsEscape(tc.in, tc.encodeSlash); got != tc.want {
			t.Errorf("awsEscape(%q, %v) = %q, want %q", tc.in, tc.encodeSlash, got, tc.want)
		}
	}
}

// TestRenderQuerySorted checks the canonical ordering that signatures
// depend on — sorted by key, then value, with AWS escaping.
func TestRenderQuerySorted(t *testing.T) {
	got := renderQuery([]queryPair{
		{"prefix", "a/b c"},
		{"list-type", "2"},
		{"delimiter", "/"},
	})
	if want := "delimiter=%2F&list-type=2&prefix=a%2Fb%20c"; got != want {
		t.Errorf("renderQuery = %q, want %q", got, want)
	}
}

// TestSignRequestOnTheWire signs a request against a test server and
// checks the server sees the Authorization header with the expected shape.
func TestSignRequestOnTheWire(t *testing.T) {
	var gotAuthz, gotDate, gotPayload string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthz = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotPayload = r.Header.Get("X-Amz-Content-Sha256")
	}))
	defer srv.Close()

	c := New(srv.URL, "bkt", "us-east-1", "AK", "SK")
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/bkt/dir/a%20b.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.signRequest(req, "/bkt/dir/a%20b.txt", "", emptyPayload,
		time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotDate != "20260918T080000Z" {
		t.Errorf("X-Amz-Date = %q", gotDate)
	}
	if gotPayload != emptyPayload {
		t.Errorf("X-Amz-Content-Sha256 = %q", gotPayload)
	}
	for _, want := range []string{
		"AWS4-HMAC-SHA256 Credential=AK/20260918/us-east-1/s3/aws4_request",
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date",
	} {
		if !strings.Contains(gotAuthz, want) {
			t.Errorf("Authorization %q missing %q", gotAuthz, want)
		}
	}
	if !strings.HasPrefix(gotAuthz, "AWS4-HMAC-SHA256 Credential=AK/") || !strings.Contains(gotAuthz, ",Signature=") {
		t.Errorf("Authorization malformed: %q", gotAuthz)
	}
}
