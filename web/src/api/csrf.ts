/**
 * The per-session CSRF tokens.
 *
 * IN MEMORY, NEVER IN STORAGE. The session cookie is HttpOnly precisely so that
 * a script injected into this page cannot read it; putting the companion token
 * in localStorage would hand back a good part of what that buys. A page reload
 * clears these, and each is re-hydrated from its own /me -- which is why the API
 * returns it there.
 *
 * TWO TOKENS, ONE PER CREDENTIAL CLASS, and they are kept apart for the same
 * reason the server keeps the identities apart. An operator session and a
 * platform-administrator session are different tables, different cookies and
 * different trees; a single shared variable would mean whichever signed in last
 * silently supplied the token for the other's requests, and the failure would
 * be a 403 nobody could explain. Both cookies are Path=/, so the browser really
 * does offer each to the other's routes -- the separation has to be explicit
 * here, not assumed from the URL.
 */

export type CredentialScope = 'operator' | 'platform'

const tokens: Record<CredentialScope, string | null> = {
  operator: null,
  platform: null,
}

export function setCsrfToken(token: string | null, scope: CredentialScope = 'operator'): void {
  tokens[scope] = token
}

export function getCsrfToken(scope: CredentialScope = 'operator'): string | null {
  return tokens[scope]
}

export function clearCsrfToken(scope: CredentialScope = 'operator'): void {
  tokens[scope] = null
}
