#!/usr/bin/env node
/*
 * Build accesslink.store into dist/.
 *
 *   1. public/ is copied as it is: the landing page, the 404 page, the
 *      console-redirect page, robots.txt, the stylesheet and the favicon. No
 *      bundler: the page is static and should stay that way.
 *   2. The API reference is built and published into dist/docs by the same
 *      script the console uses (web/scripts/build-docs.mjs), so there is one
 *      way the reference reaches a site, not two.
 *   3. sitemap.xml is written from the pages this site wants indexed -- the
 *      landing page -- plus every URL in the reference's own sitemap.txt,
 *      which build-errors.mjs derives from openapi.yaml. The 404 and
 *      console-redirect pages are deliberately not in it.
 *
 * Needs nothing installed here; the reference's build installs its own.
 */
import { execFileSync } from 'node:child_process'
import { cpSync, existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dirname, '..')
const DIST = join(ROOT, 'dist')
const ORIGIN = 'https://accesslink.store'

rmSync(DIST, { recursive: true, force: true })
cpSync(join(ROOT, 'public'), DIST, { recursive: true })

execFileSync(process.execPath, [join(ROOT, '..', 'web', 'scripts', 'build-docs.mjs'), DIST], { stdio: 'inherit' })

const docsSitemap = join(DIST, 'docs', 'sitemap.txt')
if (!existsSync(docsSitemap)) {
  console.error('site build: dist/docs/sitemap.txt is missing; the reference build did not finish')
  process.exit(1)
}
const docsUrls = readFileSync(docsSitemap, 'utf8').split(/\r?\n/).map((u) => u.trim()).filter(Boolean)
for (const url of docsUrls) {
  if (!url.startsWith(`${ORIGIN}/docs/`)) {
    console.error(`site build: ${url} in the reference's sitemap is not under ${ORIGIN}/docs/`)
    process.exit(1)
  }
}

const escape = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
const urls = [`${ORIGIN}/`, ...docsUrls]
writeFileSync(
  join(DIST, 'sitemap.xml'),
  '<?xml version="1.0" encoding="UTF-8"?>\n' +
    '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n' +
    urls.map((u) => `  <url><loc>${escape(u)}</loc></url>\n`).join('') +
    '</urlset>\n',
)

console.log(`site build: dist/ written, sitemap.xml lists ${urls.length} URLs`)
