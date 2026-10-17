import { afterEach, describe, expect, it } from 'vitest'
import { runtimeConfig } from './runtime-config'

afterEach(() => { delete window.__AINSEL_CONFIG__ })

describe('runtimeConfig OIDC', () => {
  it('accepts standard issuer/client configuration without a project ID', () => {
    window.__AINSEL_CONFIG__ = { oidcIssuer: 'https://idp.test/app/', oidcClientId: 'client' }
    expect(runtimeConfig().authMode).toBe('oidc')
  })

  it('rejects partially configured OIDC instead of disabling auth', () => {
    window.__AINSEL_CONFIG__ = { oidcIssuer: 'https://idp.test/app/', oidcClientId: '' }
    expect(runtimeConfig).toThrow('oidcIssuer and oidcClientId')
  })
})
