#!/bin/sh
# Updates a running installation to a published image version. Runs on the
# server, in the directory with docker-compose.yml and .env:
#
#   ssh server 'sh -s -- 1.2.3 [panel,core,edge]' < deploy/update.sh
#
# The deploy workflow sends it this way, so the server's own checkout may be
# older than the script.
#
# - panel: the container is recreated; bots keep running.
# - core: swapped in place with `core upgrade`, bots keep running; when the
#   old core cannot do that, the container is recreated (a short pause for
#   bots, updates wait at the edge).
# - edge: recreated only when asked for, Telegram retries what it missed.
#
# .env gets the new *_VERSION, and a copy of the previous .env is kept.
set -eu

version="${1:?usage: update.sh <version> [services]}"
version="${version#v}"
services="${2:-panel,core}"

[ -f docker-compose.yml ] && [ -f .env ] || {
  echo "update: run in the directory with docker-compose.yml and .env (now $(pwd))" >&2
  exit 1
}

dc() { docker compose "$@"; }
log() { echo "update: $*"; }

# The published image. An installation built on the server has a local name
# in IMAGE (e.g. "utbpanel"); it is switched to the registry here.
image="${UPDATE_IMAGE:-ghcr.io/snimanoff/utbpanel}"
was="$(sed -n 's/^IMAGE=//p' .env | tail -n 1)"

setenv() { # NAME VALUE
  if grep -q "^$1=" .env; then
    sed -i "s|^$1=.*|$1=$2|" .env
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}

has() { case ",$services," in *",$1,"*) return 0 ;; *) return 1 ;; esac; }

cp .env ".env.before-$version"
log "pulling $image:$version"
docker pull -q "$image:$version" >/dev/null

if [ "$was" != "$image" ]; then
  # IMAGE is shared by all three services. Services not updated now get the
  # same version too, so whenever compose recreates them it finds the image
  # in the registry instead of a local tag.
  log "IMAGE ${was:-<default>} → $image"
  setenv IMAGE "$image"
  for v in PANEL_VERSION CORE_VERSION EDGE_VERSION; do setenv "$v" "$version"; done
fi

if has panel; then
  setenv PANEL_VERSION "$version"
  dc up -d --no-deps panel
  log "panel → $version"
fi

if has core; then
  tmp="$(mktemp)"
  cid="$(docker create "$image:$version")"
  docker cp "$cid:/usr/local/bin/core" "$tmp" >/dev/null
  docker rm "$cid" >/dev/null
  if dc exec -T core core upgrade < "$tmp"; then
    # The container keeps running the new binary; the next recreation takes
    # the same version from the image.
    setenv CORE_VERSION "$version"
    log "core → $version (in place, bots kept running)"
  else
    log "core upgrade in place failed, recreating the container"
    setenv CORE_VERSION "$version"
    dc up -d --no-deps core
    log "core → $version (recreated)"
  fi
  rm -f "$tmp"
fi

if has edge; then
  setenv EDGE_VERSION "$version"
  dc up -d --no-deps edge
  log "edge → $version"
fi

# Every service must be up after the update.
sleep 5
down="$(dc ps --services --status exited --status dead --status restarting 2>/dev/null || true)"
dc ps --format '{{.Service}}\t{{.Image}}\t{{.Status}}'
if [ -n "$down" ]; then
  echo "update: not running: $down (previous settings: .env.before-$version)" >&2
  exit 1
fi

# Old images of the panel are not needed: each one is ~190 MB and every
# update leaves one more. Kept: the versions in .env (compose recreates
# services from them) and images of running containers (a core upgraded in
# place still runs from its old image; docker refuses to remove those).
keep=" $(sed -n 's/^\(PANEL\|CORE\|EDGE\)_VERSION=//p' .env | tr '\n' ' ') "
docker images "$image" --format '{{.Tag}}' | while read -r tag; do
  case "$keep" in *" $tag "*) continue ;; esac
  if docker rmi "$image:$tag" >/dev/null 2>&1; then log "removed old image $image:$tag"; fi
done || true
docker image prune -f >/dev/null 2>&1 || true
log "done"
