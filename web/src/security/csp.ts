/**
 * The console's Content Security Policy.
 *
 * CON-04 recorded that there was no CSP at all. This builds one, and it is
 * emitted into index.html at build time rather than written as a constant,
 * because ONE DIRECTIVE DEPENDS ON THE DEPLOYMENT: the console and the API are
 * on different hosts in production (app.accesslink.store and
 * api.accesslink.store) and on the same origin in development, so `connect-src`
 * cannot be known until the build knows which.
 *
 * WHY A META TAG AND NOT ONLY A HEADER. A header from the web server is
 * strictly better and is where `frame-ancestors` has to live — a meta tag cannot
 * carry it. But the console's deployment configuration does not exist yet
 * (OPS-02), and a policy that ships with the application is a policy that cannot
 * be lost when somebody sets up a new host. The header, when it lands, should
 * carry this same policy plus `frame-ancestors 'none'`, and the two agreeing is
 * cheap; a meta tag alone leaving clickjacking open is not something to leave
 * unstated, so it is stated here.
 *
 * WHAT EACH DIRECTIVE IS BUYING, since a CSP nobody can explain is a CSP
 * somebody will loosen:
 *
 *   default-src 'none'   Deny by default. Everything below is an exception, so
 *                        a resource type nobody thought about is refused rather
 *                        than allowed.
 *
 *   script-src 'self'    No inline scripts, no eval, no CDN. This is the
 *                        directive that turns an injected <script> into a
 *                        console error instead of a session-stealing payload.
 *                        The bundle is same-origin and needs nothing else.
 *
 *   connect-src          The API, and only the API. An exfiltration attempt to
 *                        an attacker's host is blocked here even if script
 *                        execution somehow succeeded.
 *
 *   img-src 'self' data: `data:` because an SVG or a favicon may be inlined by
 *                        the bundler. No remote images: a remote image is a
 *                        beacon that reports every operator who opens the page.
 *
 *   style-src            'unsafe-inline' is required and is the one concession.
 *                        Vite injects styles as inline <style> elements in
 *                        development, and removing it would leave the dev server
 *                        unusable while changing nothing about what an attacker
 *                        could do — the app has no inline style attributes and
 *                        no runtime style injection of its own (asserted by a
 *                        test), so the practical exposure is CSS-based
 *                        exfiltration, which script-src already denies the
 *                        interesting half of.
 *
 *   frame-src / object-src 'none'
 *                        Nothing is embedded. A Flash-era `object` or an iframe
 *                        to somewhere else has no legitimate use here.
 *
 *   base-uri 'none'      A `<base>` tag injected into the document can redirect
 *                        every relative URL on the page, including the API
 *                        calls. There is no reason for one to exist.
 *
 *   form-action 'self'   The console posts through fetch, never through a form
 *                        submission. An injected form that posts a password
 *                        somewhere else is refused.
 */

export interface CspOptions {
  /**
   * The API's origin, from VITE_API_BASE_URL. Empty means same-origin, which is
   * the development proxy — and then `'self'` alone already covers it.
   */
  apiBaseUrl?: string
  /** Development needs the Vite client's websocket and its inline styles. */
  development?: boolean
}

/**
 * The origin of a base URL, or null.
 *
 * An ORIGIN and not the whole URL, because `connect-src` matches on origin and a
 * path in the directive would be a source of confusion rather than of
 * narrowing. A malformed value yields null rather than throwing: a bad
 * environment variable should not fail the build with a stack trace, and the
 * resulting policy is the strict one.
 */
export function originOf(url: string | undefined): string | null {
  if (!url || !url.trim()) return null
  try {
    return new URL(url).origin
  } catch {
    return null
  }
}

export function buildCsp({ apiBaseUrl, development = false }: CspOptions = {}): string {
  const api = originOf(apiBaseUrl)

  // 'self' is always present: it covers the same-origin case, and in a
  // split-origin deployment it still covers anything the page loads from its
  // own host.
  const connect = ["'self'", api, development ? 'ws:' : null].filter(Boolean).join(' ')

  const directives = [
    "default-src 'none'",
    "script-src 'self'",
    `connect-src ${connect}`,
    "img-src 'self' data:",
    "style-src 'self' 'unsafe-inline'",
    "font-src 'self'",
    "frame-src 'none'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'self'",
  ]

  return directives.join('; ')
}
