#!/usr/bin/env bash
#
# pull-image.sh <image>: make a Docker Hub image present, through Google's Docker Hub mirror when
# Docker Hub refuses.
#
# GitHub's runners share their addresses, and Docker Hub's anonymous pull limit counts per address,
# so a pull can be refused for hours by traffic that is not ours. Measured 2026-10-09: five reruns
# over 40 minutes, every one `toomanyrequests: You have reached your unauthenticated pull rate
# limit`, with nothing about the commit under test changed. mirror.gcr.io is Google's pull-through
# cache of Docker Hub and serves the same manifests; the image is re-tagged under its Docker Hub name
# so the suites that require it find it exactly where they look.
#
# Docker's own error is printed, never discarded: "not pullable" alone cannot tell a rate limit
# from a typo in the tag.
set -uo pipefail

img="${1:?usage: pull-image.sh <image>}"
docker image inspect "$img" >/dev/null 2>&1 && exit 0

if out="$(docker pull -q "$img" 2>&1)"; then
	exit 0
fi
printf 'docker pull %s refused: %s\n' "$img" "$out" >&2

# Only a Docker Hub name has a Docker Hub mirror: no slash (an official image), or a first segment
# that names no registry host (gcr.io/…, localhost:5000/… are not Docker Hub).
case "$img" in
*/*)
	case "${img%%/*}" in
	*.* | *:* | localhost) exit 1 ;;
	esac
	mirror="mirror.gcr.io/$img"
	;;
*) mirror="mirror.gcr.io/library/$img" ;;
esac
if out="$(docker pull -q "$mirror" 2>&1)" && docker tag "$mirror" "$img"; then
	printf 'pulled %s from %s instead\n' "$img" "$mirror" >&2
	exit 0
fi
printf 'docker pull %s refused too: %s\n' "$mirror" "$out" >&2
exit 1
