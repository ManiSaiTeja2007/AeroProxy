Write-Host "1. Building and starting container cluster..." -ForegroundColor Green
podman compose -f benchmark/docker-compose.yml up -d --build

Write-Host "2. Cleaning dangling intermediate images..." -ForegroundColor Green
powershell ./benchmark/clean.ps1

Write-Host "3. Executing AeroProxy capability benchmarks..." -ForegroundColor Green
go run benchmark/run_benchmarks.go

Write-Host "4. Tearing down container cluster..." -ForegroundColor Green
podman compose -f benchmark/docker-compose.yml down

Write-Host "5. Final cleanup of container resources..." -ForegroundColor Green
powershell ./benchmark/clean.ps1
