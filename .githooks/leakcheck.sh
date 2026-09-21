#!/bin/sh
# leakcheck: refuse to push machine-specific paths, usernames or private emails.
# Scans every local commit not yet on any remote (files, message, author/committer email).
# Generic patterns are built in. Private ones (your OS username, work email domain, local dir names)
# live OUTSIDE the repo in ../.leakpatterns (one extended regex per line) and are never committed.
# Stateless on purpose: it does not read the hook's stdin, so it composes with other pre-push logic.
GENERIC='[A-Za-z]:[\/]+Users[\/]|/c/Users/|/Users/[A-Za-z]|/home/[a-z][a-z0-9_-]*/|[\/]Desktop[\/]|[\/]AppData[\/]'
EXTRA=''
[ -f ../.leakpatterns ] && EXTRA=$(grep -v '^[[:space:]]*\(#\|$\)' ../.leakpatterns | paste -sd'|' -)
PAT="$GENERIC${EXTRA:+|$EXTRA}"
EXCL=':(exclude).githooks/leakcheck.sh'
fail=0
for c in $(git rev-list HEAD --not --remotes); do
  short=$(echo "$c" | cut -c1-7)
  out=$(git grep -I -n -E "$PAT" "$c" -- . "$EXCL" 2>/dev/null | cut -c1-160 | head -5)
  [ -n "$out" ] && { echo "leakcheck: machine path or private identity in files of commit $short:"; echo "$out"; fail=1; }
  if git log -1 --format=%B "$c" | grep -qE "$PAT"; then echo "leakcheck: machine path or private identity in the message of commit $short"; fail=1; fi
  for e in $(git log -1 --format='%ae %ce' "$c"); do
    case "$e" in
      *@users.noreply.github.com|noreply@github.com|noreply@anthropic.com) ;;
      *) echo "leakcheck: commit $short has a non-noreply email"; fail=1 ;;
    esac
  done
done
[ "$fail" -eq 0 ] || { echo "leakcheck: push blocked. Remove the leak (never bypass with --no-verify)."; exit 1; }
