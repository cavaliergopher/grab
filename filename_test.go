package grab

import (
	"net/http"
	"testing"
)

func TestURLFilenames(t *testing.T) {
	expect := "filename"

	shouldPass := []string{
		"http://test.com/filename",
		"http://test.com/path/filename",
		"http://test.com/deep/path/filename",
		"http://test.com/filename?with=args",
		"http://test.com/filename#with-fragment",
	}

	shoudlFail := []string{
		"http://test.com",
		"http://test.com/",
		"http://test.com/filename/",
		"http://test.com/filename/?with=args",
		"http://test.com/filename/#with-fragment",
	}

	for _, url := range shouldPass {
		req, _ := http.NewRequest("GET", url, nil)
		resp := &http.Response{
			Request: req,
		}
		actual, err := guessFilename(resp)
		if err != nil {
			t.Errorf("%v", err)
		}

		if actual != expect {
			t.Errorf("expected '%v', got '%v'", expect, actual)
		}
	}

	for _, url := range shoudlFail {
		req, _ := http.NewRequest("GET", url, nil)
		resp := &http.Response{
			Request: req,
		}

		_, err := guessFilename(resp)
		if err != ErrNoFilename {
			t.Errorf("expected '%v', got '%v'", ErrNoFilename, err)
		}
	}
}
