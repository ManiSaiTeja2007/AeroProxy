#!/bin/bash
echo "1. Building and starting container cluster..."
podman compose -f benchmark/docker-compose.yml up -d --build

echo "2. Cleaning dangling intermediate images..."
./benchmark/clean.sh

echo "3. Executing AeroProxy capability benchmarks..."
go run benchmark/run_benchmarks.go

echo "4. Tearing down container cluster..."
podman compose -f benchmark/docker-compose.yml down

echo "5. Final cleanup of container resources..."
./benchmark/clean.sh
