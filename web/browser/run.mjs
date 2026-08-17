import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { chromium } from 'playwright-core'

import { mockApi } from './api.mjs'
import { serve } from './server.mjs'

/**
 * The real-browser pass.
 *
 * FE-01 recorded that every frontend test was jsdom, and that no browser or
 * device verification had been done. This is that verification, and it checks
 * the things jsdom structurally cannot:
 *
 *   COLOUR CONTRAST AS RENDERED. The unit suite computes ratios from the token
 *   palette, which proves the palette is sound. This runs axe against the real
 *   page, so it also catches a colour applied where the palette did not intend
 *   one — a hardcoded hex, an inherited colour, a badge on a background nobody
 *   paired it with.
 *
 *   LAYOUT AT A REAL VIEWPORT. Whether the page scrolls sideways on a phone,
 *   whether a table becomes cards at the breakpoint, whether a touch target is
 *   big enough to hit. Every one of those is a computed box, and jsdom has no
 *   boxes.
 *
 *   THE CSP ACTUALLY APPLYING. The meta tag is emitted by the build; only a
 *   browser enforces it. A policy that blocked the app's own bundle would be
 *   invisible to every other test and fatal in production.
 *
 * IT RUNS AGAINST THE BUILD, not the dev server: the dev server injects styles
 * inline and does not apply the built index.html, so a CSP or contrast result
 * from it would be measuring something that never ships.
 *
 * The API is intercepted at the network layer rather than mocked in the app, so
 * the bundle under test is byte-for-byte the one that would deploy.
 */

const AXE = join(process.cwd(), 'node_modules', 'axe-core', 'axe.min.js')
const DIST = join(process.cwd(), 'dist')

/** Desktop, tablet, and a small phone — the last is the one that finds things. */
const VIEWPORTS = [
  { name: 'desktop', width: 1280, height: 900 },
  { name: 'tablet', width: 834, height: 1112 },
  { name: 'phone', width: 360, height: 740 },
]

const SCREENS = [
  { name: 'overview', path: '/', ready: 'h1' },
  { name: 'people', path: '/people', ready: 'table, .state--empty' },
  { name: 'terminals', path: '/terminals', ready: 'table, .state--empty' },
  { name: 'terminal detail', path: '/terminals/AT-0001', ready: '.lifecycle' },
  { name: 'sites', path: '/sites', ready: 'table, .state--empty' },
  // The site detail page carries the offline-policy radio group: three options,
  // each with a paragraph of consequence, which is the widest block of prose
  // inside a form control anywhere in the console and the one most likely to
  // overflow a phone.
  { name: 'site detail', path: '/sites/site-a', ready: '.choice-list' },
  { name: 'operators', path: '/operators', ready: 'table, .state--empty' },
  { name: 'activity', path: '/activity', ready: 'table, .state--empty' },
  { name: 'events', path: '/events', ready: 'table, .state--empty' },
  { name: 'schedules', path: '/access/schedules', ready: '.rule-list, .notice' },
  { name: 'applications', path: '/settings/applications', ready: '.capability-list' },
  { name: 'firmware', path: '/settings/firmware', ready: '.rule-list, .notice' },
  { name: 'settings', path: '/settings', ready: 'h1' },
]

const failures = []
const notes = []

function check(condition, message) {
  if (condition) return true
  failures.push(message)
  return false
}

/**
 * axe's source, loaded once.
 *
 * INJECTED THROUGH THE DEBUGGER, NOT AS A <script> TAG. The obvious
 * `addScriptTag({content})` fails — and its failure was the first real result
 * this pass produced: the shipped CSP refused it with "Executing inline script
 * violates the following Content Security Policy directive: script-src 'self'".
 * That is the policy doing exactly its job, on the real page, and it is worth
 * more than any assertion here.
 *
 * Evaluating the source instead runs it through CDP, which is not subject to
 * the page's CSP — so the tool can be introduced without weakening the thing
 * being measured. Relaxing the policy for the test would have measured a page
 * that never ships.
 */
const AXE_SOURCE = readFileSync(AXE, 'utf8')

async function runAxe(page, context) {
  await page.evaluate(AXE_SOURCE)
  const violations = await page.evaluate(async () => {
    const results = await window.axe.run(document, {
      runOnly: {
        type: 'tag',
        values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'],
      },
      resultTypes: ['violations'],
    })
    return results.violations.map((violation) => ({
      id: violation.id,
      impact: violation.impact,
      help: violation.help,
      nodes: violation.nodes.slice(0, 3).map((node) => node.html),
    }))
  })

  for (const violation of violations) {
    failures.push(
      `${context}: [${violation.impact}] ${violation.id} — ${violation.help}\n      ${violation.nodes.join('\n      ')}`,
    )
  }
  return violations.length
}

/**
 * Scans the built assets for anything that looks like a credential.
 *
 * PATTERNS FOR THIS PLATFORM'S OWN SECRET SHAPES, not a generic entropy sweep:
 * a site provisioning key is `ats_` + 64 hex and a device credential is `atd_`,
 * both of which are unmistakable and neither of which has any business in a
 * bundle. Plus the generic shapes — a private key block, an inlined VITE_
 * variable whose name says secret.
 *
 * A false positive here is cheap; a missed one is a key served to every visitor.
 */
function scanBundle() {
  const patterns = [
    [/ats_[0-9a-f]{32,}/i, 'a site provisioning key'],
    [/atd_[0-9a-f]{32,}/i, 'a device credential'],
    [/-----BEGIN [A-Z ]*PRIVATE KEY-----/, 'a private key'],
    [/VITE_[A-Z_]*(SECRET|TOKEN|PASSWORD|KEY)[A-Z_]*\s*[:=]\s*["'][^"']+["']/, 'an inlined secret env var'],
  ]

  const assets = join(DIST, 'assets')
  for (const file of readdirSync(assets)) {
    const content = readFileSync(join(assets, file), 'utf8')
    for (const [pattern, what] of patterns) {
      const match = content.match(pattern)
      if (match) {
        failures.push(`assets/${file} contains ${what}: ${match[0].slice(0, 24)}…`)
      }
    }
  }
  notes.push(`${readdirSync(assets).length} built assets scanned for credentials`)
}

async function main() {
  const site = await serve(DIST)
  const browser = await chromium.launch({ channel: 'chrome' })

  try {
    const version = browser.version()
    notes.push(`Chrome ${version}`)

    for (const viewport of VIEWPORTS) {
      const context = await browser.newContext({
        viewport: { width: viewport.width, height: viewport.height },
        // A phone is a touch device, and a hover-only affordance is a real bug
        // there. Telling the browser so makes any hover media query behave as
        // it would on hardware.
        hasTouch: viewport.name === 'phone',
        deviceScaleFactor: viewport.name === 'phone' ? 3 : 1,
      })
      const page = await context.newPage()
      await mockApi(page)

      // A CSP violation, a failed request or an uncaught exception must fail the
      // run rather than be swallowed. These are exactly the faults that only a
      // browser reports.
      page.on('console', (message) => {
        const text = message.text()
        if (/Content Security Policy/i.test(text)) {
          failures.push(`${viewport.name}: CSP blocked something the app needs — ${text}`)
        }
      })
      page.on('pageerror', (error) => {
        failures.push(`${viewport.name}: uncaught exception — ${error.message}`)
      })

      for (const screen of SCREENS) {
        await page.goto(`${site.url}${screen.path}`, { waitUntil: 'networkidle' })
        await page.waitForSelector(screen.ready, { timeout: 10_000 }).catch(() => {
          failures.push(`${viewport.name}/${screen.name}: never rendered (${screen.ready})`)
        })

        const label = `${viewport.name}/${screen.name}`

        // --- axe, with contrast ENABLED because this is a real browser -------
        await runAxe(page, label)

        // --- the page must not scroll sideways -------------------------------
        const overflow = await page.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }))
        check(
          overflow.scrollWidth <= overflow.clientWidth + 1,
          `${label}: the page scrolls horizontally (${overflow.scrollWidth} > ${overflow.clientWidth}). ` +
            `A horizontally scrolling document breaks every other screen.`,
        )
      }

      // --- responsive behaviour, checked where it actually changes -----------
      await page.goto(`${site.url}/terminals`, { waitUntil: 'networkidle' })
      const layout = await page.evaluate(() => {
        const head = document.querySelector('.table thead')
        const firstCell = document.querySelector('.table tbody td')
        return {
          // MEASURED, not read off `display`. The header row is hidden by
          // clipping rather than by `display: none`, deliberately — clipping
          // keeps it in the accessibility tree — so its computed `display` is
          // unchanged and only its BOX tells you whether it is on screen. This
          // check originally read the property and reported a failure that was
          // not there, which is its own small argument for measuring in a real
          // browser rather than reasoning about the stylesheet.
          headerHeight: head ? Math.round(head.getBoundingClientRect().height) : null,
          cellLabel: firstCell
            ? getComputedStyle(firstCell, '::before').content
            : null,
        }
      })

      if (viewport.name === 'phone') {
        // Below the breakpoint each row becomes a card and every cell carries
        // its column name, because a table cannot shrink below its columns.
        check(
          layout.headerHeight !== null && layout.headerHeight <= 1,
          `phone: the table header row still occupies ${layout.headerHeight}px; rows have not become cards`,
        )
        check(
          typeof layout.cellLabel === 'string' &&
            layout.cellLabel !== 'none' &&
            layout.cellLabel.length > 2,
          `phone: cells carry no column label, so a card is a list of unidentified values (got ${layout.cellLabel})`,
        )
      } else {
        check(
          (layout.headerHeight ?? 0) > 1,
          `${viewport.name}: the table header row is not displayed above the breakpoint`,
        )
      }

      // --- touch targets ----------------------------------------------------
      if (viewport.name === 'phone') {
        const small = await page.evaluate(() => {
          const tooSmall = []
          for (const control of document.querySelectorAll('button, a, input, select')) {
            const box = control.getBoundingClientRect()
            if (box.width === 0 && box.height === 0) continue
            // 24px is the WCAG 2.2 minimum (2.5.8, AA). 44px is the comfortable
            // target and not a conformance requirement, so this holds the line
            // at the one that is.
            if (box.height < 24 || box.width < 24) {
              tooSmall.push(`${control.tagName.toLowerCase()} "${(control.textContent ?? '').trim().slice(0, 30)}" ${Math.round(box.width)}x${Math.round(box.height)}`)
            }
          }
          return tooSmall
        })
        for (const control of small) {
          failures.push(`phone: touch target below 24x24 — ${control}`)
        }
      }

      // --- a dialog, which is where the danger colours live -----------------
      //
      // Modals are swept separately because nothing on a page reaches them: the
      // destructive confirmations carry the danger fill, the typed-phrase input
      // and the only `aria-modal` in the product, and none of it is rendered
      // until something is opened.
      await page.goto(`${site.url}/terminals/AT-0001`, { waitUntil: 'networkidle' })
      const revoke = page.locator('button', { hasText: /^Revoke$/ }).first()
      if ((await revoke.count()) > 0) {
        await revoke.click()
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 })
        await runAxe(page, `${viewport.name}/revoke dialog`)

        // The dialog must fit the viewport it is on. A modal taller than a
        // phone with its confirm button below the fold is a modal that cannot
        // be completed.
        const fits = await page.evaluate(() => {
          const dialog = document.querySelector('[role="dialog"]')
          if (!dialog) return null
          const box = dialog.getBoundingClientRect()
          return {
            width: Math.round(box.width),
            viewportWidth: window.innerWidth,
            overflowsX: box.right > window.innerWidth + 1 || box.left < -1,
          }
        })
        check(
          fits !== null && !fits.overflowsX,
          `${viewport.name}: the dialog overflows the viewport horizontally (${fits?.width}px in ${fits?.viewportWidth}px)`,
        )

        await page.keyboard.press('Escape')
      }

      // --- the one-time credential panel ------------------------------------
      //
      // Swept separately for the same reason the revoke dialog is: it is only
      // reachable through two interactions, and it is where a credential, a
      // warning fill, a copy control and an acknowledgement checkbox all sit
      // inside a wide dialog. On a phone that is the densest thing the console
      // renders, and the acknowledgement checkbox is the control that must not
      // fall below the touch-target floor.
      await page.goto(`${site.url}/sites/site-a`, { waitUntil: 'networkidle' })
      const provision = page.locator('button', { hasText: /^Provision a terminal$/ }).first()
      // Checked rather than skipped silently. A sweep that quietly does nothing
      // reports "no violations" and has measured nothing, which is the failure
      // mode a conditional block invites.
      if (check(
        (await provision.count()) > 0,
        `${viewport.name}: the site page offers no way to provision a terminal, so the claim-code panel was never swept`,
      )) {
        await provision.click()
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 })
        await page.fill('input[type="text"]', 'AT-0042')
        await page.locator('button', { hasText: /^Issue claim code$/ }).click()
        await page.waitForSelector('.credential', { timeout: 5000 })
        await runAxe(page, `${viewport.name}/claim code panel`)

        const credentialFits = await page.evaluate(() => {
          const panel = document.querySelector('.credential')
          if (!panel) return null
          const box = panel.getBoundingClientRect()
          return { overflowsX: box.right > window.innerWidth + 1 || box.left < -1 }
        })
        check(
          credentialFits !== null && !credentialFits.overflowsX,
          `${viewport.name}: the claim-code panel overflows the viewport horizontally`,
        )
      }

      // --- the focus ring is actually visible -------------------------------
      await page.goto(`${site.url}/people`, { waitUntil: 'networkidle' })
      await page.keyboard.press('Tab')
      const focusRing = await page.evaluate(() => {
        const active = document.activeElement
        if (!active || active === document.body) return null
        const style = getComputedStyle(active)
        return {
          tag: active.tagName.toLowerCase(),
          outlineWidth: style.outlineWidth,
          outlineStyle: style.outlineStyle,
        }
      })
      check(
        focusRing !== null,
        `${viewport.name}: pressing Tab from the top of the page focuses nothing`,
      )
      if (focusRing) {
        check(
          focusRing.outlineStyle !== 'none' && Number.parseFloat(focusRing.outlineWidth) > 0,
          `${viewport.name}: the first focused element (${focusRing.tag}) draws no focus ring`,
        )
      }

      await context.close()
    }

    // --- the CSP is present in what actually ships --------------------------
    const context = await browser.newContext()
    const page = await context.newPage()
    await mockApi(page)
    await page.goto(`${site.url}/`, { waitUntil: 'networkidle' })
    const csp = await page.evaluate(
      () =>
        document
          .querySelector('meta[http-equiv="Content-Security-Policy"]')
          ?.getAttribute('content') ?? null,
    )
    check(csp !== null, 'the built page carries no Content-Security-Policy meta tag')
    check(
      csp?.includes("default-src 'none'"),
      'the shipped CSP does not deny by default',
    )
    // The app rendered at all, which is the proof that the policy does not block
    // its own bundle — a mistake no other test could see.
    check(
      (await page.locator('h1').count()) > 0,
      'the app did not render under its own CSP',
    )
    await context.close()

    // --- nothing secret is IN the shipped bundle ----------------------------
    //
    // The static source scan in src/security/security.test.ts proves no code
    // reads a secret from storage. This proves none was BAKED IN — an API key
    // pasted into a constant, a development token left in a fixture that got
    // imported, a .env value inlined by the bundler because it was prefixed
    // VITE_. That last one is the realistic mistake: Vite inlines every VITE_*
    // variable into the bundle by design, and somebody adding VITE_API_TOKEN
    // would publish it to every visitor without a single line of suspicious
    // code.
    scanBundle()
  } finally {
    await browser.close()
    await site.close()
  }

  console.log(`\nBrowser pass: ${notes.join(', ')}`)
  console.log(`${VIEWPORTS.length} viewports x ${SCREENS.length} screens, axe with contrast enabled\n`)

  if (failures.length > 0) {
    console.error(`FAILED (${failures.length}):\n`)
    for (const failure of failures) console.error(`  - ${failure}`)
    process.exitCode = 1
    return
  }

  console.log('No violations, no horizontal overflow, no undersized touch targets.')
}

await main()
