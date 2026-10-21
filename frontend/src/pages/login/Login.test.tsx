import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '../../auth/AuthProvider'
import { Login } from './Login'

// The OIDC provider is mocked so oidc-mode tests don't construct a real
// UserManager; local/none modes don't touch it at all.
const oidcMock = {
  user: null as null | { expired?: boolean; profile: Record<string, unknown> },
  isLoading: false,
  isAuthenticated: false,
  signinRedirect: vi.fn(),
  signoutRedirect: vi.fn(),
}
vi.mock('react-oidc-context', () => ({
  useAuth: () => oidcMock,
  AuthProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))

function seedConfig(cfg: Record<string, unknown>) {
  window.__AINSEL_CONFIG__ = cfg as never
}

function renderLogin() {
  return render(
    <MemoryRouter initialEntries={['/login']}>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/dashboard" element={<div>dashboard reached</div>} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  )
}

describe('Login', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('renders the OIDC sign-in button in oidc mode', () => {
    seedConfig({
      oidcIssuer: 'https://oidc.example.com',
      oidcClientId: 'client',
      oidcProjectId: 'project',
    })
    renderLogin()
    expect(screen.getByRole('button').textContent).toContain('Continue to login')
  })

  it('renders a username/password form in local mode and submits', async () => {
    seedConfig({ authMode: 'local' })
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          token: 'tok',
          expiresAt: '2030-01-01T00:00:00Z',
          user: { sub: 'local:admin', username: 'admin', isAdmin: true },
        }),
        { status: 200 },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    // jsdom's location.replace is not spy-able; swap the whole object.
    const replaceMock = vi.fn()
    Object.defineProperty(window, 'location', {
      value: { ...window.location, replace: replaceMock },
      writable: true,
      configurable: true,
    })

    renderLogin()
    await userEvent.type(screen.getByLabelText('Username'), 'admin')
    await userEvent.type(screen.getByLabelText('Password'), 'secret123')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toContain('/auth/login')
    expect(JSON.parse(init.body)).toEqual({ username: 'admin', password: 'secret123' })
    localStorage.clear()
  })

  it('shows the server error when local login fails', async () => {
    seedConfig({ authMode: 'local' })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ message: 'invalid credentials' }), { status: 401 }),
      ),
    )

    renderLogin()
    await userEvent.type(screen.getByLabelText('Username'), 'admin')
    await userEvent.type(screen.getByLabelText('Password'), 'wrong')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid credentials')
    localStorage.clear()
  })

  it('redirects straight to the dashboard in none mode', () => {
    seedConfig({
      oidcIssuer: '',
      oidcClientId: '',
      oidcProjectId: '',
    })
    renderLogin()
    expect(screen.getByText('dashboard reached')).toBeTruthy()
  })

  it('renders the chooser (SSO button and local form) in oidc+local mode', async () => {
    seedConfig({
      authMode: 'oidc+local',
      oidcIssuer: 'https://oidc.example.com',
      oidcClientId: 'client',
    })
    renderLogin()
    // Both paths immediately visible on one page (issue #114).
    expect(screen.getByRole('button', { name: 'Continue with single sign-on' })).toBeTruthy()
    expect(screen.getByLabelText('Username')).toBeTruthy()
    expect(screen.getByLabelText('Password')).toBeTruthy()
  })

  it('chooser SSO button triggers the OIDC redirect in oidc+local mode', async () => {
    seedConfig({
      authMode: 'oidc+local',
      oidcIssuer: 'https://oidc.example.com',
      oidcClientId: 'client',
    })
    renderLogin()
    await userEvent.click(screen.getByRole('button', { name: 'Continue with single sign-on' }))
    expect(oidcMock.signinRedirect).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('ainsel.activeMethod')).toBe('oidc')
    localStorage.clear()
  })

  it('chooser form logs in locally in oidc+local mode', async () => {
    seedConfig({
      authMode: 'oidc+local',
      oidcIssuer: 'https://oidc.example.com',
      oidcClientId: 'client',
    })
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          token: 'tok',
          expiresAt: '2030-01-01T00:00:00Z',
          user: { sub: 'local:admin', username: 'admin', isAdmin: true },
        }),
        { status: 200 },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    const replaceMock = vi.fn()
    Object.defineProperty(window, 'location', {
      value: { ...window.location, replace: replaceMock },
      writable: true,
      configurable: true,
    })
    renderLogin()
    await userEvent.type(screen.getByLabelText('Username'), 'admin')
    await userEvent.type(screen.getByLabelText('Password'), 'secret123')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0][0])).toContain('/auth/login')
    expect(localStorage.getItem('ainsel.activeMethod')).toBe('local')
    localStorage.clear()
  })

  it('renders neither form nor chooser button in pure oidc mode (regression)', () => {
    seedConfig({
      oidcIssuer: 'https://oidc.example.com',
      oidcClientId: 'client',
    })
    renderLogin()
    expect(screen.queryByLabelText('Username')).toBeNull()
    expect(screen.getByRole('button', { name: 'Continue to login' })).toBeTruthy()
  })
})
