package grabtest

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sort"
	"testing"
)

func AssertHTTPResponseStatusCode(t *testing.T, resp *http.Response, expect int) (ok bool) {
	if resp.StatusCode != expect {
		t.Errorf("expected status code: %d, got: %d", expect, resp.StatusCode)
		return
	}
	ok = true
	return true
}

func AssertHTTPResponseHeader(t *testing.T, resp *http.Response, key, format string, a ...any) (ok bool) {
	expect := fmt.Sprintf(format, a...)
	actual := resp.Header.Get(key)
	if actual != expect {
		t.Errorf("expected header %s: %s, got: %s", key, expect, actual)
		return
	}
	ok = true
	return
}

func AssertHTTPResponseContentLength(t *testing.T, resp *http.Response, n int64) (ok bool) {
	ok = true
	if resp.ContentLength != n {
		ok = false
		t.Errorf("expected header Content-Length: %d, got: %d", n, resp.ContentLength)
	}
	if !AssertHTTPResponseBodyLength(t, resp, n) {
		ok = false
	}
	return
}

func AssertHTTPResponseBodyLength(t *testing.T, resp *http.Response, n int64) (ok bool) {
	defer func() {
		if err := resp.Body.Close(); err != nil {
			panic(err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if int64(len(b)) != n {
		ok = false
		t.Errorf("expected body length: %d, got: %d", n, len(b))
	}
	return
}

func MustHTTPNewRequest(method, url string, body io.Reader) *http.Request {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		panic(err)
	}
	return req
}

func MustHTTPDo(req *http.Request) *http.Response {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

func MustHTTPDoWithClose(req *http.Request) *http.Response {
	resp := MustHTTPDo(req)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		panic(err)
	}
	if err := resp.Body.Close(); err != nil {
		panic(err)
	}
	return resp
}

// AssertRangesCover asserts that the ranges recorded by a test server tile the
// interval [0, size) exactly once: no gaps, no overlaps and nothing beyond the
// end of the file.
func AssertRangesCover(t *testing.T, rec *RangeRecorder, size int64) (ok bool) {
	t.Helper()
	ranges := rec.Ranges()
	sorted := append([][2]int64(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })

	ok = true
	next := int64(0)
	for _, r := range sorted {
		switch {
		case r[0] < next:
			t.Errorf("range %d-%d overlaps the previous range, in: %v", r[0], r[1], ranges)
			ok = false
		case r[0] > next:
			t.Errorf("bytes %d-%d were never requested, in: %v", next, r[0], ranges)
			ok = false
		case r[1] > size:
			t.Errorf("range %d-%d extends past the end of the file at %d", r[0], r[1], size)
			ok = false
		}
		next = r[1]
	}
	if next != size {
		t.Errorf("expected ranges to cover %d bytes, got: %d, in: %v", size, next, ranges)
		ok = false
	}
	return
}

func AssertSHA256Sum(t *testing.T, sum []byte, r io.Reader) (ok bool) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		panic(err)
	}
	computed := h.Sum(nil)
	ok = bytes.Equal(sum, computed)
	if !ok {
		t.Errorf(
			"expected checksum: %s, got: %s",
			MustHexEncodeString(sum),
			MustHexEncodeString(computed),
		)
	}
	return
}
