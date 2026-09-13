import { mkdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { chromium } from 'playwright-core'

import { mockApi } from './api.mjs'
import { serve } from './server.mjs'

/**
 * The release-for-transfer pass, in a real browser.
 *
 * run.mjs sweeps every screen at rest and opens one dialog. This walks the one
 * flow whose states are unreachable from a resting page and were never drawn
 * until the pre-production review drew them: an order placed and followed to
 * the terminal's own confirmation, an old-firmware unit, an offline unit, a
 * unit whose credential the order cannot be keyed with, and the destructive
 * buttons UNDER THE POINTER in both colour schemes.
 *
 * It asserts the copy as well as measuring it, because three of the defects it
 * exists to hold closed were sentences: a console command old firmware does not
 * have, "stops letting anyone in now" for a unit that is offline, and "waiting
 * for the terminal" for a release that had already finished.
 *
 * Screenshots land in dist/release-shots (dist is not committed) so a reviewer
 * can look at what was measured. The mock is api.mjs's, with the release
 * routes made stateful on top of it: an order placed here is an order the next
 * read answers, and `confirm` is the terminal sending its receipt.
 */

const AXE_SOURCE = readFileSync(join(process.cwd(), 'node_modules', 'axe-core', 'axe.min.js'), 'utf8')
const DIST = join(process.cwd(), 'dist')
const OUT = join(DIST, 'release-shots')
mkdirSync(OUT, { recursive: true })

const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'phone-390', width: 390, height: 844 },
]

// The page polls the release read every ten seconds; the confirmation
// scenario has to outwait one poll.
const RELEASE_POLL_MS = 10_000

const failures = []
const notes = []

function check(condition, message) {
  if (!condition) failures.push(message)
  return condition
}

// ---------------------------------------------------------------------------
// Fixtures the resting mock does not carry
// ---------------------------------------------------------------------------

const CAPABLE = ['wifi_provisioning', 'wifi_recovery', 'terminal_announce', 'terminal_release']
const OLD_FIRMWARE = ['wifi_provisioning', 'wifi_recovery', 'terminal_announce']

function terminal(serial, overrides) {
  return {
    id: 9,
    public_id: `terminal-public-${serial}`,
    site_id: 1,
    site_public_id: 'site-a',
    site_name: 'Lagos Distribution Centre',
    serial_number: serial,
    device_type: 'TERMINAL',
    status: 'ONLINE',
    active: true,
    release_channel: 'STABLE',
    firmware_version: '1.2.0',
    hardware_revision: 'rev-c',
    build_number: '456',
    boot_count: 12,
    last_seen_at: '2026-09-13T08:00:00Z',
    last_sync_at: '2026-09-13T08:00:00Z',
    last_heartbeat_at: '2026-09-13T08:00:00Z',
    current_firmware_version: '1.2.0',
    firmware_outdated: false,
    capabilities: CAPABLE,
    readiness: { state: 'READY' },
    application_mode: 'MULTI_PURPOSE',
    effective_applications: ['ACCESS_CONTROL'],
    credential_active: true,
    ...overrides,
  }
}

const FIXTURES = {
  'AT-0101': terminal('AT-0101', { device_name: 'North Gate' }),
  'AT-0102': terminal('AT-0102', {
    device_name: 'Old Firmware Door',
    capabilities: OLD_FIRMWARE,
    firmware_version: '1.1.0',
    firmware_outdated: true,
  }),
  'AT-0103': terminal('AT-0103', { device_name: 'Loading Bay', status: 'OFFLINE', last_heartbeat_at: '2026-09-01T08:00:00Z' }),
  'AT-0104': terminal('AT-0104', { device_name: 'Revoked Door', credential_active: false }),
  'AT-0105': terminal('AT-0105', {
    device_name: 'Revoked Old Door',
    capabilities: OLD_FIRMWARE,
    firmware_outdated: true,
    credential_active: false,
  }),
}

function detailOf(fixture) {
  const { credential_active, ...row } = fixture
  return {
    ...row,
    health: {
      pending_jobs: 0,
      failed_jobs: 0,
      credential_active,
      offline_policy: 'CACHED_GRACE',
      offline_grace_minutes: 720,
    },
  }
}

/**
 * The stateful half: orders placed, and terminals that have confirmed.
 * Registered AFTER mockApi so Playwright consults it first; anything it does
 * not own falls back to the resting mock.
 */
function releaseState() {
  const orders = new Map()
  const confirmed = new Set()

  function facts(serial, fixture) {
    const order = orders.get(serial)
    return {
      serial_number: serial,
      state: order ? 'ORDERED' : '',
      release_id: order?.release_id,
      ordered_at: order?.ordered_at,
      ordered_by_email: order?.ordered_by_email,
      terminal_capable: fixture.capabilities.includes('terminal_release'),
      order_verifiable: Boolean(order) && fixture.credential_active,
      last_seen_at: fixture.last_seen_at,
    }
  }

  const json = (status, body) => ({
    status,
    contentType: 'application/json',
    headers: { 'X-Request-ID': 'release-pass' },
    body: JSON.stringify(body),
  })

  async function handle(route) {
    const request = route.request()
    const url = new URL(request.url())
    const parts = url.pathname.split('/')
    const at = parts.indexOf('terminals')
    const serial = parts[at + 1]
    const tail = parts.slice(at + 2).join('/')
    const fixture = FIXTURES[serial]
    if (!fixture) return route.fallback()
    if (confirmed.has(serial)) return route.fulfill(json(404, { error: 'Terminal not found' }))

    const row = () =>
      orders.has(serial)
        ? {
            ...fixture,
            release: {
              state: 'ORDERED',
              ordered_at: orders.get(serial).ordered_at,
              ordered_by_email: orders.get(serial).ordered_by_email,
              order_verifiable: fixture.credential_active,
            },
          }
        : fixture

    if (tail === '' && request.method() === 'GET') return route.fulfill(json(200, detailOf(row())))
    if (tail === 'release' && request.method() === 'GET') return route.fulfill(json(200, facts(serial, fixture)))
    if (tail === 'release' && request.method() === 'POST') {
      if (!orders.has(serial)) {
        orders.set(serial, {
          release_id: `release-${serial}`,
          ordered_at: new Date().toISOString(),
          ordered_by_email: 'ops@example.com',
        })
      }
      return route.fulfill(json(200, facts(serial, fixture)))
    }
    if (tail === 'release' && request.method() === 'DELETE') {
      orders.delete(serial)
      return route.fulfill(json(200, facts(serial, fixture)))
    }
    if (tail === 'release/force' && request.method() === 'POST') {
      const body = request.postDataJSON()
      if (!body?.attest) return route.fulfill(json(400, { error: 'attest must be true', code: 'ATTESTATION_REQUIRED' }))
      if (!orders.has(serial)) return route.fulfill(json(409, { error: 'no release is in progress for that terminal', code: 'RELEASE_NOT_ORDERED' }))
      orders.delete(serial)
      confirmed.add(serial)
      return route.fulfill(json(200, { serial_number: serial, release_id: body.release_id, released: true, confirmed_by: 'OPERATOR', pending_jobs_cancelled: 3, announcements_voided: 0 }))
    }
    return route.fallback()
  }

  return {
    handle,
    /** The terminal executed the order and sent its receipt. */
    confirm(serial) {
      orders.delete(serial)
      confirmed.add(serial)
    },
    reset() {
      orders.clear()
      confirmed.clear()
    },
  }
}

// ---------------------------------------------------------------------------
// Measuring
// ---------------------------------------------------------------------------

async function axeDialog(page, label) {
  await page.evaluate(AXE_SOURCE)
  const violations = await page.evaluate(async () => {
    const results = await window.axe.run(document.querySelector('[role="dialog"]') ?? document, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'] },
      resultTypes: ['violations'],
    })
    return results.violations.map((v) => `[${v.impact}] ${v.id} — ${v.help}: ${v.nodes.slice(0, 2).map((n) => n.html).join(' | ')}`)
  })
  for (const violation of violations) failures.push(`${label}: ${violation}`)
  const fits = await page.evaluate(() => {
    const dialog = document.querySelector('[role="dialog"]')
    if (!dialog) return null
    const box = dialog.getBoundingClientRect()
    return { w: Math.round(box.width), h: Math.round(box.height), overflowsX: box.right > window.innerWidth + 1 || box.left < -1 }
  })
  check(fits !== null && !fits.overflowsX, `${label}: the dialog overflows the viewport horizontally`)
  notes.push(`${label}: dialog ${fits?.w}x${fits?.h}, axe ${violations.length ? violations.length + ' violation(s)' : 'clean'}`)
}

async function contrastOf(page, locator) {
  await page.evaluate(AXE_SOURCE)
  return locator.evaluate(async (element) => {
    const results = await window.axe.run(element, {
      runOnly: { type: 'rule', values: ['color-contrast'] },
      resultTypes: ['violations'],
    })
    const node = results.violations[0]?.nodes[0]
    return node ? node.any[0]?.message ?? 'insufficient contrast' : null
  })
}

async function shot(page, viewport, name) {
  await page.screenshot({ path: join(OUT, `${viewport.name}--${name}.png`), fullPage: true })
}

async function shotDialog(page, viewport, name) {
  const dialog = page.locator('[role="dialog"]')
  await dialog.waitFor({ timeout: 5000 })
  await dialog.screenshot({ path: join(OUT, `${viewport.name}--${name}.png`) })
  await axeDialog(page, `${viewport.name}/${name}`)
  return dialog
}

function dialogText(page) {
  return page.locator('[role="dialog"]').innerText()
}

function bannerText(page) {
  return page.locator('.notice', { hasText: /being released for transfer/i }).innerText()
}

const has = (text, pattern) => new RegExp(pattern, 'i').test(text)

// ---------------------------------------------------------------------------
// The scenarios
// ---------------------------------------------------------------------------

async function scenarios(page, viewport, site, releases) {
  const go = (path) => page.goto(`${site.url}${path}`, { waitUntil: 'networkidle' })
  const label = (name) => `${viewport.name}/${name}`

  async function order(serial, confirmName) {
    await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
    await page.getByLabel(/type .* to confirm/i).fill(serial)
    await page.locator('[role="dialog"] button.button--danger', { hasText: confirmName }).click()
    await page.waitForSelector('text=/being released for transfer/i', { timeout: 5000 })
  }

  // --- 1. capable, online: through to the terminal's own confirmation ------
  releases.reset()
  await go('/terminals/AT-0101')
  await page.waitForSelector('.lifecycle')
  await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
  await shotDialog(page, viewport, '01-order-dialog-capable')
  let text = await dialogText(page)
  check(has(text, 'the moment it receives the release order'), `${label('01')}: the capable dialog does not condition the stop on receiving the order`)
  check(!has(text, 'usb port'), `${label('01')}: a console procedure is shown to a unit that does it itself`)
  await page.getByLabel(/type .* to confirm/i).fill('AT-0101')
  await page.locator('[role="dialog"] button.button--danger', { hasText: /^Release terminal$/ }).click()
  await page.waitForSelector('text=/being released for transfer/i', { timeout: 5000 })
  await shot(page, viewport, '02-ordered-capable')
  text = await bannerText(page)
  check(has(text, 'waiting for the terminal to confirm'), `${label('02')}: the ORDERED banner is not waiting for the terminal`)
  await page.locator('.notice button', { hasText: /release anyway/i }).waitFor({ state: 'visible' })
  check(
    await page.locator('.notice button', { hasText: /release anyway/i }).isEnabled(),
    `${label('02')}: Release anyway stays disabled after the order's facts have loaded`,
  )

  // The terminal executes the order. The next poll answers 404, and that is
  // completion: a toast, and the list, and never "Terminal not found".
  releases.confirm('AT-0101')
  await page.waitForTimeout(RELEASE_POLL_MS + 1500)
  const released = page.locator('.notification__message', { hasText: /has been released/i })
  check((await released.count()) > 0, `${label('03')}: the terminal's confirmation was not announced`)
  check(
    (await page.locator('text=/terminal not found/i').count()) === 0,
    `${label('03')}: the terminal's confirmation was shown as "Terminal not found"`,
  )
  await page.waitForSelector('h1', { timeout: 5000 })
  check(/terminals/i.test(await page.locator('h1').first().innerText()), `${label('03')}: the page did not leave the released terminal`)
  await shot(page, viewport, '03-released-by-terminal')

  // --- 2. old firmware -------------------------------------------------------
  releases.reset()
  await go('/terminals/AT-0102')
  await page.waitForSelector('.lifecycle')
  const panel = await page.locator('.lifecycle__option', { hasText: 'Release for transfer' }).innerText()
  check(has(panel, 'firmware update first'), `${label('04')}: the lifecycle panel does not send old firmware to an update`)
  await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
  await shotDialog(page, viewport, '04-order-dialog-old-firmware')
  text = await dialogText(page)
  check(has(text, 'update its firmware first'), `${label('04')}: the old-firmware dialog does not say to update first`)
  check(has(text, 'release anyway is not a substitute'), `${label('04')}: the old-firmware dialog does not warn off Release anyway`)
  check(!has(text, 'usb port') && !has(text, 'type release'), `${label('04')}: the old-firmware dialog still names a console command it does not have`)
  check((await page.locator('[role="dialog"] a', { hasText: /^Firmware$/ }).count()) > 0, `${label('04')}: no link to Firmware`)
  await page.getByLabel(/type .* to confirm/i).fill('AT-0102')
  await page.locator('[role="dialog"] button.button--danger', { hasText: /^Order the release$/ }).click()
  await page.waitForSelector('text=/being released for transfer/i', { timeout: 5000 })
  await shot(page, viewport, '05-ordered-old-firmware')
  text = await bannerText(page)
  check(has(text, 'held for it'), `${label('05')}: the old-firmware banner does not say the order is held`)
  check(has(text, 'release anyway does not wipe the unit'), `${label('05')}: the old-firmware banner does not warn off Release anyway`)
  check(!has(text, 'usb console'), `${label('05')}: the old-firmware banner names a console command`)
  await page.locator('.notice button', { hasText: /release anyway/i }).click()
  await shotDialog(page, viewport, '06-force-dialog-old-firmware')
  text = await dialogText(page)
  check(has(text, 'does not wipe it'), `${label('06')}: the force dialog on old firmware does not say it will not wipe`)
  await page.keyboard.press('Escape')

  // --- 3. offline order ------------------------------------------------------
  releases.reset()
  await go('/terminals/AT-0103')
  await page.waitForSelector('.lifecycle')
  await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
  await shotDialog(page, viewport, '07-order-dialog-offline')
  text = await dialogText(page)
  check(!has(text, 'stops letting anyone in now'), `${label('07')}: an offline unit is told it stops now`)
  check(has(text, 'offline policy'), `${label('07')}: an offline unit is not told it keeps working under its offline policy`)
  check(has(text, 'the moment it receives the release order'), `${label('07')}: an offline unit is not told when it stops`)
  await page.getByLabel(/type .* to confirm/i).fill('AT-0103')
  await page.locator('[role="dialog"] button.button--danger', { hasText: /^Release terminal$/ }).click()
  await page.waitForSelector('text=/being released for transfer/i', { timeout: 5000 })
  await shot(page, viewport, '08-ordered-offline')
  check(has(await bannerText(page), 'offline right now'), `${label('08')}: the banner does not say the offline unit is still working`)

  // --- 4. order_verifiable = false -------------------------------------------
  releases.reset()
  await go('/terminals/AT-0104')
  await page.waitForSelector('.lifecycle')
  await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
  await shotDialog(page, viewport, '09-order-dialog-no-credential')
  text = await dialogText(page)
  check(has(text, 'no credential'), `${label('09')}: a revoked unit is not told it has no credential`)
  check(has(text, 'usb port'), `${label('09')}: a revoked capable unit is not given the wipe at its console`)
  check(!has(text, 'waiting for the terminal'), `${label('09')}: a revoked unit is offered the automated flow`)
  await page.getByLabel(/type .* to confirm/i).fill('AT-0104')
  await page.locator('[role="dialog"] button.button--danger', { hasText: /^Order the release$/ }).click()
  await page.waitForSelector('text=/being released for transfer/i', { timeout: 5000 })
  await shot(page, viewport, '10-ordered-no-credential')
  text = await bannerText(page)
  check(has(text, 'cannot reach it'), `${label('10')}: the unverifiable order's banner does not say the order cannot reach the unit`)
  check(!has(text, 'waiting for the terminal to confirm'), `${label('10')}: the unverifiable order is shown as waiting for the terminal`)

  await go('/terminals/AT-0105')
  await page.waitForSelector('.lifecycle')
  await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
  await shotDialog(page, viewport, '11-order-dialog-no-remote-path')
  text = await dialogText(page)
  check(has(text, 'cannot be confirmed yet'), `${label('11')}: old firmware with no credential can still be ordered`)
  check(has(text, 'claim code'), `${label('11')}: old firmware with no credential is not told to re-register`)
  await page.keyboard.press('Escape')

  // --- 5. cancel: says it only works before the terminal acts ---------------
  releases.reset()
  await go('/terminals/AT-0101')
  await page.waitForSelector('.lifecycle')
  await order('AT-0101', /^Release terminal$/)
  await page.locator('.notice button', { hasText: /cancel release/i }).click()
  await shotDialog(page, viewport, '12-cancel-dialog')
  check(has(await dialogText(page), 'not yet acted'), `${label('12')}: the cancel dialog does not say it only works before the terminal acts`)
  await page.keyboard.press('Escape')
  await page.locator('.notice button', { hasText: /release anyway/i }).click()
  await shotDialog(page, viewport, '13-force-dialog-capable')
  text = await dialogText(page)
  check(has(text, 'AT-0101'), `${label('13')}: the force dialog does not name the serial`)
  check(has(text, 'never uploaded'), `${label('13')}: the force dialog does not say events on the unit are lost`)
  await page.keyboard.press('Escape')
}

// ---------------------------------------------------------------------------
// The destructive buttons under the pointer, both schemes
// ---------------------------------------------------------------------------

async function hoverPass(page, site, releases) {
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme })
    const label = `hover/${colorScheme}`

    async function measure(locator, what, shotName) {
      if (!check((await locator.count()) > 0, `${label}: ${what} is not on the page`)) return
      await locator.first().scrollIntoViewIfNeeded()
      await locator.first().hover()
      const problem = await contrastOf(page, locator.first())
      check(problem === null, `${label}: ${what} under the pointer — ${problem}`)
      const styles = await locator.first().evaluate((el) => {
        const s = getComputedStyle(el)
        return `${s.color} on ${s.backgroundColor}`
      })
      notes.push(`${label}: ${what} hovered: ${styles}${problem ? ' — FAIL' : ''}`)
      if (shotName) {
        const dialog = page.locator('[role="dialog"]')
        if ((await dialog.count()) > 0) await dialog.screenshot({ path: join(OUT, `${shotName}--${colorScheme}.png`) })
      }
    }

    releases.reset()
    await page.goto(`${site.url}/terminals/AT-0101`, { waitUntil: 'networkidle' })
    await page.waitForSelector('.lifecycle')
    for (const name of ['Revoke', 'Retire', 'Release']) {
      await measure(page.locator('.lifecycle button.button--danger', { hasText: new RegExp(`^${name}$`) }), `${name}`)
    }
    await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
    await page.getByLabel(/type .* to confirm/i).fill('AT-0101')
    await measure(page.locator('[role="dialog"] button.button--danger'), 'Release terminal (confirm)', 'hover-release-terminal')
    await page.keyboard.press('Escape')
    await page.locator('.lifecycle button', { hasText: /^Retire$/ }).click()
    await page.getByLabel(/type .* to confirm/i).fill('AT-0101')
    await measure(page.locator('[role="dialog"] button.button--danger'), 'Retire terminal (confirm)', 'hover-retire')
    await page.keyboard.press('Escape')
    await page.locator('.lifecycle button', { hasText: /^Revoke$/ }).click()
    await page.getByLabel(/type .* to confirm/i).fill('AT-0101')
    await measure(page.locator('[role="dialog"] button.button--danger'), 'Revoke credential (confirm)', 'hover-revoke')
    await page.keyboard.press('Escape')

    await page.locator('.lifecycle button', { hasText: /^Release$/ }).click()
    await page.getByLabel(/type .* to confirm/i).fill('AT-0101')
    await page.locator('[role="dialog"] button.button--danger').click()
    await page.waitForSelector('text=/being released for transfer/i', { timeout: 5000 })
    const anyway = page.locator('.notice button.button--danger', { hasText: /release anyway/i })
    await anyway.waitFor({ state: 'visible' })
    await measure(anyway, 'Release anyway (banner)')
    await anyway.click()
    await page.getByLabel(/type .* to confirm/i).fill('RELEASE')
    await measure(page.locator('[role="dialog"] button.button--danger'), 'Release anyway (confirm)', 'hover-release-anyway')
    await page.keyboard.press('Escape')
  }
  await page.emulateMedia({ colorScheme: null })
}

// ---------------------------------------------------------------------------

async function main() {
  const site = await serve(DIST)
  const browser = await chromium.launch({ channel: 'chrome' })
  try {
    notes.push(`Chrome ${browser.version()}`)
    for (const viewport of VIEWPORTS) {
      const context = await browser.newContext({
        viewport: { width: viewport.width, height: viewport.height },
        hasTouch: viewport.name.startsWith('phone'),
        deviceScaleFactor: viewport.name.startsWith('phone') ? 2 : 1,
      })
      const page = await context.newPage()
      page.on('pageerror', (error) => failures.push(`${viewport.name}: page error — ${error.message}`))
      await mockApi(page)
      const releases = releaseState()
      await page.route('**/api/v1/console/terminals/**', (route) => releases.handle(route))

      await scenarios(page, viewport, site, releases)
      if (viewport.name === 'desktop') await hoverPass(page, site, releases)
      await context.close()
    }
  } finally {
    await browser.close()
    await site.close?.()
  }

  console.log(`Release pass: ${notes[0]}`)
  console.log(`${VIEWPORTS.length} viewports x 13 states, axe on every dialog, destructive buttons hovered in light and dark`)
  console.log(`Screenshots: ${OUT}\n`)
  for (const note of notes.slice(1)) console.log(`  ${note}`)
  if (failures.length > 0) {
    console.log(`\n${failures.length} failure(s):`)
    for (const failure of failures) console.log(`  - ${failure}`)
    process.exit(1)
  }
  console.log('\nEvery release state reads correctly, every dialog fits and passes axe, every destructive button is legible under the pointer.')
}

main().catch((error) => {
  console.error(error)
  process.exit(1)
})
