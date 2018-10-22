package grab

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
// * lastmod=[unix]		  set the Last-Modified header
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
// * status=[int]       return the given status code
//
var ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	// set status code
	statusCode := http.StatusOK
	if v := r.URL.Query().Get("status"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &statusCode); err != nil {
			panic(err)
		}
	}
	if r.Method == "HEAD" {
		if v := r.URL.Query().Get("headStatus"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &statusCode); err != nil {
				panic(err)
			}
		}
	}

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

	// set Last-Modified header
	if lastmodp := r.URL.Query().Get("lastmod"); lastmodp != "" {
		lastmodi, err := strconv.ParseInt(lastmodp, 10, 64)
		if err != nil {
			panic(err)
		}
		lastmodt := time.Unix(lastmodi, 0).UTC()
		lastmod := lastmodt.Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
		w.Header().Set("Last-Modified", lastmod)
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
	w.WriteHeader(statusCode)

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
	os.Exit(func() int {
		// clean up test web server
		defer ts.Close()

		// chdir to temp so test files downloaded to pwd are isolated and cleaned up
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		tmpDir, err := ioutil.TempDir("", "grab-")
		if err != nil {
			panic(err)
		}
		if err := os.Chdir(tmpDir); err != nil {
			panic(err)
		}
		defer func() {
			os.Chdir(cwd)
			if err := os.RemoveAll(tmpDir); err != nil {
				panic(err)
			}
		}()
		return m.Run()
	}())
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

	t.Run("headStatus", func(t *testing.T) {
		expect := http.StatusTeapot
		req, _ := http.NewRequest(
			"HEAD",
			fmt.Sprintf("%s?headStatus=%d", ts.URL, expect),
			nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		if resp.StatusCode != expect {
			t.Fatalf("expected status: %v, got: %v", expect, resp.StatusCode)
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

	t.Run("lastmod", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"?lastmod=123456789", nil)
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()

		expect := "Thu, 29 Nov 1973 21:33:09 GMT"
		if h := resp.Header.Get("Last-Modified"); h != expect {
			t.Fatalf("expected Last-Modified header: %v, got %v", expect, h)
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
		log.Fatal(err)
	}

	fmt.Println("Download saved to", resp.Filename)
}
