GO = go
GOGET = $(GO) get -u

all: check

check:
	cd v3 && $(GO) test -v -cover -race ./...
	cd v3/cmd/grab && $(MAKE) -B all

install:
	cd v3/cmd/grab && $(MAKE) install

clean:
	cd v3 && $(GO) clean -x ./...
	rm -rvf ./.test*

.PHONY: all check install clean
