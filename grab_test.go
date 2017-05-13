package grab

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// ts is a test HTTP server that serves configurable content for all test
// functions.
//
// The following URL query parameters are supported:
//
// * filename=[string]	return a filename in the Content-Disposition header of
//                      the response
//
// * nohead						  disabled support for HEAD requests
//
// * range=[bool]				allow byte range requests (default: yes)
//
// * rate=[int]					throttle file transfer to the given limit as
// 							        bytes per second
//
// * size=[int]					return a file of the specified size in bytes
//
// * sleep=[int]				delay the response by the given number of
// 								      milliseconds (before sending headers)
//
var ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	// allow HEAD requests?
	if _, ok := r.URL.Query()["nohead"]; ok && r.Method == "HEAD" {
		http.Error(w, "HEAD method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// compute transfer size from 'size' parameter (default 1Mb)
	size := 1048576
	if sizep := r.URL.Query().Get("size"); sizep != "" {
		if _, err := fmt.Sscanf(sizep, "%d", &size); err != nil {
			panic(err)
		}
	}

	// support ranged requests (default yes)?
	ranged := true
	if rangep := r.URL.Query().Get("ranged"); rangep != "" {
		if _, err := fmt.Sscanf(rangep, "%t", &ranged); err != nil {
			panic(err)
		}
	}

	// set filename in headers (default no)?
	if filenamep := r.URL.Query().Get("filename"); filenamep != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment;filename=\"%s\"", filenamep))
	}

	// sleep before responding?
	sleep := 0
	if sleepp := r.URL.Query().Get("sleep"); sleepp != "" {
		if _, err := fmt.Sscanf(sleepp, "%d", &sleep); err != nil {
			panic(err)
		}
	}

	// throttle rate to n bps
	rate := 0 // bps
	var throttle *time.Ticker
	defer func() {
		if throttle != nil {
			throttle.Stop()
		}
	}()

	if ratep := r.URL.Query().Get("rate"); ratep != "" {
		if _, err := fmt.Sscanf(ratep, "%d", &rate); err != nil {
			panic(err)
		}

		if rate > 0 {
			throttle = time.NewTicker(time.Second / time.Duration(rate))
		}
	}

	// compute offset
	offset := 0
	if rangeh := r.Header.Get("Range"); rangeh != "" {
		if _, err := fmt.Sscanf(rangeh, "bytes=%d-", &offset); err != nil {
			panic(err)
		}

		// make sure range is in range
		if offset >= size {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}

	// delay response
	if sleep > 0 {
		time.Sleep(time.Duration(sleep) * time.Millisecond)
	}

	// set response headers
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size-offset))
	if ranged {
		w.Header().Set("Accept-Ranges", "bytes")
	}

	// serve content body if method == "GET"
	if r.Method == "GET" {
		// use buffered io to reduce overhead on the reader
		bw := bufio.NewWriterSize(w, 4096)
		for i := offset; i < size; i++ {
			bw.Write([]byte{byte(i)})
			if throttle != nil {
				<-throttle.C
			}
		}
		bw.Flush()
	}
}))

func TestMain(m *testing.M) {
	defer ts.Close()
	os.Exit(m.Run())
}

// TestTestServer ensures that the test server behaves as expected so that it
// does not pollute other tests.
func TestTestServer(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"?nohead", nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()

		expectSize := 1048576
		if h := resp.ContentLength; h != int64(expectSize) {
			t.Fatalf("expected Content-Length: %v, got %v", expectSize, h)
		}
		b, _ := ioutil.ReadAll(resp.Body)
		if len(b) != expectSize {
			t.Fatalf("expected body length: %v, got %v", expectSize, len(b))
		}

		if h := resp.Header.Get("Accept-Ranges"); h != "bytes" {
			t.Fatalf("expected Accept-Ranges: bytes, got: %v", h)
		}
	})

	t.Run("nohead", func(t *testing.T) {
		req, _ := http.NewRequest("HEAD", ts.URL+"?nohead", nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			panic("HEAD request was allowed despite ?nohead being set")
		}
	})

	t.Run("size", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"?size=321", nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		if resp.ContentLength != 321 {
			t.Fatalf("expected Content-Length: %v, got %v", 321, resp.ContentLength)
		}
		b, _ := ioutil.ReadAll(resp.Body)
		if len(b) != 321 {
			t.Fatalf("expected body length: %v, got %v", 321, len(b))
		}
	})

	t.Run("ranged=false", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"?ranged=false", nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		if h := resp.Header.Get("Accept-Ranges"); h != "" {
			t.Fatalf("expected empty Accept-Ranges header, got: %v", h)
		}
	})

	t.Run("filename", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"?filename=test", nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()

		expect := "attachment;filename=\"test\""
		if h := resp.Header.Get("Content-Disposition"); h != expect {
			t.Fatalf("expected Content-Disposition header: %v, got %v", expect, h)
		}
	})
}

// TestGet tests grab.Get
func TestGet(t *testing.T) {
	filename := ".testGet"
	defer os.Remove(filename)

	resp, err := Get(filename, ts.URL)
	if err != nil {
		t.Fatalf("error in Get(): %v", err)
	}

	testComplete(t, resp)
}

func ExampleGet() {
	// download a file to /tmp
	resp, err := Get("/tmp", "http://example.com/example.zip")
	if err != nil {
		panic(err)
	}

	fmt.Println("Download saved to", resp.Filename)
}
