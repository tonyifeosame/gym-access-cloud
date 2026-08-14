import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { cleanup } from '@testing-library/react'

import { resetServerState, server } from './server'

/**
 * Test-environment shim: AbortSignal.
 *
 * Under jsdom + msw, Node's undici brand-checks `init.signal` against its own
 * AbortSignal, and NO signal constructible inside this environment passes --
 * verified directly: `new Request(url, { signal: new AbortController().signal })`
 * throws "Expected signal to be an instance of AbortSignal" for both the window
 * and global constructors, which are the same object.
 *
 * That matters here only because React Router builds its own Request for every
 * navigation, so the failure lands on code that has no signal of its own and no
 * way to avoid one. Real browsers do not have this problem; it is purely an
 * interop wrinkle between three test-only pieces.
 *
 * The shim drops the signal rather than the request. The cost is that
 * cancellation is inert under test -- so a test asserting that an in-flight
 * request is aborted would not be meaningful, and none pretends to be.
 */
function stripIncompatibleSignal<T extends { signal?: AbortSignal | null }>(
  init: T | undefined,
): T | undefined {
  if (!init?.signal) return init
  const { signal: _dropped, ...rest } = init
  return rest as T
}

const NativeRequest = globalThis.Request
class ShimmedRequest extends NativeRequest {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    super(input, stripIncompatibleSignal(init))
  }
}
globalThis.Request = ShimmedRequest as unknown as typeof Request

const nativeFetch = globalThis.fetch
globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) =>
  nativeFetch(input, stripIncompatibleSignal(init))) as typeof fetch

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))

afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetServerState()
  window.localStorage.clear()
})

afterAll(() => server.close())
