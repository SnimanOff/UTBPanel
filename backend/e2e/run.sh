#!/bin/sh
# End-to-end test of the update pipeline in docker compose:
#   delivery to the echo bot, user accounting, maintenance replies while the
#   core is down with the queue handed over afterwards, "bot unavailable"
#   after a crash, and the webhook set again; bad versions rolled back
#   (with the database when the schema changed); canary routing, promote
#   and cancel.
set -eu
cd "$(dirname "$0")/../.."
export PUBLIC_ADDR=edge COMPOSE_PROJECT_NAME=tge2e
dc() { docker compose -f docker-compose.yml -f backend/e2e/compose.yml "$@"; }
phase() { dc run --rm tester go test -count=1 -v -tags e2e -run "^$1$" ./e2e; }

docker build -q -t utbpanel:e2e . >/dev/null
dc down -v >/dev/null 2>&1 || true
dc up -d edge core panel faketg
trap 'dc logs --no-color core edge > /tmp/tge2e.log 2>&1; dc down -v >/dev/null 2>&1' EXIT

phase TestDelivery

dc stop core
phase TestCoreDown
dc start core
phase TestQueueHandedOver

dc exec -T core sh -c 'kill -9 $(pgrep -f "[r]eleases/") 2>/dev/null; true'
phase TestBotCrash

phase TestDeployRollback
phase TestCanary
phase TestBackupAndTransfer

echo "e2e: all phases passed"
