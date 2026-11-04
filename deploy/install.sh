#!/bin/sh
# Pocket Homelab — one-command install of Pocket Core and its app installer.
#
#   curl -fsSL https://pockethomelab.com/install.sh | sh
#   curl -fsSL https://pockethomelab.com/install.sh | sh -s -- --domain example.com --email you@example.com
#
# What it does (Linux with Docker; it offers to install Docker if missing):
#   - writes ~/pocket/docker-compose.yml + .env: Pocket Core, the installer agent
#     (Install / Update / Remove apps from Core's web page) and, with --domain, Caddy
#   - starts them and prints the address of Core's setup page
# Without --domain every app gets its own port on this machine (http://<this machine>:<port>),
# so nothing but a browser on the same network (or Tailscale) is needed.
# With --domain, *.<domain> must point at this machine and ports 80/443 must reach it:
# Caddy then gets HTTPS certificates for core.<domain> and every app (<app>.<domain>).
#
# Run it again to update Pocket Core to the newest release; settings in .env are kept.
#
# Options:
#   --dir DIR        where everything lives (~/pocket)
#   --host HOST      address apps are opened on without a domain (this machine's LAN address;
#                    use your Tailscale name, e.g. box.tail1234.ts.net, to reach them from anywhere)
#   --domain DOMAIN  serve core.DOMAIN and <app>.DOMAIN over HTTPS through Caddy
#   --email EMAIL    for Let's Encrypt (with --domain)
#   --version X.Y.Z  a specific Pocket Core release (default: the newest)
#   --port PORT      Core's port (8179)
#   -y, --yes        don't ask (installs Docker if missing)
set -eu

REPO=abjelosevic88/pocket-homelab-core
DIR=${POCKET_DIR:-$HOME/pocket}
HOST="" DOMAIN="" EMAIL="" VERSION="" PORT="" YES=0

say() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
ask() { # ask "question" → 0 for yes
	[ "$YES" = 1 ] && return 0
	[ -r /dev/tty ] || die "$1 Re-run with --yes to answer yes."
	printf '%s [Y/n] ' "$1" >/dev/tty
	read -r a </dev/tty || a=n
	case "$a" in "" | y | Y | yes) return 0 ;; *) return 1 ;; esac
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dir) DIR=$2; shift 2 ;;
	--host) HOST=$2; shift 2 ;;
	--domain) DOMAIN=$2; shift 2 ;;
	--email) EMAIL=$2; shift 2 ;;
	--version) VERSION=${2#v}; shift 2 ;;
	--port) PORT=$2; shift 2 ;;
	-y | --yes) YES=1; shift ;;
	-h | --help) sed -n '2,30p' "$0" 2>/dev/null || say "See https://pockethomelab.com/docs"; exit 0 ;;
	*) die "unknown option $1 (see --help)" ;;
	esac
done

[ "$(uname -s)" = Linux ] || die "this installs on Linux (a Pocket Box, a mini PC, a VM or a Raspberry Pi 4/5)."
case "$(uname -m)" in x86_64 | amd64 | aarch64 | arm64) ;; *) die "Pocket Core runs on 64-bit x86 or ARM, not $(uname -m)." ;; esac
command -v curl >/dev/null || die "curl is needed."

# ---- Docker ----
SUDO=""
[ "$(id -u)" = 0 ] || SUDO=sudo
if ! command -v docker >/dev/null 2>&1; then
	ask "Docker is not installed. Install it now (get.docker.com)?" || die "install Docker first: https://docs.docker.com/engine/install/"
	curl -fsSL https://get.docker.com | $SUDO sh
	[ "$(id -u)" = 0 ] || $SUDO usermod -aG docker "$(id -un)" || true
fi
DOCKER=docker
if ! docker info >/dev/null 2>&1; then
	if [ -n "$SUDO" ] && $SUDO docker info >/dev/null 2>&1; then
		DOCKER="$SUDO docker" # not in the docker group yet (log out and in once to drop sudo)
	else
		die "Docker is installed but not running (try: sudo systemctl start docker)."
	fi
fi
$DOCKER compose version >/dev/null 2>&1 || die "the Docker Compose plugin is missing (package docker-compose-plugin)."
SOCK=/var/run/docker.sock
[ -S "$SOCK" ] || die "no Docker socket at $SOCK (rootless Docker is not supported yet)."

# ---- settings: keep what .env has, flags win ----
mkdir -p "$DIR"
DIR=$(cd "$DIR" && pwd)
ENV="$DIR/.env"
get() { [ -f "$ENV" ] && sed -n "s/^$1=//p" "$ENV" | tail -n1 || true; }
[ -n "$DOMAIN" ] || DOMAIN=$(get POCKET_DOMAIN)
[ -n "$EMAIL" ] || EMAIL=$(get POCKET_EMAIL)
[ -n "$HOST" ] || HOST=$(get POCKET_HOST)
[ -n "$PORT" ] || PORT=$(get CORE_PORT)
[ -n "$PORT" ] || PORT=8179
TOKEN=$(get INSTALLER_TOKEN)
[ -n "$TOKEN" ] || TOKEN=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
if [ -z "$HOST" ]; then
	HOST=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')
	[ -n "$HOST" ] || HOST=$(hostname -I 2>/dev/null | awk '{print $1}')
	[ -n "$HOST" ] || die "could not tell this machine's address; pass --host."
fi
TZ_NAME=$(timedatectl show -p Timezone --value 2>/dev/null || true)
[ -n "$TZ_NAME" ] || TZ_NAME=$(cat /etc/timezone 2>/dev/null || true)
[ -n "$TZ_NAME" ] || TZ_NAME=$(readlink /etc/localtime 2>/dev/null | sed -n 's#.*zoneinfo/##p')
[ -n "$TZ_NAME" ] || TZ_NAME=UTC

if [ -z "$VERSION" ]; then
	# The newest release tag (GitHub answers /releases/latest with a redirect to it).
	VERSION=$(curl -fsSI "https://github.com/$REPO/releases/latest" | tr -d '\r' | sed -n 's#^[Ll]ocation: .*/tag/v##p' | tail -n1)
	[ -n "$VERSION" ] || die "could not find the newest Pocket Core release; pass --version."
fi

if [ -n "$DOMAIN" ]; then
	CORE_URL="https://core.$DOMAIN" APP_URL="https://{slug}.$DOMAIN" SECURE=true PROFILES=domain
	CADDYFILE="$DIR/caddy/Caddyfile"
else
	CORE_URL="http://$HOST:$PORT" APP_URL="http://$HOST:{port}" SECURE=false PROFILES="" CADDYFILE=""
fi
PUID=$(id -u) PGID=$(id -g)
DOCKER_GID=$(stat -c %g "$SOCK")

step "Installing Pocket Core $VERSION into $DIR"
mkdir -p "$DIR/core/data" "$DIR/installer" "$DIR/apps"
chmod 700 "$DIR/installer"

umask 077
cat >"$ENV" <<EOF
# Pocket Core — written by install.sh, kept when you run it again.
POCKET_VERSION=$VERSION
POCKET_DIR=$DIR
POCKET_HOST=$HOST
POCKET_DOMAIN=$DOMAIN
POCKET_EMAIL=$EMAIL
COMPOSE_PROFILES=$PROFILES
CORE_PORT=$PORT
BASE_URL=$CORE_URL
SECURE_COOKIES=$SECURE
TZ=$TZ_NAME
PUID=$PUID
PGID=$PGID
DOCKER_GID=$DOCKER_GID
INSTALLER_TOKEN=$TOKEN
INSTALLER_PUBLIC_URL=$APP_URL
INSTALLER_CADDYFILE=$CADDYFILE
EOF
umask 022

[ -f "$DIR/docker-compose.yml" ] && cp "$DIR/docker-compose.yml" "$DIR/docker-compose.yml.bak"
cat >"$DIR/docker-compose.yml" <<'EOF'
# Pocket Core + its app installer, written by install.sh (https://pockethomelab.com/install.sh).
# Settings are in .env; run install.sh again to update. Apps you install live in ./apps.
services:
  core:
    image: ghcr.io/abjelosevic88/pocket-homelab-core:${POCKET_VERSION}
    container_name: pocket-core
    restart: unless-stopped
    user: "${PUID}:${PGID}"
    ports:
      - "${CORE_PORT}:8080"
    environment:
      TZ: ${TZ}
      BASE_URL: ${BASE_URL}
      SECURE_COOKIES: ${SECURE_COOKIES}
      INSTALLER_SOCKET: /run/pocket-installer/installer.sock
      INSTALLER_TOKEN: ${INSTALLER_TOKEN}
    volumes:
      - ./core/data:/data
      - ./installer:/run/pocket-installer
    extra_hosts:
      - "host.docker.internal:host-gateway"

  # Does the Docker work behind Install / Update / Remove. Core never sees the Docker socket.
  # Root inside (the Docker socket is root-equivalent anyway), so it can give each app's data
  # folders to the uid Pocket images run as (1000) and everything else to you (FILES_OWNER).
  installer:
    image: ghcr.io/abjelosevic88/pocket-homelab-core-installer:${POCKET_VERSION}
    container_name: pocket-core-installer
    restart: unless-stopped
    network_mode: host # sees which ports are free and checks new apps on 127.0.0.1
    environment:
      TZ: ${TZ}
      INSTALLER_SOCKET: /run/pocket-installer/installer.sock
      INSTALLER_TOKEN: ${INSTALLER_TOKEN}
      INSTALLER_PUBLIC_URL: ${INSTALLER_PUBLIC_URL}
      INSTALLER_APPS_DIR: ${POCKET_DIR}/apps
      INSTALLER_CORE_URL: http://host.docker.internal:${CORE_PORT}
      INSTALLER_DOCKER_GID: ${DOCKER_GID}
      INSTALLER_FILES_OWNER: ${PUID}:${PGID}
      INSTALLER_CADDYFILE: ${INSTALLER_CADDYFILE}
      INSTALLER_CADDY_CONTAINER: pocket-caddy
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ${POCKET_DIR}:${POCKET_DIR} # same path inside and out: compose files name host folders
      - ./installer:/run/pocket-installer

  # Only with --domain (COMPOSE_PROFILES=domain): HTTPS for core.<domain> and every app.
  caddy:
    image: caddy:2
    container_name: pocket-caddy
    restart: unless-stopped
    profiles: [domain]
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./caddy:/etc/caddy
      - ./caddy/data:/data
      - ./caddy/config:/config
    extra_hosts:
      - "host.docker.internal:host-gateway"
EOF

if [ -n "$DOMAIN" ] && [ ! -f "$CADDYFILE" ]; then
	mkdir -p "$DIR/caddy"
	{
		[ -n "$EMAIL" ] && printf '{\n\temail %s\n}\n\n' "$EMAIL"
		printf '# Pocket Core. The installer adds a site here for every app it installs.\ncore.%s {\n\treverse_proxy host.docker.internal:%s\n}\n' "$DOMAIN" "$PORT"
	} >"$CADDYFILE"
fi

step "Starting"
cd "$DIR"
$DOCKER compose pull --quiet
$DOCKER compose up -d --remove-orphans

printf 'Waiting for Pocket Core'
i=0
until curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; do
	i=$((i + 1))
	[ $i -lt 60 ] || { say; die "Pocket Core did not start; see: cd $DIR && $DOCKER compose logs core"; }
	printf '.'
	sleep 2
done
say

step "Pocket Core $VERSION is running"
say "Open $CORE_URL to create your account, then Apps → Install app."
[ -n "$DOMAIN" ] && say "(core.$DOMAIN must point here with ports 80/443 open before you can sign in: Core expects HTTPS.)"
[ "$DOCKER" = docker ] || say "Tip: log out and back in once, so Docker works without sudo."
say "Files: $DIR · update: run this script again · logs: cd $DIR && docker compose logs -f"
