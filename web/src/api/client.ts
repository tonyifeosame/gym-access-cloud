import { getCsrfToken, type CredentialScope } from './csrf'

/**
 * The HTTP client.
 *
 * Everything the API expects of a browser client lives here, in one place, so
 * no individual call has to remember it:
 *
 *   credentials: 'include'   the session is an HttpOnly cookie, and fetch does
 *                            not send cookies cross-origin without this
 *   X-CSRF-Token             required on every unsafe method
 *   Content-Type: json       login rejects anything else with 415, which is
 *                            deliberate CSRF defence on an endpoint that has no
 *                            session yet to carry a token
 *
 * WHAT IS NOT HERE, AND MUST NEVER BE: the site API key. It is the provisioning
 * secret -- it registers terminals and rotates their credentials -- and no
 * browser request may carry it. There is no code path that sets X-API-Key, a
 * lint rule forbids the literal, and a test asserts its absence.
 */

const API_BASE = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '')

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'DELETE'

/**
 * A failed request, carrying what the UI can actually act on.
 *
 * The API's error strings are for humans and are explicitly not stable, so
 * behaviour branches on `status`. `requestId` is echoed on every response and
 * appears in the server log against the same request, which is what makes a
 * user-reported failure findable -- it is exposed cross-origin for this reason.
 */
export class ApiError extends Error {
  readonly status: number
  readonly requestId: string | null
  readonly retryAfterSeconds: number | null
  /**
   * The API's machine-readable reason, when it sent one — the `code` field some
   * refusals carry beside the human message.
   *
   * THE STABLE HALF OF AN ERROR. `message` is prose the API says explicitly is
   * not stable, so a screen that matched on it would break the first time the
   * wording improved; a code is the API committing to a distinction. Null on
   * every response that does not carry one, which is most of them — the default
   * remains branching on `status`.
   */
  readonly code: string | null

  constructor(
    status: number,
    message: string,
    requestId: string | null,
    retryAfterSeconds: number | null,
    code: string | null = null,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.requestId = requestId
    this.retryAfterSeconds = retryAfterSeconds
    this.code = code
  }

  get isUnauthenticated(): boolean {
    return this.status === 401
  }

  get isForbidden(): boolean {
    return this.status === 403
  }

  get isNotFound(): boolean {
    return this.status === 404
  }

  get isRateLimited(): boolean {
    return this.status === 429
  }
}

/**
 * Called whenever any request comes back 401.
 *
 * A session can end between requests -- it expires, an administrator disables
 * the account, someone changes the password elsewhere -- and every one of those
 * arrives as a 401 on whatever request happened to be in flight. Routing that
 * to one handler is what stops each caller inventing its own recovery.
 */
type UnauthenticatedHandler = () => void
let onUnauthenticated: UnauthenticatedHandler | null = null

export function setUnauthenticatedHandler(handler: UnauthenticatedHandler | null): void {
  onUnauthenticated = handler
}

interface RequestOptions {
  body?: unknown
  signal?: AbortSignal
  /** Suppresses the global 401 handler, for the boot-time session probe. */
  expectUnauthenticated?: boolean
  /**
   * Which credential class this request belongs to. Decides WHICH CSRF token is
   * sent, and whether a 401 ends the operator session.
   *
   * Defaults to `operator`, which is every call in the tenant console. The
   * platform-administration tree passes `platform`: it authenticates a different
   * table with a different cookie, and a 401 there says nothing about whether an
   * operator is still signed in — routing it to the operator's unauthenticated
   * handler would sign somebody out of a console they were using.
   */
  scope?: CredentialScope
}

export async function request<T>(
  method: HttpMethod,
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }

  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }

  const scope = options.scope ?? 'operator'

  if (!SAFE_METHODS.has(method)) {
    const token = getCsrfToken(scope)
    if (token) {
      headers['X-CSRF-Token'] = token
    }
  }

  const response = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    // The session lives in a cookie the script cannot read; without this fetch
    // simply would not send it, and every request would be anonymous.
    credentials: 'include',
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
  })

  const requestId = response.headers.get('X-Request-ID')

  // Only the OPERATOR session has a global recovery. A 401 on a platform route
  // means the platform administrator's session ended, which is a different
  // identity entirely — handing it to the operator's handler would clear a
  // console session that is perfectly valid.
  if (response.status === 401 && scope === 'operator' && !options.expectUnauthenticated) {
    onUnauthenticated?.()
  }

  if (response.status === 204) {
    return undefined as T
  }

  const payload = await readJson(response)

  if (!response.ok) {
    throw new ApiError(
      response.status,
      errorMessage(payload, response.status),
      requestId,
      retryAfter(response),
      errorCode(payload),
    )
  }

  return payload as T
}

async function readJson(response: Response): Promise<unknown> {
  const text = await response.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    // A non-JSON body means something between here and the API answered -- a
    // proxy error page, usually. Surface it as text rather than crashing on it.
    return { error: text.slice(0, 200) }
  }
}

function errorMessage(payload: unknown, status: number): string {
  if (payload && typeof payload === 'object' && 'error' in payload) {
    const message = (payload as { error?: unknown }).error
    if (typeof message === 'string' && message.length > 0) return message
  }
  return `Request failed with status ${status}`
}

/** The `code` on an error body, when the API committed to one. */
function errorCode(payload: unknown): string | null {
  if (payload && typeof payload === 'object' && 'code' in payload) {
    const code = (payload as { code?: unknown }).code
    if (typeof code === 'string' && code.length > 0) return code
  }
  return null
}

function retryAfter(response: Response): number | null {
  const header = response.headers.get('Retry-After')
  if (!header) return null
  const seconds = Number.parseInt(header, 10)
  return Number.isFinite(seconds) ? seconds : null
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, options),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('POST', path, { ...options, body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('PUT', path, { ...options, body }),
  delete: <T>(path: string, options?: RequestOptions) => request<T>('DELETE', path, options),
}
