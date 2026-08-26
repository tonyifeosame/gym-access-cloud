import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { chromium } from 'playwright-core'

import { SESSION, mockApi } from './api.mjs'
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

/**
 * Desktop, tablet and two phones — the small ones are what find things.
 *
 * 1440 IS THE DESKTOP THIS PRODUCT IS REVIEWED AT. It was 1280, which is a
 * narrower laptop and a fine width to hold — but every manual review of these
 * screens has been done at 1440, and a sweep measuring a width nobody looks at
 * is how a layout passes here and is wrong in the room.
 *
 * 390px IS THE COMMON MODERN PHONE and the width this pass is asked to clear;
 * 360 is kept beside it because it is strictly harder, and dropping it once 390
 * passes would be widening the target after aiming at it. Both are treated as
 * phones, so both get the card-layout, touch-target and dialog-fit checks.
 */
const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 834, height: 1112 },
  { name: 'phone-390', width: 390, height: 844 },
  { name: 'phone-360', width: 360, height: 740 },
]

/** Whether a viewport is below the table-to-cards breakpoint. */
function isPhone(viewport) {
  return viewport.name.startsWith('phone')
}

const SCREENS = [
  { name: 'overview', path: '/', ready: 'h1' },
  { name: 'people', path: '/people', ready: 'table, .state--empty' },
  { name: 'terminals', path: '/terminals', ready: 'table, .state--empty' },
  { name: 'terminal detail', path: '/terminals/AT-0001', ready: '.lifecycle' },
  { name: 'sites', path: '/sites', ready: 'table, .state--empty' },
  // The offline-policy radio group USED TO BE THE READY SIGNAL HERE, because it
  // rendered unconditionally. It no longer does: the panel rests on the policy
  // in force and the three options appear behind "Change policy". So the page's
  // arrival is signalled by the panel itself, and the radio group -- still the
  // widest block of prose inside a form control anywhere in the console, and the
  // one most likely to overflow a phone -- is opened and swept separately below.
  { name: 'site detail', path: '/sites/site-a', ready: '.panel .detail-list' },
  { name: 'operators', path: '/operators', ready: 'table, .state--empty' },
  { name: 'activity', path: '/activity', ready: 'table, .state--empty' },
  { name: 'events', path: '/events', ready: 'table, .state--empty' },
  { name: 'schedules', path: '/access/schedules', ready: '.rule-list, .notice' },
  { name: 'applications', path: '/settings/applications', ready: '.capability-list' },
  /*
    THE READY SIGNAL IS THE FLEET PANEL, NOT THE CATALOGUE.

    This waited on `.rule-list` -- a catalogue group -- which is now inside a
    closed disclosure and no longer the first thing the screen draws. The panel
    that answers "are my terminals all right" is, and waiting on it is also what
    proves the reordering actually happened rather than merely being intended.
  */
  { name: 'firmware', path: '/settings/firmware', ready: '#firmware-fleet-heading' },
  { name: 'settings', path: '/settings', ready: 'h1' },

  /*
    THE TWO DEAD ENDS, which render inside the shell like everything above them.

    A mistyped address, and a page an operator's role does not cover. They are
    the two screens somebody reaches by accident, which is exactly why nothing
    had measured them: no happy path goes here. Both used to render a shared
    "not implemented yet" placeholder and were rewritten to say what actually
    happened; this is the pass that checks the rewrite is a real page — one h1,
    contrast, no horizontal overflow, and a way back that is reachable on a
    phone.

    THE 404 PATH IS DELIBERATELY NONSENSE. It has to miss every route in the
    router to reach the catch-all, and a plausible-looking path is how a check
    like this quietly starts measuring a real screen instead.
  */
  { name: 'forbidden', path: '/forbidden', ready: 'h1' },
  { name: 'not found', path: '/no-such-address-exists', ready: 'h1' },
]

/*
 * WHAT A CUSTOMER MEETS BEFORE THEY HAVE A CONSOLE.
 * ---------------------------------------------------------------------------
 *
 * Every screen in SCREENS renders inside the app shell, which supplies the
 * document's landmarks, its single <h1> and the type scale. THESE RENDER ALONE
 * and each carries its own — which is exactly the class of difference axe
 * catches and a sighted read does not. They are also the first thing a new
 * customer sees, on whatever device they happen to be holding, and until now
 * not one of them had been drawn in a real browser at 360px.
 *
 * `/forgot-password` was the single exception and used to sit in SCREENS, swept
 * against an authenticated mock. It renders identically either way — the route
 * is outside RequireAuth — but sweeping it as an anonymous visitor is what it
 * actually is, so it has moved here with the rest of them.
 *
 * THE REDEEM SCREEN CARRIES A TOKEN, because that is how a recipient arrives
 * and because the page renders differently without one: with a token it asks
 * for a password twice, and without one it also offers a field to paste the
 * code into. A link is what people are sent, so a link is what is measured. The
 * page strips the token out of the address bar on arrival, which is its own
 * behaviour and unaffected by this.
 */
const UNAUTHENTICATED_SCREENS = [
  { name: 'login', path: '/login', ready: 'form.login__card' },
  { name: 'register', path: '/register', ready: 'form.login__card' },
  { name: 'forgot password', path: '/forgot-password', ready: 'form.login__card' },
  { name: 'redeem', path: '/redeem?token=browser-pass-token', ready: 'form.login__card' },
]

const failures = []
const notes = []
/*
  Targets under 24px that PASS 2.5.8 through the spacing exception.

  Kept apart from `failures` because they are conformant and must not fail
  the run, and kept at all because "none" and "forty, all of them spaced"
  are different states of this product, and only one of them is a single
  layout nudge away from a real failure.
*/
const smallButSpaced = []

function check(condition, message) {
  if (condition) return true
  failures.push(message)
  return false
}

/**
 * Makes a page's own faults fail the run.
 *
 * A CSP violation, an uncaught exception or a failed request must not be
 * swallowed — these are precisely the faults that only a browser reports, and a
 * page that throws while still rendering something axe can walk would otherwise
 * pass. EXTRACTED because the pass now opens more than one page per viewport:
 * an authenticated one, an anonymous one, and one that must change its
 * password. A page nobody instrumented is a page whose exceptions are silent.
 */
function instrument(page, label) {
  page.on('console', (message) => {
    const text = message.text()
    if (/Content Security Policy/i.test(text)) {
      failures.push(`${label}: CSP blocked something the app needs — ${text}`)
    }
  })
  page.on('pageerror', (error) => {
    failures.push(`${label}: uncaught exception — ${error.message}`)
  })
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
 * Target size, on whatever is currently on screen.
 *
 * ---------------------------------------------------------------------------
 * THIS USED TO MEASURE ONE SCREEN AND REPORT ON NINETEEN
 * ---------------------------------------------------------------------------
 *
 * The check lived AFTER the screens loop closed, below a `page.goto('/terminals')`
 * that belongs to the responsive-layout section. So "no undersized touch
 * targets" meant "none on Terminals" — the other eighteen screens, and every
 * dialog, were never measured. The overview alone has fifteen controls under
 * 24px and the sweep called itself clean for months.
 *
 * It is a function now so it can be called per screen and per dialog, which is
 * what the summary line has been claiming all along.
 *
 * ---------------------------------------------------------------------------
 * THE SPACING EXCEPTION IS PART OF THE RULE, NOT A CONCESSION
 * ---------------------------------------------------------------------------
 *
 * WCAG 2.5.8 (AA) sets 24×24 as the minimum, and then says a smaller target
 * passes anyway if a 24px-diameter circle centred on it touches no other
 * target's circle. That exception is most of what a text-link-heavy console
 * relies on: a "Manage" link in a panel header is 19px tall and has nothing
 * within 24px of it, and demanding 24px there would mean padding out every
 * inline link in the product to satisfy a rule it already meets.
 *
 * So both are computed. An undersized target that is also crowded is a real
 * failure and fails the run; an undersized target standing on its own is
 * conformant, and is reported as a note so the number is visible rather than
 * invisible. Reporting them identically would have made the check either
 * useless or unpassable.
 */
async function checkTargetSize(page, context) {
  const measured = await page.evaluate(() => {
    const targets = []
    for (const control of document.querySelectorAll('button, a, input, select, [role="button"]')) {
      const box = control.getBoundingClientRect()
      if (box.width === 0 && box.height === 0) continue
      // Inside a modal, only the modal's own controls are reachable.
      const dialog = document.querySelector('[role="dialog"]')
      if (dialog && !dialog.contains(control)) continue
      targets.push({
        tag: control.tagName.toLowerCase(),
        text: (control.textContent ?? '').trim().slice(0, 30),
        x: box.left + box.width / 2,
        y: box.top + box.height / 2,
        width: box.width,
        height: box.height,
      })
    }

    const undersized = []
    for (const target of targets) {
      if (target.height >= 24 && target.width >= 24) continue
      // The exception: does a 24px circle centred here reach another target?
      // Circles of equal diameter overlap exactly when their centres are closer
      // than one diameter, so the centre-to-centre distance is the whole test —
      // but a large neighbour's EDGE is what a finger actually lands on, so this
      // measures to the nearest point of each neighbour's box instead, which is
      // the stricter and more honest reading.
      let crowdedBy = null
      for (const other of targets) {
        if (other === target) continue
        const nearestX = Math.max(other.x - other.width / 2, Math.min(target.x, other.x + other.width / 2))
        const nearestY = Math.max(other.y - other.height / 2, Math.min(target.y, other.y + other.height / 2))
        if (Math.hypot(target.x - nearestX, target.y - nearestY) < 12) {
          crowdedBy = other.text || other.tag
          break
        }
      }
      undersized.push({
        description: `${target.tag} "${target.text}" ${Math.round(target.width)}x${Math.round(target.height)}`,
        crowdedBy,
      })
    }
    return undersized
  })

  for (const target of measured) {
    if (target.crowdedBy) {
      failures.push(
        `${context}: target below 24x24 AND crowded — ${target.description}, ` +
          `within 24px of "${target.crowdedBy}" (WCAG 2.5.8 spacing exception does not apply)`,
      )
    } else {
      smallButSpaced.push(`${context}: ${target.description}`)
    }
  }
  return measured.length
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
        hasTouch: isPhone(viewport),
        deviceScaleFactor: isPhone(viewport) ? 3 : 1,
      })
      const page = await context.newPage()
      await mockApi(page)
      instrument(page, viewport.name)

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

        // --- target size, on EVERY screen -------------------------------------
        //
        // Phones only, because that is where a finger is the pointer and where
        // the project holds the 24px line. See checkTargetSize for why the
        // spacing exception is computed rather than ignored.
        if (isPhone(viewport)) await checkTargetSize(page, label)
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

      if (isPhone(viewport)) {
        // Below the breakpoint each row becomes a card and every cell carries
        // its column name, because a table cannot shrink below its columns.
        check(
          layout.headerHeight !== null && layout.headerHeight <= 1,
          `${viewport.name}: the table header row still occupies ${layout.headerHeight}px; rows have not become cards`,
        )
        check(
          typeof layout.cellLabel === 'string' &&
            layout.cellLabel !== 'none' &&
            layout.cellLabel.length > 2,
          `${viewport.name}: cells carry no column label, so a card is a list of unidentified values (got ${layout.cellLabel})`,
        )
      } else {
        check(
          (layout.headerHeight ?? 0) > 1,
          `${viewport.name}: the table header row is not displayed above the breakpoint`,
        )
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
        if (isPhone(viewport)) await checkTargetSize(page, `${viewport.name}/revoke dialog`)

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

      // --- the offline-policy editor, now behind a disclosure ---------------
      //
      // Three options, each carrying a paragraph, inside a fieldset. Collapsing
      // it took that block off the resting page; it did NOT make it stop needing
      // to fit, so it is opened and measured rather than quietly skipped.
      const changePolicy = page.locator('button', { hasText: /^Change policy$/ }).first()
      if (check(
        (await changePolicy.count()) > 0,
        `${viewport.name}: the site page offers no way to change the offline policy, so the radio group was never swept`,
      )) {
        await changePolicy.click()
        await page.waitForSelector('.choice-list', { timeout: 5000 })
        await runAxe(page, `${viewport.name}/offline policy editor`)

        const policyFits = await page.evaluate(() => {
          const list = document.querySelector('.choice-list')
          if (!list) return null
          const box = list.getBoundingClientRect()
          return { overflowsX: box.right > window.innerWidth + 1 || box.left < -1 }
        })
        check(
          policyFits !== null && !policyFits.overflowsX,
          `${viewport.name}: the offline-policy options overflow the viewport horizontally`,
        )

        await page.locator('button', { hasText: /^Cancel$/ }).first().click()
      }
      // BEHIND A DISCLOSURE SINCE THE ANNOUNCE FLOW LANDED. A claim code stopped
      // being the site's primary action once a terminal could show its own
      // pairing code, and it now sits inside "Advanced: pre-authorise a terminal
      // for an installer". This sweep still looked for the old top-level button
      // and had been reporting its absence as a failure ever since.
      // STYLED NOW, which is the change this line's neighbour records. The
      // disclosure had no CSS at all -- no cursor, no marker of its own, no
      // spacing -- so it rendered as a raw browser triangle inside an otherwise
      // designed panel. The custom marker is asserted rather than assumed,
      // because a disclosure with no visible affordance is a heading nobody
      // presses and axe cannot see the difference.
      const summary = page.locator('summary.disclosure__summary', {
        hasText: /pre-authorise a terminal/i,
      }).first()
      if (check(
        (await summary.count()) > 0,
        `${viewport.name}: the advanced disclosure is not styled as one`,
      )) {
        const marker = await summary.evaluate((element) => ({
          cursor: getComputedStyle(element).cursor,
          glyph: getComputedStyle(element, '::before').borderLeftWidth,
        }))
        check(
          marker.cursor === 'pointer',
          `${viewport.name}: the disclosure summary does not present as pressable (cursor: ${marker.cursor})`,
        )
        check(
          Number.parseFloat(marker.glyph) > 0,
          `${viewport.name}: the disclosure draws no marker of its own (${marker.glyph})`,
        )
      }
      await summary.click()
      const provision = page.locator('button', { hasText: /^Issue a claim code$/ }).first()
      // Checked rather than skipped silently. A sweep that quietly does nothing
      // reports "no violations" and has measured nothing, which is the failure
      // mode a conditional block invites.
      if (check(
        (await provision.count()) > 0,
        `${viewport.name}: the site page offers no way to issue a claim code, so the claim-code panel was never swept`,
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

      /*
        --- the site scope, on the one screen that reads it -----------------

        It used to live in the shell's top bar and claimed, by position, to
        govern the console; only the overview ever read it. It now sits beside
        the heading whose figures it changes.

        CHECKED RATHER THAN ASSUMED, for the reason the claim-code panel below
        is: the control renders only when there is a genuine choice, so a mock
        session with no site grants draws nothing and a sweep over it reports
        "no violations" having measured an empty box. The mock now carries two
        grants precisely so this is a real control at 360px.
      */
      await page.goto(`${site.url}/`, { waitUntil: 'networkidle' })
      const scope = page.locator('.page__header .site-switcher__select').first()
      if (check(
        (await scope.count()) > 0,
        `${viewport.name}: the overview draws no site scope control, so it was never swept`,
      )) {
        const box = await scope.boundingBox()
        check(
          box !== null && box.height >= 24 && box.width >= 24,
          `${viewport.name}: the site scope control is ${Math.round(box?.width ?? 0)}x` +
            `${Math.round(box?.height ?? 0)}, below the 24x24 target floor`,
        )
        check(
          (await page.locator('header.topbar .site-switcher__select').count()) === 0,
          `${viewport.name}: a site select is back in the top bar, above screens that ignore it`,
        )
      }

      /* --- firmware: the answer before the catalogue ----------------------
       *
       * The screen was measured at 3.05 screens on a phone, of which 2.2 were
       * catalogue -- two of its three panels describing combinations the
       * customer owned no hardware for -- and its first mention of the
       * customer's own terminals was a sub-clause 676px down. The whole point
       * of the rebuild is the order, so the order is what is checked, at the
       * viewport where it hurt.
       *
       * CHECKED RATHER THAN ASSUMED, like the claim-code panel below: if the
       * update card stops rendering, every assertion about it silently passes
       * over a page that is not drawing it.
       */
      await page.goto(`${site.url}/settings/firmware`, { waitUntil: 'networkidle' })
      await page.waitForSelector('#firmware-fleet-heading', { timeout: 10_000 }).catch(() => {})

      const firmware = await page.evaluate(() => {
        const top = (selector) => {
          const element = document.querySelector(selector)
          return element ? Math.round(element.getBoundingClientRect().top + window.scrollY) : null
        }
        const update = [...document.querySelectorAll('.panel__title')].find(
          (title) => title.textContent?.trim() === 'Update available',
        )
        const advanced = document.querySelector('summary.disclosure__summary')
        return {
          fleetTop: top('#firmware-fleet-heading'),
          updateTop: update ? Math.round(update.getBoundingClientRect().top + window.scrollY) : null,
          advancedTop: advanced
            ? Math.round(advanced.getBoundingClientRect().top + window.scrollY)
            : null,
          advancedOpen: document.querySelector('.page > .panel details')?.open ?? null,
          documentHeight: document.documentElement.scrollHeight,
          viewportHeight: window.innerHeight,
        }
      })

      if (check(
        firmware.fleetTop !== null && firmware.updateTop !== null && firmware.advancedTop !== null,
        `${viewport.name}/firmware: the fleet panel, the update card or the disclosure did not render, ` +
          `so the screen's order was never measured`,
      )) {
        check(
          firmware.fleetTop < firmware.updateTop,
          `${viewport.name}/firmware: the update card (${firmware.updateTop}px) is above the fleet ` +
            `standing (${firmware.fleetTop}px); the standing answers the question first`,
        )
        check(
          firmware.updateTop < firmware.advancedTop,
          `${viewport.name}/firmware: the catalogue disclosure (${firmware.advancedTop}px) is above ` +
            `the update card (${firmware.updateTop}px)`,
        )
        check(
          firmware.advancedOpen === false,
          `${viewport.name}/firmware: the advanced catalogue is open by default (${firmware.advancedOpen})`,
        )
        /*
          THE ANSWER IS ON THE FIRST SCREEN, which is a claim about the ANSWER
          and not about the whole page.

          The bar is that a customer arriving at 360px learns, without
          scrolling, where their terminals stand and that an update exists. The
          button and the consequences sit just under the fold at that width and
          that is the right place for them -- a decision a thumb can reach by
          accident is worse than one a thumb has to scroll to. Requiring the
          CATALOGUE to be above the fold too would be a stricter bar than the
          screen needs and would be met by cutting the copy that makes the
          decision safe.
        */
        check(
          firmware.updateTop <= firmware.viewportHeight,
          `${viewport.name}/firmware: the update is not announced on the first screen ` +
            `(it starts at ${firmware.updateTop}px in ${firmware.viewportHeight}px)`,
        )
      }

      // The primary action must be pressable where it is, not after a hunt.
      const updateButton = page.locator('button', { hasText: /^Update \d+ terminals? to / }).first()
      if (check(
        (await updateButton.count()) > 0,
        `${viewport.name}/firmware: no "Update N terminals to X" action, so the primary control was never swept`,
      )) {
        const box = await updateButton.boundingBox()
        check(
          box !== null && box.height >= 24 && box.width >= 24,
          `${viewport.name}/firmware: the update action is ${Math.round(box?.width ?? 0)}x` +
            `${Math.round(box?.height ?? 0)}, below the 24x24 target floor`,
        )
      }

      /*
        "Cannot be installed" is the one fault only this console can report --
        the server withholds the offer and logs the reason where nobody will
        read it -- and it is drawn in the MAIN VIEW rather than behind the
        disclosure. The mock's BETA terminal is pointed at a version with no
        digest, no size and no address precisely so this renders and gets
        contrast-checked like everything else.
      */
      const diagnosis = page.locator('.notice', { hasText: 'Cannot be installed' }).first()
      if (check(
        (await diagnosis.count()) > 0,
        `${viewport.name}/firmware: the "cannot be installed" diagnosis is not in the main view`,
      )) {
        check(
          !(await diagnosis.evaluate((node) => Boolean(node.closest('details')))),
          `${viewport.name}/firmware: the "cannot be installed" diagnosis is inside a disclosure`,
        )
      }

      await runAxe(page, `${viewport.name}/firmware (fleet first)`)

      // And the catalogue itself, opened, because it still has to be legible.
      await page.locator('summary.disclosure__summary').first().click()
      await page.waitForSelector('.rule-list', { timeout: 5000 }).catch(() => {})
      await runAxe(page, `${viewport.name}/firmware advanced`)

      const advancedOverflow = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }))
      check(
        advancedOverflow.scrollWidth <= advancedOverflow.clientWidth + 1,
        `${viewport.name}/firmware advanced: the page scrolls horizontally ` +
          `(${advancedOverflow.scrollWidth} > ${advancedOverflow.clientWidth})`,
      )

      // The recovery advice a locked-out owner reads is swept further down,
      // with the rest of the unauthenticated screens and as an anonymous
      // visitor — which is what somebody who cannot sign in actually is.

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

      /* ------------------------------------------------------------------ *
       * WHAT A CUSTOMER MEETS BEFORE THEY HAVE A CONSOLE                    *
       * ------------------------------------------------------------------ *
       *
       * A SECOND PAGE, ANONYMOUS. /auth/me answers 401 for it, so RequireAuth
       * sends it to the login form and each of these screens renders on its
       * own — without the shell that supplies the landmarks, the single <h1>
       * and the type scale everywhere else.
       *
       * A SEPARATE PAGE RATHER THAN A RE-MOCKED ONE, because the session
       * belongs to the page: reconfiguring the authenticated page mid-run would
       * make every earlier assertion depend on the order they happen to be
       * written in.
       */
      const anonymous = await context.newPage()
      await mockApi(anonymous, { session: null })
      instrument(anonymous, `${viewport.name}/anonymous`)

      for (const screen of UNAUTHENTICATED_SCREENS) {
        await anonymous.goto(`${site.url}${screen.path}`, { waitUntil: 'networkidle' })
        await anonymous.waitForSelector(screen.ready, { timeout: 10_000 }).catch(() => {
          failures.push(`${viewport.name}/${screen.name}: never rendered (${screen.ready})`)
        })

        const label = `${viewport.name}/${screen.name}`
        await runAxe(anonymous, label)
        if (isPhone(viewport)) await checkTargetSize(anonymous, label)

        const overflow = await anonymous.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }))
        check(
          overflow.scrollWidth <= overflow.clientWidth + 1,
          `${label}: the page scrolls horizontally (${overflow.scrollWidth} > ${overflow.clientWidth}).`,
        )

        /*
          EXACTLY ONE <h1>, ON A PAGE WITH NO SHELL TO SUPPLY IT.

          Inside the console the shell guarantees this and the a11y suite checks
          it there. These four carry their own heading structure, and a card
          with no h1 — or with two — is a document a screen-reader user cannot
          navigate by heading. It is also the failure most likely to appear the
          next time one of these screens grows a second panel.
        */
        const headings = await anonymous.locator('h1').count()
        check(
          headings === 1,
          `${label}: has ${headings} <h1> elements; a page with no shell must carry exactly one`,
        )

        // Touch targets, on the screens a customer meets on their own phone
        // before anybody has helped them.
      }

      /*
        THE RECOVERY ADVICE A LOCKED-OUT OWNER ACTUALLY READS.

        Swept separately because it is only reachable by submitting: the resting
        screen is a single field, and the two routes back in — the ones that
        replaced "ask an administrator" for a company that has none — appear
        only after it. On a phone this is the longest continuous piece of prose
        the product renders, so it is where a list that does not fit shows up.

        MOVED ONTO THE ANONYMOUS PAGE. It used to run against the authenticated
        mock, which worked — the route is outside RequireAuth — while measuring
        the screen in a state no locked-out customer is ever in.
      */
      await anonymous.goto(`${site.url}/forgot-password`, { waitUntil: 'networkidle' })
      await anonymous.fill('input[type="email"]', 'owner@example.com')
      await anonymous.locator('button', { hasText: /^Request a reset$/ }).click()
      // Waited for rather than assumed: the panel replaces the form on a state
      // update after the request settles, so counting immediately after the
      // click measures the form that is still on screen.
      await anonymous.waitForSelector('.login__routes', { timeout: 10_000 }).catch(() => {})
      if (check(
        (await anonymous.locator('.login__routes').count()) > 0,
        `${viewport.name}: the reset screen offers no routes back in, so the ` +
          `advice a locked-out sole owner needs was never swept`,
      )) {
        await runAxe(anonymous, `${viewport.name}/password recovery advice`)
        if (isPhone(viewport)) await checkTargetSize(anonymous, `${viewport.name}/password recovery advice`)

        const routesFit = await anonymous.evaluate(() => {
          const list = document.querySelector('.login__routes')
          if (!list) return null
          const box = list.getBoundingClientRect()
          return { overflowsX: box.right > window.innerWidth + 1 || box.left < -1 }
        })
        check(
          routesFit !== null && !routesFit.overflowsX,
          `${viewport.name}: the recovery routes overflow the viewport horizontally`,
        )
      }

      await anonymous.close()

      /* ------------------------------------------------------------------ *
       * THE PASSWORD SOMEBODY ELSE CHOSE                                    *
       * ------------------------------------------------------------------ *
       *
       * NO URL REACHES THIS SCREEN. `must_change_password` marks a session
       * whose credential is known to a third party, and RequireAuth renders the
       * interstitial IN PLACE OF THE ENTIRE CONSOLE rather than at a route —
       * so the only way to draw it is to BE that session. It is the first
       * screen every operator who redeemed an administrator-set password ever
       * sees, and it is a block with no way past it except through the form,
       * which makes a control that does not fit a phone a lockout rather than
       * an inconvenience.
       *
       * A THIRD PAGE, for the same reason the anonymous one is a second.
       */
      const forced = await context.newPage()
      await mockApi(forced, { session: { ...SESSION, must_change_password: true } })
      instrument(forced, `${viewport.name}/forced password change`)

      await forced.goto(`${site.url}/`, { waitUntil: 'networkidle' })
      await forced.waitForSelector('form.login__card', { timeout: 10_000 }).catch(() => {})
      if (check(
        (await forced.locator('form.login__card').count()) > 0,
        `${viewport.name}: a session flagged must_change_password reached the console ` +
          `instead of the interstitial, so the block was never swept`,
      )) {
        const label = `${viewport.name}/forced password change`
        await runAxe(forced, label)

        const overflow = await forced.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }))
        check(
          overflow.scrollWidth <= overflow.clientWidth + 1,
          `${label}: the page scrolls horizontally (${overflow.scrollWidth} > ${overflow.clientWidth}).`,
        )

        /*
          THE WAY OUT MUST BE THERE.

          The screen is deliberately a block with no "later" button, so signing
          out is the only exit for somebody who reached it on a shared machine.
          A block with no exit is a trap, and it is the one thing about this
          screen that no unit test would notice going missing.
        */
        check(
          (await forced.locator('button', { hasText: /sign out/i }).count()) > 0,
          `${label}: offers no way to leave, so a session opened by accident is stuck`,
        )
      }

      await forced.close()

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
  console.log(
    `${VIEWPORTS.length} viewports x ` +
      `${SCREENS.length} signed-in + ${UNAUTHENTICATED_SCREENS.length} signed-out screens ` +
      `(plus the forced password change, which no URL reaches), axe with contrast enabled\n`,
  )

  if (failures.length > 0) {
    console.error(`FAILED (${failures.length}):\n`)
    for (const failure of failures) console.error(`  - ${failure}`)
    process.exitCode = 1
    return
  }

  /*
    THE SUMMARY NAMES THE EXEMPT COUNT rather than swallowing it. "No
    undersized touch targets" was the old line and it was false: there were
    plenty, all of them conformant through the spacing exception, and the run
    had simply never looked at the screens they were on.
  */
  if (smallButSpaced.length > 0) {
    console.log(
      `\n${smallButSpaced.length} target(s) under 24x24 pass 2.5.8 on spacing ` +
        `(nothing else within 24px). Not failures; listed so a layout change that ` +
        `crowds one is visible:`,
    )
    for (const target of smallButSpaced) console.log(`  · ${target}`)
  }

  console.log(
    '\nNo violations, no horizontal overflow, no target below 24x24 that is also crowded.',
  )
}

await main()
