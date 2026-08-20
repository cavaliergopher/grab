GO = go
STATICCHECK = honnef.co/go/tools/cmd/staticcheck@2025.1.1

all: check

# check runs the same suite as the test workflow.
check: fmt-check vet tidy test

# fmt formats all source files in place.
fmt:
	cd v3 && $(GO) fmt ./...

# fmt-check formats all source files in place and fails if any of them needed
# it. go fmt prints the name of every file it rewrites.
fmt-check:
	@unformatted="$$(cd v3 && $(GO) fmt ./...)"; \
	if [ -n "$$unformatted" ]; then \
		echo "The following files were not formatted:"; \
		echo "$$unformatted"; \
		echo "Run 'make fmt' and commit the result."; \
		exit 1; \
	fi

vet:
	cd v3 && $(GO) vet ./...
	cd v3 && $(GO) run $(STATICCHECK) ./...

tidy:
	cd v3 && $(GO) mod tidy
	@git diff --exit-code -- v3/go.mod v3/go.sum || \
		(echo "go.mod or go.sum was not tidy. Commit the result."; exit 1)

test:
	cd v3 && $(GO) test -v -cover -race -count=1 -shuffle=on ./...
	cd v3/cmd/grab && $(MAKE) -B all

# bench runs the benchmarks that measure what grab itself costs. These are the
# ones to watch for regressions. To compare a change against main:
#
#	go install golang.org/x/perf/cmd/benchstat@latest
#	git stash && make bench > /tmp/old.txt && git stash pop
#	make bench > /tmp/new.txt
#	benchstat /tmp/old.txt /tmp/new.txt
bench:
	cd v3 && $(GO) test -run '^$$' -benchmem \
		-bench 'BenchmarkTransfer|BenchmarkRangeSet|BenchmarkCheckpointStore' ./...

# bench-network runs the benchmarks that show what Request.RangeSize and
# Request.Concurrency are worth, against a server behind a simulated round trip
# and per connection bandwidth limit. They are meant to be read rather than
# tracked: being dominated by simulated latency, they say little about a change
# to grab. See docs/range-requests.md.
bench-network:
	cd v3 && $(GO) test -run '^$$' -benchtime 5x \
		-bench 'BenchmarkRangeSize|BenchmarkConcurrency' ./...

install:
	cd v3/cmd/grab && $(MAKE) install

clean:
	cd v3 && $(GO) clean -x ./...

.PHONY: all check fmt fmt-check vet tidy test bench bench-network install clean
