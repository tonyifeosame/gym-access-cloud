import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import { MAX_OFFLINE_GRACE_MINUTES, type Role } from '../../api/types'
import { makeSession, makeSite, SITE_A } from '../../test/fixtures'
import { renderWithSession } from '../../test/render'
import { expectNoDoorWording } from '../../test/vocabulary'
import { failNext, offlinePolicyFor, resetServerState, seed, state } from '../../test/server'
import { OfflinePolicyPanel } from './OfflinePolicyPanel'
import { describeGrace, graceError, usesGracePeriod } from './offlinePolicy'

/**
 * What terminals do when they cannot reach the platform.
 *
 * THE PROPERTY UNDER TEST IS A CLAIM ABOUT A BUILDING, not about a component.
 * Every assertion here exists because getting it wrong is either a site locked
 * out during an outage or a door admitting somebody who was dismissed this
 * morning — and only one of those gets reported.
 *
 * THE TESTS ARE SPLIT BETWEEN WHAT IS IN FORCE AND WHAT THE FORM WOULD SEND, and
 * that split is the design rather than tidiness. The site projection carries the
 * validated columns a terminal is actually sent, so the panel can show the real
 * policy — and the moment somebody touches a radio, the selected option stops
 * describing the doors and starts describing an intention. Conflating the two is
 * how a screen ends up reporting a safety decision that was never applied.
 */

const SITE = makeSite({ id: SITE_A.site_id, name: SITE_A.site_name, terminal_count: 4 })

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

function renderPanel() {
  return renderWithSession(<OfflinePolicyPanel site={SITE} />)
}

/**
 * Opens the editor.
 *
 * THE PANEL RESTS ON THE ANSWER, NOT THE QUESTION. It used to render the three
 * options and their consequences unconditionally -- about two hundred and forty
 * words permanently open, with the policy in force stated three times over,
 * because the pre-selected radio repeated what the badge and the summary had
 * already said. The alternatives now appear when somebody asks to change
 * something, so every test about the CHOICE goes through here first, and the
 * tests about what is IN FORCE deliberately do not.
 */
async function openEditor(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Change policy' }))
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The bound, which was wrong
// ---------------------------------------------------------------------------

describe('the grace period’s bounds are the platform’s', () => {
  it('allows 30 days, which this console used to refuse', () => {
    // The old guided setting capped at 10,080 minutes — seven days — a number
    // this build invented. `models.MaxOfflineGraceMinutes`, the CHECK constraint
    // and the firmware's own constant are all 43,200.
    expect(MAX_OFFLINE_GRACE_MINUTES).toBe(43_200)
    expect(graceError('43200')).toBeUndefined()
    expect(graceError('10081')).toBeUndefined()
  })

  it('refuses more than 30 days, naming the real limit', () => {
    expect(graceError('43201')).toMatch(/43,200 minutes \(30 days\)/)
  })

  it('accepts zero, which is not the same instruction as DENY_ALL', () => {
    // Zero means the terminal stops trusting its cache immediately under a
    // policy that could later be widened. The policy itself is a separate
    // decision, and collapsing the two would remove one of them.
    expect(graceError('0')).toBeUndefined()
  })

  it('refuses a negative, a fraction and a blank', () => {
    expect(graceError('-1')).toBeTruthy()
    expect(graceError('5.5')).toBeTruthy()
    expect(graceError('   ')).toBeTruthy()
  })
})

describe('the grace period reads as a duration', () => {
  it('turns minutes into something a person can weigh', () => {
    // "43200 minutes" is not a length of time anybody can reason about, and the
    // whole point of the control is deciding how long a building runs on stale
    // information.
    expect(describeGrace(43_200)).toBe('30 days')
    expect(describeGrace(720)).toBe('12 hours')
    expect(describeGrace(45)).toBe('45 minutes')
    expect(describeGrace(1)).toBe('1 minute')
  })

  it('says what zero actually means rather than "0 minutes"', () => {
    expect(describeGrace(0)).toMatch(/refuses as soon as contact is lost/)
  })
})

describe('the grace period belongs to exactly one policy', () => {
  it('applies to CACHED_GRACE and to neither of the others', () => {
    expect(usesGracePeriod('CACHED_GRACE')).toBe(true)
    expect(usesGracePeriod('DENY_ALL')).toBe(false)
    expect(usesGracePeriod('CACHED_INDEFINITE')).toBe(false)
    expect(usesGracePeriod('')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// The panel
// ---------------------------------------------------------------------------

describe('choosing what happens during an outage', () => {
  it('RESTS ON THE ANSWER, not on the question', async () => {
    // The page this sits on is opened to check a terminal count. Three options
    // and three paragraphs of consequence, permanently expanded, is a safety
    // control shouting at somebody who did not come to change one -- and the
    // pre-selected radio stated the policy in force a third time, after the
    // badge and the summary had both said it.
    signIn()
    renderPanel()

    expect(await screen.findByText('In force now')).toBeInTheDocument()
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.queryByText(/a network fault becomes a lockout/i)).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Apply to every terminal here' }),
    ).not.toBeInTheDocument()
  })

  it('offers all three policies, each with its consequence beside it, once asked', async () => {
    // A select would hide two of the three behind an interaction. The choice is
    // between exposures, and they have to be readable side by side. NOTHING WAS
    // CUT from the consequences -- they moved to the moment they are weighed.
    const user = userEvent.setup()
    signIn()
    renderPanel()
    await openEditor(user)

    expect(screen.getByRole('radio', { name: 'Refuse everybody' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Keep working for a limited time' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Keep working indefinitely' })).toBeInTheDocument()

    expect(screen.getByText(/a network fault becomes a lockout/i)).toBeInTheDocument()
    expect(screen.getByText(/until the grace period runs out/i)).toBeInTheDocument()
    expect(screen.getByText(/keeps getting in until it reconnects/i)).toBeInTheDocument()
  })

  it('closes the editor again on cancel, leaving what is in force on screen', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()
    await openEditor(user)

    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    const shown = screen.getByText('In force now').closest('div') as HTMLElement
    expect(within(shown).getByText('Keep working for a limited time')).toBeInTheDocument()
  })

  it('re-seeds from the site when reopened, not from the abandoned selection', async () => {
    // Somebody may open this, wander off, and come back after a colleague has
    // changed the policy. Starting from a stale selection is how a change nobody
    // saw gets reverted.
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await openEditor(user)

    expect(screen.getByRole('radio', { name: 'Keep working for a limited time' })).toBeChecked()
    expect(screen.getByRole('radio', { name: 'Refuse everybody' })).not.toBeChecked()
  })

  it('SHOWS WHAT IS ACTUALLY IN FORCE, from the site rather than from a default', async () => {
    // The site projection carries the validated columns a terminal is really
    // sent. Showing a plausible default instead would be telling an operator
    // their building behaves in a way it may not — the one field pair where
    // that mistake describes a door rather than a record.
    signIn()
    renderPanel()

    expect(await screen.findByText('In force now')).toBeInTheDocument()
    const shown = screen.getByText('In force now').closest('div') as HTMLElement
    expect(within(shown).getByText('Keep working for a limited time')).toBeInTheDocument()
  })

  it('shows the grace period in force as a duration, not a raw count', async () => {
    signIn()
    renderPanel()

    await screen.findByText('In force now')
    expect(screen.getByText('12 hours')).toBeInTheDocument()
  })

  it('pre-selects the policy in force, so the form starts from the truth', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()
    await openEditor(user)

    expect(screen.getByRole('radio', { name: 'Keep working for a limited time' })).toBeChecked()
    expect(screen.getByRole('radio', { name: 'Refuse everybody' })).not.toBeChecked()
  })

  it('cannot be applied while the form matches what the site already holds', async () => {
    // Applying an unchanged policy would push a settings job to every terminal
    // at the site for no reason. The disabled button IS the message now -- the
    // sentence that used to say "this is what the site is already set to" was
    // explaining a state the control was already showing.
    const user = userEvent.setup()
    signIn()
    renderPanel()
    await openEditor(user)

    expect(screen.getByRole('button', { name: 'Apply to every terminal here' })).toBeDisabled()
    expect(screen.queryByText('This changes what your terminals do')).not.toBeInTheDocument()
  })

  /*
    THE SUBJECT OF THIS NOTE IS THE TERMINALS, and it now says so. It read
    "This changes what the doors do", which named one kind of hardware for a
    setting that is sent to every terminal at the site whatever each one is
    mounted to.
  */
  it('names the terminals rather than the doors in the impact note', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))

    expect(screen.getByText('This changes what your terminals do')).toBeInTheDocument()
    expectNoDoorWording('The offline-policy panel', document.body.textContent ?? '')
  })

  it('WARNS WHAT THE CHANGE REPLACES once something is chosen', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))

    expect(screen.getByText('This changes what your terminals do')).toBeInTheDocument()
    expect(
      screen.getByText(/all 4 terminals at this site/i),
    ).toBeInTheDocument()
    // The case that matters during an actual outage.
    expect(
      screen.getByText(/already offline will not hear about this until it comes back/i),
    ).toBeInTheDocument()
  })

  it('SHOWS THE GRACE PERIOD ONLY FOR THE POLICY THAT USES IT', async () => {
    // A number shown beside DENY_ALL reads as a value that applies, and the
    // platform ignores it for both of the other two.
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))
    expect(screen.queryByLabelText(/Grace period/)).not.toBeInTheDocument()

    await user.click(screen.getByRole('radio', { name: 'Keep working for a limited time' }))
    expect(screen.getByLabelText(/Grace period/)).toBeInTheDocument()

    await user.click(screen.getByRole('radio', { name: 'Keep working indefinitely' }))
    expect(screen.queryByLabelText(/Grace period/)).not.toBeInTheDocument()
  })

  it('states the platform’s real limit beside the field', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Keep working for a limited time' }))
    expect(screen.getByText(/43,200 minutes — 30 days/)).toBeInTheDocument()
  })

  it('says how long the entered period actually is', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Keep working for a limited time' }))
    expect(screen.getByText(/That is 12 hours/)).toBeInTheDocument()
  })

})

describe('applying a policy', () => {
  it('sends the policy the operator chose', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))
    await user.click(screen.getByRole('button', { name: 'Apply to every terminal here' }))

    await waitFor(() => expect(offlinePolicyFor(SITE_A.site_id)?.policy).toBe('DENY_ALL'))
  })

  it('sends the grace period only with the policy that uses it', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Keep working for a limited time' }))
    const grace = screen.getByLabelText(/Grace period/)
    await user.clear(grace)
    await user.type(grace, '2880')
    await user.click(screen.getByRole('button', { name: 'Apply to every terminal here' }))

    await waitFor(() => expect(offlinePolicyFor(SITE_A.site_id)?.grace).toBe(2880))
    expect(offlinePolicyFor(SITE_A.site_id)?.policy).toBe('CACHED_GRACE')
  })

  it('accepts a value the OLD console would have refused', async () => {
    // 20,160 minutes is fourteen days: legal on the platform, rejected by the
    // 10,080 bound this build used to enforce with a message naming no real rule.
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Keep working for a limited time' }))
    const grace = screen.getByLabelText(/Grace period/)
    await user.clear(grace)
    await user.type(grace, '20160')
    await user.click(screen.getByRole('button', { name: 'Apply to every terminal here' }))

    await waitFor(() => expect(offlinePolicyFor(SITE_A.site_id)?.grace).toBe(20_160))
  })

  it('refuses an out-of-range grace period before asking the server', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Keep working for a limited time' }))
    const grace = screen.getByLabelText(/Grace period/)
    await user.clear(grace)
    await user.type(grace, '99999')

    const before = state.requests.filter((entry) => entry.method === 'PUT').length
    await user.click(screen.getByRole('button', { name: 'Apply to every terminal here' }))

    expect(await screen.findByText(/at most 43,200 minutes/)).toBeInTheDocument()
    expect(state.requests.filter((entry) => entry.method === 'PUT')).toHaveLength(before)
  })

  it('confirms in words what the site will now do', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Keep working indefinitely' }))
    await user.click(screen.getByRole('button', { name: 'Apply to every terminal here' }))

    expect(
      await screen.findByText(/keep working offline for as long as an outage lasts/i),
    ).toBeInTheDocument()
  })

  it('reports a failure instead of claiming the policy changed', async () => {
    const user = userEvent.setup()
    signIn()
    renderPanel()

    // A real change, so the button is live — an unchanged form is disabled.
    await openEditor(user)
    await user.click(screen.getByRole('radio', { name: 'Refuse everybody' }))

    failNext('update-site', 500)
    await user.click(screen.getByRole('button', { name: 'Apply to every terminal here' }))

    expect(await screen.findByText(/Could not change the offline policy/)).toBeInTheDocument()
    // And the panel still reports the policy the platform actually holds.
    const shown = screen.getByText('In force now').closest('div') as HTMLElement
    expect(within(shown).getByText('Keep working for a limited time')).toBeInTheDocument()
  })
})

describe('role restrictions mirror the server', () => {
  /*
    A NON-ADMIN IS SHOWN THE ANSWER AND NOTHING ELSE, which is the correction
    these three tests now hold the line on.

    The panel used to render the whole editor for every role and disable it. A
    manager or a viewer therefore got a hundred and eighty words of consequence
    text describing three choices they could not make, below the one line that
    told them what was actually true -- an offer that could only ever have
    produced a 403 if it had been reachable.

    Read-only is not the same as edit-disabled: what somebody cannot do, they
    should not have to read past.
  */
  it('shows a VIEWER what is in force and no editor at all', async () => {
    signIn('VIEWER')
    renderPanel()

    expect(await screen.findByText('In force now')).toBeInTheDocument()
    const shown = screen.getByText('In force now').closest('div') as HTMLElement
    expect(within(shown).getByText('Keep working for a limited time')).toBeInTheDocument()

    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Change policy' })).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Apply to every terminal here' }),
    ).not.toBeInTheDocument()
  })

  it('REFUSES A MANAGER, because this rides on the site route rather than settings', async () => {
    // The panel below this one is MANAGER, and the two look like the same kind
    // of change. They are not: the offline policy is carried on
    // `PUT /console/sites/{id}`, which is ADMIN.
    signIn('MANAGER')
    renderPanel()

    expect(await screen.findByText(/administrator or owner action/i)).toBeInTheDocument()
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Change policy' })).not.toBeInTheDocument()
  })

  it('lets an ADMIN set it', async () => {
    signIn('ADMIN')
    renderPanel()

    // Waited for rather than asserted immediately: the panel renders before
    // GET /auth/me resolves, and until it does there is no role to gate on.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Change policy' })).toBeEnabled(),
    )
    expect(screen.queryByText(/administrator or owner action/i)).not.toBeInTheDocument()
  })
})
