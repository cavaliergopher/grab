GO = go
GOGET = $(GO) get -u

all: test lint

test:
	$(GO) test -v -cover

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

.PHONY: all deps test test-deps lint lint-deps
