package grab

import (
	"fmt"
	"os"
	"testing"
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
			t.Errorf("Response.Progress returned %v (%v/%v bytes), expected 1", p, resp.BytesComplete(), resp.Size)
		}
	}
}

// TestResponseProgress tests the functions which indicate the progress of an
// in-process file transfer.
func TestResponseProgress(t *testing.T) {
	filename := ".testResponseProgress"
	defer os.Remove(filename)

	sleep := 300            // ms
	size := int64(1024 * 8) // bytes

	// request a slow transfer
	req, _ := NewRequest(filename, fmt.Sprintf("%s?sleep=%v&size=%v", ts.URL, sleep, size))
	resp := DefaultClient.Do(req)

	// make sure transfer has not started
	if resp.IsComplete() {
		t.Errorf("Transfer should not have started")
	}

	if p := resp.Progress(); p != 0 {
		t.Errorf("Transfer should not have started yet but progress is %v", p)
	}

	// wait for transfer to complete
	<-resp.Done

	// make sure transfer is complete
	if p := resp.Progress(); p != 1 {
		t.Errorf("Transfer is complete but progress is %v", p)
	}

	if s := resp.BytesComplete(); s != size {
		t.Errorf("Expected to transfer %v bytes, got %v", size, s)
	}
}
