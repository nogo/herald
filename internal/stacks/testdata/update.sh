#!/bin/bash
set -euo pipefail

echo "=== Nextcloud Update ==="
echo "Stack: $STACK_NAME"
echo "Dir:   $STACK_DIR"

# Enable maintenance mode
docker compose -f "$COMPOSE_FILE" exec -T nextcloud-app php occ maintenance:mode --on

# Pull latest images and rebuild
docker compose -f "$COMPOSE_FILE" build --pull
docker compose -f "$COMPOSE_FILE" up -d

# Wait for containers to be healthy
sleep 10

# Run upgrade
docker compose -f "$COMPOSE_FILE" exec -T nextcloud-app php occ upgrade
docker compose -f "$COMPOSE_FILE" exec -T nextcloud-app php occ db:add-missing-indices
docker compose -f "$COMPOSE_FILE" exec -T nextcloud-app php occ db:add-missing-columns

# Disable maintenance mode
docker compose -f "$COMPOSE_FILE" exec -T nextcloud-app php occ maintenance:mode --off

echo "=== Nextcloud Update Complete ==="
