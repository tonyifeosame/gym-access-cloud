import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import { buildCsp, originOf } from './csp'

/**
 * The security properties of the shipped console, asserted rather than
 * documented.
 *
 * These are STATIC checks over the source tree, and that is deliberate: the
 * failures they catch are ones a component test cannot see, because they are
 * about what the code is capable of rather than about what one screen did. A
 * component test proves a screen did not put a token in localStorage this time;
 * these prove no code exists that could.
 *
 * Each one names the finding it protects. They are cheap, they run on every
 * push, and the change that breaks one of them will look reasonable in review —
 * which is the whole argument for having them.
 */

const SRC = join(process.cwd(), 'src')

function sourceFiles(directory: string = SRC): string[] {
  return readdirSync(directory).flatMap((entry) => {
    const path = join(directory, entry)
    if (statSync(path).isDirectory()) return sourceFiles(path)
    return /\.(ts|tsx)$/.test(entry) ? [path] : []
  })
}

/** Production source: everything except the tests and their fixtures. */
function productionFiles(): string[] {
  return sourceFiles().filter(
    (path) => !/\.test\.tsx?$/.test(path) && !path.includes(`${join('src', 'test')}`),
  )
}

function read(path: string): string {
  return readFileSync(path, 'utf8')
}

/** Reports offending files with their matched line, so a failure is actionable. */
function offenders(pattern: RegExp, files: string[] = productionFiles()): string[] {
  const found: string[] = []
  for (const path of files) {
    for (const [index, line] of read(path).split('\n').entries()) {
      // Comments discuss these things constantly — that is the point of the
      // comments — so only real code counts.
      const code = line.replace(/\/\/.*$/, '').replace(/^\s*\*.*$/, '')
      if (pattern.test(code)) {
        found.push(`${path.replace(process.cwd(), '')}:${index + 1}: ${line.trim()}`)
      }
    }
  }
  return found
}

// ---------------------------------------------------------------------------
// Credentials never reach browser storage
// ---------------------------------------------------------------------------

/**
 * The one module permitted to touch browser storage, and what it may keep there.
 *
 * SiteContext remembers WHICH SITE an operator last selected, keyed by company.
 * That is a view preference and a site's public id, which already appears in
 * every URL — nothing about it is secret, and losing it is a papercut rather
 * than a failure, which is why the module swallows a storage exception instead
 * of propagating one.
 *
 * The allowlist has exactly one entry ON PURPOSE. Storage survives the tab, so
 * anything written there outlives the panel that produced it; a credential put
 * here would outlive the session that minted it. Any NEW use of storage fails
 * this test and has to argue its case in review, which is the whole point.
 */
const STORAGE_ALLOWLIST = [join('src', 'context', 'SiteContext.tsx')]

describe('nothing durable holds a credential', () => {
  it('touches browser storage in exactly one module, and it is not a credential', () => {
    // The session cookie is HttpOnly precisely so a script injected into this
    // page cannot read it. Putting the CSRF token, a site provisioning key or a
    // handover link into storage would hand back most of what that buys.
    const uses = offenders(/\b(localStorage|sessionStorage)\b/)
    const unexpected = uses.filter(
      (entry) => !STORAGE_ALLOWLIST.some((allowed) => entry.includes(allowed)),
    )
    expect(unexpected).toEqual([])
  })

  it('keeps only a site SELECTION in storage, never a token', () => {
    const source = read(join(SRC, 'context', 'SiteContext.tsx'))
    // The written value is the selection and the key is derived from the
    // company's public id. Neither is secret; both appear in URLs already.
    expect(source).toMatch(/setItem\(storageKey\(companyId\), selection\)/)
    expect(source).not.toMatch(/token|csrf|api_key|password/i)
  })

  it('never reads or writes document.cookie', () => {
    // The session cookie is HttpOnly and unreadable; anything else this console
    // put in a cookie would be a credential in a place a script can reach.
    expect(offenders(/document\.cookie/)).toEqual([])
  })

  it('never sets X-API-Key', () => {
    // The site key is the PROVISIONING secret: it registers terminals and
    // rotates their credentials. No browser request may carry one, and there is
    // no code path that could.
    expect(offenders(/X-API-Key/i)).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// Secrets never reach a URL or a log
// ---------------------------------------------------------------------------

describe('secrets stay out of URLs and logs', () => {
  it('puts no credential into a query string', () => {
    // The ONE exception is the redemption link, which necessarily carries its
    // token because the recipient has nothing else to authenticate with — and
    // RedeemPage strips it from the address bar on arrival. Everything else
    // that builds a URL must not.
    //
    // MATCHED ON THE LITERAL PARAMETER NAME, not on the line. An earlier version
    // of this test matched any line mentioning "key" and flagged
    // `params.set(key, …)` inside a loop over a filter object — a variable named
    // `key`, carrying nothing secret. A check that cries wolf gets deleted, so
    // it reads the name being SET rather than the words nearby.
    const names = new Set<string>()
    for (const path of productionFiles()) {
      for (const match of read(path).matchAll(/params\.set\(\s*'([^']+)'/g)) {
        names.add(match[1] as string)
      }
    }

    const secretish = [...names].filter((name) =>
      /token|password|secret|api[-_]?key|credential/i.test(name),
    )
    expect(secretish).toEqual([])
  })

  it('builds a computed query parameter in only one place, over a typed filter', () => {
    // `params.set(variable, …)` cannot be checked by name, so the places that do
    // it are allowlisted instead. The one that exists loops over an EventQuery,
    // whose fields are filters — a decision, a serial, a date — and none of them
    // is a credential. A new computed builder has to argue its case in review.
    const computed = offenders(/params\.set\(\s*[A-Za-z_$]/)
    const unexpected = computed.filter((entry) => !entry.includes(join('src', 'api', 'endpoints.ts')))
    expect(unexpected).toEqual([])
    // And there is exactly one, so this cannot quietly become the normal way.
    expect(computed).toHaveLength(1)
  })

  it('logs nothing but the error boundary’s message', () => {
    // A console.log of a response body is how a person's name, an email address
    // or a one-time link ends up in a browser log somebody screenshots. The
    // error boundary is the single permitted caller and logs a message and a
    // component stack, never props.
    const logging = offenders(/console\.(log|debug|info|warn|error|trace)\(/)
    const unexpected = logging.filter((entry) => !entry.includes('ErrorBoundary.tsx'))
    expect(unexpected).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// Biometrics stay an abstraction
// ---------------------------------------------------------------------------

describe('the biometric boundary holds', () => {
  it('names no template, locator, sensor or vendor concept anywhere', () => {
    // `biometric_enrolled` is the entire biometric surface — a boolean. The
    // credential itself is an abstraction the backend owns, which is what lets
    // the storage change without the console noticing. The first crack in that
    // is a field named after a sensor.
    expect(
      offenders(/fingerprint_template|finger_index|template_data|slot:|sensor_id/i),
    ).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// The Content Security Policy
// ---------------------------------------------------------------------------

describe('the content security policy', () => {
  it('denies everything by default', () => {
    // A resource type nobody thought about is refused rather than allowed.
    expect(buildCsp()).toContain("default-src 'none'")
  })

  it('allows no remote scripts, objects, frames or base tags', () => {
    const policy = buildCsp({ apiBaseUrl: 'https://api.accesslink.store' })
    expect(policy).toContain("script-src 'self'")
    expect(policy).toContain("object-src 'none'")
    expect(policy).toContain("frame-src 'none'")
    // A <base> injected into the document redirects every relative URL on the
    // page, including the API calls.
    expect(policy).toContain("base-uri 'none'")
    expect(policy).toContain("form-action 'self'")
  })

  it('permits no inline or eval’d SCRIPT even though inline style is allowed', () => {
    // The one concession is style: Vite injects styles inline in development.
    // Script is the directive that turns an injected <script> into a console
    // error instead of a session-stealing payload, and it stays closed.
    const policy = buildCsp({ development: true })
    expect(policy).toMatch(/script-src 'self'(;|$)/)
    expect(policy).not.toContain("script-src 'self' 'unsafe-inline'")
    expect(policy).not.toContain('unsafe-eval')
  })

  it('narrows connect-src to the API and nothing else', () => {
    // An exfiltration attempt to an attacker's host is refused here even if
    // script execution somehow succeeded.
    const policy = buildCsp({ apiBaseUrl: 'https://api.accesslink.store' })
    expect(policy).toContain("connect-src 'self' https://api.accesslink.store")
    expect(policy).not.toContain('connect-src *')
  })

  it('takes the ORIGIN of the API base, never a path', () => {
    expect(originOf('https://api.accesslink.store/api/v1/')).toBe('https://api.accesslink.store')
  })

  it('falls back to the strict policy when the API base is missing or malformed', () => {
    // A bad environment variable must not fail the build with a stack trace,
    // and the resulting policy is the tight one rather than an open one.
    expect(originOf(undefined)).toBeNull()
    expect(originOf('not a url')).toBeNull()
    expect(buildCsp({ apiBaseUrl: 'not a url' })).toContain("connect-src 'self'")
  })

  it('opens the websocket only in development', () => {
    // Vite's HMR client needs it; a production build must not.
    expect(buildCsp({ development: true })).toContain('ws:')
    expect(buildCsp({ development: false })).not.toContain('ws:')
  })
})
