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

describe('runtimeConfig oidc+local', () => {
  it('accepts explicit oidc+local with a complete OIDC config', () => {
    window.__AINSEL_CONFIG__ = {
      authMode: 'oidc+local',
      oidcIssuer: 'https://idp.test/app/',
      oidcClientId: 'client',
    }
    expect(runtimeConfig().authMode).toBe('oidc+local')
  })

  it('rejects oidc+local without issuer/client ID (the SSO half must work)', () => {
    window.__AINSEL_CONFIG__ = { authMode: 'oidc+local', oidcIssuer: '', oidcClientId: '' }
    expect(runtimeConfig).toThrow('oidc+local')
  })

  it('is never derived from an empty config', () => {
    window.__AINSEL_CONFIG__ = { oidcIssuer: '', oidcClientId: '', oidcProjectId: '' }
    expect(runtimeConfig().authMode).toBe('none')
  })
})
