package grab

import (
	"fmt"
	"os"
	"testing"
)

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
		t.Fatal("Error in GetAsync transfer: %v", resp.Error)
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
