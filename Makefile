GO = go
GOGET = $(GO) get -u

all: check lint

check:
	$(GO) test -v -cover ./...

clean:
	$(GO) clean -x ./...
	rm -vf ./.test*

test-deps:
	$(GOGET) golang.org/x/tools/cmd/cover
	$(GOGET) github.com/djherbis/times

lint:
	gofmt -l -e -s . || :
	go vet . || :
	golint . || :
	gocyclo -over 15 . || :
	misspell ./* || :

lint-deps:	
	$(GOGET) github.com/golang/lint/golint
	$(GOGET) github.com/fzipp/gocyclo
	$(GOGET) github.com/client9/misspell/cmd/misspell

.PHONY: all deps check test-deps lint lint-deps
