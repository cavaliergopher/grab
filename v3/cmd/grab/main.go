package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cavaliergopher/grab/v3"
	"github.com/cavaliergopher/grab/v3/pkg/grabui"
)

// byteSize is a flag.Value that accepts a plain number of bytes, or a number
// with a unit such as 512KiB, 8MiB or 1GB.
type byteSize int64

func (b *byteSize) String() string {
	if b == nil || *b == 0 {
		return "0"
	}
	return strconv.FormatInt(int64(*b), 10)
}

func (b *byteSize) Set(s string) error {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for _, unit := range []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
	} {
		if rest, ok := strings.CutSuffix(s, unit.suffix); ok {
			s, mult = strings.TrimSpace(rest), unit.mult
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("not a size: %q", s)
	}
	if n < 0 {
		return fmt.Errorf("size must not be negative: %q", s)
	}
	*b = byteSize(n * mult)
	return nil
}

func main() {
	var rangeSize byteSize
	dst := flag.String("o", ".", "write downloads to this directory")
	flag.Var(&rangeSize, "range-size",
		"download each file in ranges of this size, e.g. 8MiB (default: one request)")
	concurrency := flag.Int("concurrency", 1,
		"maximum ranges to download at the same time; needs -range-size")
	batch := flag.Int("batch", 0,
		"maximum files to download at the same time (default: all of them)")
	http1 := flag.Bool("http1", false,
		"use HTTP/1.1 rather than negotiating HTTP/2")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] url...\n\nflags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	urls := flag.Args()
	if len(urls) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	client := grab.NewClient()
	if *http1 {
		t := client.HTTPClient.(*http.Client).Transport.(*http.Transport)
		// An empty, non-nil TLSNextProto stops the client handling HTTP/2.
		// That alone is not enough: the TLS config it inherits still offers h2
		// over ALPN, so the server answers in a protocol nothing is left to
		// parse it. The advertised protocols have to be narrowed too.
		t.ForceAttemptHTTP2 = false
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{}
		} else {
			t.TLSClientConfig = t.TLSClientConfig.Clone()
		}
		t.TLSClientConfig.NextProtos = []string{"http/1.1"}
	}

	reqs := make([]*grab.Request, len(urls))
	for i, url := range urls {
		req, err := grab.NewRequest(*dst, url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
			os.Exit(1)
		}
		req.RangeSize = int64(rangeSize)
		req.Concurrency = *concurrency
		reqs[i] = req
	}

	ui := grabui.NewConsoleClient(client)
	respch := ui.Do(context.Background(), *batch, reqs...)

	failed := 0
	for resp := range respch {
		if err := resp.Err(); err != nil {
			failed++
			continue
		}
		// A summary line per file, so that a transfer can be timed without
		// wrapping the command in a stopwatch.
		fmt.Printf("%s\t%d bytes\t%s\t%.2f MB/s\t%s\n",
			resp.Filename,
			resp.BytesComplete(),
			resp.Duration().Round(time.Millisecond),
			resp.BytesPerSecond()/1e6,
			resp.HTTPResponse.Proto)
	}
	os.Exit(failed)
}
