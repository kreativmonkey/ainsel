import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { userDisplayName, isUnsyncedUser, updateUser } from './users'

describe('userDisplayName', () => {
  it('returns username when it differs from id', () => {
    const user = { id: '123', username: 'alice', email: 'alice@example.com' }
    expect(userDisplayName(user)).toBe('alice')
  })

  it('falls back to email when username equals id', () => {
    const user = { id: '123', username: '123', email: 'alice@example.com' }
    expect(userDisplayName(user)).toBe('alice@example.com')
  })

  it('falls back to email when username is empty', () => {
    const user = { id: '123', username: '', email: 'alice@example.com' }
    expect(userDisplayName(user)).toBe('alice@example.com')
  })

  it('falls back to id when both username and email are empty', () => {
    const user = { id: '123', username: '', email: '' }
    expect(userDisplayName(user)).toBe('123')
  })
})

describe('isUnsyncedUser', () => {
  it('returns false when username is present and differs from id', () => {
    expect(isUnsyncedUser({ id: '123', username: 'alice', email: '' })).toBe(false)
  })

  it('returns false when email is present even if username is empty', () => {
    expect(isUnsyncedUser({ id: '123', username: '', email: 'a@b.com' })).toBe(false)
  })

  it('returns false when email is present even if username equals id', () => {
    expect(isUnsyncedUser({ id: '123', username: '123', email: 'a@b.com' })).toBe(false)
  })

  it('returns true when username is empty and email is empty', () => {
    expect(isUnsyncedUser({ id: '123', username: '', email: '' })).toBe(true)
  })

  it('returns true when username equals id and email is empty', () => {
    expect(isUnsyncedUser({ id: '123', username: '123', email: '' })).toBe(true)
  })
})

describe('updateUser', () => {
  const mockFetch = () => globalThis.fetch as ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('PATCHes /users/{id} with the disabled flag', async () => {
    mockFetch().mockResolvedValue(
      new Response(JSON.stringify({ id: 'u1', disabled: true }), { status: 200 }),
    )
    await updateUser('u1', { disabled: true })
    const [url, opts] = mockFetch().mock.calls[0]
    expect(url).toContain('/users/u1')
    expect(opts.method).toBe('PATCH')
    expect(JSON.parse(opts.body)).toEqual({ disabled: true })
  })

  it('PATCHes isAdmin and disabled together', async () => {
    mockFetch().mockResolvedValue(
      new Response(JSON.stringify({ id: 'u1', disabled: true, isAdmin: false }), { status: 200 }),
    )
    await updateUser('u1', { isAdmin: false, disabled: true })
    const [, opts] = mockFetch().mock.calls[0]
    expect(JSON.parse(opts.body)).toEqual({ isAdmin: false, disabled: true })
  })
})
