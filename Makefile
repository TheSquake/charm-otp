.PHONY: all build test run lint vet fmt check-deps clean

all: build test lint

build:
	cd src && go build -o ../charm-otp main.go
	@echo "Build complete: ./charm-otp"

test:
	cd src && go test -v ./...
	@echo "Tests passed"

run: build
	@echo "Run with: ./charm-otp (enter testpass for NewDatabase.enc)"
	./charm-otp

lint: vet fmt

vet:
	cd src && go vet . 

fmt:
	cd src && go fmt ./...

check-deps:
	cd src && go mod tidy
	@echo "Dependencies checked"

clean:
	rm -f charm-otp
	cd src && go clean -i
	@echo "Cleaned"

# Integration note: requires otpclient-cli, wl-copy, foot terminal, NewDatabase.enc with testpass
