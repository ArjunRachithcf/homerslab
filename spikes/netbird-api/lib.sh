#!/usr/bin/env bash
# Helpers for the spike; source it:  . ./lib.sh
# api METHOD PATH [JSON]   calls the management API from the tools container (inside the sandbox),
#                          with the token in secrets/tools/auth.hdr (or none when NOAUTH=1).
#                          Raw body -> secrets/tmp/last.json; a redacted record -> out/transcript.md;
#                          prints the HTTP status.
# j FILTER                 jq -r on the last raw body.
# use_token FILE           makes the token in FILE (secrets/...) the one api sends.
SPIKE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
DC=(docker compose -p hlspike-netbird -f "$SPIKE/compose.yaml")
OUT=$SPIKE/out/transcript.md
TMP=$SPIKE/secrets/tmp
mkdir -p "$TMP" "$SPIKE/out"

# Hides every credential the API hands out: PATs (nbp_), proxy tokens (nbx_), setup keys,
# invite tokens, passwords.
redact() {
  sed -E \
    -e 's/nb[a-z]_[A-Za-z0-9]{6,}/nb?_<redacted>/g' \
    -e 's/("(key|plain_token|personal_access_token|invite_token|password|token)"[[:space:]]*:[[:space:]]*")[^"]*"/\1<redacted>"/g' \
    -e 's/(pat: )[^ "]+/\1<hash redacted>/g'
}

api() {
  local method=$1 path=$2 body=${3-} hdr=/secrets/auth.hdr resp code
  [ "${NOAUTH-}" = 1 ] && hdr=/dev/null
  if [ -n "$body" ]; then
    resp=$(printf '%s' "$body" | "${DC[@]}" exec -T tools curl -sS -m 30 -X "$method" -H "@$hdr" \
      -H 'Content-Type: application/json' --data-binary @- -w '\n%{http_code}' "http://nb.hlspike.test$path")
  else
    resp=$("${DC[@]}" exec -T tools curl -sS -m 30 -X "$method" -H "@$hdr" \
      -w '\n%{http_code}' "http://nb.hlspike.test$path" </dev/null)
  fi
  code=${resp##*$'\n'}
  printf '%s' "${resp%$'\n'*}" > "$TMP/last.json"
  {
    printf '\n`%s %s` -> **%s**%s\n' "$method" "$(printf '%s' "$path" | redact)" "$code" "${NOAUTH:+ (no token)}"
    [ -n "$body" ] && printf 'request: `%s`\n' "$(printf '%s' "$body" | redact)"
    printf '```json\n%s\n```\n' "$( (jq . "$TMP/last.json" 2>/dev/null || cat "$TMP/last.json") | redact | head -c 4000)"
  } >> "$OUT"
  echo "$code"
}

j() { jq -r "$1" "$TMP/last.json"; }

use_token() { printf 'Authorization: Token %s\n' "$(cat "$1")" > "$SPIKE/secrets/tools/auth.hdr"; }

note() { printf '\n### %s\n' "$*" >> "$OUT"; }
