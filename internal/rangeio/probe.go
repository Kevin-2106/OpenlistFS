package rangeio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProbeResult reports one diagnostic Range request.
type ProbeResult struct {
	Status       int
	ContentRange string
	Length       int64 // Content-Length of the response body
	TTFB         time.Duration
	Total        time.Duration
	Received     int64 // bytes actually read (and discarded)
	Err          error
}

// ProbeRange performs one bare HTTP Range request against link for
// diagnostics; data is discarded, never written to disk.
func ProbeRange(ctx context.Context, client *http.Client, link Link, off, length int64) ProbeResult {
	if client == nil {
		client = defaultClient()
	}
	res := ProbeResult{}
	end := off + length - 1
	if off+length > link.Size {
		end = link.Size - 1
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		res.Err = err
		return res
	}
	for k, v := range link.Header {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))
	req.Header.Set("Accept-Encoding", "identity")

	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	res.ContentRange = resp.Header.Get("Content-Range")
	res.Length = resp.ContentLength
	res.TTFB = time.Since(t0)

	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, length))
	res.Received = n
	res.Total = time.Since(t0)
	if err != nil {
		res.Err = err
	}
	return res
}
