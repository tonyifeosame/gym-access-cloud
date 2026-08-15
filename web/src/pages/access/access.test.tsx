import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Permission, Role, Schedule } from '../../api/types'
import {
  makePermission,
  makePerson,
  makeSchedule,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
  SITE_B,
} from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, resetTerminalModes, seed, state } from '../../test/server'
import { PersonDetailPage } from '../people/PersonDetailPage'
import { TerminalDetailPage } from '../terminals/TerminalDetailPage'
import { SchedulesPage } from './SchedulesPage'
import {
  crossesMidnight,
  describeDays,
  describeReason,
  describeWindow,
  maskOf,
  standingOf,
} from './accessVocabulary'

/**
 * Access control (APP-02).
 *
 * `permissions` and `doors` were created by a migration with a note that the
 * engine would land "in a later sprint". It did not, and zero lines read either
 * table — so every active person with a credential opened every terminal in
 * their company, permanently. The engine now exists; this is the console over
 * it.
 *
 * THE PROPERTY WORTH MORE THAN THE REST:
 *
 *   Absence of permission is not permission.
 *
 * A person with no rules reaches nothing. That is the opposite of what an
 * operator carries in from most products like this, and getting it backwards
 * ADMITS people rather than locking them out — the failure nobody reports.
 * Several tests below assert only that the console says so, in words, and they
 * are the most valuable ones here.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name }),
]

const TERMINALS = [
  makeTerminal({ serial_number: 'AT-0001', site_public_id: SITE_A.site_id }),
  makeTerminal({
    id: 2,
    public_id: 'terminal-public-2',
    serial_number: 'AT-0002',
    device_name: 'Loading Bay',
    site_public_id: SITE_B.site_id,
    site_name: SITE_B.site_name,
  }),
]

function signIn(
  role: Role = 'MANAGER',
  data: { permissions?: Permission[]; schedules?: Schedule[] } = {},
) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    applications: [{ code: 'ACCESS_CONTROL', settings: {} }],
  })
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({
    sites: SITES,
    terminals: TERMINALS,
    people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })],
    permissions: data.permissions ?? [],
    schedules: data.schedules ?? [],
  })
  return session
}

function render(path: string) {
  const router = createMemoryRouter(
    [
      { path: '/people/:externalId', element: <PersonDetailPage /> },
      { path: '/terminals/:serial', element: <TerminalDetailPage /> },
      { path: '/access/schedules', element: <SchedulesPage /> },
      { path: '/people', element: <p>People</p> },
      { path: '/terminals', element: <p>Terminals</p> },
      { path: '/sites/:siteId', element: <p>Site</p> },
    ],
    { initialEntries: [path] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The vocabulary, as pure functions
// ---------------------------------------------------------------------------

describe('reading the model', () => {
  it('names the common day patterns instead of listing them', () => {
    // "Mon, Tue, Wed, Thu, Fri" makes somebody check five items to confirm one
    // idea.
    expect(describeDays(127)).toBe('Every day')
    expect(describeDays(31)).toBe('Weekdays')
    expect(describeDays(96)).toBe('Weekends')
    expect(describeDays(maskOf([1, 4]))).toBe('Mon, Wed')
  })

  it('calls an empty day mask NEVER rather than rendering nothing', () => {
    // An empty string would read as a formatting bug rather than as a window
    // that matches nothing.
    expect(describeDays(0)).toBe('Never')
  })

  it('TREATS AN END BEFORE A START AS CROSSING MIDNIGHT, not as invalid', () => {
    // A 22:00–06:00 night shift is ONE window whose day mask names the day it
    // STARTS on. Refusing it would make night shifts inexpressible; splitting it
    // in two would make Sunday night's shift look like Monday's.
    expect(crossesMidnight({ days_of_week: 127, start_time: '22:00', end_time: '06:00' })).toBe(true)
    expect(crossesMidnight({ days_of_week: 127, start_time: '09:00', end_time: '17:00' })).toBe(false)
    expect(
      describeWindow({ days_of_week: 127, start_time: '22:00', end_time: '06:00' }),
    ).toMatch(/into the next day/)
  })

  it('trims a stored HH:MM:SS to what somebody typed', () => {
    expect(describeWindow({ days_of_week: 127, start_time: '09:00:00', end_time: '17:00:00' })).toBe(
      'Every day 09:00–17:00',
    )
  })

  it('tells expired, not-yet and inactive apart', () => {
    // All three look identical in a list that only shows dates, and an operator
    // asking "why can she not get in" is looking at exactly this.
    const now = new Date('2026-08-15T12:00:00Z')
    expect(standingOf(makePermission(), now)).toBe('IN_FORCE')
    expect(standingOf(makePermission({ active: false }), now)).toBe('INACTIVE')
    expect(standingOf(makePermission({ ends_at: '2026-07-01T00:00:00Z' }), now)).toBe('EXPIRED')
    expect(standingOf(makePermission({ starts_at: '2026-09-01T00:00:00Z' }), now)).toBe('NOT_YET')
  })

  it('gives every denial reason a meaning AND a remedy where one exists', () => {
    // "Denied" tells an operator standing next to a confused person nothing they
    // cannot already see.
    const noRule = describeReason('NO_PERMISSION')
    expect(noRule.meaning).toMatch(/absence of permission is not permission/i)
    expect(noRule.remedy).toBeTruthy()

    const schedule = describeReason('OUTSIDE_SCHEDULE')
    expect(schedule.remedy).toMatch(/timezone/i)

    // A deny explains why a company-wide grant did not save them.
    expect(describeReason('EXPLICIT_DENY').meaning).toMatch(/beats every allow/i)
  })

  it('renders a reason it has never met rather than dropping it', () => {
    const unknown = describeReason('QUARANTINE_HOLD')
    expect(unknown.label).toBe('Quarantine hold')
  })
})

// ---------------------------------------------------------------------------
// A person's access
// ---------------------------------------------------------------------------

describe('a person with no rules', () => {
  it('IS DESCRIBED AS REACHING NOTHING, not as unconfigured', () => {
    // The single most important sentence in this module.
    signIn()
    render('/people/P-0001')

    return screen.findByText('This person cannot get in anywhere').then(() => {
      expect(screen.getByText(/having no rules means reaching/i)).toBeInTheDocument()
      expect(screen.getByText(/not an unconfigured state that defaults to open/i)).toBeInTheDocument()
    })
  })
})

describe('a person’s access rules', () => {
  it('shows where each rule applies, and whether it is doing anything', async () => {
    signIn('MANAGER', {
      permissions: [
        makePermission({ id: 'p1', person_id: 'P-0001', site_name: 'Lagos Depot' }),
        makePermission({
          id: 'p2',
          person_id: 'P-0001',
          scope_type: 'TERMINAL',
          site_id: undefined,
          site_name: undefined,
          device_serial: 'AT-0002',
          device_name: 'Loading Bay',
          effect: 'DENY',
        }),
        makePermission({
          id: 'p3',
          person_id: 'P-0001',
          scope_type: 'COMPANY',
          site_id: undefined,
          site_name: undefined,
          ends_at: '2026-01-01T00:00:00Z',
        }),
      ],
    })
    render('/people/P-0001')

    // The heading renders immediately; the rules arrive with their own fetch.
    expect(await screen.findByText('Lagos Depot')).toBeInTheDocument()
    expect(screen.getByText('Loading Bay')).toBeInTheDocument()
    // A deny is marked as one, in words as well as by its edge.
    expect(screen.getByText('Deny')).toBeInTheDocument()
    // And the expired company-wide rule is marked rather than reading as active.
    expect(screen.getByText('Expired')).toBeInTheDocument()
  })

  it('says a rule with no schedule applies at any time', async () => {
    signIn('MANAGER', { permissions: [makePermission({ person_id: 'P-0001' })] })
    render('/people/P-0001')

    expect(await screen.findByText(/applies at any time of day/i)).toBeInTheDocument()
  })

  it('grants access and shows the new rule', async () => {
    const user = userEvent.setup()
    signIn()
    render('/people/P-0001')

    await screen.findByText('This person cannot get in anywhere')
    await user.click(screen.getByRole('button', { name: 'Grant access' }))

    const dialog = screen.getByRole('dialog')
    await user.selectOptions(within(dialog).getByLabelText(/^Where/), 'SITE')
    await user.selectOptions(within(dialog).getByLabelText(/^Site/), SITE_A.site_id)
    await user.click(within(dialog).getByRole('button', { name: 'Grant access' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText(SITE_A.site_name)).toBeInTheDocument()
  })

  it('WARNS THAT A COMPANY-WIDE RULE COVERS TERMINALS THAT DO NOT EXIST YET', async () => {
    // Often what somebody wants for staff, and rarely what they want for a
    // visitor — and it is not obvious from the word "everywhere".
    const user = userEvent.setup()
    signIn()
    render('/people/P-0001')

    await screen.findByText('This person cannot get in anywhere')
    await user.click(screen.getByRole('button', { name: 'Grant access' }))
    await user.selectOptions(screen.getByLabelText(/^Where/), 'COMPANY')

    expect(screen.getByText('This covers terminals that do not exist yet')).toBeInTheDocument()
  })

  it('says a DENY beats every allow, where the choice is made', async () => {
    const user = userEvent.setup()
    signIn()
    render('/people/P-0001')

    await screen.findByText('This person cannot get in anywhere')
    await user.click(screen.getByRole('button', { name: 'Grant access' }))
    await user.selectOptions(screen.getByLabelText(/^Effect/), 'DENY')

    expect(screen.getByText(/beats EVERY allow at every scope/i)).toBeInTheDocument()
  })

  it('does not send a site when the scope is the whole company', async () => {
    // The server refuses a COMPANY scope that names a site, rightly: a rule
    // saying two things about where it applies is a rule nobody can read.
    const user = userEvent.setup()
    signIn()
    render('/people/P-0001')

    await screen.findByText('This person cannot get in anywhere')
    await user.click(screen.getByRole('button', { name: 'Grant access' }))
    await user.selectOptions(screen.getByLabelText(/^Where/), 'SITE')
    await user.selectOptions(screen.getByLabelText(/^Site/), SITE_A.site_id)
    // Switch to COMPANY after choosing a site — the value is still in state.
    await user.selectOptions(screen.getByLabelText(/^Where/), 'COMPANY')
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Grant access' }),
    )

    // A 400 would mean the console sent both. It did not.
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('says what removing a rule costs before it happens', async () => {
    const user = userEvent.setup()
    signIn('MANAGER', {
      permissions: [makePermission({ person_id: 'P-0001', site_name: 'Lagos Depot' })],
    })
    render('/people/P-0001')

    // "Remove rule", not "Remove" — the page header's Remove deletes the
    // PERSON. Two identical labels doing things that different was a real
    // defect this test found.
    await user.click(await screen.findByRole('button', { name: 'Remove rule' }))

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/loses access to/i)).toBeInTheDocument()
    // And that it is not instant: terminals learn on their next sync.
    expect(within(dialog).getByText(/next sync/i)).toBeInTheDocument()
  })

  it('lets a VIEWER read the rules but not change them', async () => {
    // "Why was she refused" is a question somebody at a front desk has to be
    // able to answer.
    signIn('VIEWER', {
      permissions: [makePermission({ person_id: 'P-0001', site_name: 'Lagos Depot' })],
    })
    render('/people/P-0001')

    expect(await screen.findByText('Lagos Depot')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Grant access' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove rule' })).not.toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as no access', async () => {
    // "No rules" and "we could not ask" mean opposite things here.
    signIn()
    failNext('permissions', 500)
    render('/people/P-0001')

    await screen.findByRole('heading', { name: 'Access' })
    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve permissions/)
  })
})

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

describe('schedules', () => {
  it('says a schedule on its own admits nobody', async () => {
    signIn()
    render('/access/schedules')

    expect(await screen.findByText('A schedule on its own admits nobody')).toBeInTheDocument()
  })

  it('shows how many rules depend on one BEFORE it is edited', async () => {
    signIn('MANAGER', {
      schedules: [makeSchedule({ name: 'Office hours', permission_count: 4 })],
    })
    render('/access/schedules')

    await screen.findByText('Office hours')
    expect(screen.getByText('4 rules')).toBeInTheDocument()
    expect(screen.getByText('Weekdays 09:00–17:00')).toBeInTheDocument()
  })

  it('says the default timezone is the site’s, not the company’s', async () => {
    signIn('MANAGER', { schedules: [makeSchedule({ timezone: undefined })] })
    render('/access/schedules')

    expect(
      await screen.findByText(/Times are in each terminal's own site timezone/i),
    ).toBeInTheDocument()
  })

  it('creates a schedule with several windows', async () => {
    const user = userEvent.setup()
    signIn()
    render('/access/schedules')

    await screen.findByText('No schedules yet')
    await user.click(screen.getByRole('button', { name: 'New schedule' }))

    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^Name/), 'Night shift')
    await user.click(within(dialog).getByRole('button', { name: 'Add another window' }))
    await user.click(within(dialog).getByRole('button', { name: 'Create schedule' }))

    expect(await screen.findByText('Night shift')).toBeInTheDocument()
  })

  it('EXPLAINS A MIDNIGHT-CROSSING WINDOW instead of refusing it', async () => {
    const user = userEvent.setup()
    signIn()
    render('/access/schedules')

    await screen.findByText('No schedules yet')
    await user.click(screen.getByRole('button', { name: 'New schedule' }))

    const start = screen.getByLabelText('Start (window 1)')
    await user.clear(start)
    await user.type(start, '22:00')
    const end = screen.getByLabelText('End (window 1)')
    await user.clear(end)
    await user.type(end, '06:00')

    expect(screen.getByText(/This window crosses midnight/i)).toBeInTheDocument()
    expect(screen.getByText(/the days it STARTS on/i)).toBeInTheDocument()
    // And it is still submittable.
    expect(screen.getByRole('button', { name: 'Create schedule' })).toBeEnabled()
  })

  it('refuses a window with no days, and says which one', async () => {
    const user = userEvent.setup()
    signIn()
    render('/access/schedules')

    await screen.findByText('No schedules yet')
    await user.click(screen.getByRole('button', { name: 'New schedule' }))
    await user.type(screen.getByLabelText(/^Name/), 'Broken')

    // Clear every day on the only window.
    for (const day of ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']) {
      await user.click(screen.getByLabelText(day))
    }
    await user.click(screen.getByRole('button', { name: 'Create schedule' }))

    expect(await screen.findByText(/Window 1 has no days selected/i)).toBeInTheDocument()
  })

  it('WARNS THAT EDITING ONE CHANGES EVERY RULE THAT USES IT', async () => {
    const user = userEvent.setup()
    signIn('MANAGER', { schedules: [makeSchedule({ permission_count: 12 })] })
    render('/access/schedules')

    await screen.findByText('Office hours')
    await user.click(screen.getByRole('button', { name: 'Edit' }))

    expect(screen.getByText('This changes every rule that uses it')).toBeInTheDocument()
    expect(screen.getByText(/12 access rules refer/i)).toBeInTheDocument()
  })

  it('turns a refused deletion into an instruction rather than a failure', async () => {
    // The server refuses while rules depend on it, and the refusal is correct:
    // cascading would set the column NULL and silently WIDEN every rule that
    // used it.
    const user = userEvent.setup()
    signIn('MANAGER', { schedules: [makeSchedule({ permission_count: 3 })] })
    render('/access/schedules')

    await screen.findByText('Office hours')
    await user.click(screen.getByRole('button', { name: 'Delete' }))

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/will be/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/no time restriction at all/i)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Delete schedule' }))
    // Stays open, showing the server's refusal.
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/still used by permissions/i)
  })

  it('deletes one nothing depends on', async () => {
    const user = userEvent.setup()
    signIn('MANAGER', { schedules: [makeSchedule({ permission_count: 0 })] })
    render('/access/schedules')

    await screen.findByText('Office hours')
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete schedule' }),
    )

    await waitFor(() => expect(screen.queryByText('Office hours')).not.toBeInTheDocument())
  })
})

// ---------------------------------------------------------------------------
// The decision preview
// ---------------------------------------------------------------------------

describe('“would this person get in”', () => {
  it('answers with a reason and a remedy, not just a verdict', async () => {
    const user = userEvent.setup()
    signIn()
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))

    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Person identifier/), 'P-0001')
    await user.click(within(dialog).getByRole('button', { name: 'Check' }))

    expect(await within(dialog).findByText('Would be refused')).toBeInTheDocument()
    // The reason, and what to do about it.
    expect(within(dialog).getByText(/No rule covers this/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/Grant them access at this terminal/i)).toBeInTheDocument()
  })

  it('grants when a rule covers the terminal, and names the rule', async () => {
    const user = userEvent.setup()
    signIn('MANAGER', {
      permissions: [
        makePermission({ id: 'rule-7', person_id: 'P-0001', site_id: SITE_A.site_id }),
      ],
    })
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))
    await user.type(screen.getByLabelText(/Person identifier/), 'P-0001')
    await user.click(screen.getByRole('button', { name: 'Check' }))

    expect(await screen.findByText('Would be let in')).toBeInTheDocument()
    expect(screen.getByText('rule-7')).toBeInTheDocument()
  })

  it('shows that a DENY beats an allow', async () => {
    const user = userEvent.setup()
    signIn('MANAGER', {
      permissions: [
        makePermission({ id: 'allow-1', person_id: 'P-0001', scope_type: 'COMPANY', site_id: undefined }),
        makePermission({
          id: 'deny-1',
          person_id: 'P-0001',
          scope_type: 'TERMINAL',
          site_id: undefined,
          device_serial: 'AT-0001',
          effect: 'DENY',
        }),
      ],
    })
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))
    await user.type(screen.getByLabelText(/Person identifier/), 'P-0001')
    await user.click(screen.getByRole('button', { name: 'Check' }))

    expect(await screen.findByText('Would be refused')).toBeInTheDocument()
    expect(screen.getByText('deny-1')).toBeInTheDocument()
  })

  it('says NOTHING WAS RECORDED, because that is the reason it is safe to repeat', async () => {
    const user = userEvent.setup()
    signIn()
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))

    expect(screen.getByText('Nothing was recorded')).toBeInTheDocument()
    expect(screen.getByText(/cannot appear in Events/i)).toBeInTheDocument()
  })

  it('clears a previous answer when the question changes', async () => {
    // An answer about one person sitting under another person's name would be
    // read as being about them.
    const user = userEvent.setup()
    signIn()
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))
    await user.type(screen.getByLabelText(/Person identifier/), 'P-0001')
    await user.click(screen.getByRole('button', { name: 'Check' }))
    await screen.findByText('Would be refused')

    await user.type(screen.getByLabelText(/Person identifier/), '9')
    expect(screen.queryByText('Would be refused')).not.toBeInTheDocument()
  })

  it('marks an identifier that matched nobody', async () => {
    const user = userEvent.setup()
    signIn()
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))
    await user.type(screen.getByLabelText(/Person identifier/), 'NOT-A-PERSON')
    await user.click(screen.getByRole('button', { name: 'Check' }))

    expect(await screen.findByText('Not recognised')).toBeInTheDocument()
  })

  it('sends the request to THIS terminal, never one named in the body', async () => {
    // The terminal is the path parameter and is authorised upstream, so an
    // operator cannot preview a decision at a terminal they are not scoped to.
    const user = userEvent.setup()
    signIn()
    render('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: 'Check access' }))
    await user.type(screen.getByLabelText(/Person identifier/), 'P-0001')
    await user.click(screen.getByRole('button', { name: 'Check' }))

    await screen.findByText('Would be refused')
    const request = state.requests.find((entry) => entry.url.includes('/evaluate'))
    expect(request?.url).toContain('/terminals/AT-0001/evaluate')
  })
})
