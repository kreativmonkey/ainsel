// Default runtime config — neutral defaults so the app boots in no-auth
// mode (pnpm dev, port-forward installs). In production this file is
// overwritten by frontend/docker-entrypoint-runtime-config.sh at container
// startup from env vars (AUTH_MODE, OIDC_ISSUER, OIDC_CLIENT_ID,
// OIDC_SCOPES; OIDC_PROJECT_ID is a legacy compatibility setting).
window.__AINSEL_CONFIG__ = {
  oidcIssuer: "",
  oidcClientId: "",
  oidcProjectId: "",
  oidcScopes: "",
  forgejoApiBase: "",
  forgejoRepo: "",
};
