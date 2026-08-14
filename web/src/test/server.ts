import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'

import type { Session } from '../api/types'
import { makeSession } from './fixtures'

/**
 * A mock AccessLink API.
 *
 * Modelled on the real contracts rather than on what the client happens to
 * expect: 401 for no session, the same body from login and /me, 204 from logout.
 * A mock that agrees with the client instead of the API would pass while the
 * real thing failed.
 */

interface ServerState {
  session: Session | null
  loginStatus: number
  loginRetryAfter?: number
  requests: { method: string; url: string; headers: Headers }[]
}

export const state: ServerState = {
  session: null,
  loginStatus: 200,
  requests: [],
}

export function resetServerState(session: Session | null = null): void {
  state.session = session
  state.loginStatus = 200
  state.loginRetryAfter = undefined
  state.requests = []
}

function record(request: Request): void {
  state.requests.push({
    method: request.method,
    url: request.url,
    headers: new Headers(request.headers),
  })
}

const REQUEST_ID = 'test-request-id'

function json(body: object, status = 200) {
  return HttpResponse.json(body, {
    status,
    headers: { 'X-Request-ID': REQUEST_ID },
  })
}

export const handlers = [
  http.get('*/api/v1/auth/me', ({ request }) => {
    record(request)
    if (!state.session) {
      return json({ error: 'Authentication required' }, 401)
    }
    return json(state.session)
  }),

  http.post('*/api/v1/auth/login', async ({ request }) => {
    record(request)

    if (state.loginStatus === 429) {
      return HttpResponse.json(
        { error: 'Too many failed attempts, this account is temporarily locked' },
        {
          status: 429,
          headers: {
            'X-Request-ID': REQUEST_ID,
            'Retry-After': String(state.loginRetryAfter ?? 60),
          },
        },
      )
    }
    if (state.loginStatus !== 200) {
      return json({ error: 'Invalid email or password' }, state.loginStatus)
    }

    state.session = state.session ?? makeSession()
    return json(state.session)
  }),

  http.post('*/api/v1/auth/logout', ({ request }) => {
    record(request)
    state.session = null
    return new HttpResponse(null, { status: 204, headers: { 'X-Request-ID': REQUEST_ID } })
  }),

  http.get('*/api/v1/console/company', ({ request }) => {
    record(request)
    if (!state.session) return json({ error: 'Authentication required' }, 401)
    return json({
      ...state.session.company,
      active: true,
      created_at: '2026-01-01T00:00:00Z',
    })
  }),
]

export const server = setupServer(...handlers)
