import { act, render, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { AuthProvider, useAuth } from './AuthProvider'
import { UnauthorizedError, request, setUnauthorizedHandler } from '../api/client'
import type { ResolvedConfig } from '../runtime-config'

// react-oidc-context is mocked so OidcBridge can be driven with a precise
// library state (expired user, refresh token present, navigator active…)
// without standing up a real UserManager against an IdP. The shape mirrors
// the subset of the library's useAuth() state that OidcBridge reads.
type MockUser = {
  access_token: string
  expired: boolean | undefined
  refresh_token?: string
  profile: { sub?: unknown; preferred_username?: string; email?: string }
}

let providerProps: Record<string, unknown>

let mockOidc: {
  user: MockUser | null
  isLoading: boolean
  activeNavigator?: string
  signinSilent: ReturnType<typeof vi.fn>
  signinRedirect: ReturnType<typeof vi.fn>
  signoutRedirect: ReturnType<typeof vi.fn>
}

vi.mock('react-oidc-context', () => ({
  AuthProvider: ({ children, ...props }: { children: ReactNode }) => { providerProps = props; return <>{children}</> },
  useAuth: () => mockOidc,
}))

const OIDC_CFG: ResolvedConfig = {
  authMode: 'oidc',
  oidcIssuer: 'https://oidc.example.com',
  oidcClientId: 'test-client',
  oidcProjectId: 'test-project',
}

function mockUser(overrides: { expired?: boolean; refresh_token?: string } = {}): MockUser {
  return {
    access_token: 'tok-123',
    expired: overrides.expired ?? false,
    refresh_token: overrides.refresh_token,
    profile: { sub: 'user-1', preferred_username: 'alice', email: 'alice@example.com' },
  }
}

function Wrapper({ children }: { children: ReactNode }) {
  return <AuthProvider>{children}</AuthProvider>
}

function seedConfig(cfg: ResolvedConfig) {
  window.__AINSEL_CONFIG__ = cfg
}

async function triggerUnauthorized() {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })))
  await expect(request('/foo')).rejects.toBeInstanceOf(UnauthorizedError)
}

describe('AuthProvider (oidc mode)', () => {
  beforeEach(() => {
    localStorage.clear()
    setUnauthorizedHandler(null)
    mockOidc = {
      user: null,
      isLoading: false,
      signinSilent: vi.fn().mockResolvedValue(null),
      signinRedirect: vi.fn().mockResolvedValue(undefined),
      signoutRedirect: vi.fn().mockResolvedValue(undefined),
    }
  })

  it('renders children inside the OIDC context', () => {
    seedConfig(OIDC_CFG)
    const { getByTestId } = render(
      <AuthProvider>
        <div data-testid="child">hi</div>
      </AuthProvider>,
    )
    expect(getByTestId('child').textContent).toBe('hi')
  })

  it('mirrors a valid access token', () => {
    mockOidc.user = mockUser()
    seedConfig(OIDC_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('tok-123')
  })

  it('treats an expired stored user as unauthenticated', () => {
    // Regression test for the infinite refresh loop: an expired user kept
    // its dead token mirrored, so RequireAuth never redirected to the IdP
    // and every reload produced another wave of 401s.
    mockOidc.user = mockUser({ expired: true })
    seedConfig(OIDC_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBeNull()
    expect(result.current.ready).toBe(true)
  })

  it('mirrors tokens without an expiry claim (matches isAuthenticated semantics)', () => {
    mockOidc.user = mockUser({ expired: undefined })
    seedConfig(OIDC_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('tok-123')
  })

  it('on 401 renews silently when a refresh token is available', async () => {
    mockOidc.user = mockUser({ refresh_token: 'rt-1' })
    seedConfig(OIDC_CFG)
    renderHook(() => useAuth(), { wrapper: Wrapper })
    await triggerUnauthorized()
    expect(mockOidc.signinSilent).toHaveBeenCalledTimes(1)
    expect(mockOidc.signinRedirect).not.toHaveBeenCalled()
  })

  it('on 401 redirects to the IdP when there is no refresh token', async () => {
    // Without a refresh token, silent renewal would need a hidden iframe
    // against the IdP — which cannot work when app and IdP are on separate
    // sites — so recovery goes straight to an interactive redirect.
    mockOidc.user = mockUser()
    seedConfig(OIDC_CFG)
    renderHook(() => useAuth(), { wrapper: Wrapper })
    await triggerUnauthorized()
    expect(mockOidc.signinSilent).not.toHaveBeenCalled()
    expect(mockOidc.signinRedirect).toHaveBeenCalledTimes(1)
  })

  it('on 401 falls back to signinRedirect when the silent renew fails', async () => {
    mockOidc.user = mockUser({ refresh_token: 'rt-1' })
    mockOidc.signinSilent = vi.fn().mockRejectedValue(new Error('renew failed'))
    seedConfig(OIDC_CFG)
    renderHook(() => useAuth(), { wrapper: Wrapper })
    await triggerUnauthorized()
    expect(mockOidc.signinSilent).toHaveBeenCalledTimes(1)
    expect(mockOidc.signinRedirect).toHaveBeenCalledTimes(1)
  })

  it('on 401 does nothing while a navigator is already active', async () => {
    mockOidc.user = mockUser()
    mockOidc.activeNavigator = 'signinRedirect'
    seedConfig(OIDC_CFG)
    renderHook(() => useAuth(), { wrapper: Wrapper })
    await triggerUnauthorized()
    expect(mockOidc.signinSilent).not.toHaveBeenCalled()
    expect(mockOidc.signinRedirect).not.toHaveBeenCalled()
  })

  it('only recovers once for a burst of 401s', async () => {
    mockOidc.user = mockUser()
    seedConfig(OIDC_CFG)
    renderHook(() => useAuth(), { wrapper: Wrapper })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })))
    await Promise.allSettled([request('/a'), request('/b'), request('/c')])
    expect(mockOidc.signinRedirect).toHaveBeenCalledTimes(1)
  })
})

describe('AuthProvider (local mode)', () => {
  const LOCAL_CFG: ResolvedConfig = {
    // The OIDC fields are ignored in local mode (runtimeConfig short-circuits
    // on authMode), but they are part of the config type.
    authMode: 'local',
    oidcIssuer: 'https://oidc.example.com',
    oidcClientId: 'test-client',
    oidcProjectId: 'test-project',
  }

  function seedLocalSession() {
    localStorage.setItem('ainsel.local.token', 'local-tok')
    localStorage.setItem('ainsel.local.user', JSON.stringify({ sub: 'u', username: 'bob' }))
    localStorage.setItem('ainsel.local.expiresAt', new Date(Date.now() + 60_000).toISOString())
  }

  beforeEach(() => {
    localStorage.clear()
    setUnauthorizedHandler(null)
  })

  it('restores an unexpired local session', () => {
    seedLocalSession()
    seedConfig(LOCAL_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('local-tok')
  })

  it('on 401 drops the session so RequireAuth routes to /login', async () => {
    seedLocalSession()
    seedConfig(LOCAL_CFG)
    const { result, rerender } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('local-tok')
    await act(async () => {
      await triggerUnauthorized()
    })
    expect(localStorage.getItem('ainsel.local.token')).toBeNull()
    rerender()
    expect(result.current.token).toBeNull()
  })
})

describe('AuthProvider (oidc+local coexistence mode)', () => {
  const COEXIST_CFG: ResolvedConfig = {
    authMode: 'oidc+local',
    oidcIssuer: 'https://oidc.example.com',
    oidcClientId: 'test-client',
  }

  function seedLocalSession(expiresAt = new Date(Date.now() + 60_000).toISOString()) {
    localStorage.setItem('ainsel.local.token', 'local-tok')
    localStorage.setItem('ainsel.local.user', JSON.stringify({ sub: 'local:bob', username: 'bob', isAdmin: false }))
    localStorage.setItem('ainsel.local.expiresAt', expiresAt)
  }

  beforeEach(() => {
    localStorage.clear()
    setUnauthorizedHandler(null)
    mockOidc = {
      user: null,
      isLoading: false,
      signinSilent: vi.fn().mockResolvedValue(null),
      signinRedirect: vi.fn().mockResolvedValue(undefined),
      signoutRedirect: vi.fn().mockResolvedValue(undefined),
    }
  })

  it('prefers the marked local session over a live OIDC user', () => {
    // The explicit local login is the act the user just performed; the
    // single-active-method marker keeps exactly one token in the client.
    seedLocalSession()
    localStorage.setItem('ainsel.activeMethod', 'local')
    mockOidc.user = mockUser()
    seedConfig(COEXIST_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('local-tok')
    expect(result.current.user?.username).toBe('bob')
    expect(result.current.mode).toBe('oidc+local')
  })

  it('falls back to the OIDC user when the marked local session is expired', () => {
    seedLocalSession(new Date(Date.now() - 1000).toISOString())
    localStorage.setItem('ainsel.activeMethod', 'local')
    mockOidc.user = mockUser()
    seedConfig(COEXIST_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('tok-123')
    expect(result.current.user?.sub).toBe('user-1')
  })

  it('uses the local session when marked oidc but no OIDC user is present', () => {
    seedLocalSession()
    localStorage.setItem('ainsel.activeMethod', 'oidc')
    seedConfig(COEXIST_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    expect(result.current.token).toBe('local-tok')
  })

  it('local login stores the session and marks the method local', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            token: 'fresh-tok',
            expiresAt: '2030-01-01T00:00:00Z',
            user: { sub: 'local:admin', username: 'admin', isAdmin: true },
          }),
          { status: 200 },
        ),
      ),
    )
    seedConfig(COEXIST_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    await act(async () => {
      await result.current.login('admin', 'secret123')
    })
    expect(result.current.token).toBe('fresh-tok')
    expect(localStorage.getItem('ainsel.activeMethod')).toBe('local')
    vi.unstubAllGlobals()
  })

  it('logout clears both namespaces and signs out the IdP when a user exists', () => {
    seedLocalSession()
    localStorage.setItem('ainsel.activeMethod', 'local')
    mockOidc.user = mockUser()
    seedConfig(COEXIST_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    act(() => result.current.logout())
    expect(localStorage.getItem('ainsel.local.token')).toBeNull()
    expect(localStorage.getItem('ainsel.activeMethod')).toBeNull()
    expect(mockOidc.signoutRedirect).toHaveBeenCalledTimes(1)
  })

  it('on 401 with the local method active, drops the local session without touching the IdP', async () => {
    seedLocalSession()
    localStorage.setItem('ainsel.activeMethod', 'local')
    mockOidc.user = mockUser()
    seedConfig(COEXIST_CFG)
    const { result, rerender } = renderHook(() => useAuth(), { wrapper: Wrapper })
    await act(async () => {
      await triggerUnauthorized()
    })
    expect(localStorage.getItem('ainsel.local.token')).toBeNull()
    expect(mockOidc.signinSilent).not.toHaveBeenCalled()
    expect(mockOidc.signinRedirect).not.toHaveBeenCalled()
    rerender()
    // The healthy OIDC session takes over (marker preference, not truth).
    expect(result.current.token).toBe('tok-123')
  })

  it('on 401 with the OIDC method active, recovers via the IdP and keeps the local session', async () => {
    seedLocalSession()
    localStorage.setItem('ainsel.activeMethod', 'oidc')
    mockOidc.user = mockUser({ refresh_token: 'rt-1' })
    seedConfig(COEXIST_CFG)
    renderHook(() => useAuth(), { wrapper: Wrapper })
    await triggerUnauthorized()
    expect(mockOidc.signinSilent).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('ainsel.local.token')).toBe('local-tok')
  })

  it('signinRedirect marks the method oidc', () => {
    seedConfig(COEXIST_CFG)
    const { result } = renderHook(() => useAuth(), { wrapper: Wrapper })
    act(() => result.current.signinRedirect())
    expect(localStorage.getItem('ainsel.activeMethod')).toBe('oidc')
    expect(mockOidc.signinRedirect).toHaveBeenCalledTimes(1)
  })
})

describe('OIDC provider configuration', () => {
  it('uses standard scopes without a vendor project ID', () => {
    seedConfig({ authMode: 'oidc', oidcIssuer: 'https://idp.test/application/o/app/', oidcClientId: 'app' })
    render(<Wrapper><span /></Wrapper>)
    expect(providerProps.scope).toBe('openid profile email offline_access')
    expect(providerProps.authority).toBe('https://idp.test/application/o/app/')
  })

  it('uses explicitly configured provider scopes', () => {
    seedConfig({ ...OIDC_CFG, oidcScopes: 'openid email custom:audience' })
    render(<Wrapper><span /></Wrapper>)
    expect(providerProps.scope).toBe('openid email custom:audience')
  })

  it('keeps legacy project scopes when explicitly supplied', () => {
    seedConfig(OIDC_CFG)
    render(<Wrapper><span /></Wrapper>)
    expect(providerProps.scope).toBe('openid profile email urn:zitadel:iam:org:project:id:test-project:aud')
  })
})
