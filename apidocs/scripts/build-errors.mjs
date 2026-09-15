#!/usr/bin/env node
/*
 * Generate the error-code pages.
 *
 * The API puts `doc_url: https://accesslink.store/docs/errors/<code>` on every
 * public error it serves (models/api_errors.go). This turns the registered
 * list in openapi.yaml (x-accesslink-error-codes, held equal to the server's
 * list by openapi_docs_test.go) into one static page per code under
 * dist/errors/<code>/index.html -- plain HTML, no script, so the page a
 * developer lands on from an error body is readable anywhere -- plus an
 * index at dist/errors/ and a sitemap.
 *
 * Runs after `vite build`, so dist/ already holds the reference.
 */
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve, join } from 'node:path'
import YAML from 'yaml'

const ROOT = resolve(import.meta.dirname, '..')
const DIST = join(ROOT, 'dist')
if (!existsSync(join(DIST, 'index.html'))) {
  console.error('dist/index.html is missing: run `vite build` first')
  process.exit(1)
}

const doc = YAML.parse(readFileSync(join(ROOT, 'openapi.yaml'), 'utf8'))
const codes = doc['x-accesslink-error-codes']
// The public address of the site and the path it is mounted at. The pages
// link with absolute paths under BASE so they work wherever dist/ is copied.
const BASE = '/docs'
const site = `https://accesslink.store${BASE}`

const escape = (s) =>
  String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

const TYPE_NOTES = {
  authentication_error: 'The credential could not be accepted. Every 401 carries `WWW-Authenticate: Bearer realm="accesslink"`.',
  permission_error: 'The credential is valid but may not do this. Checked before any lookup, so it is reported even for a resource that does not exist.',
  invalid_request_error: 'The request itself is wrong. `param` names the field or query parameter at fault; fix it and send the request again under a new Idempotency-Key.',
  not_found_error: 'No such resource in your company. The same answer for one that never existed and one that belongs to another company.',
  conflict_error: 'The request conflicts with the current state. Read the body, decide, and send a corrected request.',
  gone_error: 'What the request refers to is no longer available. Start again from the first page.',
  rate_limit_error: 'You are sending too fast. Wait for `Retry-After` seconds and pace against the `RateLimit-*` headers.',
  api_error: 'The failure is on our side. Retry after `Retry-After` on a 503; quote the `request_id` to support on a 500.',
}

const page = ({ title, heading, body, canonical }) => `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>${escape(title)}</title>
<link rel="canonical" href="${canonical}">
<link rel="icon" href="${BASE}/favicon.svg" type="image/svg+xml">
<style>
  :root { color-scheme: light dark; --accent: #1f5fbf; --muted: #5d6b7b; --line: #d6dde6; --code: #f1f4f8; }
  @media (prefers-color-scheme: dark) { :root { --accent: #8ab8ff; --muted: #97a4b4; --line: #2b3542; --code: #161d27; } }
  body { margin: 0; font: 16px/1.55 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
  main { max-width: 46rem; margin: 0 auto; padding: 2rem 1rem 4rem; }
  a { color: var(--accent); }
  a:focus-visible, button:focus-visible { outline: 3px solid var(--accent); outline-offset: 2px; }
  nav.crumbs { font-size: 0.9rem; color: var(--muted); margin-bottom: 1.5rem; }
  h1 { font-size: 1.6rem; margin: 0 0 0.25rem; overflow-wrap: anywhere; }
  .status { display: inline-block; font-weight: 700; padding: 0.1rem 0.5rem; border-radius: 0.4rem; background: var(--code); margin-right: 0.5rem; }
  code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; background: var(--code); border-radius: 0.3rem; }
  code { padding: 0.05rem 0.3rem; overflow-wrap: anywhere; }
  pre { padding: 0.9rem 1rem; overflow-x: auto; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 0.35rem 1rem; }
  dt { color: var(--muted); }
  dd { margin: 0; }
  table { border-collapse: collapse; width: 100%; display: block; overflow-x: auto; }
  th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--line); vertical-align: top; }
  .reserved { color: var(--muted); font-style: italic; }
  footer { margin-top: 3rem; font-size: 0.9rem; color: var(--muted); border-top: 1px solid var(--line); padding-top: 1rem; }
</style>
</head>
<body>
<main>
<nav class="crumbs" aria-label="Breadcrumb"><a href="${BASE}/">AccessLink API reference</a> › <a href="${BASE}/errors/">Errors</a>${heading ? ' › ' + escape(heading) : ''}</nav>
${body}
<footer>Generated from <a href="${BASE}/openapi.yaml">openapi.yaml</a>. Codes are additive only: a code is never renamed, removed or given a different meaning.</footer>
</main>
</body>
</html>
`

// One page per code.
for (const c of codes) {
  const dir = join(DIST, 'errors', c.code)
  mkdirSync(dir, { recursive: true })
  const example = {
    error: {
      type: c.type,
      code: c.code,
      message: c.message,
      request_id: '7bd8490b23dbdbcc',
      doc_url: `${site}/errors/${c.code}`,
    },
  }
  const body = `
<h1><code>${escape(c.code)}</code></h1>
<p><span class="status" aria-label="HTTP status">${c.status}</span> <code>${escape(c.type)}</code>${c.reserved ? ' <span class="reserved">— registered and reserved; not served by any route in this version</span>' : ''}</p>
<p>${escape(c.message)}</p>
<dl>
  <dt>Status</dt><dd>Always <code>${c.status}</code> — one code, one status.</dd>
  <dt>Type</dt><dd><code>${escape(c.type)}</code>. ${escape(TYPE_NOTES[c.type] ?? '')}</dd>
  <dt>Branch on</dt><dd><code>code</code> and the HTTP status. The <code>message</code> is for a person and is not stable.</dd>
</dl>
<h2>Example body</h2>
<pre><code>${escape(JSON.stringify(example, null, 2))}</code></pre>
<p>See <a href="${BASE}/#description/errors">Errors</a> in the reference for the full table, and <a href="${BASE}/#description/rate-limits">Rate limits</a> for the headers a client paces against.</p>
`
  // Written twice, as errors/<code>/index.html and errors/<code>.html: the
  // API's doc_url has no trailing slash, and static hosts differ on which of
  // the two files answers a clean URL. Both forms resolve either way.
  const html = page({ title: `${c.code} — AccessLink API errors`, heading: c.code, body, canonical: `${site}/errors/${c.code}` })
  writeFileSync(join(dir, 'index.html'), html)
  writeFileSync(join(DIST, 'errors', `${c.code}.html`), html)
}

// The index of every code.
const byType = new Map()
for (const c of codes) byType.set(c.type, [...(byType.get(c.type) ?? []), c])
const rows = [...byType.entries()]
  .map(
    ([type, list]) => `
<tr><th scope="row"><code>${escape(type)}</code></th><td>${list[0].status}</td><td>${list
      .map((c) => `<a href="${BASE}/errors/${c.code}"><code>${escape(c.code)}</code></a>${c.reserved ? ' <span class="reserved">(reserved)</span>' : ''}`)
      .join('<br>')}</td></tr>`,
  )
  .join('')
mkdirSync(join(DIST, 'errors'), { recursive: true })
writeFileSync(
  join(DIST, 'errors', 'index.html'),
  page({
    title: 'Error codes — AccessLink API',
    heading: '',
    canonical: `${site}/errors/`,
    body: `
<h1>Error codes</h1>
<p>Every public-API error is one object under <code>error</code> with a stable <code>code</code>. The API puts a link to the matching page below in every error body's <code>doc_url</code>.</p>
<table>
<thead><tr><th scope="col">Type</th><th scope="col">Status</th><th scope="col">Codes</th></tr></thead>
<tbody>${rows}</tbody>
</table>
`,
  }),
)

// A sitemap for the crawlers, and a manifest the render check reads.
const urls = [`${site}/`, `${site}/errors/`, ...codes.map((c) => `${site}/errors/${c.code}`)]
writeFileSync(join(DIST, 'sitemap.txt'), urls.join('\n') + '\n')

console.log(`error pages: ${codes.length} codes under dist/errors/, index and sitemap written`)
