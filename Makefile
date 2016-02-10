
GO = go

all: test lint

deps:
	$(GO) get -u github.com/djherbis/times
	$(GO) get -u github.com/golang/lint/golint
	$(GO) get -u github.com/fzipp/gocyclo
	$(GO) get -u github.com/client9/misspell/cmd/misspell

test:
	$(GO) test -v -cover

lint:
	golint . || :
	gocyclo -over 15 . || :
	misspell ./* || :

.PHONY: all deps test lint
