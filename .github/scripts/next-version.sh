#!/bin/sh
# Prints the next release tag for HEAD, or nothing when no release is due.
# A release is due when a feat, fix or perf commit landed since the last v*
# tag. Versions stay 0.x: feat and breaking changes (! or BREAKING CHANGE:)
# bump the minor, everything else the patch. The first release is v0.1.0.
set -eu

last=$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || true)
range=HEAD
[ -n "$last" ] && range="$last..HEAD"

subjects=$(git log --format=%s "$range")
bodies=$(git log --format=%b "$range")

printf '%s\n' "$subjects" | grep -Eq '^(feat|fix|perf)(\([^)]*\))?!?:' || exit 0

if [ -z "$last" ]; then
	echo v0.1.0
	exit 0
fi

minor=false
printf '%s\n' "$subjects" | grep -Eq '^feat(\([^)]*\))?!?:|^[a-z]+(\([^)]*\))?!:' && minor=true
printf '%s\n' "$bodies" | grep -q '^BREAKING CHANGE:' && minor=true

ver=${last#v}
maj=${ver%%.*}
rest=${ver#*.}
min=${rest%%.*}
pat=${rest#*.}
if $minor; then
	echo "v$maj.$((min + 1)).0"
else
	echo "v$maj.$min.$((pat + 1))"
fi
