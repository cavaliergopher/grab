package grab

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/djherbis/times"
)

// testComplete validates that a completed Response has all the desired fields.
func testComplete(t *testing.T, resp *Response) {
	<-resp.Done
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
	if resp.Err() == nil {
		if resp.Filename == "" {
			t.Errorf("Response.Filename is empty")
		}

		if resp.Size == 0 {
			t.Error("Response.Size is zero")
		}

		if p := resp.Progress(); p != 1.00 {
			t.Errorf("Response.Progress returned %v (%v/%v bytes), expected 1", p, resp.BytesTransferred(), resp.Size)
		}
	}
}

// TestFilenameResolutions tests that the destination filename for Requests can
// be determined correctly, using an explicitly requested path,
// Content-Disposition headers or a URL path.
func TestFilenameResolution(t *testing.T) {
	testCases := []struct {
		Name     string
		Filename string
		URL      string
		Expect   string
	}{
		{"Using Request.Filename", ".testWithFilename", "/url-filename?filename=header-filename", ".testWithFilename"},
		{"Using Content-Disposition Header", "", "/url-filename?filename=.testWithHeaderFilename", ".testWithHeaderFilename"},
		{"Using Content-Disposition Header with target directory", ".test", "/url-filename?filename=header-filename", ".test/header-filename"},
		{"Using URL Path", "", "/.testWithURLFilename?params-filename", ".testWithURLFilename"},
		{"Using URL Path with target directory", ".test", "/url-filename?garbage", ".test/url-filename"},
		{"Failure", "", "", ""},
	}

	err := os.Mkdir(".test", 0777)
	if err != nil {
		panic(err)
	}
	defer os.Remove(".test")

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			req, _ := NewRequest(tc.Filename, ts.URL+tc.URL)
			resp := DefaultClient.Do(req)
			defer os.Remove(resp.Filename)
			if err := resp.Err(); err != nil {
				if tc.Expect != "" || err != ErrNoFilename {
					panic(err)
				}
			} else {
				if tc.Expect == "" {
					t.Errorf("expected: %v, got: %v", ErrNoFilename, err)
				}
			}

			if resp.Filename != tc.Expect {
				t.Errorf("Filename mismatch. Expected '%s', got '%s'.", tc.Expect, resp.Filename)
			}

			testComplete(t, resp)
		})
	}
}

// TestChecksums checks that checksum validation behaves as expected for valid
// and corrupted downloads.
func TestChecksums(t *testing.T) {
	testCases := []struct {
		size  int
		hash  hash.Hash
		sum   string
		match bool
	}{
		{128, md5.New(), "37eff01866ba3f538421b30b7cbefcac", true},
		{128, md5.New(), "37eff01866ba3f538421b30b7cbefcad", false},
		{1024, md5.New(), "b2ea9f7fcea831a4a63b213f41a8855b", true},
		{1024, md5.New(), "b2ea9f7fcea831a4a63b213f41a8855c", false},
		{1048576, md5.New(), "c35cc7d8d91728a0cb052831bc4ef372", true},
		{1048576, md5.New(), "c35cc7d8d91728a0cb052831bc4ef373", false},
		{128, sha1.New(), "e6434bc401f98603d7eda504790c98c67385d535", true},
		{128, sha1.New(), "e6434bc401f98603d7eda504790c98c67385d536", false},
		{1024, sha1.New(), "5b00669c480d5cffbdfa8bdba99561160f2d1b77", true},
		{1024, sha1.New(), "5b00669c480d5cffbdfa8bdba99561160f2d1b78", false},
		{1048576, sha1.New(), "ecfc8e86fdd83811f9cc9bf500993b63069923be", true},
		{1048576, sha1.New(), "ecfc8e86fdd83811f9cc9bf500993b63069923bf", false},
		{128, sha256.New(), "471fb943aa23c511f6f72f8d1652d9c880cfa392ad80503120547703e56a2be5", true},
		{128, sha256.New(), "471fb943aa23c511f6f72f8d1652d9c880cfa392ad80503120547703e56a2be4", false},
		{1024, sha256.New(), "785b0751fc2c53dc14a4ce3d800e69ef9ce1009eb327ccf458afe09c242c26c9", true},
		{1024, sha256.New(), "785b0751fc2c53dc14a4ce3d800e69ef9ce1009eb327ccf458afe09c242c26c8", false},
		{1048576, sha256.New(), "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83", true},
		{1048576, sha256.New(), "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c82", false},
		{128, sha512.New(), "1dffd5e3adb71d45d2245939665521ae001a317a03720a45732ba1900ca3b8351fc5c9b4ca513eba6f80bc7b1d1fdad4abd13491cb824d61b08d8c0e1561b3f7", true},
		{128, sha512.New(), "1dffd5e3adb71d45d2245939665521ae001a317a03720a45732ba1900ca3b8351fc5c9b4ca513eba6f80bc7b1d1fdad4abd13491cb824d61b08d8c0e1561b3f8", false},
		{1024, sha512.New(), "37f652be867f28ed033269cbba201af2112c2b3fd334a89fd2f757938ddee815787cc61d6e24a8a33340d0f7e86ffc058816b88530766ba6e231620a130b566c", true},
		{1024, sha512.New(), "37f652bf867f28ed033269cbba201af2112c2b3fd334a89fd2f757938ddee815787cc61d6e24a8a33340d0f7e86ffc058816b88530766ba6e231620a130b566d", false},
		{1048576, sha512.New(), "ac1d097b4ea6f6ad7ba640275b9ac290e4828cd760a0ebf76d555463a4f505f95df4f611629539a2dd1848e7c1304633baa1826462b3c87521c0c6e3469b67af", true},
		{1048576, sha512.New(), "ac1d097c4ea6f6ad7ba640275b9ac290e4828cd760a0ebf76d555463a4f505f95df4f611629539a2dd1848e7c1304633baa1826462b3c87521c0c6e3469b67af", false},
	}

	for _, tc := range testCases {
		comparison := "Match"
		if !tc.match {
			comparison = "Mismatch"
		}

		t.Run(fmt.Sprintf("%s %s", comparison, tc.sum[:8]), func(t *testing.T) {
			filename := fmt.Sprintf(".testChecksum-%s-%s", comparison, tc.sum[:8])
			defer os.Remove(filename)

			b, _ := hex.DecodeString(tc.sum)
			req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", tc.size))
			req.SetChecksum(tc.hash, b, true)

			resp := DefaultClient.Do(req)
			if err := resp.Err(); err != nil {
				if err != ErrBadChecksum {
					panic(err)
				} else if tc.match {
					t.Errorf("error: %v", err)
				}
			} else if !tc.match {
				t.Errorf("expected: %v, got: %v", ErrBadChecksum, err)
			}

			testComplete(t, resp)
		})
	}
}

// TestContentLength ensures that ErrBadLength is returned if a server response
// does not match the requested length.
func TestContentLength(t *testing.T) {
	size := int64(32768)
	testCases := []struct {
		Name   string
		URL    string
		Expect int64
		Match  bool
	}{
		{"Good size in HEAD request", fmt.Sprintf("?size=%d", size), size, true},
		{"Good size in GET request", fmt.Sprintf("?nohead&size=%d", size), size, true},
		{"Bad size in HEAD request", fmt.Sprintf("?size=%d", size-1), size, false},
		{"Bad size in GET request", fmt.Sprintf("?nohead&size=%d", size-1), size, false},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			req, _ := NewRequest(".testSize-mismatch-head", ts.URL+tc.URL)
			req.Size = size

			resp := DefaultClient.Do(req)
			defer os.Remove(resp.Filename)
			err := resp.Err()
			if tc.Match {
				if err == ErrBadLength {
					t.Errorf("error: %v", err)
				} else if err != nil {
					panic(err)
				} else {
					if resp.Size != size {
						t.Errorf("expected %v bytes, got %v bytes", size, resp.Size)
					}
				}
			} else {
				if err == nil {
					t.Errorf("expected: %v, got %v", ErrBadLength, err)
				} else if err != ErrBadLength {
					panic(err)
				}
			}

			testComplete(t, resp)
		})
	}
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
		req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", segsize))

		// checksum the last request
		if i == segs-1 {
			req.SetChecksum(sha256.New(), sum, false)
		}

		// transfer
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			t.Errorf("Error segment %d (%d bytes): %v", i+1, segsize, err)
			break
		}

		if i > 0 && !resp.DidResume {
			t.Errorf("Expected segment %d to resume previous segment but it did not.", i+1)
		}

		testComplete(t, resp)

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
		req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", size/segs))
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != ErrBadLength {
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
	req, _ := NewRequest(filename, ts.URL)
	resp := DefaultClient.Do(req)
	testComplete(t, resp)

	// redownload
	req, _ = NewRequest(filename, ts.URL)
	resp = DefaultClient.Do(req)
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
	req, _ = NewRequest(filename, ts.URL)
	req.SetChecksum(sha256.New(), []byte{0x01, 0x02, 0x03, 0x04}, true)

	resp = DefaultClient.Do(req)
	if err := resp.Err(); err != ErrBadChecksum {
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
		reqs := make([]*Request, tests)
		for i := 0; i < len(reqs); i++ {
			filename := fmt.Sprintf(".testBatch.%d", i+1)
			reqs[i], _ = NewRequest(filename, ts.URL+fmt.Sprintf("/request_%d?size=%d&sleep=10", i, size))
			reqs[i].Label = fmt.Sprintf("Test %d", i+1)
			reqs[i].SetChecksum(sha256.New(), sumb, false)
		}

		// batch run
		responses := DefaultClient.DoBatch(workerCount, reqs...)

		// listen for responses
	Loop:
		for i := 0; i < len(reqs); {
			select {
			case resp := <-responses:
				if resp == nil {
					break Loop
				}

				testComplete(t, resp)
				if err := resp.Err(); err != nil {
					t.Errorf("%s: %v", resp.Filename, err)
				}

				// remove test file
				if resp.IsComplete() {
					os.Remove(resp.Filename) // ignore errors
				}
				i++
			}
		}
	}
}

// TestCancelContext tests that a batch of requests can be cancel using a
// context.Context cancellation. Requests are cancelled in multiple states:
// in-progress and unstarted.
func TestCancelContext(t *testing.T) {
	tests := 256
	client := NewClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reqs := make([]*Request, tests)
	for i := 0; i < tests; i++ {
		req, _ := NewRequest("", fmt.Sprintf("%s/.testCancelContext%d?size=134217728", ts.URL, i))
		reqs[i] = req.WithContext(ctx)
	}

	respch := client.DoBatch(8, reqs...)
	time.Sleep(time.Millisecond * 500)
	cancel()
	for resp := range respch {
		// err should be context.Cancelled or http.errRequestCanceled
		if !strings.Contains(resp.Err().Error(), "canceled") {
			t.Errorf("expected '%v', got '%v'", context.Canceled, resp.Err())
		}

		if resp.Filename != "" {
			os.Remove(resp.Filename)
		}
	}
}

// TODO: UserAgent string tests

func Example_cancellation() {
	// create context with a 100ms timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// create new download request with context
	req, err := NewRequest("./", "http://www.golang-book.com/public/pdf/gobook.pdf")
	if err != nil {
		panic(err)
	}
	req = req.WithContext(ctx)

	// send download request
	resp := DefaultClient.Do(req)
	if err := resp.Err(); err != nil {
		fmt.Printf("error: request cancelled\n")
	}

	// Output:
	// error: request cancelled
}
