#!/usr/bin/env node
/*
 * Check the built accesslink.store (dist/) the way Render will serve it.
 *
 * The routes and headers are read from the accesslink-site service in
 * render.yaml, and the server below answers as Render does: an existing file,
 * a directory's index.html when the request ends in a slash, then the rules
 * top-down, then 404 with 404.html. So what passes here is what is deployed.
 *
 *   - every page: lang, viewport, a title, a favicon; the indexable page has a
 *     description, a canonical URL and sharing metadata WITHOUT an image; the
 *     404 and console-redirect pages say noindex;
 *   - robots.txt names the sitemap; sitemap.xml is well formed, lists the
 *     landing page and the reference, and every URL in it answers 200 here;
 *   - an unknown path, inside /docs or out, is a real 404 with the 404 page;
 *   - a console path (/redeem?token=...#x) lands on the same path, query and
 *     fragment on app.accesslink.store;
 *   - in Chrome, at desktop and two phone widths in both colour schemes,
 *     /, /docs, /404, /404.html and two unknown paths: the right status, no
 *     console errors or CSP violations under the deployed policy (404 pages
 *     included -- only the document's own 404 report is excused), nothing
 *     from a third party, no horizontal scrolling, and the call to action
 *     above the fold; the error detector is itself probed first;
 *   - without script, the console-redirect page claims nothing and links to
 *     the console's home;
 *   - then the API reference's own render check, run against THIS service
 *     (apidocs/scripts/check-site.mjs with DOCS_SERVICE=accesslink-site).
 *
 * SITE_SCREENSHOTS=<dir> also saves a screenshot per viewport there.
 * Exit 1 on any failure, with every failure listed.
 */
import { execFileSync } from 'node:child_process'
import { createServer } from 'node:http'
import { existsSync, mkdirSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { extname, join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dirname, '..')
const DIST = join(ROOT, 'dist')

// NO DEPENDENCIES OF ITS OWN. The browser driver and the YAML parser are the
// ones the API reference already installs in ../apidocs (its own render check
// uses the same two), and this site's build installs them there. A second
// copy here would be a second lockfile to keep current for nothing.
const APIDOCS = join(ROOT, '..', 'apidocs')
if (!existsSync(join(APIDOCS, 'node_modules', 'playwright-core')) || !existsSync(join(APIDOCS, 'node_modules', 'yaml'))) {
  console.error('site check: ../apidocs/node_modules is missing; `npm run build` installs it')
  process.exit(1)
}
const fromApidocs = createRequire(join(APIDOCS, 'package.json'))
const { chromium } = fromApidocs('playwright-core')
const YAML = fromApidocs('yaml')
const ORIGIN = 'https://accesslink.store'
const CONSOLE = 'https://app.accesslink.store'

if (!existsSync(join(DIST, 'index.html')) || !existsSync(join(DIST, 'docs', 'index.html'))) {
  console.error('site check: dist/ is incomplete; run `npm run build` first')
  process.exit(1)
}

const failures = []
const notes = []
const check = (ok, msg) => {
  if (!ok) failures.push(msg)
  return ok
}
const read = (rel) => readFileSync(join(DIST, rel), 'utf8')

// --- render.yaml -------------------------------------------------------------
const render = YAML.parse(readFileSync(join(ROOT, '..', 'render.yaml'), 'utf8'))
const service = render.services.find((svc) => svc.name === 'accesslink-site')
if (!service) {
  console.error('render.yaml has no accesslink-site service')
  process.exit(1)
}
const routes = service.routes ?? []
check(!routes.some((r) => r.source === '/*'), 'accesslink-site must not have a catch-all rule: unknown paths have to be 404s')
const headers = (service.headers ?? [])
  .filter((h) => h.path === '/*')
  .map((h) => [h.name, String(h.value).replace(/\s+/g, ' ').trim()])
check(headers.some(([n]) => n === 'Content-Security-Policy'), 'accesslink-site declares no Content-Security-Policy')

// --- the pages' markup -------------------------------------------------------
const attr = (html, re) => (html.match(re) ?? [])[1]
const meta = (html, key, name) => attr(html, new RegExp(`<meta\\s+${key}="${name}"\\s+content="([^"]*)"`, 's'))
for (const page of ['index.html', '404.html', 'console-redirect.html']) {
  const html = read(page)
  check(/<html lang="en">/.test(html), `${page}: no lang`)
  check(/<meta name="viewport"/.test(html), `${page}: no viewport`)
  const title = attr(html, /<title>([^<]+)<\/title>/)
  check(Boolean(title) && title.includes('AccessLink'), `${page}: no title naming AccessLink`)
  check(/<link rel="icon" href="\/favicon\.svg"/.test(html), `${page}: no favicon`)
  check(!/(src|href)="https?:\/\/(?!accesslink\.store|app\.accesslink\.store)/.test(html), `${page}: links to a third party`)
}
const home = read('index.html')
const description = meta(home, 'name', 'description')
check(Boolean(description) && description.length >= 50 && description.length <= 160, `index.html: description missing or not 50-160 characters (${description?.length ?? 0})`)
check(attr(home, /<link rel="canonical" href="([^"]+)"/) === `${ORIGIN}/`, 'index.html: canonical is not the apex')
check(meta(home, 'name', 'robots') === 'index, follow', 'index.html: the landing page must be indexable')
for (const p of ['og:type', 'og:site_name', 'og:url', 'og:title', 'og:description']) check(Boolean(meta(home, 'property', p)), `index.html: no ${p}`)
check(meta(home, 'name', 'twitter:card') === 'summary', 'index.html: twitter:card should be "summary" until real artwork exists')
// No image until approved artwork exists; see the note in index.html.
check(!/property="og:image|name="twitter:image/.test(home), 'index.html: carries a sharing image; there is no approved artwork yet')
check((home.match(/<h1[\s>]/g) ?? []).length === 1, 'index.html: expected exactly one <h1>')
for (const page of ['404.html', 'console-redirect.html']) check(meta(read(page), 'name', 'robots') === 'noindex', `${page}: must be noindex`)
// Every image has an alt attribute (empty is right for the decorative mark).
for (const page of ['index.html', '404.html']) {
  for (const img of read(page).match(/<img\b[^>]*>/g) ?? []) check(/\balt="/.test(img), `${page}: <img> without alt: ${img}`)
}

// --- robots.txt and sitemap.xml ----------------------------------------------
const robots = read('robots.txt')
check(/^User-agent: \*$/m.test(robots) && !/^Disallow: \/\s*$/m.test(robots), 'robots.txt: the public site must be crawlable')
check(robots.includes(`Sitemap: ${ORIGIN}/sitemap.xml`), 'robots.txt: does not name the sitemap')
const sitemap = read('sitemap.xml')
check(sitemap.startsWith('<?xml version="1.0" encoding="UTF-8"?>') && sitemap.includes('<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'), 'sitemap.xml: not a sitemap document')
const locs = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1])
check(locs[0] === `${ORIGIN}/`, 'sitemap.xml: the landing page is not listed first')
check(locs.includes(`${ORIGIN}/docs/`) && locs.some((u) => u.includes('/docs/errors/')), 'sitemap.xml: the reference is missing')
check(new Set(locs).size === locs.length, 'sitemap.xml: duplicate URLs')
check(!locs.some((u) => /404|console-redirect/.test(u)), 'sitemap.xml: lists a page that is noindex')

// --- a Render-shaped server --------------------------------------------------
const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.yaml': 'application/yaml; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
  '.xml': 'application/xml; charset=utf-8',
  '.woff2': 'font/woff2',
}
const existing = (path) => {
  const file = join(DIST, path)
  if (!file.startsWith(DIST) || !existsSync(file)) return null
  if (statSync(file).isDirectory()) {
    if (!path.endsWith('/')) return null
    return existsSync(join(file, 'index.html')) ? join(file, 'index.html') : null
  }
  return file
}
const matchRule = (rule, path) => {
  const star = rule.source.indexOf('*')
  if (star < 0) return rule.source === path ? '' : null
  const prefix = rule.source.slice(0, star)
  return path.startsWith(prefix) ? path.slice(prefix.length) : null
}
const deployed = Object.fromEntries(headers.filter(([n]) => n !== 'Strict-Transport-Security'))
const server = createServer((req, res) => {
  const path = decodeURIComponent((req.url ?? '/').split('?')[0])
  let file = existing(path)
  if (!file) {
    for (const rule of routes) {
      const captured = matchRule(rule, path)
      if (captured === null) continue
      const destination = rule.destination.replace('*', captured)
      if (rule.type === 'redirect') {
        res.writeHead(301, { Location: destination })
        res.end()
        return
      }
      file = existing(destination)
      if (file) res.setHeader('X-Rewritten-To', destination)
      break
    }
  }
  if (!file) {
    res.writeHead(404, { 'Content-Type': TYPES['.html'], ...deployed })
    res.end(readFileSync(join(DIST, '404.html')))
    return
  }
  res.writeHead(200, { 'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream', ...deployed })
  res.end(readFileSync(file))
})
await new Promise((r) => server.listen(0, '127.0.0.1', r))
const local = `http://127.0.0.1:${server.address().port}`
const toLocal = (url) => url.replace(ORIGIN, local)

for (const path of ['/', '/robots.txt', '/sitemap.xml', '/favicon.svg', '/site.css', '/docs/']) {
  check((await fetch(local + path)).status === 200, `GET ${path} is not 200`)
}
for (const url of locs) check((await fetch(toLocal(url))).status === 200, `sitemap URL ${url} does not answer 200`)
notes.push(`${locs.length} sitemap URLs answer`)
for (const path of ['/no-such-page', '/docs/no-such-page', '/docs/errors/no_such_code', '/index.htm']) {
  const res = await fetch(local + path)
  const body = await res.text()
  check(res.status === 404 && body.includes('Page not found'), `GET ${path} answered ${res.status}; expected 404 with the 404 page`)
}
const bare = await fetch(`${local}/docs`, { redirect: 'manual' })
check(bare.status === 301 && bare.headers.get('location') === '/docs/', 'GET /docs does not redirect to /docs/')
for (const path of ['/login', '/redeem', '/people/MEM001', '/settings/api-credentials', '/platform/login', '/access/schedules']) {
  const res = await fetch(local + path)
  check(res.status === 200 && res.headers.get('x-rewritten-to') === '/console-redirect.html', `GET ${path} is not sent to the console-redirect page`)
}

// --- in a browser ------------------------------------------------------------
const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'phone-390', width: 390, height: 844 },
  { name: 'phone-360', width: 360, height: 740 },
]
// What each page must answer. /404 is not a file (the file is 404.html), so it
// is an unknown path like any other and must come back as one.
const PAGES = [
  { path: '/', status: 200, ready: 'h1' },
  { path: '/docs', status: 200, ready: 'text=Quick start >> visible=true' },
  { path: '/404', status: 404, ready: 'h1' },
  { path: '/404.html', status: 200, ready: 'h1' },
  { path: '/no-such-page', status: 404, ready: 'h1' },
  { path: '/docs/no-such-page', status: 404, ready: 'h1' },
]
const shots = process.env.SITE_SCREENSHOTS ? resolve(process.env.SITE_SCREENSHOTS) : null
if (shots) mkdirSync(shots, { recursive: true })

/*
 * EVERY ERROR ON EVERY PAGE IS A FAILURE, 404 pages included. Exactly one
 * message is expected and excused: the browser's own report that the DOCUMENT
 * it was sent to answered 404, on a visit made to get a 404 -- matched on that
 * document's exact URL, so a script error, a blocked resource, a missing
 * stylesheet or a CSP violation on the same page still fails.
 *
 * CSP violations are also reported by name from a `securitypolicyviolation`
 * listener installed before any page script runs (Playwright's init scripts
 * are not subject to the page's policy), so they are caught even if Chrome
 * changes how it words them on the console.
 */
/*
 * ONE KNOWN, EXCUSED VIOLATION: ZOD'S EVAL-CAPABILITY PROBE IN THE /docs VIEWER.
 *
 * The API reference is Scalar, bundled from npm, and Scalar bundles zod 4.
 * zod decides once, at first use, whether it may compile validators with
 * `new Function`. Its test is literally
 *
 *     try { const F = Function; return new F(""), true } catch { return false }
 *
 * Our Content-Security-Policy has no 'unsafe-eval', so the browser refuses that
 * call -- reports it as a violation -- and zod takes its no-eval path. Nothing
 * fails, nothing is evaluated, and the policy did exactly its job. The
 * production policy is NOT relaxed for this and must not be.
 *
 * The exception is pinned to that probe and nothing else. A violation is
 * excused only when EVERY one of these holds:
 *
 *   1. the page being checked is /docs, and the browser's document is /docs/;
 *   2. it is an ENFORCED script-src refusal of `eval`;
 *   3. its reported source is a real file under dist/docs/assets/;
 *   4. the code at the reported line and column is zod's probe itself:
 *      `new X(""),!0}catch{return!1}` directly after
 *      `try{const X=Function;return `, with zod's `includes("Cloudflare")`
 *      guard in front of it -- read from the built file, not assumed;
 *   5. it happens at most once per page load (zod caches the answer).
 *
 * Anything else -- eval from any other code, on any other page, a second probe,
 * any other directive -- fails the check like every other violation. If a
 * Scalar or zod upgrade changes the minified probe, condition 4 stops matching
 * and the check fails: that is deliberate, and means looking again. Every time
 * the exception is used, a NOTE is printed.
 */
const ZOD_PROBE = /includes\("Cloudflare"\)\)\)return!1;try\{const ([\w$]+)=Function;return $/
const ZOD_PROBE_CALL = /^new ([\w$]+)\(""\),!0\}catch\{return!1\}/
function isZodEvalProbe(v, pagePath, excusedSoFar) {
  if (pagePath !== '/docs' || excusedSoFar >= 1) return false
  if (v.documentURI !== `${local}/docs/`) return false
  if (v.effectiveDirective !== 'script-src' || v.blockedURI !== 'eval' || v.disposition !== 'enforce') return false
  if (!v.sourceFile?.startsWith(`${local}/docs/assets/`) || !v.sourceFile.endsWith('.js')) return false
  const file = join(DIST, decodeURIComponent(new URL(v.sourceFile).pathname))
  if (!file.startsWith(join(DIST, 'docs', 'assets')) || !existsSync(file)) return false
  const line = readFileSync(file, 'utf8').split('\n')[v.lineNumber - 1]
  if (line === undefined || !(v.columnNumber > 0)) return false
  const before = line.slice(Math.max(0, v.columnNumber - 1 - 200), v.columnNumber - 1)
  const at = line.slice(v.columnNumber - 1, v.columnNumber - 1 + 60)
  const guard = before.match(ZOD_PROBE)
  const call = at.match(ZOD_PROBE_CALL)
  return Boolean(guard && call && guard[1] === call[1])
}
const excusedNotes = []

async function watch(context) {
  const page = await context.newPage()
  const errors = []
  const thirdParty = new Set()
  let expected404 = null
  let current = '(none)'
  let excusedThisLoad = 0
  page.on('console', (m) => {
    if (m.type() !== 'error') return
    const where = m.location().url
    const text = m.text()
    if (expected404 !== null && where === expected404 && /status of 404/.test(text)) return
    if (text.startsWith('CSP violation: {')) {
      const v = JSON.parse(text.slice('CSP violation: '.length))
      const at = `${v.sourceFile || 'inline'}:${v.lineNumber}:${v.columnNumber}`.replace(local, '')
      if (isZodEvalProbe(v, current, excusedThisLoad)) {
        excusedThisLoad += 1
        const note = `NOTE: excused zod's eval-capability probe on ${current} (${at}); refused by the CSP as intended`
        excusedNotes.push(note)
        console.log(note)
        return
      }
      errors.push(`${current}: CSP violation: ${v.effectiveDirective} blocked ${v.blockedURI || 'an inline resource'} at ${at}`)
      return
    }
    errors.push(`${current}: ${text.slice(0, 200)} [${where || 'page'}]`)
  })
  page.on('pageerror', (e) => errors.push(`${current}: pageerror: ${e.message}`))
  page.on('request', (r) => { if (!r.url().startsWith(local)) thirdParty.add(new URL(r.url()).host) })
  // Everything the browser says about the violation, as data, so the
  // exception above can be decided on facts rather than on wording.
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      console.error('CSP violation: ' + JSON.stringify({
        effectiveDirective: e.effectiveDirective,
        blockedURI: e.blockedURI,
        disposition: e.disposition,
        documentURI: e.documentURI,
        sourceFile: e.sourceFile,
        lineNumber: e.lineNumber,
        columnNumber: e.columnNumber,
      }))
    })
  })
  // `as` names the page for the exception's purposes when a self-test has
  // to navigate somewhere else to get there (see the /docs imitation probe).
  const visit = async ({ path, status, ready, as }) => {
    expected404 = status === 404 ? local + path : null
    current = as ?? path
    // One page load per visit, so this is where "at most once per page load"
    // (condition 5 of the zod exception) restarts -- and nowhere else: the
    // reference rewrites its own URL hash as it scrolls, which is not a load.
    excusedThisLoad = 0
    const res = await page.goto(local + path, { waitUntil: 'networkidle' })
    try {
      await page.waitForSelector(ready, { timeout: 20_000 })
    } catch {
      throw new Error(`${path} never showed ${ready} (status ${res?.status()}; errors so far: ${errors.slice(-3).join(' | ') || 'none'})`)
    }
    return res
  }
  return { page, errors, thirdParty, visit }
}

const browser = await chromium.launch({ channel: 'chrome' })
try {
  // --- the detector detects --------------------------------------------------
  // Two probes, served under the deployed headers, that MUST be reported: an
  // inline script the policy blocks, and a missing image on a page that is
  // itself a 404. If either went unnoticed the checks below would prove nothing.
  {
    const context = await browser.newContext()
    const probe = await watch(context)
    const policy = Object.fromEntries(headers)
    await probe.page.route(`${local}/__probe-csp`, (route) => route.fulfill({
      status: 200,
      headers: { ...policy, 'Content-Type': 'text/html' },
      body: '<!doctype html><title>probe</title><h1>probe</h1><script>document.title = "ran"</script>',
    }))
    await probe.page.route(`${local}/__probe-404`, (route) => route.fulfill({
      status: 404,
      headers: { ...policy, 'Content-Type': 'text/html' },
      body: '<!doctype html><title>probe</title><h1>probe</h1><img src="/__probe-missing.png" alt="">',
    }))
    await probe.visit({ path: '/__probe-csp', status: 200, ready: 'h1' })
    check(probe.errors.some((e) => e.startsWith('/__probe-csp: CSP violation: script-src')),
      `the checker did not see a CSP violation it was shown (saw: ${probe.errors.join(' | ') || 'nothing'})`)
    const before = probe.errors.length
    await probe.visit({ path: '/__probe-404', status: 404, ready: 'h1' })
    const after = probe.errors.slice(before)
    check(after.some((e) => e.includes('/__probe-missing.png')), `the checker excused an error on a 404 page (saw: ${after.join(' | ') || 'nothing'})`)
    check(!after.some((e) => e.includes('/__probe-404]')), 'the checker reported the 404 it asked for as an error')
    await context.close()

    // The zod exception cannot be reached by imitation. With the checker
    // treating the page as /docs, /docs/ is answered by a probe page loading
    // two scripts: zod's probe copied VERBATIM, guard and all, served under
    // the REAL bundle's filename (so it passes the "file is in the build"
    // test and must fail on the code actually read from the build), and a
    // plain eval from a file that is not in the build at all. Both refusals
    // must be reported, and nothing excused. Navigated to directly: a route
    // does not see the request that follows the /docs redirect.
    const bundle = readdirSync(join(DIST, 'docs', 'assets')).find((name) =>
      name.endsWith('.js') && readFileSync(join(DIST, 'docs', 'assets', name), 'utf8').includes('includes("Cloudflare")'))
    if (check(Boolean(bundle), 'no built /docs bundle carries the zod probe; the imitation test has nothing to imitate')) {
      const docsContext = await browser.newContext()
      const docsProbe = await watch(docsContext)
      await docsProbe.page.route(`${local}/docs/`, (route) => route.fulfill({
        status: 200,
        headers: { ...policy, 'Content-Type': 'text/html' },
        body: `<!doctype html><title>probe</title><h1>probe</h1><script src="/docs/assets/${bundle}"></script><script src="/docs/assets/__probe-eval.js"></script>`,
      }))
      await docsProbe.page.route(`${local}/docs/assets/${bundle}`, (route) => route.fulfill({
        status: 200,
        headers: { ...policy, 'Content-Type': 'text/javascript' },
        body: 'var n=navigator;function z(){var e;if(typeof n<"u"&&((e=n==null?void 0:n.userAgent)!=null&&e.includes("Cloudflare")))return!1;try{const t=Function;return new t(""),!0}catch{return!1}}z();',
      }))
      await docsProbe.page.route(`${local}/docs/assets/__probe-eval.js`, (route) => route.fulfill({
        status: 200,
        headers: { ...policy, 'Content-Type': 'text/javascript' },
        body: 'try{eval("1")}catch{}',
      }))
      const notesBefore = excusedNotes.length
      await docsProbe.visit({ path: '/docs/', as: '/docs', status: 200, ready: 'h1' })
      const evals = docsProbe.errors.filter((e) => e.startsWith('/docs: CSP violation: script-src blocked eval'))
      check(evals.length === 2 && evals.some((e) => e.includes(bundle)) && evals.some((e) => e.includes('__probe-eval.js')),
        `an eval on /docs that is not the built zod probe was not reported (saw: ${docsProbe.errors.join(' | ') || 'nothing'})`)
      check(excusedNotes.length === notesBefore, `the zod exception excused an imitation: ${excusedNotes.slice(notesBefore).join(' | ')}`)
      await docsContext.close()
    }
    notes.push('the error detector catches a CSP violation, a broken resource on a 404 page, and an eval on /docs that only imitates the zod probe')
  }

  // --- every page, every width, both schemes ----------------------------------
  for (const scheme of ['light', 'dark']) {
    for (const vp of VIEWPORTS) {
      const label = `${vp.name}/${scheme}`
      const context = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, colorScheme: scheme, hasTouch: vp.name !== 'desktop' })
      const { page, errors, thirdParty, visit } = await watch(context)

      for (const target of PAGES) {
        const res = await visit(target)
        // /docs answers through its redirect; the final document is what counts.
        check(res?.status() === target.status, `${label} ${target.path}: answered ${res?.status()}, expected ${target.status}`)
        if (target.status === 404) check((await page.textContent('h1'))?.includes('Page not found'), `${label} ${target.path}: not the 404 page`)
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
        check(overflow <= 1, `${label} ${target.path}: scrolls horizontally by ${overflow}px`)
        if (shots) await page.screenshot({ path: join(shots, `${vp.name}-${scheme}${target.path.replace(/[^a-z0-9]+/gi, '-')}.png`), fullPage: true })
      }

      await visit(PAGES[0])
      // The call to action is on screen without scrolling.
      const cta = page.getByRole('link', { name: 'Learn more' })
      const box = await cta.boundingBox()
      check(box !== null && box.y + box.height <= vp.height, `${label}: "Learn more" is below the fold`)
      // ...and goes somewhere that exists.
      await cta.click()
      check(await page.evaluate(() => location.hash === '#how-it-works' && Boolean(document.getElementById('how-it-works'))), `${label}: "Learn more" does not reach the how-it-works section`)
      // Body text contrast, both schemes.
      const ratio = await page.evaluate(() => {
        const lum = (rgb) => {
          const [r, g, b] = rgb.match(/\d+(\.\d+)?/g).slice(0, 3).map(Number).map((v) => v / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
          return 0.2126 * r + 0.7152 * g + 0.0722 * b
        }
        const worst = [...document.querySelectorAll('.hero__lead, .card p, .hero__note, .footer a, .masthead__nav a, .button--primary')]
          .map((el) => {
            let node = el
            let bg = getComputedStyle(node).backgroundColor
            while (node && /rgba?\(0, 0, 0, 0\)|transparent/.test(bg)) { node = node.parentElement; bg = node ? getComputedStyle(node).backgroundColor : 'rgb(255, 255, 255)' }
            const a = lum(getComputedStyle(el).color), b = lum(bg)
            return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
          })
        return Math.min(...worst)
      })
      check(ratio >= 4.5, `${label}: lowest text contrast is ${ratio.toFixed(2)}:1, below 4.5:1`)
      check(errors.length === 0, `${label}: console errors: ${errors.slice(0, 3).join(' | ')}`)
      check(thirdParty.size === 0, `${label}: third-party requests: ${[...thirdParty].join(', ')}`)
      await context.close()
    }
  }
  notes.push(`${PAGES.map((p) => p.path).join(', ')} at 1440/390/360 in light and dark: right status, no console or CSP errors, no overflow, CTA above the fold`)

  // --- the console's old addresses ---------------------------------------------
  // Path, query and fragment arrive intact on app.accesslink.store, encoded
  // characters included, and nothing in the path can change the host.
  {
    const context = await browser.newContext()
    const { page, errors } = await watch(context)
    await page.route(`${CONSOLE}/**`, (route) => route.fulfill({ status: 200, contentType: 'text/html', body: '<title>console</title><h1>console</h1>' }))
    const cases = [
      ['/redeem?token=abc123&next=%2Fpeople#frag', `${CONSOLE}/redeem?token=abc123&next=%2Fpeople#frag`],
      ['/login?error=google_denied&next=%2Fsites', `${CONSOLE}/login?error=google_denied&next=%2Fsites`],
      ['/people/MEM001', `${CONSOLE}/people/MEM001`],
      ['/sites/a%20b%2Fc?x=1&y=%26#h%20i', `${CONSOLE}/sites/a%20b%2Fc?x=1&y=%26#h%20i`],
      ['/platform/companies/42', `${CONSOLE}/platform/companies/42`],
      ['/people//evil.example/x', `${CONSOLE}/people//evil.example/x`],
    ]
    for (const [from, to] of cases) {
      await page.goto(local + from)
      await page.waitForURL(`${CONSOLE}/**`, { timeout: 5000 }).catch(() => {})
      check(page.url() === to, `${from} forwarded to ${page.url()}, expected ${to}`)
      check(new URL(page.url()).host === 'app.accesslink.store', `${from} left the console's host: ${page.url()}`)
    }
    check(errors.length === 0, `console redirect: errors: ${errors.slice(0, 3).join(' | ')}`)
    await context.close()

    // Without script nothing forwards, so the page must not say it is
    // forwarding, and its link is the console's home.
    const noScript = await browser.newContext({ javaScriptEnabled: false })
    const still = await noScript.newPage()
    await still.goto(`${local}/redeem?token=abc123`)
    check(still.url() === `${local}/redeem?token=abc123`, 'the console redirect moved without script')
    const text = (await still.textContent('main')) ?? ''
    check(!/taking you|redirecting|you will be|in a moment/i.test(text), `without script the redirect page claims to be forwarding: "${text.trim()}"`)
    check((await still.getAttribute('#console-link', 'href')) === `${CONSOLE}/`, 'without script the link is not the console home')
    await noScript.close()
    notes.push(`${cases.length} old console addresses forward intact; the no-script fallback links to the console home and claims nothing`)
  }
} catch (error) {
  failures.push(`browser pass failed: ${String(error.message).split(/\r?\n/)[0]}`)
} finally {
  await browser.close()
  server.close()
}

// --- the reference, under this service's rules and policy ---------------------
try {
  execFileSync(process.execPath, [join(ROOT, '..', 'apidocs', 'scripts', 'check-site.mjs')], {
    stdio: 'inherit',
    env: { ...process.env, DOCS_SERVICE: 'accesslink-site', DOCS_SITE_ROOT: DIST },
  })
  notes.push('the API reference check passed under accesslink-site')
} catch {
  failures.push('the API reference check failed under accesslink-site (output above)')
}

if (failures.length > 0) {
  console.error(`site check: ${failures.length} failure(s)`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
if (excusedNotes.length > 0) notes.push(`${excusedNotes.length} zod eval-capability probe(s) on /docs excused, each noted above`)
console.log(`site check passed: ${notes.join('; ')}`)
