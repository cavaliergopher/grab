package grab

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"github.com/djherbis/times"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ts is the test HTTP server instance initiated by TestMain().
var ts *httptest.Server

// TestMail starts a HTTP test server for all test cases to use as a download
// source.
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

		// compute offset
		offset := 0
		if rangeh := r.Header.Get("Range"); rangeh != "" {
			if _, err := fmt.Sscanf(rangeh, "bytes=%d-", &offset); err != nil {
				panic(err)
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
			}
			bw.Flush()
		}
	}))

	defer ts.Close()

	// run tests
	os.Exit(m.Run())
}

// testFilename executes a request and asserts that the downloaded filename
// matches the given filename.
func testFilename(t *testing.T, req *Request, filename string) {
	// fetch
	resp, err := DefaultClient.Do(req)
	if err != nil {
		t.Errorf("Error in Client.Do(): %v", err)
	}

	// delete downloaded file
	if err := os.Remove(resp.Filename); err != nil {
		t.Errorf("Error deleting test file: %v", err)
	}

	// compare filename
	if resp.Filename != filename {
		t.Errorf("Filename mismatch. Expected '%s', got '%s'.", filename, resp.Filename)
	}
}

// TestWithFilename asserts that the downloaded filename matches a filename
// specified explicitly via Request.Filename, and not a name matching the
// request URL or Content-Disposition header.
func TestWithFilename(t *testing.T) {
	req, _ := NewRequest(ts.URL + "/url-filename?filename=header-filename")
	req.Filename = ".testWithFilename"

	testFilename(t, req, ".testWithFilename")
}

// TestWithHeaderFilename asserts that the downloaded filename matches a
// filename specified explicitly via the Content-Disposition header and not a
// name matching the request URL.
func TestWithHeaderFilename(t *testing.T) {
	req, _ := NewRequest(ts.URL + "/url-filename?filename=.testWithHeaderFilename")
	testFilename(t, req, ".testWithHeaderFilename")
}

// TestWithURLFilename asserts that the downloaded filename matches the
// requested URL.
func TestWithURLFilename(t *testing.T) {
	req, _ := NewRequest(ts.URL + "/.testWithURLFilename?params-filename")
	testFilename(t, req, ".testWithURLFilename")
}

// testChecksum executes a request and asserts that the computed checksum for
// the downloaded file does or does not match the expected checksum.
func testChecksum(t *testing.T, size int, sum string, match bool) {
	// create request
	req, _ := NewRequest(ts.URL + fmt.Sprintf("?size=%d", size))
	req.Filename = fmt.Sprintf(".testChecksum-%s", sum)

	// set expected checksum
	sumb, _ := hex.DecodeString(sum)
	req.SetChecksum("sha256", sumb)

	// fetch
	resp, err := DefaultClient.Do(req)
	if err != nil {
		if !IsChecksumMismatch(err) {
			t.Errorf("Error in Client.Do(): %v", err)
		} else if match {
			t.Errorf("%v (%v bytes)", err, size)
		}
	} else if !match {
		t.Errorf("Expected checksum mismatch but comparison succeeded (%v bytes)", size)
	}

	// delete downloaded file
	if err := os.Remove(resp.Filename); err != nil {
		t.Errorf("Error deleting test file: %v", err)
	}
}

// TestChecksums executes a number of checksum tests via testChecksum.
func TestChecksums(t *testing.T) {
	testChecksum(t, 128, "471fb943aa23c511f6f72f8d1652d9c880cfa392ad80503120547703e56a2be5", true)
	testChecksum(t, 128, "471fb943aa23c511f6f72f8d1652d9c880cfa392ad80503120547703e56a2be4", false)

	testChecksum(t, 1024, "785b0751fc2c53dc14a4ce3d800e69ef9ce1009eb327ccf458afe09c242c26c9", true)
	testChecksum(t, 1024, "785b0751fc2c53dc14a4ce3d800e69ef9ce1009eb327ccf458afe09c242c26c8", false)

	testChecksum(t, 1048576, "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83", true)
	testChecksum(t, 1048576, "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c82", false)
}

// testSize executes a request and asserts that the file size for the downloaded
// file does or does not match the expected size.
func testSize(t *testing.T, url string, size uint64, match bool) {
	req, _ := NewRequest(url)
	req.Filename = ".testSize-mismatch-head"
	req.Size = size

	resp, err := DefaultClient.Do(req)
	if match {
		if err != nil {
			t.Errorf(err.Error())
		}
	} else {
		// we want a ContentLengthMismatch error
		if !IsContentLengthMismatch(err) {
			t.Errorf("Expected content length mismatch. Got: %v", err)
		}
	}

	if err == nil {
		// delete file
		if err := os.Remove(resp.Filename); err != nil {
			t.Errorf("Error deleting test file: %v", err)
		}
	}
}

// TestSize exeuctes a number of size tests via testSize.
func TestSize(t *testing.T) {
	size := uint64(32768)

	// bad size should error in HEAD request
	testSize(t, ts.URL+fmt.Sprintf("?size=%d", size-1), size, false)

	// bad size should error in GET request
	testSize(t, ts.URL+fmt.Sprintf("?nohead&size=%d", size-1), size, false)

	// test good size in HEAD request
	testSize(t, ts.URL+fmt.Sprintf("?size=%d", size), size, true)

	// test good size in GET request
	testSize(t, ts.URL+fmt.Sprintf("?nohead&size=%d", size), size, true)

	// test good size with no Content-Length header
	// TODO: testSize(t, ts.URL+fmt.Sprintf("?nocl&size=%d", size), size, false)
}

// TestAutoResume tests segmented downloading of a large file.
func TestAutoResume(t *testing.T) {
	segs := 8
	size := 1048576
	filename := ".testAutoResume"

	// TODO: random segment size

	// download segment at a time
	filebtime := time.Time{}
	for i := 0; i < segs; i++ {
		// request larger segment
		segsize := (i + 1) * (size / segs)
		req, _ := NewRequest(ts.URL + fmt.Sprintf("?size=%d", segsize))
		req.Filename = filename

		// checksum the last request
		if i == segs-1 {
			sum, _ := hex.DecodeString("fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83")
			req.SetChecksum("sha256", sum)
		}

		// transfer
		if resp, err := DefaultClient.Do(req); err != nil {
			t.Errorf("Error segment %d (%d bytes): %v", i+1, segsize, err)
			break
		} else if i > 0 && !resp.DidResume {
			t.Errorf("Expected segment %d to resume previous segment but it did not.", i+1)
		}

		// only check birth time on OS's that support it
		if times.HasBirthTime {
			if ts, err := times.Stat(req.Filename); err != nil {
				t.Errorf(err.Error())
			} else {
				if filebtime.Second() == 0 && filebtime.Nanosecond() == 0 {
					filebtime = ts.BirthTime()
				} else {
					// check creation date (only accurate to one second)
					if ts.BirthTime() != filebtime {
						t.Errorf("File timestamp changed for segment %d ( from %v to %v )", i+1, filebtime, ts.BirthTime())
						break
					}
				}
			}

			// sleep to allow ctime to roll over at least once
			time.Sleep(time.Duration(1100/segs) * time.Millisecond)
		}
	}

	// TODO: redownload and check time stamp

	// TODO: ensure checksum is performed on pre-existing file

	// error if existing file is larger than requested file
	{
		// request smaller segment
		req, _ := NewRequest(ts.URL + fmt.Sprintf("?size=%d", size/segs))
		req.Filename = filename

		// transfer
		if _, err := DefaultClient.Do(req); !IsContentLengthMismatch(err) {
			t.Errorf("Expected bad length error, got: %v", err)
		}
	}

	// TODO: existing file is corrupted

	// delete downloaded file
	if err := os.Remove(filename); err != nil {
		t.Errorf("Error deleting test file: %v", err)
	}
}

// TestBatch executes multiple requests simultaneously and validates the
// responses.
func TestBatch(t *testing.T) {
	tests := 32
	size := 32768
	sum := "e11360251d1173650cdcd20f111d8f1ca2e412f572e8b36a4dc067121c1799b8"
	sumb, _ := hex.DecodeString(sum)

	// test with 4 workers and with one per request
	for _, workerCount := range []int{4, 0} {
		// create requests
		done := make(chan *Response, 0)
		reqs := make([]*Request, tests)
		for i := 0; i < len(reqs); i++ {
			reqs[i], _ = NewRequest(ts.URL + fmt.Sprintf("/request_%d?size=%d&sleep=10", i, size))
			reqs[i].Label = fmt.Sprintf("Test %d", i+1)
			reqs[i].Filename = fmt.Sprintf(".testBatch.%d", i+1)
			reqs[i].NotifyOnClose = done
			if err := reqs[i].SetChecksum("sha256", sumb); err != nil {
				t.Fatal(err.Error())
			}
		}

		// batch run
		responses := DefaultClient.DoBatch(workerCount, reqs...)

		// listen for responses
		for i := 0; i < len(reqs); {
			select {
			case <-responses:
				// swallow responses channel for newly initiated responses

			case resp := <-done:
				// handle errors
				if resp.Error != nil {
					t.Errorf("%s: %v", resp.Filename, resp.Error)
				}

				// remove test file
				if resp.IsComplete() && resp.Error == nil {
					os.Remove(resp.Filename)
				}
				i++
			}
		}
	}
}

// TestCancel validates that a request can be successfully cancelled before a
// file transfer starts.
func TestCancel(t *testing.T) {
	client := NewClient()

	// slow request
	req, _ := NewRequest(ts.URL + "/.testCancel?sleep=2000")
	ch := client.DoAsync(req)

	// sleep and cancel before request is served
	time.Sleep(500 * time.Millisecond)
	client.CancelRequest(req)

	// wait for response
	resp := <-ch

	// validate error
	if resp.Error == nil ||
		!(strings.Contains(resp.Error.Error(), "request canceled") || strings.Contains(resp.Error.Error(), "use of closed network connection")) {
		t.Errorf("Expected 'request cancelled' error; got: '%v'", resp.Error)
	}
}

// TestCancelInProcess validates that a request can be successfully cancelled
// after a file transfer has started.
func TestCancelInProcess(t *testing.T) {
	client := NewClient()

	// large file request
	req, _ := NewRequest(ts.URL + "/.testCancelInProcess?size=134217728")
	done := make(chan *Response)
	req.NotifyOnClose = done

	resp := <-client.DoAsync(req)

	// wait until some data is transferred
	for resp.BytesTransferred() < 1048576 {
		time.Sleep(100 * time.Millisecond)
	}

	// cancel request
	client.CancelRequest(req)

	// wait for closure
	<-done

	// validate error
	if resp.Error == nil ||
		!(strings.Contains(resp.Error.Error(), "request canceled") || strings.Contains(resp.Error.Error(), "use of closed network connection")) {
		t.Errorf("Expected 'request cancelled' error; got: '%v'", resp.Error)
	}

	// delete downloaded file
	if err := os.Remove(resp.Filename); err != nil {
		t.Errorf("Error deleting test file: %v", err)
	}
}
