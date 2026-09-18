package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AWS Signature V4 for service "s3", hand-rolled to keep the dependency
// surface at zero. Only the two shapes this client needs: header-signed
// requests and presigned GETs.

const (
	sigAlgo         = "AWS4-HMAC-SHA256"
	emptyPayload    = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// awsEscape percent-encodes everything but RFC 3986 unreserved characters
// (and '/' when encodeSlash is false). url.QueryEscape is unusable here —
// it encodes spaces as '+'.
func awsEscape(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~',
			c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xF])
		}
	}
	return b.String()
}

// queryPair is one query parameter. The request URL and the canonical
// query string are rendered from the same pairs so they cannot diverge.
type queryPair struct{ k, v string }

// renderQuery renders pairs sorted by key (then value), AWS-escaped.
func renderQuery(pairs []queryPair) string {
	sorted := append([]queryPair(nil), pairs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].k != sorted[j].k {
			return sorted[i].k < sorted[j].k
		}
		return sorted[i].v < sorted[j].v
	})
	parts := make([]string, len(sorted))
	for i, p := range sorted {
		parts[i] = awsEscape(p.k, true) + "=" + awsEscape(p.v, true)
	}
	return strings.Join(parts, "&")
}

// signingKey derives the SigV4 key chain for service "s3".
func signingKey(secret, date, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "s3")
	return hmacSHA256(kService, "aws4_request")
}

// canonicalRequest assembles the SigV4 canonical request. headers must
// carry lowercase names, trimmed values, and be sorted by name; the block
// keeps each header's trailing newline, which is what produces the blank
// line before the signed-headers list.
func canonicalRequest(method, uri, query string, headers [][2]string, payloadHash string) string {
	var hb strings.Builder
	names := make([]string, len(headers))
	for i, h := range headers {
		hb.WriteString(h[0])
		hb.WriteByte(':')
		hb.WriteString(h[1])
		hb.WriteByte('\n')
		names[i] = h[0]
	}
	parts := []string{method, uri, query, hb.String(), strings.Join(names, ";"), payloadHash}
	return strings.Join(parts, "\n")
}

// authorization returns the SigV4 Authorization header value. headers must
// already include x-amz-date and x-amz-content-sha256. Validated against
// the worked example in the AWS documentation (see sign_test.go).
func authorization(accessKey, secretKey, region, method, uri, query string, headers [][2]string, payloadHash, stamp string) string {
	date := stamp[:8]
	scope := date + "/" + region + "/s3/aws4_request"
	creq := canonicalRequest(method, uri, query, headers, payloadHash)
	sts := strings.Join([]string{sigAlgo, stamp, scope, sha256Hex(creq)}, "\n")
	sig := hex.EncodeToString(hmacSHA256(signingKey(secretKey, date, region), sts))
	names := make([]string, len(headers))
	for i, h := range headers {
		names[i] = h[0]
	}
	return fmt.Sprintf("%s Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		sigAlgo, accessKey, scope, strings.Join(names, ";"), sig)
}

// canonicalHeadersOf flattens req's headers (plus host) into the sorted
// lowercase form SigV4 signs. Everything already set on the request is
// signed — a superset of the required minimum, valid as long as both
// sides see the same wire bytes.
func canonicalHeadersOf(req *http.Request) [][2]string {
	names := []string{"host"}
	values := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		name := strings.ToLower(k)
		names = append(names, name)
		values[name] = strings.TrimSpace(strings.Join(v, ","))
	}
	sort.Strings(names)
	headers := make([][2]string, len(names))
	for i, n := range names {
		headers[i] = [2]string{n, values[n]}
	}
	return headers
}

// signRequest stamps req with x-amz-date, x-amz-content-sha256 and the
// Authorization header. path/query are the escaped request target the
// signature covers — the caller must send exactly those bytes.
func (c *Client) signRequest(req *http.Request, path, query, payloadHash string, now time.Time) {
	if c.accessKey == "" {
		return // anonymous access (public buckets)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authorization(c.accessKey, c.secretKey, c.Region,
		req.Method, path, query, canonicalHeadersOf(req), payloadHash, stamp))
}

// presignGet returns a URL granting GET access to key for ttl. Only the
// host header is signed, so the reader may add its own Range header freely.
func (c *Client) presignGet(key string, ttl time.Duration, now time.Time) string {
	path := c.objectPath(key)
	if c.accessKey == "" {
		return c.Endpoint + path
	}
	stamp := now.UTC().Format("20060102T150405Z")
	scope := stamp[:8] + "/" + c.Region + "/s3/aws4_request"
	query := renderQuery([]queryPair{
		{"X-Amz-Algorithm", sigAlgo},
		{"X-Amz-Credential", c.accessKey + "/" + scope},
		{"X-Amz-Date", stamp},
		{"X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10)},
		{"X-Amz-SignedHeaders", "host"},
	})
	sts := strings.Join([]string{
		sigAlgo, stamp, scope,
		sha256Hex(canonicalRequest("GET", path, query, [][2]string{{"host", c.host}}, unsignedPayload)),
	}, "\n")
	sig := hex.EncodeToString(hmacSHA256(signingKey(c.secretKey, stamp[:8], c.Region), sts))
	return c.Endpoint + path + "?" + query + "&X-Amz-Signature=" + sig
}
