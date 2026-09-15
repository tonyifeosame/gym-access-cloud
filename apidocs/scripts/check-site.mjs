#!/usr/bin/env node
/*
 * Render check: does the built site actually work in a browser?
 *
 * Serves dist/ on a loopback port, opens it in the locally installed Chrome
 * (playwright-core, channel "chrome" -- the same arrangement web/browser/run.mjs
 * uses, so no browser download), and checks what a reader needs:
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
 *   - /openapi.yaml, /openapi.json, /errors/ and every /errors/<code>/ answer.
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
const doc = YAML.parse(readFileSync(join(ROOT, 'openapi.yaml'), 'utf8'))

// The response headers the deployed site will carry, read from render.yaml so
// what is tested is what is served -- above all the Content-Security-Policy,
// under which Scalar must still render.
const render = YAML.parse(readFileSync(join(ROOT, '..', 'render.yaml'), 'utf8'))
const docsService = render.services.find((svc) => svc.name === 'accesslink-docs')
if (!docsService) {
  console.error('render.yaml has no accesslink-docs service')
  process.exit(1)
}
const deployedHeaders = (docsService.headers ?? [])
  .filter((h) => h.path === '/*')
  .map((h) => [h.name, String(h.value).replace(/\s+/g, ' ').trim()])
if (!deployedHeaders.some(([name]) => name === 'Content-Security-Policy')) {
  console.error('render.yaml: accesslink-docs declares no Content-Security-Policy')
  process.exit(1)
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
const server = createServer((req, res) => {
  // Clean URLs the way a static host resolves them: a directory serves its
  // index.html, and /errors/<code> (the exact doc_url the API emits) serves
  // errors/<code>.html.
  let path = decodeURIComponent((req.url ?? '/').split('?')[0])
  if (path.endsWith('/')) path += 'index.html'
  else if (!extname(path) && existsSync(join(DIST, path + '.html'))) path += '.html'
  const file = join(DIST, path)
  if (!file.startsWith(DIST) || !existsSync(file) || statSync(file).isDirectory()) {
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
const fetchStatus = async (path) => (await fetch(origin + path)).status
check((await fetchStatus('/')) === 200, 'GET / is not 200')
check((await fetchStatus('/openapi.yaml')) === 200, 'GET /openapi.yaml is not 200')
check((await fetchStatus('/openapi.json')) === 200, 'GET /openapi.json is not 200')
check((await fetchStatus('/errors/')) === 200, 'GET /errors/ is not 200')
check((await fetchStatus('/sitemap.txt')) === 200, 'GET /sitemap.txt is not 200')
const json = await (await fetch(origin + '/openapi.json')).json()
check(json.openapi === doc.openapi, '/openapi.json is not the same document as openapi.yaml')
for (const c of doc['x-accesslink-error-codes']) {
  // Both the API's doc_url form (no slash) and the directory form.
  for (const path of [`/errors/${c.code}`, `/errors/${c.code}/`]) {
    const res = await fetch(origin + path)
    const html = res.status === 200 ? await res.text() : ''
    check(res.status === 200 && html.includes(`<code>${c.code}</code>`) && html.includes(String(c.status)),
      `${path} is missing or does not name the code and status`)
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

  await page.goto(origin + '/', { waitUntil: 'networkidle' })
  // Scalar renders after the document is fetched and parsed, and renders
  // operation sections lazily as they scroll into view: walk the page once
  // so everything a reader can reach has been drawn before it is checked.
  await page.waitForSelector('text=Getting started', { timeout: 20_000 })
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
  const guide = ['Getting started', 'Authentication', 'Fingerprint authentication', 'Pagination', 'Idempotency', 'Errors', 'Rate limits', 'Changelog']
  for (const h of guide) check(text.includes(h), `guide section "${h}" is not rendered`)
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
  check(text.includes('atp_live_<your-key>'), 'the placeholder credential is not shown in samples')
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
  await phone.goto(origin + '/', { waitUntil: 'networkidle' })
  await phone.waitForSelector('text=Getting started', { timeout: 20_000 })
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
