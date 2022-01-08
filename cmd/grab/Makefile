SOURCES = main.go

all : grab

grab: $(SOURCES)
	go build -o grab $(SOURCES)

clean:
	go clean -x
	rm -vf grab

check:
	go test -v .

install:
	go install -v .

.PHONY: all clean check install
