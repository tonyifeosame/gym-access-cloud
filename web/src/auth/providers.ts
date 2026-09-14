import { useQuery } from '@tanstack/react-query'

import { apiUrl } from '../api/client'
import * as endpoints from '../api/endpoints'
import type { AuthProviders } from '../api/types'

/**
 * Which ways in this deployment offers, and the words for the ways a Google
 * sign-in can come back.
 *
 * THE FALLBACK IS PASSWORD-ONLY. If /auth/providers cannot be read -- the API
 * is down, or predates the endpoint -- the login screen draws exactly what it
 * drew before any of this existed. A console that hid the password form
 * because a capability probe failed would lock everybody out over a probe.
 */
export const PASSWORD_ONLY: AuthProviders = {
  password: { enabled: true },
  google: { enabled: false, start_path: '/api/v1/auth/google/start' },
  signup: { enabled: true },
  password_reset: { email_delivery: false },
}

export const AUTH_PROVIDERS_QUERY_KEY = ['auth', 'providers'] as const

/**
 * Reads the providers once per page load. Static per deployment, so there is
 * nothing to refetch on focus and no reason to expire it.
 */
export function useAuthProviders(): AuthProviders {
  const query = useQuery({
    queryKey: AUTH_PROVIDERS_QUERY_KEY,
    queryFn: ({ signal }) => endpoints.fetchAuthProviders(signal),
    staleTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
  })
  return query.data ?? PASSWORD_ONLY
}

/**
 * The href for the "Sign in with Google" control.
 *
 * A NAVIGATION, NOT A FETCH. The flow is a redirect chain -- to Google, back
 * to the API, back to the console -- and only a real navigation can carry the
 * session cookie the API sets at the end of it. `next` is where the console
 * should land afterwards; the API sanitises it again on its own side.
 */
export function googleSignInHref(providers: AuthProviders, next: string | null): string {
  const target = next && next.startsWith('/') && !next.startsWith('//') ? next : '/'
  return apiUrl(`${providers.google.start_path}?next=${encodeURIComponent(target)}`)
}

/**
 * The outcome codes the API sends the browser back with, in words.
 *
 * KEYED ON THE CODE, which the API documents as stable, never on prose. Each
 * says what happened and what to do next; none says anything about any
 * account beyond what the person already proved to Google. Unknown codes get
 * a generic sentence rather than nothing, so a newer API's code still reads
 * as a failure and not as a blank screen.
 */
export function describeGoogleOutcome(code: string | null): string | null {
  if (!code) return null
  switch (code) {
    case 'google_cancelled':
      return 'Google sign-in was cancelled. You can try again, or sign in with your password.'
    case 'google_no_account':
      return (
        'No AccessLink account is linked to that Google account. Sign in with the ' +
        'email and password for your account, ask an administrator in your company ' +
        'to invite you, or create a new account.'
      )
    case 'google_email_unverified':
      return (
        'Google has not verified the email address on that account, so it cannot be ' +
        'used to sign in here. Verify it with Google, or sign in with your password.'
      )
    case 'google_account_conflict':
      return (
        'That email address belongs to an AccessLink account that is already linked ' +
        'to a different Google account. Sign in with your password instead.'
      )
    case 'google_state_mismatch':
      return (
        'That sign-in link had expired or was opened in a different browser. Start ' +
        'again from this page.'
      )
    case 'google_failed':
      return 'Google sign-in did not complete. Try again, or sign in with your password.'
    default:
      return 'Sign-in did not complete. Try again, or sign in with your password.'
  }
}
