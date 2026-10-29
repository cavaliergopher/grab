package grab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadRequestBody(t *testing.T) {
	for _, tt := range []struct {
		name, method               string
		streaming, redirect, store bool
	}{
		{name: "POST", method: http.MethodPost},
		{name: "POST to file", method: http.MethodPost, streaming: true, store: true},
		{name: "streaming GET", method: http.MethodGet, streaming: true},
		{name: "redirected POST", method: http.MethodPost, redirect: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const payload = "download parameters"
			requests := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests <- r.Method + ":" + r.URL.Path + ":" + string(body)
				if r.Method == http.MethodHead {
					_, _ = io.WriteString(w, "download contents")
					return
				}
				if r.Method != tt.method || string(body) != payload {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if tt.redirect && r.URL.Path == "/download" {
					http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
					return
				}
				_, _ = io.WriteString(w, "download contents")
			}))
			defer server.Close()

			var body io.Reader = strings.NewReader(payload)
			if tt.streaming {
				// This reader cannot be replayed through http.Request.GetBody.
				body = struct{ io.Reader }{body}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, tt.method, server.URL+"/download", body)
			if err != nil {
				t.Fatal(err)
			}
			filename := ""
			if tt.store {
				filename = t.TempDir()
			}
			resp := NewClient().Do(&Request{HTTPRequest: req, Filename: filename, NoStore: !tt.store})
			if err := resp.Err(); err != nil {
				t.Fatalf("download failed: %v", err)
			}
			got, err := resp.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "download contents" {
				t.Fatalf("downloaded %q, want %q", got, "download contents")
			}
			paths := []string{"/download"}
			if tt.redirect {
				paths = append(paths, "/final")
			}
			for _, path := range paths {
				want := tt.method + ":" + path + ":" + payload
				if got := <-requests; got != want {
					t.Fatalf("received %q, want %q", got, want)
				}
			}
			select {
			case extra := <-requests:
				t.Fatalf("unexpected extra request: %q", extra)
			default:
			}
		})
	}
}
