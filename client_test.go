package grab

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// ts is the test HTTP server instance initiated by TestMain().
var ts *httptest.Server

// TestMail starts a HTTP test server for all test cases to use as a download
// source.
func TestMain(m *testing.M) {
	// start test HTTP server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		// compute offset
		offset := 0
		if rangeh := r.Header.Get("Range"); rangeh != "" {
			if _, err := fmt.Sscanf(rangeh, "bytes=%d-", &offset); err != nil {
				panic(err)
			}
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
// specified explicitely via Request.Filename, and not a name matching the
// request URL or Content-Disposition header.
func TestWithFilename(t *testing.T) {
	req, _ := NewRequest(ts.URL + "/url-filename?filename=header-filename")
	req.Filename = ".testWithFilename"

	testFilename(t, req, ".testWithFilename")
}

// TestWithHeaderFilename asserts that the downloaded filename matches a
// filename specified explicitely via the Content-Disposition header and not a
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

func TestAutoResume(t *testing.T) {
	segs := 32
	size := 1048576
	filename := ".testAutoResume"

	// TODO: random segment size

	// download segment at a time
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
		if _, err := DefaultClient.Do(req); err != nil {
			t.Errorf("Error segment %d (%d bytes): %v", i, segsize, err)
			break
		}
	}

	// TODO: redownload and check time stamp

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
