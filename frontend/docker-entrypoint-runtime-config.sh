#!/bin/sh
# Write /usr/share/nginx/html/runtime-config.js from env so the frontend
# discovers its auth mode + OIDC settings at startup. One image, deploy
# anywhere.
#
# AUTH_MODE controls what the SPA does at load time:
#   oidc       - OIDC redirect flow (requires OIDC_ISSUER/CLIENT_ID)
#   local      - hub username/password login
#   oidc+local - coexistence: login page offers SSO and local login
#                (requires OIDC_ISSUER/CLIENT_ID)
#   none       - no auth (port-forward or edge-proxy installs)
#
# When AUTH_MODE is empty, behavior is backward compatible: config is only
# written when issuer/client ID are set. Partial OIDC settings fail startup.
set -eu

ROOT=/usr/share/nginx/html
OUT="${ROOT}/runtime-config.js"

AUTH_MODE="${AUTH_MODE:-}"
OIDC_ISSUER="${OIDC_ISSUER:-}"
OIDC_CLIENT_ID="${OIDC_CLIENT_ID:-}"
OIDC_PROJECT_ID="${OIDC_PROJECT_ID:-}"
OIDC_SCOPES="${OIDC_SCOPES:-}"
FORGEJO_API_BASE="${FORGEJO_API_BASE:-}"
FORGEJO_REPO="${FORGEJO_REPO:-}"

write_config() {
  auth_mode="$1"
  cat > "$OUT" <<EOF
window.__AINSEL_CONFIG__ = {
  authMode: "${auth_mode}",
  oidcIssuer: "${OIDC_ISSUER}",
  oidcClientId: "${OIDC_CLIENT_ID}",
  oidcProjectId: "${OIDC_PROJECT_ID}",
  oidcScopes: "${OIDC_SCOPES}",
  forgejoApiBase: "${FORGEJO_API_BASE}",
  forgejoRepo: "${FORGEJO_REPO}",
};
EOF
}

case "$AUTH_MODE" in
  local|none)
    write_config "$AUTH_MODE"
    echo "ainsel-hub-frontend: runtime-config.js written (authMode=${AUTH_MODE})"
    exit 0
    ;;
  oidc|oidc+local)
    if [ -z "$OIDC_ISSUER" ] || [ -z "$OIDC_CLIENT_ID" ]; then
      echo "ainsel-hub-frontend: ERROR: AUTH_MODE=$AUTH_MODE requires OIDC_ISSUER and OIDC_CLIENT_ID" >&2
      exit 1
    fi
    write_config "$AUTH_MODE"
    echo "ainsel-hub-frontend: runtime-config.js written (authMode=${AUTH_MODE}, issuer=${OIDC_ISSUER})"
    exit 0
    ;;
  "")
    if [ -z "$OIDC_ISSUER$OIDC_CLIENT_ID$OIDC_PROJECT_ID" ]; then
      exit 0
    fi
    if [ -z "$OIDC_ISSUER" ] || [ -z "$OIDC_CLIENT_ID" ]; then
      echo "ainsel-hub-frontend: ERROR: incomplete OIDC settings; issuer and client ID are required" >&2
      exit 1
    fi
    write_config "oidc"
    echo "ainsel-hub-frontend: runtime-config.js written (authMode=oidc, issuer=${OIDC_ISSUER})"
    exit 0
    ;;
  *)
    echo "ainsel-hub-frontend: ERROR: invalid AUTH_MODE '${AUTH_MODE}' (want oidc, local, oidc+local, or none)" >&2
    exit 1
    ;;
esac
