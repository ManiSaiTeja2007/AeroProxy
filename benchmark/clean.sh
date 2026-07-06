#!/bin/bash
echo "Cleaning Podman build cache and dangling images..."
podman image prune -f
podman builder prune -f
echo "Cleanup completed successfully!"
