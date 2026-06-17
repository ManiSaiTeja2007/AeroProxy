Write-Host "Cleaning Podman build cache and dangling images..." -ForegroundColor Green
podman image prune -f
podman builder prune -f
Write-Host "Cleanup completed successfully!" -ForegroundColor Green
