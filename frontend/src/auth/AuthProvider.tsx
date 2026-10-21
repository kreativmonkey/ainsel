import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { AuthProvider as OidcProvider, useAuth as useOidcAuth } from 'react-oidc-context'
import { WebStorageStateStore } from 'oidc-client-ts'
import { setAuthToken, setUnauthorizedHandler } from '../api/client'
import { login as apiLogin } from '../api/auth'
import { runtimeConfig, type AuthMode, type ResolvedConfig } from '../runtime-config'

// AuthUser is the identity shape exposed by useAuth() in every mode.
export type AuthUser = {
  sub: string
  username: string
  email?: string
  isAdmin?: boolean
}

// AuthState is the unified auth surface. Components should only use this —
// never react-oidc-context directly — so all modes (oidc/local/none) behave
// identically from their point of view.
export type AuthState = {
  mode: AuthMode
  ready: boolean
  token: string | null
  user: AuthUser | null
  /** Informational only — admin decisions must come from the users API. */
  isAdmin: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => void
  signinRedirect: () => void
  signoutRedirect: () => void
}

const AuthCtx = createContext<AuthState | null>(null)

// localStorage keys for the local-mode session.
const TOKEN_KEY = 'ainsel.local.token'
const USER_KEY = 'ainsel.local.user'
const EXPIRES_KEY = 'ainsel.local.expiresAt'

// Marker for which method the user last authenticated through in
// 'oidc+local' mode (issue #114). Exactly one method is "active" per
// browser so the API client always carries exactly one token; the marker is
// a preference, not truth — an expired or absent session on the marked side
// falls back to the other.
const METHOD_KEY = 'ainsel.activeMethod'
type AuthMethod = 'local' | 'oidc'

function clearStoredSession() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
  localStorage.removeItem(EXPIRES_KEY)
}

// loadStoredSession restores a local session unless it is expired. Expired
// sessions are cleared eagerly so RequireAuth routes to /login immediately
// instead of triggering one round of 401s.
function loadStoredSession(): { token: string | null; user: AuthUser | null } {
  try {
    const token = localStorage.getItem(TOKEN_KEY)
    const expiresAt = localStorage.getItem(EXPIRES_KEY)
    if (!token || !expiresAt || new Date(expiresAt).getTime() <= Date.now()) {
      clearStoredSession()
      return { token: null, user: null }
    }
    const user = JSON.parse(localStorage.getItem(USER_KEY) ?? 'null') as AuthUser | null
    return { token, user }
  } catch {
    clearStoredSession()
    return { token: null, user: null }
  }
}

// useLocalSession owns the local-session storage shared by the 'local' mode
// provider and the 'oidc+local' composite provider. The token is mirrored
// into the API client during render (not in an effect) so the first fetch
// wave after login already carries it — same reasoning as the OIDC
// TokenSync in OidcBridge.
function useLocalSession() {
  const [session, setSession] = useState(loadStoredSession)
  setAuthToken(session.token)

  const loginLocal = useCallback(async (username: string, password: string) => {
    const res = await apiLogin(username, password)
    localStorage.setItem(TOKEN_KEY, res.token)
    localStorage.setItem(USER_KEY, JSON.stringify(res.user))
    localStorage.setItem(EXPIRES_KEY, res.expiresAt)
    setSession({ token: res.token, user: res.user })
  }, [])

  const logoutLocal = useCallback(() => {
    clearStoredSession()
    setSession({ token: null, user: null })
  }, [])

  return { session, setSession, loginLocal, logoutLocal }
}

function LocalAuthProvider({ children }: { children: ReactNode }) {
  const { session, loginLocal, logoutLocal } = useLocalSession()
  const login = loginLocal
  const logout = logoutLocal

  const state: AuthState = useMemo(
    () => ({
      mode: 'local',
      ready: true,
      token: session.token,
      user: session.user,
      isAdmin: Boolean(session.user?.isAdmin),
      login,
      logout,
      signinRedirect: () => {
        window.location.href = `${import.meta.env.BASE_URL}login`
      },
      signoutRedirect: logout,
    }),
    [session, login, logout],
  )

  // 401 recovery: drop the stale session and let RequireAuth route to
  // /login (local mode uses a client-side <Navigate>). No page reload, so
  // no loop is possible.
  setUnauthorizedHandler(() => logout())

  return <AuthCtx.Provider value={state}>{children}</AuthCtx.Provider>
}

function NoneAuthProvider({ children }: { children: ReactNode }) {
  setAuthToken(null)
  // No session to recover in 'none' mode; clear any previously registered
  // handler so stray 401s only surface as errors in the UI.
  setUnauthorizedHandler(null)
  const state: AuthState = useMemo(
    () => ({
      mode: 'none',
      ready: true,
      token: null,
      user: null,
      isAdmin: false,
      login: async () => undefined,
      logout: () => undefined,
      signinRedirect: () => undefined,
      signoutRedirect: () => undefined,
    }),
    [],
  )
  return <AuthCtx.Provider value={state}>{children}</AuthCtx.Provider>
}

function oidcProviderConfig(cfg: ResolvedConfig) {
  return {
    authority: cfg.oidcIssuer,
    client_id: cfg.oidcClientId,
    redirect_uri: `${window.location.origin}${import.meta.env.BASE_URL.replace(/\/$/, '')}/auth/callback`,
    post_logout_redirect_uri: `${window.location.origin}${import.meta.env.BASE_URL}`,
    response_type: 'code',
    scope: cfg.oidcScopes || (cfg.oidcProjectId
      ? `openid profile email urn:zitadel:iam:org:project:id:${cfg.oidcProjectId}:aud`
      : 'openid profile email offline_access'),
    loadUserInfo: true,
    userStore: new WebStorageStateStore({ store: window.localStorage }),
    automaticSilentRenew: true,
    // After signinRedirectCallback, clear the code from the URL so it can't
    // be reused and isn't visible in history.
    onSigninCallback: () => {
      window.history.replaceState({}, document.title, window.location.pathname)
    },
  }
}

// OidcBridge adapts react-oidc-context state to the unified AuthState.
function OidcBridge({ children }: { children: ReactNode }) {
  const oidc = useOidcAuth()

  // An expired stored user still exposes its dead access token — and
  // react-oidc-context keeps the user around even after silent-renew
  // failures (it only flips isAuthenticated). Mirroring the token
  // unconditionally made RequireAuth's "no token → redirect to IdP"
  // guard unreachable, so a stale session rendered the app and every API
  // call 401'd. Treat expired users as unauthenticated: RequireAuth then
  // bounces to the IdP on the next render instead of looping.
  const accessToken = oidc.user && !oidc.user.expired ? oidc.user.access_token : null

  // Mirror the OIDC access token into the API client during render — not in
  // a useEffect — so that any child component's data fetch sees the token on
  // the very first render after auth completes.
  setAuthToken(accessToken)

  // 401 recovery for oidc mode. If the IdP issued a refresh token,
  // signinSilent renews via the refresh-token grant (no iframe). Without
  // one, silent renewal would have to run through a hidden iframe against
  // the IdP — which cannot work in this deployment (the IdP session cookie
  // is SameSite=Lax and the app and IdP live on separate sites, e.g.
  // app.example.com vs auth.example.com), so we go straight to
  // an interactive signinRedirect instead of waiting for the iframe to
  // time out.
  setUnauthorizedHandler(() => {
    if (oidc.activeNavigator || oidc.isLoading) return // recovery already in flight
    void (async () => {
      if (oidc.user?.refresh_token) {
        try {
          await oidc.signinSilent()
          return // token renewed; the mirror above picks it up on re-render
        } catch {
          // fall through to an interactive login
        }
      }
      void oidc.signinRedirect().catch(() => {
        // The IdP is unreachable — land on /login so the user gets the
        // "Continue to login" button as a manual retry instead of a blank page.
        window.location.href = `${import.meta.env.BASE_URL}login`
      })
    })()
  })

  const state: AuthState = useMemo(
    () => ({
      mode: 'oidc',
      ready: !oidc.isLoading,
      token: accessToken,
      user: oidc.user?.profile
        ? {
            sub: String(oidc.user.profile.sub ?? ''),
            username: (oidc.user.profile.preferred_username as string | undefined) ?? '',
            email: (oidc.user.profile.email as string | undefined) ?? '',
          }
        : null,
      isAdmin: false, // resolved via the users API where it matters
      login: async () => {
        throw new Error('local login is not available in oidc mode')
      },
      logout: () => {
        void oidc.signoutRedirect()
      },
      signinRedirect: () => {
        void oidc.signinRedirect()
      },
      signoutRedirect: () => {
        void oidc.signoutRedirect()
      },
    }),
    [oidc, accessToken],
  )

  return <AuthCtx.Provider value={state}>{children}</AuthCtx.Provider>
}

// OidcLocalBridge adapts the OIDC state plus the local-session state to the
// unified AuthState for 'oidc+local' coexistence mode (issue #114). It runs
// inside the react-oidc-context provider, so the redirect/callback/silent-
// renew machinery is exactly the one 'oidc' mode uses — nothing forked.
//
// Exactly one method is active per browser, tracked by the ainsel.activeMethod
// marker set at login time. The marker is a preference: if the marked side
// has no live session, the other side is used when it has one. This keeps
// setAuthToken fed with exactly one token — two live identities in one
// browser would produce confusing /auth/me results and mixed-origin calls.
function OidcLocalBridge({ children }: { children: ReactNode }) {
  const oidc = useOidcAuth()
  const { session, loginLocal, logoutLocal } = useLocalSession()

  // Same expired-user rule as OidcBridge: an expired stored user still
  // exposes its dead access token and must count as unauthenticated.
  const oidcToken = oidc.user && !oidc.user.expired ? oidc.user.access_token : null
  const oidcUser: AuthUser | null = oidc.user?.profile
    ? {
        sub: String(oidc.user.profile.sub ?? ''),
        username: (oidc.user.profile.preferred_username as string | undefined) ?? '',
        email: (oidc.user.profile.email as string | undefined) ?? '',
      }
    : null

  const marker = localStorage.getItem(METHOD_KEY)
  const localActive =
    marker !== 'oidc' ? Boolean(session.token) : Boolean(session.token) && !oidcToken
  const activeMethod: AuthMethod = localActive ? 'local' : 'oidc'

  const token = localActive ? session.token : oidcToken
  const user = localActive ? session.user : oidcUser

  // Mirror the active token during render so children's first fetch wave
  // carries it (see useLocalSession / OidcBridge for the rationale).
  setAuthToken(token)

  // Local session cleared means "no local session is active" — drop the
  // marker so the OIDC side can take over instead of a stale preference.
  const clearLocal = useCallback(() => {
    logoutLocal()
    localStorage.removeItem(METHOD_KEY)
  }, [logoutLocal])

  const login = useCallback(
    async (username: string, password: string) => {
      await loginLocal(username, password)
      localStorage.setItem(METHOD_KEY, 'local')
    },
    [loginLocal],
  )

  const logout = useCallback(() => {
    // Clear both namespaces: a "logged out" user must not silently reappear
    // from the other store.
    clearLocal()
    // Only drive the IdP signout when an OIDC session exists; without one
    // signoutRedirect would navigate to the IdP for a user who never used it.
    if (oidc.user) void oidc.signoutRedirect()
  }, [clearLocal, oidc])

  const signinRedirect = useCallback(() => {
    localStorage.setItem(METHOD_KEY, 'oidc')
    void oidc.signinRedirect()
  }, [oidc])

  // 401 recovery follows the source of the token that was attached: a 401
  // on a local token must never bounce the user to the external IdP, and a
  // 401 on an OIDC token must not clear a healthy local session.
  setUnauthorizedHandler(() => {
    if (activeMethod === 'local') {
      // RequireAuth routes to /login afterwards (client-side <Navigate>).
      // No page reload, so no loop is possible.
      clearLocal()
      return
    }
    if (oidc.activeNavigator || oidc.isLoading) return // recovery already in flight
    void (async () => {
      if (oidc.user?.refresh_token) {
        try {
          await oidc.signinSilent()
          return // token renewed; the mirror above picks it up on re-render
        } catch {
          // fall through to an interactive login
        }
      }
      void oidc.signinRedirect().catch(() => {
        // The IdP is unreachable — land on /login so the user gets the
        // chooser page as a manual retry instead of a blank page.
        window.location.href = `${import.meta.env.BASE_URL}login`
      })
    })()
  })

  const state: AuthState = useMemo(
    () => ({
      mode: 'oidc+local',
      ready: !oidc.isLoading,
      token,
      user,
      isAdmin: Boolean(session.user?.isAdmin) && activeMethod === 'local',
      login,
      logout,
      signinRedirect,
      signoutRedirect: logout,
    }),
    [oidc, token, user, session, activeMethod, login, logout, signinRedirect],
  )

  return <AuthCtx.Provider value={state}>{children}</AuthCtx.Provider>
}

export function AuthProvider({ children }: { children: ReactNode }) {
  // Read config at render time, not module-load time, so tests can seed
  // window.__AINSEL_CONFIG__ before mounting the provider.
  const cfg = useMemo(() => runtimeConfig(), [])

  if (cfg.authMode === 'oidc') {
    return (
      <OidcProvider {...oidcProviderConfig(cfg)}>
        <OidcBridge>{children}</OidcBridge>
      </OidcProvider>
    )
  }
  if (cfg.authMode === 'oidc+local') {
    return (
      <OidcProvider {...oidcProviderConfig(cfg)}>
        <OidcLocalBridge>{children}</OidcLocalBridge>
      </OidcProvider>
    )
  }
  if (cfg.authMode === 'local') {
    return <LocalAuthProvider>{children}</LocalAuthProvider>
  }
  return <NoneAuthProvider>{children}</NoneAuthProvider>
}

// useAuth returns the unified auth state for the active mode.
// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthState {
  const ctx = useContext(AuthCtx)
  if (!ctx) {
    throw new Error('useAuth must be used within <AuthProvider>')
  }
  return ctx
}
