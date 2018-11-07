package grab

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFilenameResolutions tests that the destination filename for Requests can
// be determined correctly, using an explicitly requested path,
// Content-Disposition headers or a URL path - with or without an existing
// target directory.
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
	defer os.RemoveAll(".test")

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
	tests := []struct {
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

	for _, test := range tests {
		var expect error
		comparison := "Match"
		if !test.match {
			comparison = "Mismatch"
			expect = ErrBadChecksum
		}

		t.Run(fmt.Sprintf("With%s%s", comparison, test.sum[:8]), func(t *testing.T) {
			filename := fmt.Sprintf(".testChecksum-%s-%s", comparison, test.sum[:8])
			defer os.Remove(filename)

			b, _ := hex.DecodeString(test.sum)
			req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", test.size))
			req.SetChecksum(test.hash, b, true)

			resp := DefaultClient.Do(req)
			err := resp.Err()
			if err != expect {
				t.Errorf("expected error: %v, got: %v", expect, err)
			}

			// ensure mismatch file was deleted
			if !test.match {
				if _, err := os.Stat(filename); err == nil {
					t.Errorf("checksum failure not cleaned up: %s", filename)
				} else if !os.IsNotExist(err) {
					panic(err)
				}
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
				} else if resp.Size != size {
					t.Errorf("expected %v bytes, got %v bytes", size, resp.Size)
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
	sum, _ := hex.DecodeString("fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83")
	filename := ".testAutoResume"

	defer os.Remove(filename)

	for i := 0; i < segs; i++ {
		segsize := (i + 1) * (size / segs)
		t.Run(fmt.Sprintf("With%vBytes", segsize), func(t *testing.T) {
			req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", segsize))
			if i == segs-1 {
				req.SetChecksum(sha256.New(), sum, false)
			}
			resp := DefaultClient.Do(req)
			if err := resp.Err(); err != nil {
				t.Errorf("error: %v", err)
				return
			}
			if i > 0 && !resp.DidResume {
				t.Errorf("expected Response.DidResume to be true")
			}
			testComplete(t, resp)
		})
	}

	t.Run("WithFailure", func(t *testing.T) {
		// request smaller segment
		req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", size-1))
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != ErrBadLength {
			t.Errorf("expected ErrBadLength for smaller request, got: %v", err)
		}
	})

	t.Run("WithNoResume", func(t *testing.T) {
		req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", size+1))
		req.NoResume = true
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			panic(err)
		}
		if resp.DidResume == true {
			t.Errorf("expected Response.DidResume to be false")
		}
		testComplete(t, resp)
	})

	t.Run("WithNoResumeAndTruncate", func(t *testing.T) {
		req, _ := NewRequest(filename, ts.URL+fmt.Sprintf("?size=%d", size-1))
		req.NoResume = true
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			t.Errorf("error in response: %v", err)
		}
		if resp.DidResume == true {
			t.Errorf("expected Response.DidResume to be false")
		}
		testComplete(t, resp)
	})
	// TODO: test when existing file is corrupted
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
		defer os.Remove(resp.Filename)

		// err should be context.Canceled or http.errRequestCanceled
		if !strings.Contains(resp.Err().Error(), "canceled") {
			t.Errorf("expected '%v', got '%v'", context.Canceled, resp.Err())
		}
	}
}

// TestNestedDirectory tests that missing subdirectories are created.
func TestNestedDirectory(t *testing.T) {
	dir := "./.testNested/one/two/three"
	filename := ".testNestedFile"
	expect := dir + "/" + filename

	t.Run("Create", func(t *testing.T) {
		req, _ := NewRequest(expect, ts.URL+"/"+filename)
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			panic(err)
		}
		defer os.RemoveAll("./.testNested/")

		if resp.Filename != expect {
			t.Errorf("expected nested Request.Filename to be %v, got %v", expect, resp.Filename)
		}
	})

	t.Run("No create", func(t *testing.T) {
		req, _ := NewRequest(expect, ts.URL+"/"+filename)
		req.NoCreateDirectories = true

		resp := DefaultClient.Do(req)
		err := resp.Err()
		if !os.IsNotExist(err) {
			t.Errorf("expected: %v, got: %v", os.ErrNotExist, err)
		}
	})
}

// TestRemoteTime tests that the timestamp of the downloaded file can be set
// according to the timestamp of the remote file.
func TestRemoteTime(t *testing.T) {
	filename := "./.testRemoteTime"
	defer os.Remove(filename)

	// random number between 0 and now
	lastmod := rand.Int63n(time.Now().Unix())
	u := fmt.Sprintf("%s?lastmod=%d", ts.URL, lastmod)
	req, _ := NewRequest(filename, u)
	resp := DefaultClient.Do(req)
	if err := resp.Err(); err != nil {
		panic(err)
	}
	fi, err := os.Stat(resp.Filename)
	if err != nil {
		panic(err)
	}
	if fi.ModTime().Unix() != lastmod {
		t.Errorf("expected %v, got %v", time.Unix(lastmod, 0), fi.ModTime())
	}
}

func TestResponseCode(t *testing.T) {
	filename := "./.testResponseCode"

	t.Run("With404", func(t *testing.T) {
		defer os.Remove(filename)
		req, _ := NewRequest(filename, ts.URL+"?status=404")
		resp := DefaultClient.Do(req)
		expect := StatusCodeError(http.StatusNotFound)
		err := resp.Err()
		if err != expect {
			t.Errorf("expected %v, got '%v'", expect, err)
		}
		if !IsStatusCodeError(err) {
			t.Errorf("expected IsStatusCodeError to return true for %T: %v", err, err)
		}
	})

	t.Run("WithIgnoreNon2XX", func(t *testing.T) {
		defer os.Remove(filename)
		req, _ := NewRequest(filename, ts.URL+"?status=404")
		req.IgnoreBadStatusCodes = true
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			t.Errorf("expected nil, got '%v'", err)
		}
	})
}

func TestBeforeCopyHook(t *testing.T) {
	filename := "./.testBeforeCopy"
	t.Run("Noop", func(t *testing.T) {
		defer os.RemoveAll(filename)
		called := false
		req, _ := NewRequest(filename, ts.URL)
		req.BeforeCopy = func(resp *Response) error {
			called = true
			if resp.IsComplete() {
				t.Error("Response object passed to BeforeCopy hook has already been closed")
			}
			if resp.Progress() != 0 {
				t.Error("Download progress already > 0 when BeforeCopy hook was called")
			}
			if resp.Duration() == 0 {
				t.Error("Duration was zero when BeforeCopy was called")
			}
			if resp.BytesComplete() != 0 {
				t.Error("BytesComplete already > 0 when BeforeCopy hook was called")
			}
			return nil
		}
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			t.Errorf("unexpected error using BeforeCopy hook: %v", err)
		}
		testComplete(t, resp)
		if !called {
			t.Error("BeforeCopy hook was never called")
		}
	})

	t.Run("WithError", func(t *testing.T) {
		defer os.RemoveAll(filename)
		testError := errors.New("test")
		req, _ := NewRequest(filename, ts.URL)
		req.BeforeCopy = func(resp *Response) error {
			return testError
		}
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != testError {
			t.Errorf("expected error '%v', got '%v'", testError, err)
		}
		if resp.BytesComplete() != 0 {
			t.Errorf("expected 0 bytes completed for canceled BeforeCopy hook, got %d",
				resp.BytesComplete())
		}
		testComplete(t, resp)
	})
}

func TestAfterCopyHook(t *testing.T) {
	filename := "./.testAfterCopy"
	t.Run("Noop", func(t *testing.T) {
		defer os.RemoveAll(filename)
		called := false
		req, _ := NewRequest(filename, ts.URL)
		req.AfterCopy = func(resp *Response) error {
			called = true
			if resp.IsComplete() {
				t.Error("Response object passed to AfterCopy hook has already been closed")
			}
			if resp.Progress() <= 0 {
				t.Error("Download progress was 0 when AfterCopy hook was called")
			}
			if resp.Duration() == 0 {
				t.Error("Duration was zero when AfterCopy was called")
			}
			if resp.BytesComplete() <= 0 {
				t.Error("BytesComplete was 0 when AfterCopy hook was called")
			}
			return nil
		}
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != nil {
			t.Errorf("unexpected error using AfterCopy hook: %v", err)
		}
		testComplete(t, resp)
		if !called {
			t.Error("AfterCopy hook was never called")
		}
	})

	t.Run("WithError", func(t *testing.T) {
		defer os.RemoveAll(filename)
		testError := errors.New("test")
		req, _ := NewRequest(filename, ts.URL)
		req.AfterCopy = func(resp *Response) error {
			return testError
		}
		resp := DefaultClient.Do(req)
		if err := resp.Err(); err != testError {
			t.Errorf("expected error '%v', got '%v'", testError, err)
		}
		if resp.BytesComplete() <= 0 {
			t.Errorf("ByteCompleted was %d after AfterCopy hook was called",
				resp.BytesComplete())
		}
		testComplete(t, resp)
	})
}

func TestIssue37(t *testing.T) {
	// ref: https://github.com/cavaliercoder/grab/issues/37
	filename := "./.testIssue37"
	largeSize := 2097152
	smallSize := 1048576
	defer os.RemoveAll(filename)

	// download large file
	req, _ := NewRequest(filename, fmt.Sprintf("%s?size=%d", ts.URL, largeSize))
	resp := DefaultClient.Do(req)
	if err := resp.Err(); err != nil {
		t.Fatal(err)
	}

	// download new, smaller version of same file
	req, _ = NewRequest(filename, fmt.Sprintf("%s?size=%d", ts.URL, smallSize))
	req.NoResume = true
	resp = DefaultClient.Do(req)
	if err := resp.Err(); err != nil {
		t.Fatal(err)
	}

	// local file should have truncated and not resumed
	if resp.DidResume {
		t.Errorf("expected download to truncate, resumed instead")
	}

	fi, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}

	if fi.Size() != int64(smallSize) {
		t.Errorf("expected file size %d, got %d", smallSize, fi.Size())
	}
}

// TestHeadBadStatus validates that HEAD requests that return non-200 can be
// ignored and succeed if the GET requests succeeeds.
//
// Fixes: https://github.com/cavaliercoder/grab/issues/43
func TestHeadBadStatus(t *testing.T) {
	expect := http.StatusOK
	filename := ".testIssue43"
	testURL := fmt.Sprintf(
		"%s/%s?headStatus=%d",
		ts.URL,
		filename,
		http.StatusForbidden)
	req, _ := NewRequest("", testURL)
	resp := DefaultClient.Do(req)
	if err := resp.Err(); err != nil {
		t.Fatal(err)
	}
	if resp.HTTPResponse.StatusCode != expect {
		t.Errorf(
			"expected status code: %d, got:% d",
			expect,
			resp.HTTPResponse.StatusCode)
	}
}
