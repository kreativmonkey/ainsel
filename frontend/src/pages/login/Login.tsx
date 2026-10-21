import { useEffect, useState, type FormEvent } from 'react'
import { Navigate, useSearchParams } from 'react-router-dom'
import { useAuth } from '../../auth/AuthProvider'
import { Button } from '../../primitives/Button'
import { Field } from '../../primitives/Field'
import { Input } from '../../primitives/Input'

// Login renders per auth mode:
//   - local:      username/password form against /api/v1/auth/login,
//   - oidc+local: chooser — SSO button and username/password form side by
//                 side on one page (issue #114), no auto-redirect to the IdP,
//   - oidc:       redirect button to the external IdP (legacy flow),
//   - none:       straight through to the dashboard.
export function Login() {
  const auth = useAuth()
  const [searchParams] = useSearchParams()
  const paramError = searchParams.get('error')

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // Already authenticated — bounce straight in.
  useEffect(() => {
    if (auth.ready && auth.token) {
      window.location.replace(`${import.meta.env.BASE_URL}dashboard`)
    }
  }, [auth.ready, auth.token])

  if (auth.mode === 'none') {
    return <Navigate to="/dashboard" replace />
  }

  const submitLocal = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await auth.login(username, password)
      window.location.replace(`${import.meta.env.BASE_URL}dashboard`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Sign-in failed')
      setBusy(false)
    }
  }

  const displayError = error ?? paramError

  const ssoButton = (label: string) => (
    <Button
      type="button"
      variant="primary"
      onClick={() => auth.signinRedirect()}
      style={{ marginTop: 24 }}
    >
      {label}
    </Button>
  )

  const localForm = (
    <form onSubmit={submitLocal} style={{ marginTop: 24, display: 'grid', gap: 16 }}>
      <Field label="Username" htmlFor="login-username">
        <Input
          id="login-username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoFocus
          required
        />
      </Field>
      <Field label="Password" htmlFor="login-password">
        <Input
          id="login-password"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
      </Field>
      <Button type="submit" variant="primary" disabled={busy}>
        {busy ? 'Signing in…' : 'Sign in'}
      </Button>
    </form>
  )

  const errorLine = displayError && (
    <p style={{ color: 'var(--signal, #b00)', marginTop: 16 }} role="alert">
      Sign-in failed: {displayError}
    </p>
  )

  return (
    <div style={{ padding: 32, maxWidth: 480, margin: '0 auto' }}>
      <h1>AInsel</h1>
      {auth.mode === 'oidc+local' ? (
        <>
          <p>Sign in with your account or via single sign-on.</p>
          {errorLine}
          {ssoButton('Continue with single sign-on')}
          <div
            role="separator"
            aria-label="or"
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 12,
              margin: '24px 0 0',
              color: 'var(--ink)',
              opacity: 0.65,
            }}
          >
            <span style={{ flex: 1, borderTop: '1px solid currentColor' }} />
            or
            <span style={{ flex: 1, borderTop: '1px solid currentColor' }} />
          </div>
          {localForm}
        </>
      ) : auth.mode === 'local' ? (
        <>
          <p>Sign in with your local AInsel account.</p>
          {errorLine}
          {localForm}
        </>
      ) : (
        <>
          <p>Sign in with your AInsel account.</p>
          {errorLine}
          {ssoButton('Continue to login')}
        </>
      )}
    </div>
  )
}
