package grab

import "testing"
import "net/http"

func TestURLFilenames(t *testing.T) {
	tests := map[string]string{
		"http://test.com/path/filename": "filename",
	}

	for url, expect := range tests {
		req, _ := http.NewRequest("GET", url, nil)
		resp := &http.Response{
			Request: req,
		}
		actual, err := guessFilename(resp)
		if err != nil {
			panic(err)
		}

		if actual != expect {
			t.Errorf("expected '%v', got '%v'", expect, actual)
		}
	}
}
