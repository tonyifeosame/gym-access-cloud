#!/usr/bin/env node
/*
 * Build the public API documentation into a static site's dist/docs/.
 *
 * Two sites publish it. The public site (../site, accesslink.store) is the one
 * https://accesslink.store/docs is served from once the domains are split; it
 * passes its own dist/ as the first argument. The console still publishes a
 * copy into its dist/docs (no argument) so that the reference keeps answering
 * on accesslink.store for as long as that domain still points at the console
 * service -- deploy order is not something a build can see. Remove the
 * console's copy once the apex has moved (see docs/render-free-deployment.md).
 *
 * The docs (../apidocs) are a separate Vite project with base /docs/. They
 * are published as part of the console's static site so that
 * https://accesslink.store/docs is served from the same origin as the
 * console -- real files under dist/docs/, which Render serves ahead of the
 * console's SPA rewrite. This runs as the console's `postbuild` hook, so the
 * service's build command (`npm ci && npm run build`) needs nothing extra.
 *
 * Their own build validates the OpenAPI document and its examples before
 * anything is emitted, so a docs regression fails the console build here
 * rather than shipping.
 */
import { execFileSync } from 'node:child_process'
import { cpSync, existsSync, readdirSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

const WEB = resolve(import.meta.dirname, '..')
const APIDOCS = resolve(WEB, '..', 'apidocs')
const SOURCE = join(APIDOCS, 'dist')
const SITE_DIST = process.argv[2] ? resolve(process.argv[2]) : join(WEB, 'dist')
const TARGET = join(SITE_DIST, 'docs')

if (!existsSync(join(SITE_DIST, 'index.html'))) {
  console.error(`build-docs: ${SITE_DIST}/index.html is missing; run the site's own build first`)
  process.exit(1)
}

const npm = (args) => execFileSync('npm', args, { cwd: APIDOCS, stdio: 'inherit', shell: process.platform === 'win32' })

// A clean install from the lockfile when there is none (CI); a developer's
// existing install is kept.
if (!existsSync(join(APIDOCS, 'node_modules'))) npm(['ci', '--no-audit', '--no-fund'])
npm(['run', 'build'])

if (!existsSync(join(SOURCE, 'index.html')) || !existsSync(join(SOURCE, 'openapi.yaml'))) {
  console.error('build-docs: apidocs/dist is incomplete')
  process.exit(1)
}
rmSync(TARGET, { recursive: true, force: true })
cpSync(SOURCE, TARGET, { recursive: true })
const size = (dir) => {
  let total = 0
  const walk = (d) => {
    for (const entry of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, entry.name)
      entry.isDirectory() ? walk(p) : (total += statSync(p).size)
    }
  }
  walk(dir)
  return total
}
console.log(`build-docs: published apidocs/dist to ${TARGET} (${(size(TARGET) / 1024 / 1024).toFixed(1)} MB)`)
