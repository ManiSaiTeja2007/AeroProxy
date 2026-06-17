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
	podman image prune -f

cluster-up:
	podman compose -f benchmark/docker-compose.yml up -d --build
	podman image prune -f

cluster-down:
	podman compose -f benchmark/docker-compose.yml down
	podman image prune -f

benchmark:
	podman compose -f benchmark/docker-compose.yml up -d --build
	podman image prune -f
	go run benchmark/run_benchmarks.go
	podman compose -f benchmark/docker-compose.yml down
	podman image prune -f

clean:
	go clean
	rm -f aeroproxy aeroproxy.exe
