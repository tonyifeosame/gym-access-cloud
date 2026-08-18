import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role } from '../../api/types'
import { makeSession, makeSite, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { ProvisionTerminalDialog } from './ClaimCodeDialog'
import { SiteDetailPage } from './SiteDetailPage'

/**
 * Provisioning a terminal with a claim code.
 *
 * THE THING BEING TESTED IS AN ABSENCE AS MUCH AS A PRESENCE. A claim code
 * exists so that the site's provisioning key never has to leave the platform,
 * and the failure mode is not a broken flow — it is a screen that offers the key
 * as the quicker alternative, which an installer under time pressure will take.
 * Several tests below therefore assert that nothing on this path mentions the key
 * as a way to register hardware.
 *
 * The rest assert the four facts an installer has to leave with: shown once,
 * works once, expires, and issuing another one kills this one. The last is the
 * one discovered at a door if the console does not say it.
 */

const SITE = makeSite({ id: SITE_A.site_id, name: SITE_A.site_name, terminal_count: 2 })

function signIn(role: Role = 'ADMIN') {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ sites: [SITE] })
  return session
}

function renderDialog() {
  return renderWithSession(
    <ProvisionTerminalDialog open site={SITE} onClose={() => {}} />,
  )
}

function renderSite() {
  const router = createMemoryRouter([{ path: '/sites/:siteId', element: <SiteDetailPage /> }], {
    initialEntries: [`/sites/${SITE_A.site_id}`],
  })
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

/** Fills the form and submits it. */
async function issueFor(user: ReturnType<typeof userEvent.setup>, serial: string) {
  await user.type(screen.getByLabelText(/Serial number/), serial)
  await user.click(screen.getByRole('button', { name: 'Issue claim code' }))
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Getting to it
// ---------------------------------------------------------------------------

describe('the site offers a claim code as the advanced path', () => {
  /**
   * WHAT CHANGED, AND WHY THE OLD ASSERTION WAS RIGHT TO FAIL.
   *
   * "Provision a terminal" used to be the site's PRIMARY action, because a
   * claim code was the only way to bring hardware up without handing out the
   * site key. It is no longer the only way: a terminal now announces itself and
   * is added from the Terminals page with a code it displays on its own screen,
   * which needs no serial number and no cable.
   *
   * So the claim code is demoted rather than removed. It is still exactly right
   * for the case it was built for — pre-authorising a serial before the hardware
   * arrives — and it is behind a disclosure so that a customer setting up their
   * first door does not find it first and conclude they need a laptop.
   */
  it('keeps the claim code, behind Advanced, and not as the primary action', async () => {
    const user = userEvent.setup()
    signIn()
    renderSite()

    // NOT in the page header any more.
    await screen.findByRole('heading', { name: SITE_A.site_name })
    expect(
      screen.queryByRole('button', { name: 'Provision a terminal' }),
    ).not.toBeInTheDocument()

    // Reachable, and honest about what it is for.
    const advanced = await screen.findByText(/pre-authorise a terminal for an installer/i)
    await user.click(advanced)

    expect(screen.getByText(/before the hardware arrives/i)).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Issue a claim code' }),
    ).toBeInTheDocument()

    // And it points at the path a customer should use instead.
    expect(screen.getByRole('link', { name: /the terminals page/i })).toBeInTheDocument()
  })

  it('does not offer it to somebody who could not use it', async () => {
    // ADMIN, matching the server: issuing a code authorises hardware to join
    // this site and be handed a credential.
    signIn('MANAGER')
    renderSite()

    await screen.findByRole('heading', { name: SITE_A.site_name })
    expect(
      screen.queryByText(/pre-authorise a terminal for an installer/i),
    ).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The form
// ---------------------------------------------------------------------------

describe('asking for a code', () => {
  it('SAYS THE SITE KEY IS NOT INVOLVED', async () => {
    signIn()
    renderDialog()

    expect(
      await screen.findByText(/provisioning key is not involved and is never handed out/i),
    ).toBeInTheDocument()
  })

  it('states that the code is single use, expiring, and supersedes an earlier one', async () => {
    signIn()
    renderDialog()

    expect(await screen.findByText('One code, one terminal, one use')).toBeInTheDocument()
    expect(screen.getByText(/cannot be read back/i)).toBeInTheDocument()
    expect(screen.getByText(/expires whether or not it is used/i)).toBeInTheDocument()
    expect(
      screen.getByText(/Issuing a second code for the same serial cancels the first/i),
    ).toBeInTheDocument()
  })

  it('requires a serial, because the code is bound to one', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await user.click(await screen.findByRole('button', { name: 'Issue claim code' }))
    expect(await screen.findByText(/A serial number is required/)).toBeInTheDocument()
    expect(state.requests.some((entry) => entry.method === 'POST')).toBe(false)
  })

  it('refuses a serial longer than the firmware can hold, before the server does', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-000000000000001')
    expect(await screen.findByText(/15 characters or fewer/)).toBeInTheDocument()
    expect(state.requests.some((entry) => entry.method === 'POST')).toBe(false)
  })

  it('REFUSES A LIFETIME THE SERVER WOULD SILENTLY SHORTEN', async () => {
    // The store clamps to 24 hours and returns success. Passing a week through
    // would show an expiry the operator did not choose and never explain why.
    const user = userEvent.setup()
    signIn()
    renderDialog()

    const lifetime = await screen.findByLabelText(/Valid for/)
    await user.clear(lifetime)
    await user.type(lifetime, '10080')
    await issueFor(user, 'AT-0009')

    expect(await screen.findByText(/caps a claim code at 1440 minutes/)).toBeInTheDocument()
    expect(state.requests.some((entry) => entry.method === 'POST')).toBe(false)
  })

  it('defaults to the platform’s own default rather than inventing one', async () => {
    signIn()
    renderDialog()
    expect(await screen.findByLabelText(/Valid for/)).toHaveValue(120)
  })
})

// ---------------------------------------------------------------------------
// The code itself
// ---------------------------------------------------------------------------

describe('showing the code', () => {
  it('shows it exactly once, and says so before anything else', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')

    const panel = await screen.findByRole('alertdialog')
    expect(
      within(panel).getByText(/shown once, works once, and cannot be recovered/i),
    ).toBeInTheDocument()
  })

  it('names the serial it is bound to, in the heading and the body', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')

    const panel = await screen.findByRole('alertdialog')
    expect(within(panel).getByRole('heading', { name: /AT-0009/ })).toBeInTheDocument()
    expect(within(panel).getByText(/will not register any other unit/i)).toBeInTheDocument()
  })

  it('shows the code as the API returned it, in a field that can be copied', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')

    const field = (await screen.findByLabelText('Claim code')) as HTMLInputElement
    // Exactly the string the server minted — not reformatted, not re-grouped.
    // An installer types what is on screen into a serial console.
    expect(field.value).toBe('H7K2-0M9P')
    expect(field).toHaveAttribute('readonly')
    expect(screen.getByRole('button', { name: 'Copy code' })).toBeInTheDocument()
  })

  it('states when it expires', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    expect(await screen.findByText(/Expires/)).toBeInTheDocument()
  })

  it('shows the non-secret prefix as what the trail records', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    expect(await screen.findByText('H7K2…')).toBeInTheDocument()
  })

  it('WARNS WHEN IT HAS JUST KILLED AN EARLIER CODE', async () => {
    // The fact most likely to strand somebody: an installer is holding a
    // printout that stopped working the moment this was issued.
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    await screen.findByLabelText('Claim code')
    await user.click(screen.getByRole('button', { name: 'Done' }).closest('div')!
      .querySelector('input[type="checkbox"]')!)
    await user.click(screen.getByRole('button', { name: 'Done' }))

    // Second code for the same serial.
    await issueFor(user, 'AT-0009')
    expect(
      await screen.findByText(/1 earlier code for this serial stopped working/i),
    ).toBeInTheDocument()
  })

  it('says nothing about superseding when there was nothing to supersede', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    await screen.findByLabelText('Claim code')
    expect(screen.queryByText(/stopped working/i)).not.toBeInTheDocument()
  })

  it('cannot be dismissed until the operator says they have it', async () => {
    // A plain close button is pressed reflexively, and the cost here is an
    // installer standing at a door with nothing to type.
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    await screen.findByLabelText('Claim code')

    expect(screen.getByRole('button', { name: 'Done' })).toBeDisabled()
    expect(screen.getByText(/You have not copied the code yet/)).toBeInTheDocument()

    await user.click(screen.getByLabelText(/I have copied this code/))
    expect(screen.getByRole('button', { name: 'Done' })).toBeEnabled()
  })

  it('explains what the installer does with it', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    expect(await screen.findByText('What the installer does with it')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The key is never the fallback
// ---------------------------------------------------------------------------

describe('the site provisioning key is never offered as a substitute', () => {
  it('is not mentioned as a way to register hardware anywhere in the flow', async () => {
    const user = userEvent.setup()
    signIn()
    const { container } = renderDialog()

    await screen.findByLabelText(/Serial number/)
    await issueFor(user, 'AT-0009')
    await screen.findByLabelText('Claim code')

    const text = container.textContent ?? ''
    // The phrase appears once, in the dialog's own description, and only to say
    // the key is NOT involved. It must never appear as an instruction.
    expect(text).not.toMatch(/use the (site|provisioning) key/i)
    expect(text).not.toMatch(/rotate/i)
  })

  it('never puts a site key or a device key on screen', async () => {
    const user = userEvent.setup()
    signIn()
    const { container } = renderDialog()

    await issueFor(user, 'AT-0009')
    await screen.findByLabelText('Claim code')

    for (const forbidden of ['ats_', 'atd_', 'api_key']) {
      expect(container.innerHTML).not.toContain(forbidden)
    }
  })

  it('never sends X-API-Key from the browser', async () => {
    const user = userEvent.setup()
    signIn()
    renderDialog()

    await issueFor(user, 'AT-0009')
    await screen.findByLabelText('Claim code')

    const posts = state.requests.filter((entry) => entry.url.includes('claim-codes'))
    expect(posts.length).toBeGreaterThan(0)
    for (const request of posts) {
      expect(request.headers.get('X-API-Key')).toBeNull()
      // The session's CSRF token is what authorises it.
      expect(request.headers.get('X-CSRF-Token')).toBe('csrf-token-value')
    }
  })
})

// ---------------------------------------------------------------------------
// Failure
// ---------------------------------------------------------------------------

describe('when issuing fails', () => {
  it('reports it and shows no code', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('claim-code', 500)
    renderDialog()

    await issueFor(user, 'AT-0009')

    expect(await screen.findByText(/Failed to issue claim code/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Claim code')).not.toBeInTheDocument()
  })

  it('leaves the form usable so the operator can try again', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('claim-code', 500)
    renderDialog()

    await issueFor(user, 'AT-0009')
    await screen.findByText(/Failed to issue claim code/)

    await user.click(screen.getByRole('button', { name: 'Issue claim code' }))
    await waitFor(() => expect(screen.getByLabelText('Claim code')).toBeInTheDocument())
  })
})
