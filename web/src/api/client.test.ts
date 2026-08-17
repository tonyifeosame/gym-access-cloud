import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, api, setUnauthenticatedHandler } from './client'
import { setCsrfToken } from './csrf'
import * as endpoints from './endpoints'
import { resetServerState, state } from '../test/server'
import { makeSession } from '../test/fixtures'

describe('api client', () => {
  beforeEach(() => {
    setCsrfToken(null)
    setUnauthenticatedHandler(null)
  })

  it('sends the session cookie and the CSRF token on unsafe methods only', async () => {
    resetServerState(makeSession())
    setCsrfToken('csrf-token-value')

    await endpoints.fetchSession()
    await endpoints.logout()

    const [get, post] = state.requests
    expect(get?.method).toBe('GET')
    expect(get?.headers.get('X-CSRF-Token')).toBeNull()

    expect(post?.method).toBe('POST')
    expect(post?.headers.get('X-CSRF-Token')).toBe('csrf-token-value')
  })

  it('never sends a site API key', async () => {
    // The provisioning secret must not exist in the browser at all. This is the
    // assertion behind the lint rule.
    resetServerState(makeSession())
    setCsrfToken('csrf-token-value')

    await endpoints.fetchSession()
    await endpoints.logout()

    for (const request of state.requests) {
      expect(request.headers.get('X-API-Key')).toBeNull()
      expect(request.headers.get('x-api-key')).toBeNull()
    }
  })

  it('captures the CSRF token from a session body', async () => {
    resetServerState(makeSession({ csrf_token: 'fresh-token' }))
    setCsrfToken(null)

    await endpoints.fetchSession()
    await endpoints.logout()

    expect(state.requests[1]?.headers.get('X-CSRF-Token')).toBe('fresh-token')
  })

  it('reports no session as null rather than an error', async () => {
    resetServerState(null)
    await expect(endpoints.fetchSession()).resolves.toBeNull()
  })

  it('surfaces the status, message and request id of a failure', async () => {
    resetServerState(null)

    const error = await api.get('/api/v1/console/company').catch((caught) => caught)

    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).status).toBe(401)
    expect((error as ApiError).isUnauthenticated).toBe(true)
    expect((error as ApiError).requestId).toBe('test-request-id')
  })

  it('routes any 401 to the unauthenticated handler', async () => {
    resetServerState(null)
    const handler = vi.fn()
    setUnauthenticatedHandler(handler)

    await api.get('/api/v1/console/company').catch(() => undefined)

    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('does not treat the boot-time session probe as a session ending', async () => {
    // Otherwise a first visit would trip the "your session ended" path.
    resetServerState(null)
    const handler = vi.fn()
    setUnauthenticatedHandler(handler)

    await endpoints.fetchSession()

    expect(handler).not.toHaveBeenCalled()
  })

  it('carries Retry-After off a rate-limited login', async () => {
    resetServerState(null)
    state.loginStatus = 429
    state.loginRetryAfter = 90

    const error = await endpoints.login('ops@example.com', 'secret').catch((caught) => caught)

    expect((error as ApiError).isRateLimited).toBe(true)
    expect((error as ApiError).retryAfterSeconds).toBe(90)
  })
})
