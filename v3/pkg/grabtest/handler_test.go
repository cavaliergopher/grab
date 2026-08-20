package grabtest

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandlerDefaults(t *testing.T) {
	WithTestServer(t, func(url string) {
		resp := MustHTTPDo(MustHTTPNewRequest("GET", url, nil))
		AssertHTTPResponseStatusCode(t, resp, http.StatusOK)
		AssertHTTPResponseContentLength(t, resp, 1048576)
		AssertHTTPResponseHeader(t, resp, "Accept-Ranges", "bytes")
	})
}

func TestHandlerMethodWhitelist(t *testing.T) {
	tests := []struct {
		Whitelist        []string
		Method           string
		ExpectStatusCode int
	}{
		{[]string{"GET", "HEAD"}, "GET", http.StatusOK},
		{[]string{"GET", "HEAD"}, "HEAD", http.StatusOK},
		{[]string{"GET"}, "HEAD", http.StatusMethodNotAllowed},
		{[]string{"HEAD"}, "GET", http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		WithTestServer(t, func(url string) {
			resp := MustHTTPDoWithClose(MustHTTPNewRequest(test.Method, url, nil))
			AssertHTTPResponseStatusCode(t, resp, test.ExpectStatusCode)
		}, MethodWhitelist(test.Whitelist...))
	}
}

func TestHandlerHeaderBlacklist(t *testing.T) {
	contentLength := 4096
	WithTestServer(t, func(url string) {
		resp := MustHTTPDo(MustHTTPNewRequest("GET", url, nil))
		defer resp.Body.Close()
		if resp.ContentLength != -1 {
			t.Errorf("expected Response.ContentLength: -1, got: %d", resp.ContentLength)
		}
		AssertHTTPResponseHeader(t, resp, "Content-Length", "")
		AssertHTTPResponseBodyLength(t, resp, int64(contentLength))
	},
		ContentLength(contentLength),
		HeaderBlacklist("Content-Length"),
	)
}

func TestHandlerStatusCodeFuncs(t *testing.T) {
	expect := 418 // I'm a teapot
	WithTestServer(t, func(url string) {
		resp := MustHTTPDo(MustHTTPNewRequest("GET", url, nil))
		defer resp.Body.Close()
		AssertHTTPResponseStatusCode(t, resp, expect)
	},
		StatusCode(func(req *http.Request) int { return expect }),
	)
}

func TestHandlerContentLength(t *testing.T) {
	tests := []struct {
		Method          string
		ContentLength   int
		ExpectHeaderLen int64
		ExpectBodyLen   int
	}{
		{"GET", 321, 321, 321},
		{"HEAD", 321, 321, 0},
		{"GET", 0, 0, 0},
		{"HEAD", 0, 0, 0},
	}

	for _, test := range tests {
		WithTestServer(t, func(url string) {
			resp := MustHTTPDo(MustHTTPNewRequest(test.Method, url, nil))
			defer resp.Body.Close()

			AssertHTTPResponseHeader(t, resp, "Content-Length", "%d", test.ExpectHeaderLen)

			b, err := io.ReadAll(resp.Body)
			if err != nil {
				panic(err)
			}
			if len(b) != test.ExpectBodyLen {
				t.Errorf(
					"expected body length: %v, got: %v, in: %v",
					test.ExpectBodyLen,
					len(b),
					test,
				)
			}
		},
			ContentLength(test.ContentLength),
		)
	}
}

func TestHandlerAcceptRanges(t *testing.T) {
	header := "Accept-Ranges"
	n := 128
	t.Run("Enabled", func(t *testing.T) {
		WithTestServer(t, func(url string) {
			req := MustHTTPNewRequest("GET", url, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", n/2))
			resp := MustHTTPDo(req)
			AssertHTTPResponseHeader(t, resp, header, "bytes")
			AssertHTTPResponseContentLength(t, resp, int64(n/2))
		},
			ContentLength(n),
		)
	})

	t.Run("Disabled", func(t *testing.T) {
		WithTestServer(t, func(url string) {
			req := MustHTTPNewRequest("GET", url, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", n/2))
			resp := MustHTTPDo(req)
			AssertHTTPResponseHeader(t, resp, header, "")
			AssertHTTPResponseContentLength(t, resp, int64(n))
		},
			AcceptRanges(false),
			ContentLength(n),
		)
	})
}

func TestHandlerByteRanges(t *testing.T) {
	n := 128
	tests := []struct {
		Name              string
		Range             string
		ExpectStatusCode  int
		ExpectContentLen  int64
		ExpectContentRnge string
	}{
		{"None", "", http.StatusOK, 128, ""},
		{"Open ended", "bytes=64-", http.StatusPartialContent, 64, "bytes 64-127/128"},
		{"Closed", "bytes=0-63", http.StatusPartialContent, 64, "bytes 0-63/128"},
		{"Single byte", "bytes=7-7", http.StatusPartialContent, 1, "bytes 7-7/128"},
		{"Final byte", "bytes=127-127", http.StatusPartialContent, 1, "bytes 127-127/128"},
		{"Clamped to size", "bytes=64-999", http.StatusPartialContent, 64, "bytes 64-127/128"},
		{"Whole file", "bytes=0-127", http.StatusPartialContent, 128, "bytes 0-127/128"},

		// grab never sends these, so the handler rejects them rather than
		// letting a regression that did go unnoticed
		{"Suffix form", "bytes=-64", http.StatusBadRequest, 0, ""},
		{"Multiple ranges", "bytes=0-15,32-47", http.StatusBadRequest, 0, ""},
		{"Unknown unit", "chunks=0-15", http.StatusBadRequest, 0, ""},
		{"Not a number", "bytes=abc-", http.StatusBadRequest, 0, ""},
		{"Backwards", "bytes=64-32", http.StatusBadRequest, 0, ""},
		{"Past the end", "bytes=128-", http.StatusRequestedRangeNotSatisfiable, 0, ""},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			WithTestServer(t, func(url string) {
				req := MustHTTPNewRequest("GET", url, nil)
				if test.Range != "" {
					req.Header.Set("Range", test.Range)
				}
				resp := MustHTTPDo(req)
				defer resp.Body.Close()

				if !AssertHTTPResponseStatusCode(t, resp, test.ExpectStatusCode) {
					return
				}
				AssertHTTPResponseHeader(t, resp, "Content-Range", "%s", test.ExpectContentRnge)
				if test.ExpectStatusCode >= 300 {
					return
				}

				b, err := io.ReadAll(resp.Body)
				if err != nil {
					panic(err)
				}
				if int64(len(b)) != test.ExpectContentLen {
					t.Fatalf("expected body length: %d, got: %d", test.ExpectContentLen, len(b))
				}

				// each byte of the body is addressed by its absolute offset in
				// the file, so a range must return the same bytes it would have
				// been sent as part of the whole file
				start := int64(0)
				if test.ExpectContentRnge != "" {
					if _, err := fmt.Sscanf(test.ExpectContentRnge, "bytes %d-", &start); err != nil {
						panic(err)
					}
				}
				for i, got := range b {
					if want := byte(start + int64(i)); got != want {
						t.Fatalf("byte %d: expected %d, got: %d", start+int64(i), want, got)
					}
				}
			},
				ContentLength(n),
			)
		})
	}
}

func TestHandlerETag(t *testing.T) {
	t.Run("Default is stable", func(t *testing.T) {
		WithTestServer(t, func(url string) {
			first := MustHTTPDoWithClose(MustHTTPNewRequest("GET", url, nil))
			etag := first.Header.Get("ETag")
			if etag == "" {
				t.Fatal("expected an ETag header by default")
			}
			second := MustHTTPDoWithClose(MustHTTPNewRequest("GET", url, nil))
			AssertHTTPResponseHeader(t, second, "ETag", "%s", etag)
		})
	})

	t.Run("Can change between requests", func(t *testing.T) {
		var n int64
		WithTestServer(t, func(url string) {
			first := MustHTTPDoWithClose(MustHTTPNewRequest("GET", url, nil))
			AssertHTTPResponseHeader(t, first, "ETag", `"1"`)
			second := MustHTTPDoWithClose(MustHTTPNewRequest("GET", url, nil))
			AssertHTTPResponseHeader(t, second, "ETag", `"2"`)
		},
			ETag(func(req *http.Request) string {
				return fmt.Sprintf(`"%d"`, atomic.AddInt64(&n, 1))
			}),
		)
	})
}

func TestHandlerRecordRanges(t *testing.T) {
	n := 128
	rec := &RangeRecorder{}
	WithTestServer(t, func(url string) {
		// a HEAD request is not recorded
		MustHTTPDoWithClose(MustHTTPNewRequest("HEAD", url, nil))

		for _, spec := range []string{"bytes=0-63", "bytes=64-"} {
			req := MustHTTPNewRequest("GET", url, nil)
			req.Header.Set("Range", spec)
			MustHTTPDoWithClose(req)
		}

		expect := [][2]int64{{0, 64}, {64, 128}}
		if got := rec.Ranges(); !reflect.DeepEqual(got, expect) {
			t.Errorf("expected ranges: %v, got: %v", expect, got)
		}
		AssertRangesCover(t, rec, int64(n))

		rec.Reset()
		if got := rec.Ranges(); len(got) != 0 {
			t.Errorf("expected no ranges after Reset, got: %v", got)
		}
	},
		ContentLength(n),
		RecordRanges(rec),
	)
}

func TestHandlerAttachmentFilename(t *testing.T) {
	filename := "foo.pdf"
	WithTestServer(t, func(url string) {
		resp := MustHTTPDoWithClose(MustHTTPNewRequest("GET", url, nil))
		AssertHTTPResponseHeader(t, resp, "Content-Disposition", `attachment;filename="%s"`, filename)
	},
		AttachmentFilename(filename),
	)
}

func TestHandlerLastModified(t *testing.T) {
	WithTestServer(t, func(url string) {
		resp := MustHTTPDoWithClose(MustHTTPNewRequest("GET", url, nil))
		AssertHTTPResponseHeader(t, resp, "Last-Modified", "Thu, 29 Nov 1973 21:33:09 GMT")
	},
		LastModified(time.Unix(123456789, 0)),
	)
}
