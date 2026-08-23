package main

import (
	"math"
	"testing"
)

func TestByteSizeSet(t *testing.T) {
	tests := []struct {
		in     string
		expect int64
		fails  bool
	}{
		{in: "0", expect: 0},
		{in: "512", expect: 512},
		{in: " 8 ", expect: 8},
		{in: "512KiB", expect: 512 << 10},
		{in: "8MiB", expect: 8 << 20},
		{in: "1GiB", expect: 1 << 30},
		{in: "1KB", expect: 1000},
		{in: "1MB", expect: 1000000},
		{in: "1GB", expect: 1000000000},
		{in: "4K", expect: 4 << 10},
		{in: "4M", expect: 4 << 20},
		{in: "4G", expect: 4 << 30},
		{in: "8 MiB", expect: 8 << 20},

		// the largest value each unit can carry, and the first that cannot
		{in: "9223372036854775807", expect: math.MaxInt64},
		{in: "9223372036", expect: 9223372036},
		{in: "9223372036GB", expect: 9223372036000000000},
		{in: "9223372037GB", fails: true},
		{in: "8589934591GiB", expect: 8589934591 << 30},
		{in: "8589934592GiB", fails: true},
		{in: "10000000000GB", fails: true},

		{in: "", fails: true},
		{in: "-1", fails: true},
		{in: "-1MiB", fails: true},
		{in: "banana", fails: true},
		{in: "8XiB", fails: true},
	}

	for _, test := range tests {
		var b byteSize
		err := b.Set(test.in)
		if test.fails {
			if err == nil {
				t.Errorf("Set(%q): expected an error, got: %d", test.in, int64(b))
			}
			continue
		}
		if err != nil {
			t.Errorf("Set(%q): unexpected error: %v", test.in, err)
			continue
		}
		if int64(b) != test.expect {
			t.Errorf("Set(%q): expected %d, got: %d", test.in, test.expect, int64(b))
		}
	}
}
