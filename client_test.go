package grab

import (
	"encoding/hex"
	"fmt"
	"github.com/djherbis/times"
	"os"
	"strings"
	"testing"
	"time"
)

// testComplete validates that a completed transfer response has all the desired
// fields filled.
func testComplete(t *testing.T, resp *Response) {
	if !resp.IsComplete() {
		t.Errorf("Response.IsComplete returned false")
	}

	if resp.Start.IsZero() {
		t.Errorf("Response.Start is zero")
	}

	if resp.End.IsZero() {
		t.Error("Response.End is zero")
	}

	if eta := resp.ETA(); eta != resp.End {
		t.Errorf("Response.ETA is not equal to Response.End: %v", eta)
	}

	// the following fields should only be set if no error occurred
	if resp.Error == nil {
		if resp.Filename == "" {
			t.Errorf("Response.Filename is empty")
		}

		if p := resp.Progress(); p != 1.00 {
			t.Errorf("Response.Progress returned %v, expected 1.00", p)
		}
	}
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

	testComplete(t, resp)
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

// TestWithNoFilename ensures a No Filename error is returned when none can be
// determined.
func TestWithNoFilename(t *testing.T) {
	req, _ := NewRequest(ts.URL)
	resp, err := DefaultClient.Do(req)
	if err == nil || !IsNoFilename(err) {
		t.Errorf("Expected no filename error, got: %v", err)
		os.Remove(resp.Filename)
	}
}

// TestWithExistingDir asserts that the resolved filename from the response
// is appended to the existing dir specified explicitly via Request.Filename
func TestWithExistingDir(t *testing.T) {
	err := os.Mkdir(".test", 0777)
	if err != nil {
		t.Error(err)
	}

	// test naming via Content-Disposition headers
	req, _ := NewRequest(ts.URL + "/url-filename?filename=header-filename")
	req.Filename = ".test"

	testFilename(t, req, ".test/header-filename")

	// test naming via URL
	req, _ = NewRequest(ts.URL + "/url-filename?garbage")
	req.Filename = ".test"

	testFilename(t, req, ".test/url-filename")

	// cleanup
	err = os.Remove(".test")
	if err != nil {
		t.Error(err)
	}
}

// testChecksum executes a request and asserts that the computed checksum for
// the downloaded file does or does not match the expected checksum.
func testChecksum(t *testing.T, size int, algorithm, sum string, match bool) {
	filename := fmt.Sprintf(".testChecksum-%s-%s", algorithm, sum[:8])
	defer os.Remove(filename)

	// create request
	req, _ := NewRequest(ts.URL + fmt.Sprintf("?size=%d", size))
	req.Filename = filename

	// set expected checksum
	sumb, _ := hex.DecodeString(sum)
	req.SetChecksum(algorithm, sumb)

	// fetch
	resp, err := DefaultClient.Do(req)
	if err != nil {
		if !IsChecksumMismatch(err) {
			t.Errorf("Error in Client.Do(): %v", err)
		} else if match {
			t.Errorf("%v (%v bytes)", err, size)
		}
	} else if !match {
		t.Errorf("Expected checksum mismatch but comparison succeeded (%s %v bytes)", algorithm, size)
	}

	testComplete(t, resp)
}

// TestChecksums executes a number of checksum tests via testChecksum.
func TestChecksums(t *testing.T) {
	// test md5
	testChecksum(t, 128, "md5", "37eff01866ba3f538421b30b7cbefcac", true)
	testChecksum(t, 128, "md5", "37eff01866ba3f538421b30b7cbefcad", false)

	testChecksum(t, 1024, "md5", "b2ea9f7fcea831a4a63b213f41a8855b", true)
	testChecksum(t, 1024, "md5", "b2ea9f7fcea831a4a63b213f41a8855c", false)

	testChecksum(t, 1048576, "md5", "c35cc7d8d91728a0cb052831bc4ef372", true)
	testChecksum(t, 1048576, "md5", "c35cc7d8d91728a0cb052831bc4ef373", false)

	// test sha1
	testChecksum(t, 128, "sha1", "e6434bc401f98603d7eda504790c98c67385d535", true)
	testChecksum(t, 128, "sha1", "e6434bc401f98603d7eda504790c98c67385d536", false)

	testChecksum(t, 1024, "sha1", "5b00669c480d5cffbdfa8bdba99561160f2d1b77", true)
	testChecksum(t, 1024, "sha1", "5b00669c480d5cffbdfa8bdba99561160f2d1b78", false)

	testChecksum(t, 1048576, "sha1", "ecfc8e86fdd83811f9cc9bf500993b63069923be", true)
	testChecksum(t, 1048576, "sha1", "ecfc8e86fdd83811f9cc9bf500993b63069923bf", false)

	// test sha256
	testChecksum(t, 128, "sha256", "471fb943aa23c511f6f72f8d1652d9c880cfa392ad80503120547703e56a2be5", true)
	testChecksum(t, 128, "sha256", "471fb943aa23c511f6f72f8d1652d9c880cfa392ad80503120547703e56a2be4", false)

	testChecksum(t, 1024, "sha256", "785b0751fc2c53dc14a4ce3d800e69ef9ce1009eb327ccf458afe09c242c26c9", true)
	testChecksum(t, 1024, "sha256", "785b0751fc2c53dc14a4ce3d800e69ef9ce1009eb327ccf458afe09c242c26c8", false)

	testChecksum(t, 1048576, "sha256", "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83", true)
	testChecksum(t, 1048576, "sha256", "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c82", false)

	// test sha512
	testChecksum(t, 128, "sha512", "1dffd5e3adb71d45d2245939665521ae001a317a03720a45732ba1900ca3b8351fc5c9b4ca513eba6f80bc7b1d1fdad4abd13491cb824d61b08d8c0e1561b3f7", true)
	testChecksum(t, 128, "sha512", "1dffd5e3adb71d45d2245939665521ae001a317a03720a45732ba1900ca3b8351fc5c9b4ca513eba6f80bc7b1d1fdad4abd13491cb824d61b08d8c0e1561b3f8", false)

	testChecksum(t, 1024, "sha512", "37f652be867f28ed033269cbba201af2112c2b3fd334a89fd2f757938ddee815787cc61d6e24a8a33340d0f7e86ffc058816b88530766ba6e231620a130b566c", true)
	testChecksum(t, 1024, "sha512", "37f652be867f28ed033269cbba201af2112c2b3fd334a89fd2f757938ddee815787cc61d6e24a8a33340d0f7e86ffc058816b88530766ba6e231620a130b566d", false)

	testChecksum(t, 1048576, "sha512", "ac1d097b4ea6f6ad7ba640275b9ac290e4828cd760a0ebf76d555463a4f505f95df4f611629539a2dd1848e7c1304633baa1826462b3c87521c0c6e3469b67af", true)
	testChecksum(t, 1048576, "sha512", "ac1d097b4ea6f6ad7ba640275b9ac290e4828cd760a0ebf76d555463a4f505f95df4f611629539a2dd1848e7c1304633baa1826462b3c87521c0c6e3469b67a0", false)

	// check unsupported
	req, _ := NewRequest(ts.URL)
	if err := req.SetChecksum("bad", []byte{}); err == nil {
		t.Fatalf("Expected error for unsupported hash type, got %v", err)
	}
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

	testComplete(t, resp)
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
	sum, _ := hex.DecodeString("fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83")

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
			req.SetChecksum("sha256", sum)
		}

		// transfer
		if resp, err := DefaultClient.Do(req); err != nil {
			t.Errorf("Error segment %d (%d bytes): %v", i+1, segsize, err)
			break
		} else {
			if i > 0 && !resp.DidResume {
				t.Errorf("Expected segment %d to resume previous segment but it did not.", i+1)
			}

			testComplete(t, resp)
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

func TestSkipExisting(t *testing.T) {
	filename := ".testSkipExisting"
	defer os.Remove(filename)

	// download a file
	req, _ := NewRequest(ts.URL)
	req.Filename = filename
	resp, _ := DefaultClient.Do(req)
	testComplete(t, resp)

	// redownload
	req, _ = NewRequest(ts.URL)
	req.Filename = filename
	resp, _ = DefaultClient.Do(req)
	testComplete(t, resp)

	// ensure download was resumed
	if !resp.DidResume {
		t.Fatalf("Expected download to skip existing file, but it did not")
	}

	// ensure all bytes were resumed
	if resp.Size == 0 || resp.Size != resp.bytesResumed {
		t.Fatalf("Expected to skip %d bytes in redownload; got %d", resp.Size, resp.bytesResumed)
	}

	// ensure checksum is performed on pre-existing file
	req, _ = NewRequest(ts.URL)
	req.Filename = filename
	sum, _ := hex.DecodeString("badd")
	req.SetChecksum("sha256", sum)

	_, err := DefaultClient.Do(req)
	if err == nil || !IsChecksumMismatch(err) {
		t.Fatalf("Expected checksum error, got: %v", err)
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
				testComplete(t, resp)

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
	testComplete(t, resp)

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
	testComplete(t, resp)

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
