#!/bin/sh
# Usage: scripts/smoke/run.sh [slug…]   (no slugs = every app in the catalog; RESULTS=file for the JSON)
# Needs Docker on this machine; uses the published install.sh and catalog, so it tests what people get.
# Throwaway Docker-in-Docker host: install.sh, then every catalog app through Core. Removed at the end (results kept).
set -u
C=pocket-smoke
docker rm -f -v $C >/dev/null 2>&1
docker run -d --privileged --name $C docker:dind >/dev/null || exit 1
until docker exec $C docker info >/dev/null 2>&1; do sleep 2; done
docker exec $C apk add -q curl python3 >/dev/null
docker exec $C sh -c 'curl -fsSL https://pockethomelab.com/install.sh | sh -s -- --yes' >/dev/null 2>&1 || { echo "install.sh failed"; docker exec $C sh -c 'cd /root/pocket && docker compose logs --tail 30'; }
HOST=$(docker exec $C sh -c "ip -4 route get 1.1.1.1 | awk '{for(i=1;i<NF;i++) if(\$i==\"src\"){print \$(i+1); exit}}'")
docker exec $C sh -c 'cd /root/pocket && docker compose ps --format "{{.Name}} {{.Status}}"'
docker exec -i $C python3 - "http://$HOST:8179" "$@" < "$(dirname "$0")/driver.py"
docker cp $C:/root/results.json "${RESULTS:-results.json}" 2>/dev/null
docker rm -f -v $C >/dev/null 2>&1
docker rmi docker:dind >/dev/null 2>&1
echo finished
