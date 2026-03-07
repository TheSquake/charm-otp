.PHONY: all build test run lint vet fmt check-deps clean

all: build test lint

build:
	cd src && go build -o ../simple-otp-tui main.go
	@echo "Build complete: ./simple-otp-tui"

test:
	cd src && go test -v ./...
	@echo "Tests passed"

run: build
	@echo "Run with: ./simple-otp-tui (enter testpass for NewDatabase.enc)"
	./simple-otp-tui

lint: vet fmt

vet:
	cd src && go vet . 

fmt:
	cd src && go fmt ./...

check-deps:
	cd src && go mod tidy
	@echo "Dependencies checked"

clean:
	rm -f simple-otp-tui
	cd src && go clean -i
	@echo "Cleaned"

# Integration note: requires otpclient-cli, wl-copy, foot terminal, NewDatabase.enc with testpass
