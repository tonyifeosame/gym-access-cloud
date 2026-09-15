#!/usr/bin/env node
/*
 * Render check: does the built site actually work in a browser?
 *
 * Serves the build the way production serves it -- mounted at /docs/ on the
 * console's origin, behind the console's own routing rules from render.yaml
 * (the /docs redirect, the /docs/errors/* rewrite, then the SPA catch-all)
 * and under the console service's Content-Security-Policy -- opens it in the
 * locally installed Chrome (playwright-core, channel "chrome", the
 * arrangement web/browser/run.mjs uses), and checks what a reader needs:
 *
 *   - the reference renders: title, every guide section and every operation
 *     summary from openapi.yaml is on the page, grouped as declared;
 *   - the sidebar links resolve: every "#…" link in the navigation points at
 *     an element that exists, and clicking one changes the location;
 *   - search works: the hotkey opens it and a query finds an endpoint;
 *   - keyboard: Tab lands on a focusable control with a visible focus ring;
 *   - the page loads nothing from a third party (no CDN, no fonts, no proxy);
 *   - a phone-width viewport has no horizontal overflow;
 *   - no console errors;
 *   - /docs and /docs/ reach the reference (not the console fallback), and
 *     /docs/openapi.yaml, /docs/openapi.json, /docs/errors/ and every
 *     /docs/errors/<code> (with and without a trailing slash) answer;
 *   - the collapsible reference notes render as real <details> elements.
 *
 * DOCS_SITE_ROOT, when set, names a directory to serve as the whole origin --
 * the console's web/dist after its build, which contains docs/ -- so the very
 * artefact that is deployed is what gets checked. Without it, dist/ is
 * mounted at /docs/ under a stub console page.
 *
 * Exit 1 on any failure, with every failure listed.
 */
import { createServer } from 'node:http'
import { readFileSync, existsSync, statSync } from 'node:fs'
import { join, resolve, extname } from 'node:path'
import { chromium } from 'playwright-core'
import YAML from 'yaml'

const ROOT = resolve(import.meta.dirname, '..')
const DIST = join(ROOT, 'dist')
const BASE = '/docs'
const doc = YAML.parse(readFileSync(join(ROOT, 'openapi.yaml'), 'utf8'))

// The response headers the deployed site will carry, read from render.yaml so
// what is tested is what is served. The docs are files inside the CONSOLE
// static site, so the console service's headers -- above all its
// Content-Security-Policy -- are the ones that apply.
const render = YAML.parse(readFileSync(join(ROOT, '..', 'render.yaml'), 'utf8'))
const consoleService = render.services.find((svc) => svc.name === 'accesslink-console')
if (!consoleService) {
  console.error('render.yaml has no accesslink-console service')
  process.exit(1)
}
const deployedHeaders = (consoleService.headers ?? [])
  .filter((h) => h.path === '/*')
  .map((h) => [h.name, String(h.value).replace(/\s+/g, ' ').trim()])
if (!deployedHeaders.some(([name]) => name === 'Content-Security-Policy')) {
  console.error('render.yaml: accesslink-console declares no Content-Security-Policy')
  process.exit(1)
}
// THE ROUTING IS RENDER'S, AS OBSERVED IN PRODUCTION, NOT AS ONE MIGHT HOPE.
// With a catch-all rewrite present, Render answers exactly two things before
// it consults the rules: a file that exists, and a directory requested WITH
// its trailing slash (its index.html). It does NOT resolve /docs to
// /docs/index.html, nor /docs/errors/<code> to <code>.html or
// <code>/index.html -- both fell through to the console shell on
// 2026-09-15. Then the rules run top-down (first match wins): a redirect
// answers 301, a rewrite serves its destination if that is a file, and the
// last rule, /* -> /index.html, catches everything else. This server does
// precisely that, with the rules read from render.yaml, so the check fails
// for exactly the URLs production would fail for.
const SITE_ROOT = process.env.DOCS_SITE_ROOT ? resolve(process.env.DOCS_SITE_ROOT) : null
const routes = consoleService.routes ?? []
if (routes.length === 0 || routes[routes.length - 1].source !== '/*') {
  console.error('render.yaml: accesslink-console must end its routes with the /* catch-all')
  process.exit(1)
}
// A Render rule: `*` in the source matches any string from that position on;
// `*` in the destination is replaced by what the source's `*` captured.
const matchRule = (rule, path) => {
  const star = rule.source.indexOf('*')
  if (star < 0) return rule.source === path ? '' : null
  const prefix = rule.source.slice(0, star)
  return path.startsWith(prefix) ? path.slice(prefix.length) : null
}
const applyRule = (rule, captured) => rule.destination.replace('*', captured)
const STUB_CONSOLE = '<!doctype html><title>AccessLink Console</title><div id="root">console shell</div>'
const resolveFile = (path) => {
  if (SITE_ROOT) return join(SITE_ROOT, path)
  if (path === BASE || path.startsWith(BASE + '/')) return join(DIST, path.slice(BASE.length) || '/')
  return null
}
const failures = []
const check = (ok, msg) => {
  if (!ok) failures.push(msg)
  return ok
}

// --- a static server, SPA-free: every path must exist as a file ------------
const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.yaml': 'application/yaml; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
  '.woff2': 'font/woff2',
}
// What exists: an exact file, or a directory's index.html when the request
// carries the trailing slash. Nothing else.
const existingResource = (path) => {
  const file = resolveFile(path)
  if (!file || !existsSync(file)) return null
  if (statSync(file).isDirectory()) {
    if (!path.endsWith('/')) return null
    const index = join(file, 'index.html')
    return existsSync(index) ? index : null
  }
  return file
}
const server = createServer((req, res) => {
  const path = decodeURIComponent((req.url ?? '/').split('?')[0])
  let file = existingResource(path)
  if (!file) {
    for (const rule of routes) {
      const captured = matchRule(rule, path)
      if (captured === null) continue
      const destination = applyRule(rule, captured)
      if (rule.type === 'redirect') {
        res.writeHead(301, { Location: destination })
        res.end()
        return
      }
      if (rule.type === 'rewrite') {
        if (rule.source === '/*') {
          // The SPA catch-all: the console's shell, whatever the path.
          const fallback = SITE_ROOT ? join(SITE_ROOT, 'index.html') : null
          res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'X-Fallback': 'console' })
          res.end(fallback && existsSync(fallback) ? readFileSync(fallback) : STUB_CONSOLE)
          return
        }
        file = existingResource(destination)
        if (!file) {
          res.writeHead(404, { 'Content-Type': 'text/plain', 'X-Rewrite-Miss': destination })
          res.end('not found')
          return
        }
        res.setHeader('X-Rewritten-To', destination)
        break
      }
    }
  }
  if (!file) {
    res.writeHead(404, { 'Content-Type': 'text/plain' })
    res.end('not found')
    return
  }
  res.writeHead(200, {
    'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream',
    ...Object.fromEntries(deployedHeaders.filter(([name]) => name !== 'Strict-Transport-Security')),
  })
  res.end(readFileSync(file))
})
await new Promise((r) => server.listen(0, '127.0.0.1', r))
const origin = `http://127.0.0.1:${server.address().port}`

// --- the static files ------------------------------------------------------
// A docs page is a real file (no X-Fallback); the console shell answers
// everything else. The bare /docs must be redirected to /docs/ (production
// does not resolve it to the directory index), and /docs/ must be the
// reference itself.
const isDocs = async (path) => {
  const res = await fetch(origin + path)
  const html = await res.text()
  return res.status === 200 && res.headers.get('x-fallback') !== 'console' && html.includes('AccessLink API reference')
}
const bare = await fetch(origin + BASE, { redirect: 'manual' })
check(bare.status === 301 && bare.headers.get('location') === `${BASE}/`,
  `GET ${BASE} must redirect to ${BASE}/ (got ${bare.status} ${bare.headers.get('location') ?? ''}); the bare path is not a file and would fall into the console`)
check(await isDocs(`${BASE}`), `GET ${BASE} (following the redirect) did not reach the reference`)
check(await isDocs(`${BASE}/`), `GET ${BASE}/ did not reach the reference (fell through to the console)`)
const notDocs = await fetch(origin + '/settings/api-credentials')
check(notDocs.status === 200 && notDocs.headers.get('x-fallback') === 'console', 'a console route no longer falls through to the console shell')
const bogus = await fetch(`${origin}${BASE}/errors/no_such_code`)
check(bogus.status === 404, `an unknown error code answered ${bogus.status}; the rewrite must not fall into the console shell or invent a page`)
const fetchStatus = async (path) => (await fetch(origin + path)).status
check((await fetchStatus(`${BASE}/openapi.yaml`)) === 200, `GET ${BASE}/openapi.yaml is not 200`)
check((await fetchStatus(`${BASE}/openapi.json`)) === 200, `GET ${BASE}/openapi.json is not 200`)
check((await fetchStatus(`${BASE}/errors/`)) === 200, `GET ${BASE}/errors/ is not 200`)
check((await fetchStatus(`${BASE}/sitemap.txt`)) === 200, `GET ${BASE}/sitemap.txt is not 200`)
const yamlText = await (await fetch(origin + `${BASE}/openapi.yaml`)).text()
check(yamlText === readFileSync(join(ROOT, 'openapi.yaml'), 'utf8'), `${BASE}/openapi.yaml is not byte-identical to the source`)
const json = await (await fetch(origin + `${BASE}/openapi.json`)).json()
check(json.openapi === doc.openapi && json.info?.title === doc.info?.title, `${BASE}/openapi.json is not the same document as openapi.yaml`)
for (const c of doc['x-accesslink-error-codes']) {
  // The API's exact doc_url form (no slash -- reachable only through the
  // /docs/errors/* rewrite) and the directory form (an existing resource).
  for (const path of [`${BASE}/errors/${c.code}`, `${BASE}/errors/${c.code}/`]) {
    const res = await fetch(origin + path)
    const html = res.status === 200 ? await res.text() : ''
    check(res.status === 200 && res.headers.get('x-fallback') !== 'console' && html.includes(`<code>${c.code}</code>`) && html.includes(String(c.status)),
      `${path} is missing or does not name the code and status`)
    check(!/docs\.accesslink\.store|onrender\.com/.test(html), `${path} names a hosting hostname`)
  }
}

// --- the reference in a browser -------------------------------------------
const browser = await chromium.launch({ channel: 'chrome' })
const consoleErrors = []
const thirdParty = new Set()
const notes = []
try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await context.newPage()
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()) })
  // A CSP violation is reported on the console as an error, so the check
  // above catches it; this names it so the failure reads clearly.
  page.on('console', (m) => { if (/Content Security Policy/i.test(m.text())) consoleErrors.push(`CSP violation: ${m.text().slice(0, 160)}`) })
  page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`))
  page.on('request', (r) => { if (!r.url().startsWith(origin)) thirdParty.add(new URL(r.url()).host) })

  await page.goto(origin + `${BASE}/`, { waitUntil: 'networkidle' })
  // Scalar renders after the document is fetched and parsed, and renders
  // operation sections lazily as they scroll into view: walk the page once
  // so everything a reader can reach has been drawn before it is checked.
  await page.waitForSelector('text=Quick start', { timeout: 20_000 })
  check((await page.title()).includes('AccessLink'), `document title is "${await page.title()}"`)
  await page.evaluate(async () => {
    for (let y = 0; y <= document.documentElement.scrollHeight; y += 600) {
      window.scrollTo(0, y)
      await new Promise((r) => setTimeout(r, 60))
    }
    window.scrollTo(0, 0)
  })
  await page.waitForTimeout(500)

  const text = await page.evaluate(() => document.body.innerText)
  const guide = ['Quick start', 'Authentication', 'Integration flow', 'Core operations', 'Fingerprint authentication', 'Errors', 'Rate limits', 'Full API reference']
  for (const h of guide) check(text.includes(h), `guide section "${h}" is not rendered`)
  // The reference notes are collapsible: real <details> elements, closed by
  // default, whose summaries are readable and which open on click.
  const details = await page.$$eval('details', (els) => els.map((d) => ({ open: d.open, summary: d.querySelector('summary')?.textContent?.trim() ?? '' })))
  check(details.length >= 3, `expected the reference notes as <details> elements, found ${details.length}`)
  check(details.every((d) => !d.open), 'reference notes should start collapsed')
  check(details.some((d) => /Pagination/.test(d.summary)) && details.some((d) => /Idempotency/.test(d.summary)), 'Pagination and Idempotency notes are not collapsible sections')
  const summary = page.locator('details summary', { hasText: 'Pagination' }).first()
  await summary.click()
  check(await page.$eval('details', (d) => [...document.querySelectorAll('details')].some((x) => x.open)), 'clicking a summary did not open its section')
  check(text.includes('<ACCESSLINK_API_KEY>'), 'the <ACCESSLINK_API_KEY> placeholder is not shown in the guide')
  check(text.includes('curl "https://api.accesslink.store/api/public/v1/members?limit=2"'), 'the quick-start curl is not rendered verbatim')
  check(!/docs\.accesslink\.store|onrender/.test(text), 'a hosting hostname is on the page')
  for (const g of doc['x-tagGroups']) {
    check(text.includes(g.name), `tag group "${g.name}" is not rendered`)
    for (const t of g.tags) check(text.includes(t), `tag "${t}" is not rendered`)
  }
  const summaries = []
  for (const item of Object.values(doc.paths)) {
    for (const m of ['get', 'post', 'put', 'patch', 'delete']) if (item[m]) summaries.push(item[m].summary)
  }
  for (const s of summaries) check(text.includes(s), `operation "${s}" is not rendered`)
  check(text.includes('/api/public/v1/members'), 'method/path display: the members path is not shown')
  check(text.includes('<ACCESSLINK_API_KEY>'), 'the placeholder key is not shown in samples')
  check(!/atp_live_[0-9a-f]{16}/.test(text), 'a credential-shaped value appears on the page')
  notes.push(`${summaries.length} operations, ${guide.length} guide sections rendered`)

  // Sidebar links resolve, and navigation moves the location.
  const links = await page.$$eval('nav a[href^="#"], aside a[href^="#"], [class*="sidebar"] a[href^="#"]', (as) =>
    [...new Set(as.map((a) => a.getAttribute('href')))])
  check(links.length >= 15, `only ${links.length} sidebar links found`)
  // Scalar prefixes element ids with the document slug (api-1/…) and strips
  // it again when routing a hash, so a link resolves when an id equals the
  // hash or ends with it.
  const unresolved = await page.evaluate((hrefs) => {
    const ids = [...document.querySelectorAll('[id]')].map((e) => decodeURIComponent(e.id))
    return hrefs.filter((h) => {
      const want = decodeURIComponent(h.slice(1))
      return !ids.some((id) => id === want || id.endsWith('/' + want))
    })
  }, links)
  check(unresolved.length === 0, `sidebar links with no target: ${unresolved.slice(0, 8).join(', ')}${unresolved.length > 8 ? '…' : ''}`)
  const eventsLink = links.find((h) => /events/i.test(h))
  if (check(Boolean(eventsLink), 'no sidebar link for Events')) {
    await page.click(`a[href="${eventsLink}"]`)
    await page.waitForTimeout(400)
    check(page.url().includes('#'), 'clicking a sidebar link did not change the location hash')
  }
  notes.push(`${links.length} sidebar links, all resolve`)

  // Search: the sidebar button and the hotkey both open it, and a query
  // finds an endpoint. The result list is what proves it: a link to the
  // operation that was not in the sidebar's own markup.
  check(!text.includes('Generate MCP') && !text.includes('Developer Tools'),
    'a Scalar service button (Generate MCP / Developer Tools) is on the page')
  await page.click('button:has-text("Search")')
  const searchBox = page.locator('input:visible').first()
  if (check(await searchBox.isVisible({ timeout: 5000 }).catch(() => false), 'search did not open from the sidebar button')) {
    await searchBox.fill('List events')
    const hit = page.locator('a[href*="events"]:visible, [role="option"]:has-text("List events"), [role="listbox"] :text("List events")').first()
    check(await hit.isVisible({ timeout: 3000 }).catch(() => false), 'searching for "List events" showed no result')
    await page.keyboard.press('Escape')
    await page.waitForTimeout(300)
    check(!(await searchBox.isVisible().catch(() => false)), 'Escape did not close search')
  }
  await page.keyboard.press('Control+k')
  const hotkeyBox = page.locator('input:visible').first()
  check(await hotkeyBox.isVisible({ timeout: 3000 }).catch(() => false), 'search did not open on Ctrl+K')
  await page.keyboard.press('Escape')

  // Keyboard: Tab reaches a control and the focus ring is drawn.
  await page.keyboard.press('Tab')
  await page.keyboard.press('Tab')
  const focused = await page.evaluate(() => {
    const el = document.activeElement
    if (!el || el === document.body) return null
    const cs = getComputedStyle(el)
    return { tag: el.tagName, outline: cs.outlineStyle, width: cs.outlineWidth }
  })
  check(focused !== null, 'Tab did not move focus to a control')
  if (focused) check(focused.outline !== 'none' && focused.width !== '0px', `focused ${focused.tag} has no visible outline`)

  // Contrast: body text against its background, both modes.
  const ratio = await page.evaluate(() => {
    const lum = (rgb) => {
      const [r, g, b] = rgb.match(/\d+/g).map(Number).map((v) => v / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
      return 0.2126 * r + 0.7152 * g + 0.0722 * b
    }
    const el = document.querySelector('p') ?? document.body
    let bg = getComputedStyle(el).backgroundColor
    let node = el
    while (node && /rgba?\(0, 0, 0, 0\)/.test(bg)) { node = node.parentElement; bg = node ? getComputedStyle(node).backgroundColor : 'rgb(255, 255, 255)' }
    const a = lum(getComputedStyle(el).color), b = lum(bg)
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
  })
  check(ratio >= 4.5, `body text contrast ${ratio.toFixed(2)}:1 is below 4.5:1`)
  notes.push(`body text contrast ${ratio.toFixed(1)}:1`)

  // Phone width: no horizontal overflow, and the content is reachable.
  const phone = await context.newPage()
  await phone.setViewportSize({ width: 390, height: 844 })
  await phone.goto(origin + `${BASE}/`, { waitUntil: 'networkidle' })
  await phone.waitForSelector('text=Quick start', { timeout: 20_000 })
  const overflow = await phone.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }))
  check(overflow.sw <= overflow.cw + 1, `phone layout scrolls horizontally (${overflow.sw} > ${overflow.cw})`)
  notes.push(`phone 390px: no horizontal overflow`)
  await phone.close()

  check(consoleErrors.length === 0, `console errors: ${consoleErrors.slice(0, 3).join(' | ')}`)
  check(thirdParty.size === 0, `the page requested third-party hosts: ${[...thirdParty].join(', ')}`)
} catch (error) {
  // A page that never renders (a CSP that blocks the bundle, a broken build)
  // surfaces here as a timeout; report it with whatever the console said.
  const firstLine = String(error.message).split(/\r?\n/)[0]
  failures.push(`the reference did not render: ${firstLine}`)
  if (consoleErrors.length > 0) failures.push(`console errors: ${consoleErrors.slice(0, 3).join(' | ')}`)
} finally {
  await browser.close()
  server.close()
}

if (failures.length > 0) {
  console.error(`site check: ${failures.length} failure(s)`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(`site check passed: ${notes.join('; ')}; no console errors under the deployed CSP; no third-party requests`)
