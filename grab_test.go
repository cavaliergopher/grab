package grab

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// ts is the test HTTP server instance initiated by TestMain().
var ts *httptest.Server

// TestMain starts a HTTP test server for all test cases to use as a download
// source. It serves a configurable stream of sequential data.
//
// The following headers are supported:
//
// * Range: bytes=[offset]-		request a byte range of the requested file
//
// The following URL query parameters are supported:
//
// * filename=[string]			return a filename in the Content-Disposition
// 								header of the response
//
// * nohead						prohibit HEAD requests
//
// * range=[bool]				allow byte range requests (default: yes)
//
// * rate=[int]					throttle file transfer to the given limit as
// 								bytes per second
//
// * size=[int]					return a file of the specified size in bytes
//
// * sleep=[int]				delay the response by the given number of
// 								milliseconds (before sending headers)
//
func TestMain(m *testing.M) {
	// start test HTTP server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Accept-Ranges", "bytes")

		// serve content body if method == "GET"
		if r.Method == "GET" {
			// write to stream
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

	defer ts.Close()

	// run tests
	os.Exit(m.Run())
}

// TestGet tests a simple file download using a convenience wrapper.
func TestGet(t *testing.T) {
	filename := ".testGet"
	defer os.Remove(filename)

	_, err := Get(filename, ts.URL)
	if err != nil {
		t.Fatalf("Error in Get(): %v", err)
	}
}

// TestGetAsync tests a simple file download using a convenience wrapper.
func TestGetAsync(t *testing.T) {
	filename := ".testGetAsync"
	defer os.Remove(filename)

	respch, err := GetAsync(filename, ts.URL+"?sleep=200")
	if err != nil {
		t.Fatalf("Error in GetAsync: %v", err)
	}

	if resp := <-respch; resp.Error != nil {
		t.Fatalf("Error in GetAsync transfer: %v", resp.Error)
	}
}

// TestGetBatch tests simple batch downloads using a convenience wrapper.
func TestGetBatch(t *testing.T) {
	// create request urls
	urls := make([]string, 5)
	for i := 0; i < len(urls); i++ {
		urls[i] = fmt.Sprintf("%s/.testGetBatch.%d", ts.URL, i)
	}

	// clean up downloaded files
	defer func() {
		for i := 0; i < len(urls); i++ {
			os.Remove(fmt.Sprintf("./.testGetBatch.%d", i))
		}
	}()

	// make sure path is a directory
	if _, err := GetBatch(0, os.Args[0], urls...); !IsBadDestination(err) {
		t.Errorf("Expected bad destination error, got: %v", err)
	}

	// download some files yo!
	respch, _ := GetBatch(0, "", urls...)
	success := 0
	for resp := range respch {
		if resp.Error != nil {
			t.Errorf("%s: %v", resp.Request.URL(), resp.Error)
		} else {
			success++
		}
	}

	if success != len(urls) {
		t.Fatalf("Expected %d successful downloads, got %d", len(urls), success)
	}
}
