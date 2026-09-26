#!/bin/sh
# CI client for the ku-deploy forced command on alpha (HomeLab ops/ku/deploy.sh).
#
#   scripts/alpha-deploy.sh stage <sha> < image.tar
#   scripts/alpha-deploy.sh roll <sha>
#
# ALPHA_DEPLOY_SSH_KEY holds the private key; stage streams stdin to alpha.
# 10.11.99.1 is alpha's Incus bridge address, reachable from runner jobs.
set -eu

: "${ALPHA_DEPLOY_SSH_KEY:?ALPHA_DEPLOY_SSH_KEY is required}"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
umask 077
printf '%s\n' "$ALPHA_DEPLOY_SSH_KEY" >"$dir/key"
echo '10.11.99.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHPYd4ULykO9apRGmeT24afDlYXMorHc/JzrJWM/iHpo' >"$dir/known_hosts"

ssh -i "$dir/key" -o IdentitiesOnly=yes -o BatchMode=yes \
	-o StrictHostKeyChecking=yes -o UserKnownHostsFile="$dir/known_hosts" \
	-o ServerAliveInterval=30 \
	ku-deploy@10.11.99.1 "$1" "$2"
