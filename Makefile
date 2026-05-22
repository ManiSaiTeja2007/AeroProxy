.PHONY: build test docker-build cluster-up clean

BINARY_NAME=aeroproxy
ifeq ($(OS),Windows_NT)
    BINARY_NAME=aeroproxy.exe
endif

build:
	go build -ldflags="-s -w" -o $(BINARY_NAME) ./cmd/aeroproxy/main.go

test:
	go test -v -race -count=1 ./test/...

docker-build:
	podman build -t aeroproxy:latest .

cluster-up:
	podman-compose up --build --scale aeroproxy-2=2

clean:
	go clean
	rm -f aeroproxy aeroproxy.exe
