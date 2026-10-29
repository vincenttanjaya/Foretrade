#!/bin/sh
# Writes .env for docker compose: copies .env.example when .env is missing,
# then fills each required value that is still empty. Safe to rerun: it never
# changes a value that is set.
set -eu
cd "$(dirname "$0")/.."
umask 077 # .env holds secrets: readable by its owner only

[ -f .env ] || cp .env.example .env

# hex N: N random bytes as lower-case hex.
hex() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }

# value KEY: KEY's value in .env, or "" (the last line wins, as in compose;
# single quotes, which keep a $ literal, are not part of it).
value() { sed -n "s/^$1=//p" .env | tail -n 1 | sed "s/^'\(.*\)'\$/\1/"; }

# fill KEY VALUE: sets KEY=VALUE when KEY is missing or empty.
fill() {
	[ -n "$(value "$1")" ] && return 0
	if grep -q "^$1=" .env; then
		sed "s|^$1=.*|$1=$2|" .env > .env.tmp && mv .env.tmp .env
	else
		printf '%s=%s\n' "$1" "$2" >> .env
	fi
}

fill POSTGRES_PASSWORD "$(hex 24)"
fill SECRET_KEY "$(hex 32)"
fill ADMIN_EMAIL admin@example.com
fill ADMIN_PASSWORD "$(hex 12)"

web=$(value WEB_PORT)
admin=$(value ADMIN_PORT)
cat <<MSG
.env is ready. Start Foretrade with:

  docker compose up -d --build --wait

Web app:        http://localhost:${web:-8080}
Admin console:  http://localhost:${admin:-8081}  (this machine only)
Admin sign-in:  $(value ADMIN_EMAIL) / $(value ADMIN_PASSWORD)

Then open the admin console, Settings > AI provider, to connect a model.
MSG
