/**
 * The per-session CSRF token.
 *
 * IN MEMORY, NEVER IN STORAGE. The session cookie is HttpOnly precisely so that
 * a script injected into this page cannot read it; putting the companion token
 * in localStorage would hand back a good part of what that buys. A page reload
 * clears this, and it is re-hydrated from GET /auth/me -- which is why the API
 * returns it there.
 */
let csrfToken: string | null = null

export function setCsrfToken(token: string | null): void {
  csrfToken = token
}

export function getCsrfToken(): string | null {
  return csrfToken
}

export function clearCsrfToken(): void {
  csrfToken = null
}
