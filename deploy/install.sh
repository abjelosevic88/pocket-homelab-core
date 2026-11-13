#!/bin/sh
# Pocket Homelab — one-command install of Pocket Core and its app installer.
#
#   curl -fsSL https://pockethomelab.com/install.sh | sh
#
# What it does (Linux with Docker; it offers to install Docker if missing):
#   - writes ~/pocket/docker-compose.yml + .env: Pocket Core, the installer agent (Install / Update /
#     Remove apps from Core's web page), and Caddy and Tailscale for when they are switched on
#   - starts them and prints the address of Core's setup page
# Then everything else happens in the browser: create your account, and the setup asks how phones
# reach Pocket with a padlock (Pocket's free address, your own domain on Cloudflare, or just this
# network's addresses), which apps to start with (bundles or one by one) and whether to connect
# Tailscale for away from home. Until a name is chosen every app has its own port on this machine
# (http://<this machine>:<port>), so nothing but a browser on the same network is needed.
#
# Run it again to update Pocket Core to the newest release; settings in .env are kept.
#
# Options:
#   --dir DIR        where everything lives (~/pocket)
#   --host HOST      address apps are opened on without a name (this machine's LAN address)
#   --version X.Y.Z  a specific Pocket Core release (default: the newest)
#   --port PORT      Core's port (8179)
#   --notify URL     where update notifications go: an ntfy topic (https://ntfy.sh/my-topic) or an
#                    Apprise …/notify/<key> URL (also settable later in Core: Settings → Box)
#   -y, --yes        don't ask (installs Docker if missing)
# The same choices as the setup in the browser, all at once (a new install; for scripts):
#   --address pocket       Pocket's free address, <random>.box.pockethomelab.net, with a padlock
#   --address plain        this network's addresses (the default until a name is chosen)
#   --cloudflare-token T --cloudflare-domain example.com [--cloudflare-name pocket]
#                          your own domain on Cloudflare: Pocket at core.pocket.example.com
#   --bundle LIST          app bundles to install: essentials, office, developer, media, family,
#                          money, health (comma separated; pockethomelab.com/box/bundles)
#   --apps LIST            single apps to install as well, as slugs (notes,tasks; pockethomelab.com/apps)
#   --admin-email E        the email you will create your Core account with; the apps make it
#                          their admin (asked for when bundles or apps are given)
#   --tailscale-key KEY    connect Tailscale with this auth key (tskey-auth-…)
# Your own domain without Cloudflare, with ports 80/443 open to this machine:
#   --domain DOMAIN --email EMAIL   Caddy gets a certificate for core.DOMAIN and each <app>.DOMAIN
set -eu

REPO=abjelosevic88/pocket-homelab-core
BUNDLES_URL=https://raw.githubusercontent.com/abjelosevic88/pocket-homelab-box/main/bundles.json
DIR=${POCKET_DIR:-$HOME/pocket}
HOST="" DOMAIN="" EMAIL="" VERSION="" PORT="" NOTIFY="" YES=0 APPS="" BUNDLES="" ADMIN_EMAIL=""
ADDRESS="" CF_TOKEN="" CF_DOMAIN="" CF_NAME=pocket TS_KEY=""

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
	--notify) NOTIFY=$2; shift 2 ;;
	--address) ADDRESS=$2; shift 2 ;;
	--cloudflare-token) CF_TOKEN=$2; ADDRESS=cloudflare; shift 2 ;;
	--cloudflare-domain) CF_DOMAIN=$2; shift 2 ;;
	--cloudflare-name) CF_NAME=$2; shift 2 ;;
	--bundle | --bundles) BUNDLES=$2; shift 2 ;;
	--apps) APPS=$2; shift 2 ;;
	--admin-email) ADMIN_EMAIL=$2; shift 2 ;;
	--tailscale-key) TS_KEY=$2; shift 2 ;;
	-y | --yes) YES=1; shift ;;
	-h | --help) sed -n '2,38p' "$0" 2>/dev/null || say "See https://pockethomelab.com/docs"; exit 0 ;;
	*) die "unknown option $1 (see --help)" ;;
	esac
done

case "$ADDRESS" in "" | pocket | plain) ;; cloudflare)
	[ -n "$CF_TOKEN" ] && [ -n "$CF_DOMAIN" ] || die "your own domain on Cloudflare needs --cloudflare-token and --cloudflare-domain"
	case "$CF_TOKEN$CF_DOMAIN$CF_NAME" in *[!A-Za-z0-9._-]*) die "--cloudflare-token, --cloudflare-domain and --cloudflare-name take letters, digits, dots, dashes and underscores" ;; esac ;;
*) die "--address is pocket or plain (or use --cloudflare-token for your own domain)" ;; esac
[ -z "$ADDRESS" ] || [ -z "$DOMAIN" ] || die "--domain already sets the address; leave out --address"
case "$TS_KEY" in "" | tskey-*) ;; *) die "--tailscale-key takes a Tailscale auth key (tskey-auth-…)" ;; esac
case "$TS_KEY" in *[!A-Za-z0-9_-]*) die "--tailscale-key: that doesn't look like an auth key" ;; esac

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
[ -n "$NOTIFY" ] || NOTIFY=$(get NOTIFY_URL)
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

# The address. With --domain: Caddy and that domain, as set here. Otherwise the installer agent's
# address service (Settings → System in Core) switches the install over to a name once there is one
# and keeps it in .env (POCKET_ADDRESS), so running this again keeps it.
NAME=$(get POCKET_ADDRESS)
ADDR_SOCK="" PROFILES="" CADDYFILE=""
if [ -n "$DOMAIN" ]; then
	CORE_URL="https://core.$DOMAIN" APP_URL="https://{slug}.$DOMAIN" SECURE=true PROFILES=domain NAME=""
	CADDYFILE="$DIR/caddy/Caddyfile"
else
	ADDR_SOCK=/run/pocket-installer/address.sock
	# Keep what was switched on (proxy for a name, tailscale), drop --domain's.
	for p in $(get COMPOSE_PROFILES | tr ',' ' '); do
		case "$p" in proxy | tailscale) PROFILES="${PROFILES:+$PROFILES,}$p" ;; esac
	done
	if [ -n "$NAME" ]; then
		CORE_URL="https://core.$NAME" APP_URL="https://{slug}.$NAME" SECURE=true
	else
		CORE_URL="http://$HOST:$PORT" APP_URL="http://$HOST:{port}" SECURE=false
	fi
fi
PUID=$(id -u) PGID=$(id -g)
DOCKER_GID=$(stat -c %g "$SOCK")

# ---- apps to install right away (--bundle, --apps; a new install only) ----
# Only a Core without data is new: its registration key is chosen here and pinned for the first
# start, so the installer agent can install the apps before anyone has an account. Afterwards the
# pin is dropped again (Core keeps the key, and Settings → Box can rotate it).
NEW=1
[ -n "$(ls -A "$DIR/core/data" 2>/dev/null)" ] && NEW=0
if [ -n "$BUNDLES" ]; then
	json=$(curl -fsSL "$BUNDLES_URL") || die "could not fetch the bundles ($BUNDLES_URL)"
	for b in $(printf '%s' "$BUNDLES" | tr ',' ' '); do
		printf '%s' "$json" | grep -q "\"id\": *\"$b\"" || die "no bundle '$b' (there are: $(printf '%s' "$json" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | tr '\n' ' '))"
	done
	# bundles.json has each bundle's "id" a line before its "apps": [...] (pocket-homelab-box).
	from=$(printf '%s\n' "$json" | awk -v want=",$BUNDLES," '
		/"id":/ { cur = $0; sub(/.*"id": *"/, "", cur); sub(/".*/, "", cur) }
		/"apps":/ && cur != "" && index(want, "," cur ",") { s = $0; sub(/.*\[/, "", s); sub(/\].*/, "", s); gsub(/[" ]/, "", s); print s }')
	APPS="${APPS:+$APPS,}$(printf '%s' "$from" | tr '\n' ',')"
fi
APPS=$(printf '%s' "$APPS" | tr ',' '\n' | awk 'NF && !seen[$0]++' | paste -sd, -)
for a in $(printf '%s' "$APPS" | tr ',' ' '); do
	case "$a" in *[!a-z0-9-]*) die "--apps takes app slugs like notes,tasks, not '$a' (pockethomelab.com/apps)" ;; esac
done
if [ -n "$APPS" ] && [ "$NEW" = 0 ]; then
	say "Pocket Core is already set up here: add apps in Core (Apps → Install app); --bundle and --apps are for new installs."
	APPS=""
fi
REGKEY=""
if [ -n "$APPS" ]; then
	if [ -z "$ADMIN_EMAIL" ] && [ "$YES" != 1 ] && [ -r /dev/tty ]; then
		printf 'Installing Pocket Core with %s.\nThe email you will create your Core account with (the apps make it their admin)%s: ' \
			"$(printf '%s' "$APPS" | sed 's/,/, /g')" "${EMAIL:+ [$EMAIL]}" >/dev/tty
		read -r ADMIN_EMAIL </dev/tty || ADMIN_EMAIL=""
	fi
	[ -n "$ADMIN_EMAIL" ] || ADMIN_EMAIL=$EMAIL
	ADMIN_EMAIL=$(printf '%s' "$ADMIN_EMAIL" | tr 'A-Z' 'a-z')
	case "$ADMIN_EMAIL" in
	"") say "No email given: installing Pocket Core only (pick the apps in the browser instead)."; APPS="" ;;
	*[!a-z0-9._%+@-]* | *@*@* | @* | *@) die "'$ADMIN_EMAIL' does not look like an email address." ;;
	*@*) ;;
	*) die "'$ADMIN_EMAIL' does not look like an email address." ;;
	esac
	[ -z "$APPS" ] || REGKEY="pcreg_$(od -An -tx1 -N24 /dev/urandom | tr -d ' \n')"
fi
if [ "$ADDRESS" = pocket ] || [ "$ADDRESS" = cloudflare ]; then
	busy=$(ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E '[:.](80|443)$' || true)
	[ -z "$busy" ] || [ "$NAME" != "" ] || die "ports 80/443 are taken on this machine ($(printf '%s' "$busy" | tr '\n' ' ')); a name with a padlock needs them for Caddy"
fi

step "Installing Pocket Core $VERSION into $DIR"
mkdir -p "$DIR/core/data" "$DIR/installer" "$DIR/apps" "$DIR/caddy"
chmod 700 "$DIR/installer"

umask 077
cat >"$ENV" <<EOF
# Pocket Core — written by install.sh, kept when you run it again. The installer agent changes
# BASE_URL, SECURE_COOKIES, INSTALLER_PUBLIC_URL, COMPOSE_PROFILES and POCKET_ADDRESS itself when
# the address is changed in Core (Settings → System).
POCKET_VERSION=$VERSION
POCKET_DIR=$DIR
POCKET_HOST=$HOST
POCKET_DOMAIN=$DOMAIN
POCKET_EMAIL=$EMAIL
POCKET_ADDRESS=$NAME
COMPOSE_PROFILES=$PROFILES
CORE_PORT=$PORT
BASE_URL=$CORE_URL
SECURE_COOKIES=$SECURE
NOTIFY_URL=$NOTIFY
TZ=$TZ_NAME
PUID=$PUID
PGID=$PGID
DOCKER_GID=$DOCKER_GID
INSTALLER_TOKEN=$TOKEN
INSTALLER_PUBLIC_URL=$APP_URL
INSTALLER_CADDYFILE=$CADDYFILE
BOX_ADDRESS=$ADDR_SOCK
# Set only while install.sh installs the first apps (--bundle, --apps).
REGISTRATION_KEY=$REGKEY
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
      NOTIFY_URL: ${NOTIFY_URL:-}
      REGISTRATION_KEY: ${REGISTRATION_KEY:-}
      INSTALLER_SOCKET: /run/pocket-installer/installer.sock
      INSTALLER_TOKEN: ${INSTALLER_TOKEN}
      # The address choice and Tailscale, through the installer agent (empty with --domain).
      BOX_ADDRESS: ${BOX_ADDRESS:-}
    volumes:
      - ./core/data:/data
      - ./installer:/run/pocket-installer
    extra_hosts:
      - "host.docker.internal:host-gateway"

  # Does the Docker work behind Install / Update / Remove, and switches the install to the address
  # chosen in Core. Core never sees the Docker socket. Root inside (the Docker socket is
  # root-equivalent anyway), so it can give each app's data folders to the uid Pocket images run as
  # (1000) and everything else to you (FILES_OWNER).
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
      INSTALLER_ADDRESS_SOCKET: ${BOX_ADDRESS:-}
      INSTALLER_POCKET_DIR: ${POCKET_DIR}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ${POCKET_DIR}:${POCKET_DIR} # same path inside and out: compose files name host folders
      - ./installer:/run/pocket-installer

  # HTTPS for core.<name> and every app: with --domain (profile domain), or once Core has a name
  # (profile proxy; the installer agent writes ./caddy/Caddyfile and ./caddy/tls then).
  caddy:
    image: caddy:2
    container_name: pocket-caddy
    restart: unless-stopped
    profiles: [domain, proxy]
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./caddy:/etc/caddy
      - ./caddy/data:/data
      - ./caddy/config:/config
    extra_hosts:
      - "host.docker.internal:host-gateway"

  # Away from home (profile tailscale): started when you connect Tailscale in Core.
  tailscale:
    image: tailscale/tailscale:stable
    container_name: pocket-tailscale
    restart: unless-stopped
    profiles: [tailscale]
    network_mode: host
    cap_add: [NET_ADMIN, NET_RAW]
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      TS_STATE_DIR: /var/lib/tailscale
      TS_USERSPACE: "false"
    volumes:
      - ./tailscale:/var/lib/tailscale
EOF

if [ -n "$DOMAIN" ] && [ ! -f "$CADDYFILE" ]; then
	{
		[ -n "$EMAIL" ] && printf '{\n\temail %s\n}\n\n' "$EMAIL"
		printf '# Pocket Core. The installer adds a site here for every app it installs.\ncore.%s {\n\treverse_proxy host.docker.internal:%s\n}\n' "$DOMAIN" "$PORT"
	} >"$CADDYFILE"
fi

step "Starting"
cd "$DIR"
[ -n "${POCKET_NO_PULL:-}" ] || $DOCKER compose pull --quiet # POCKET_NO_PULL=1: images built here (tests)
$DOCKER compose up -d --remove-orphans

wait_core() {
	printf 'Waiting for Pocket Core'
	i=0
	until curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; do
		i=$((i + 1))
		[ $i -lt 60 ] || { say; die "Pocket Core did not start; see: cd $DIR && $DOCKER compose logs core"; }
		printf '.'
		sleep 2
	done
	say
}
wait_core

# The installer agent's two sockets: installs (bearer token) and the address/Tailscale service.
agent() { # agent PATH [JSON]
	if [ $# -gt 1 ]; then
		curl -fsS --unix-socket "$DIR/installer/installer.sock" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' --data "$2" "http://agent$1"
	else
		curl -fsS --unix-socket "$DIR/installer/installer.sock" -H "Authorization: Bearer $TOKEN" "http://agent$1"
	fi
}
address() { # address METHOD PATH [JSON]: answers JSON, {"error": …} when refused
	curl -sS --unix-socket "$DIR/installer/address.sock" -X "$1" -H 'Content-Type: application/json' ${3:+--data} ${3:+"$3"} "http://agent$2"
}
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -n1; } # field NAME < json
wait_agent() {
	i=0
	until agent /v1/status >/dev/null 2>&1 && { [ -z "$ADDR_SOCK" ] || [ -S "$DIR/installer/address.sock" ]; }; do
		i=$((i + 1))
		[ $i -lt 30 ] || die "the installer agent did not start; see: cd $DIR && $DOCKER compose logs installer"
		sleep 2
	done
}

# ---- the first apps, through the installer agent (the same Install as Core's Apps page) ----
INSTALLED="" FAILED=""
if [ -n "$APPS" ]; then
	step "Installing apps"
	wait_agent
	for a in $(printf '%s' "$APPS" | tr ',' ' '); do
		printf '  %s ' "$a"
		job=$(agent /v1/install "{\"slug\":\"$a\",\"registration_key\":\"$REGKEY\",\"admin_email\":\"$ADMIN_EMAIL\"}" 2>&1) || {
			say "could not start: $job"
			FAILED="$FAILED $a"
			continue
		}
		id=$(printf '%s' "$job" | field id) state=running out="" n=0
		# The agent gives up on a job after 20 minutes itself; this only guards against it vanishing.
		while [ "$state" = running ] && [ $n -lt 450 ]; do
			sleep 3
			n=$((n + 1))
			printf '.'
			out=$(agent "/v1/jobs/$id" 2>/dev/null) || continue
			state=$(printf '%s' "$out" | field state)
		done
		if [ "$state" = done ]; then
			say " ok"
			INSTALLED="$INSTALLED $a"
		else
			err=$(printf '%s' "$out" | field error)
			say " failed${err:+: $err}"
			FAILED="$FAILED $a"
		fi
	done
	# Drop the pin: Core keeps the key it was given, and rotating it in Settings now sticks.
	sed -i 's/^REGISTRATION_KEY=.*/REGISTRATION_KEY=/' "$ENV"
	$DOCKER compose up -d core
	wait_core
fi

# ---- the address and Tailscale, when given here (otherwise: in the browser) ----
if [ -n "$ADDRESS" ]; then
	step "Secure address"
	wait_agent
	case "$ADDRESS" in
	cloudflare)
		zones=$(address POST /v1/address/cloudflare/zones "{\"token\":\"$CF_TOKEN\"}")
		zid=$(printf '%s' "$zones" | tr '{' '\n' | sed -n "s/.*\"id\":\"\([^\"]*\)\",\"name\":\"$CF_DOMAIN\".*/\1/p" | head -n1)
		[ -n "$zid" ] || die "Cloudflare: $(printf '%s' "$zones" | field error) (this token can't change $CF_DOMAIN)"
		out=$(address PUT /v1/address "{\"mode\":\"cloudflare\",\"token\":\"$CF_TOKEN\",\"zone_id\":\"$zid\",\"sub\":\"$CF_NAME\"}") ;;
	*) out=$(address PUT /v1/address "{\"mode\":\"$ADDRESS\"}") ;;
	esac
	err=$(printf '%s' "$out" | field error)
	[ -z "$err" ] || die "the address: $err"
	if [ "$ADDRESS" = plain ]; then
		say "This network's addresses: Pocket Core at http://$HOST:$PORT."
	else
		printf 'Getting the name and its certificate (a minute or two)'
		n=0
		while [ $n -lt 100 ]; do
			sleep 5
			n=$((n + 1))
			printf '.'
			out=$(address GET /v1/address 2>/dev/null || true)
			NAME=$(printf '%s' "$out" | field domain)
			[ -z "$NAME" ] || break
			case "$(printf '%s' "$out" | sed -n 's/.*"status":{"state":"\([a-z-]*\)".*/\1/p')" in
			failed | limited | signin | offline) say; say "Not yet: $(printf '%s' "$out" | sed -n 's/.*"status":{[^}]*"message":"\([^"]*\)".*/\1/p') It keeps trying; Settings → System in Core shows how far it got."; break ;;
			esac
		done
		[ -z "$NAME" ] || { say; CORE_URL="https://core.$NAME"; say "Pocket is at $CORE_URL"; }
	fi
fi
if [ -n "$TS_KEY" ]; then
	step "Tailscale"
	wait_agent
	out=$(address POST /v1/tailscale/up "{\"authkey\":\"$TS_KEY\"}")
	err=$(printf '%s' "$out" | field error)
	[ -z "$err" ] || say "Tailscale didn't connect: $err"
	[ -n "$err" ] || say "On your tailnet as $(printf '%s' "$out" | field name)."
fi

step "Pocket Core $VERSION is running"
if [ -n "$INSTALLED" ]; then
	say "Open $CORE_URL and create your account with $ADMIN_EMAIL: it becomes the admin of Core and of"
	say "the apps installed with it ($(printf '%s' "$INSTALLED" | sed 's/^ //; s/ /, /g')). Apps → Install app adds more."
elif [ "$NEW" = 1 ]; then
	say "Open $CORE_URL to create your account; the setup there picks the address and your apps."
else
	say "Open $CORE_URL"
fi
[ -z "$FAILED" ] || say "Not installed:$FAILED (try again from Core: Apps → Install app)."
[ -n "$DOMAIN" ] && say "(core.$DOMAIN must point here with ports 80/443 open before you can sign in: Core expects HTTPS.)"
[ "$DOCKER" = docker ] || say "Tip: log out and back in once, so Docker works without sudo."
say "Files: $DIR · update: run this script again · logs: cd $DIR && docker compose logs -f"
