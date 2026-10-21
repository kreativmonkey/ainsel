export type AuthMode = 'oidc' | 'local' | 'oidc+local' | 'none'

type AinselConfig = {
  authMode?: AuthMode
  oidcIssuer: string
  oidcClientId: string
  oidcProjectId?: string
  oidcScopes?: string
  forgejoApiBase?: string
  forgejoRepo?: string
}

export type ResolvedConfig = AinselConfig & { authMode: AuthMode }

declare global {
  interface Window {
    __AINSEL_CONFIG__?: AinselConfig
  }
}

// runtimeConfig resolves the runtime configuration, including the effective
// auth mode:
//   - explicit authMode from the deployment wins ('local' and 'none' need no
//     OIDC fields; 'oidc+local' needs the full OIDC config like 'oidc'),
//   - otherwise issuer and client ID means 'oidc',
//   - all-empty OIDC fields mean 'none' (port-forward / edge-proxy installs),
//   - a *partially* filled OIDC configuration is a broken deploy and throws.
//
// 'oidc+local' (issue #114) is only reachable explicitly — it is a
// deliberate operational decision, never derived from partial config.
export function runtimeConfig(): ResolvedConfig {
  const c = window.__AINSEL_CONFIG__
  if (!c) {
    throw new Error(
      "runtime-config.js missing: window.__AINSEL_CONFIG__ is not defined",
    )
  }

  if (c.authMode === 'local' || c.authMode === 'none') {
    return { ...c, authMode: c.authMode }
  }

  const { oidcIssuer, oidcClientId, oidcProjectId } = c
  const allEmpty = !oidcIssuer && !oidcClientId && !oidcProjectId
  if (c.authMode === 'oidc+local') {
    // The composite provider mounts the OIDC machinery, so the SSO half
    // needs a complete client config regardless of the local settings.
    if (!oidcIssuer || !oidcClientId) {
      throw new Error(
        "runtime-config.js incomplete: " +
          "authMode 'oidc+local' requires oidcIssuer and oidcClientId",
      )
    }
    return { ...c, authMode: 'oidc+local' }
  }
  if (c.authMode === 'oidc' || !allEmpty) {
    if (!oidcIssuer || !oidcClientId) {
      throw new Error(
        "runtime-config.js incomplete: " +
          "window.__AINSEL_CONFIG__ must have oidcIssuer and oidcClientId",
      )
    }
    return { ...c, authMode: 'oidc' }
  }

  return { ...c, authMode: 'none' }
}
