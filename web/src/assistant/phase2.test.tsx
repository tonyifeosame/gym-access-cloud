import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import type { AssistantEvent, Role, Session } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { AppShell } from '../layout/AppShell'
import { SchedulesPage } from '../pages/access/SchedulesPage'
import { PeopleListPage } from '../pages/people/PeopleListPage'
import { PersonDetailPage } from '../pages/people/PersonDetailPage'
import { TerminalDetailPage } from '../pages/terminals/TerminalDetailPage'
import { TerminalsListPage } from '../pages/terminals/TerminalsListPage'
import { makePendingTerminal, makePerson, makeSchedule, makeSession, makeSite, makeTerminal } from '../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { resetServerState, seed, state } from '../test/server'
import { domainsFor } from './useAssistantChat'

/**
 * Phase 2a's half of the console: the screens behind the panel refresh by
 * the DOMAINS the server names on a result rather than by a list of tool
 * names kept here; a hand-off to the fleet page lands on the pending panel;
 * the new confirmation cards show the server's wording; and a multi-step
 * request presents one card per consequential step, in order, each only
 * after the previous one ran.
 */

function signIn(role: Role = 'MANAGER', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  return session
}

function renderShell(path: string, client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        ),
        children: [
          { index: true, element: <p>Overview</p> },
          { path: 'people', element: <PeopleListPage /> },
          { path: 'people/:externalId', element: <PersonDetailPage /> },
          { path: 'terminals', element: <TerminalsListPage /> },
          { path: 'terminals/:serial', element: <TerminalDetailPage /> },
          { path: 'access/schedules', element: <SchedulesPage /> },
        ],
      },
    ],
    { initialEntries: [path] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

async function openPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Assistant' }))
  return screen.getByRole('dialog', { name: 'Assistant' })
}

async function say(user: ReturnType<typeof userEvent.setup>, panel: HTMLElement, text: string) {
  await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), `${text}{Enter}`)
}

const reads = (needle: string) => () =>
  state.requests.filter((r) => r.method === 'GET' && r.url.includes(needle)).length

beforeEach(() => setCsrfToken(null))

describe('domain-driven refresh', () => {
  it('reads the domains the server named, and falls back to the capabilities map', () => {
    expect(domainsFor('resync_terminal', { domains: ['terminals', 'audit'] }, undefined)).toEqual(['terminals', 'audit'])
    expect(
      domainsFor('resync_terminal', {}, { enabled: true, effects: { resync_terminal: ['terminals', 'audit'] } }),
    ).toEqual(['terminals', 'audit'])
    expect(domainsFor('search_people', {}, { enabled: true, effects: { resync_terminal: ['terminals'] } })).toEqual([])
    // The event's word wins over a stale map.
    expect(domainsFor('t', { domains: ['sites'] }, { enabled: true, effects: { t: ['people'] } })).toEqual(['sites'])
  })

  it('refreshes the fleet after a terminal write, and leaves people alone', async () => {
    const user = userEvent.setup()
    signIn()
    const site = makeSite({ id: 'site-1', name: 'Lagos' })
    seed({
      assistantEnabled: true,
      sites: [site],
      terminals: [makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' })],
      people: [makePerson()],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'resync_terminal', arguments: { serial: 'AT-1' } },
          { type: 'tool.result', call_id: 'c1', tool: 'resync_terminal', status: 'EXECUTED', summary: 'ok', domains: ['terminals', 'audit'] },
          { type: 'handoff', call_id: 'c1', kind: 'terminal', route: '/terminals/AT-1', label: 'Open AT-1' },
          { type: 'assistant.message', text: 'Reception has been asked to resync.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('Reception')
    const terminalReads = reads('/console/terminals')
    const peopleReads = reads('/console/people')
    const before = terminalReads()
    const peopleBefore = peopleReads()

    const panel = await openPanel(user)
    await say(user, panel, 'resync reception')
    await within(panel).findByText('Reception has been asked to resync.')
    expect(within(panel).getByText('Resynced a terminal')).toBeInTheDocument()

    await waitFor(() => expect(terminalReads()).toBeGreaterThan(before))
    expect(peopleReads()).toBe(peopleBefore)
    expect(within(panel).getByRole('link', { name: 'Open AT-1' })).toHaveAttribute('href', '/terminals/AT-1')
  })

  it('refreshes schedules after a schedule write', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      schedules: [makeSchedule({ id: 'sch-1', name: 'Weekend' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'create_schedule', arguments: { name: 'Nights' } },
          { type: 'tool.result', call_id: 'c1', tool: 'create_schedule', status: 'EXECUTED', summary: 'ok', domains: ['schedules', 'audit'] },
          { type: 'assistant.message', text: 'Added Nights.' },
        ],
      ],
    })
    renderShell('/access/schedules')
    await screen.findByText('Weekend')
    const scheduleReads = reads('/console/schedules')
    const before = scheduleReads()

    const panel = await openPanel(user)
    await say(user, panel, 'add a Nights schedule')
    await within(panel).findByText('Added Nights.')
    expect(within(panel).getByText('Added a schedule')).toBeInTheDocument()
    await waitFor(() => expect(scheduleReads()).toBeGreaterThan(before))
  })

  it('refreshes the person after an update, a deactivation and a cancelled enrolment', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'update_person', arguments: { external_id: 'P-0001', full_name: 'Ada Okonkwo-Bello' } },
          { type: 'tool.result', call_id: 'c1', tool: 'update_person', status: 'EXECUTED', summary: 'ok', domains: ['people', 'audit'] },
          { type: 'assistant.message', text: 'Corrected the name.' },
        ],
        [
          { type: 'tool.call', call_id: 'c2', tool: 'cancel_enrollment', arguments: { external_id: 'P-0001' } },
          { type: 'tool.result', call_id: 'c2', tool: 'cancel_enrollment', status: 'EXECUTED', summary: 'ok', domains: ['people', 'audit'] },
          { type: 'assistant.message', text: 'Cancelled.' },
        ],
      ],
    })
    renderShell('/people/P-0001')
    await screen.findByRole('heading', { name: 'Ada Okonkwo', level: 1 })
    const personReads = reads('/console/people/P-0001')
    let before = personReads()

    const panel = await openPanel(user)
    await say(user, panel, 'fix the surname')
    await within(panel).findByText('Corrected the name.')
    expect(within(panel).getByText('Corrected a person')).toBeInTheDocument()
    await waitFor(() => expect(personReads()).toBeGreaterThan(before))

    before = personReads()
    await say(user, panel, 'cancel the enrolment')
    await within(panel).findByText('Cancelled.')
    expect(within(panel).getByText('Cancelled an enrolment')).toBeInTheDocument()
    await waitFor(() => expect(personReads()).toBeGreaterThan(before))
  })

  it('uses the capabilities map when a result arrives without domains', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      assistantEffects: { resync_terminal: ['terminals', 'audit'] },
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'resync_terminal', arguments: { serial: 'AT-1' } },
          // A replayed result: status and tool, no domains.
          { type: 'tool.result', call_id: 'c1', tool: 'resync_terminal', status: 'EXECUTED', summary: 'replayed' },
          { type: 'assistant.message', text: 'Done.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('Reception')
    const panel = await openPanel(user)
    // The capabilities query has to have answered for the fallback to know
    // anything; opening the panel reads it.
    await waitFor(() => expect(state.requests.some((r) => r.url.includes('/assistant/capabilities'))).toBe(true))
    const terminalReads = reads('/console/terminals')
    const before = terminalReads()
    await say(user, panel, 'resync')
    await within(panel).findByText('Done.')
    await waitFor(() => expect(terminalReads()).toBeGreaterThan(before))
  })

  it('refreshes nothing after a read, or after a write that did not run', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson()],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'explain_denial', arguments: { external_id: 'P-0001' } },
          { type: 'tool.result', call_id: 'c1', tool: 'explain_denial', status: 'EXECUTED', summary: 'ok' },
          { type: 'tool.call', call_id: 'c2', tool: 'update_person', arguments: { external_id: 'P-0001' } },
          { type: 'tool.result', call_id: 'c2', tool: 'update_person', status: 'NOT_FOUND', summary: 'Not found in your company', domains: ['people'] },
          { type: 'assistant.message', text: 'Nothing changed.' },
        ],
      ],
    })
    renderShell('/people')
    await screen.findByRole('heading', { name: 'People' })
    const peopleReads = reads('/console/people')
    const before = peopleReads()
    const panel = await openPanel(user)
    await say(user, panel, 'why was Ada refused, and rename her')
    await within(panel).findByText('Nothing changed.')
    expect(within(panel).getByText('Looked into a refusal')).toBeInTheDocument()
    // Give any invalidation a chance to fire, then assert it did not.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(peopleReads()).toBe(before)
  })
})

describe('the pending-terminals hand-off', () => {
  it('lands on the fleet page with the waiting panel focused, and consumes the parameter', async () => {
    signIn('ADMIN')
    seed({
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' })],
      pendingTerminals: [makePendingTerminal({ serial_number: 'AT-NEW-1' })],
    })
    renderShell('/terminals?pending=1')
    const heading = await screen.findByRole('heading', { name: 'Waiting to be set up' })
    await waitFor(() => expect(heading).toHaveFocus())
    expect(screen.getByText('AT-NEW-1')).toBeInTheDocument()
  })

  it('does nothing for a viewer, who is never shown the panel', async () => {
    signIn('VIEWER')
    seed({
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' })],
      pendingTerminals: [makePendingTerminal({ serial_number: 'AT-NEW-1' })],
    })
    renderShell('/terminals?pending=1')
    await screen.findByText('Reception')
    expect(screen.queryByRole('heading', { name: 'Waiting to be set up' })).not.toBeInTheDocument()
    expect(state.requests.some((r) => r.url.includes('terminal-announcements'))).toBe(false)
  })
})

describe('the new confirmation cards', () => {
  it('shows the deactivation wording and refreshes the person once it ran', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'set_person_active', arguments: { external_id: 'P-0001', active: false } },
          { type: 'tool.result', call_id: 'c1', tool: 'set_person_active', status: 'CONFIRMATION_REQUESTED', summary: 'waiting for your approval' },
          {
            type: 'confirmation.required',
            call_id: 'c1',
            confirmation_id: 'conf-1',
            token: 'v1.conf-1.sig',
            tool: 'set_person_active',
            arguments: { external_id: 'P-0001', active: false },
            consequence: {
              title: 'Deactivate Ada Okonkwo?',
              body: 'Every terminal in your company will be told to stop admitting them. The record and any enrolled credential are kept.',
              warnings: ['Reversible — you can activate them again from this same screen.'],
            },
            phrase_required: '',
            expires_at: '2026-09-17T12:00:00Z',
          },
          { type: 'assistant.message', text: 'Waiting for your approval.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'set_person_active', status: 'CONFIRMED_EXECUTED', summary: 'done', domains: ['people', 'onboarding', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'set_person_active', outcome: 'approved' },
          { type: 'handoff', call_id: 'conf-1', kind: 'person', route: '/people/P-0001', label: 'Open Ada Okonkwo' },
          { type: 'assistant.message', text: 'Ada Okonkwo is deactivated.' },
        ],
      ],
    })
    renderShell('/people/P-0001')
    await screen.findByRole('heading', { name: 'Ada Okonkwo', level: 1 })
    const personReads = reads('/console/people/P-0001')
    const before = personReads()

    const panel = await openPanel(user)
    await say(user, panel, 'deactivate Ada')
    const card = await within(panel).findByRole('region', { name: 'Deactivate Ada Okonkwo?' })
    expect(within(card).getByText(/stop admitting them/)).toBeInTheDocument()
    expect(within(card).getByText(/Reversible/)).toBeInTheDocument()
    expect(personReads()).toBe(before)

    await user.click(within(card).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('Ada Okonkwo is deactivated.')
    expect(within(card).getByText('Approved')).toBeInTheDocument()
    await waitFor(() => expect(personReads()).toBeGreaterThan(before))
    expect(within(panel).getByRole('link', { name: 'Open Ada Okonkwo' })).toHaveAttribute('href', '/people/P-0001')
  })
})

describe('a multi-step request', () => {
  const grantCard: AssistantEvent = {
    type: 'confirmation.required',
    call_id: 'c2',
    confirmation_id: 'conf-grant',
    token: 'v1.conf-grant.sig',
    tool: 'grant_access',
    arguments: { external_id: '4471', effect: 'ALLOW', scope_type: 'SITE', site_id: 'site-1' },
    consequence: { title: 'Let John Okafor in at Reception?', body: 'Adds a rule that lets John Okafor in at Reception, at any time.' },
    phrase_required: '',
    expires_at: '2026-09-17T12:00:00Z',
  }
  const enrolCard: AssistantEvent = {
    type: 'confirmation.required',
    call_id: 'c3',
    confirmation_id: 'conf-enrol',
    token: 'v1.conf-enrol.sig',
    tool: 'start_enrollment',
    arguments: { external_id: '4471', serial: 'AT-1' },
    consequence: { title: 'Ready to start fingerprint enrollment at Reception (Lagos)?', body: 'John Okafor must be standing at Reception.' },
    phrase_required: '',
    expires_at: '2026-09-17T12:00:00Z',
  }

  it('creates John, asks once for the rule, then once for the enrolment, and hands off', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })],
      assistantTurns: [
        // Turn 1: the safe step runs; the consequential one pauses.
        [
          { type: 'tool.call', call_id: 'c1', tool: 'create_person', arguments: { external_id: '4471', full_name: 'John Okafor' } },
          { type: 'tool.result', call_id: 'c1', tool: 'create_person', status: 'EXECUTED', summary: 'ok', domains: ['people', 'onboarding', 'audit'] },
          { type: 'tool.call', call_id: 'c2', tool: 'grant_access', arguments: { external_id: '4471' } },
          { type: 'tool.result', call_id: 'c2', tool: 'grant_access', status: 'CONFIRMATION_REQUESTED', summary: 'waiting for your approval' },
          grantCard,
          { type: 'assistant.message', text: 'Added John Okafor. Approve the Reception rule to continue.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        // Settlement 1: the rule runs, then the next step pauses.
        [
          { type: 'tool.result', call_id: 'conf-grant', tool: 'grant_access', status: 'CONFIRMED_EXECUTED', summary: 'done', domains: ['permissions', 'onboarding', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-grant', tool: 'grant_access', outcome: 'approved' },
          { type: 'tool.call', call_id: 'c3', tool: 'start_enrollment', arguments: { external_id: '4471', serial: 'AT-1' } },
          { type: 'tool.result', call_id: 'c3', tool: 'start_enrollment', status: 'CONFIRMATION_REQUESTED', summary: 'waiting for your approval' },
          enrolCard,
          { type: 'assistant.message', text: 'The rule is in. Approve the enrolment when John is at Reception.' },
          { type: 'turn.completed', turn_id: 't2', stop_reason: 'confirmation' },
        ],
        // Settlement 2: the enrolment starts and the console takes over.
        [
          { type: 'tool.result', call_id: 'conf-enrol', tool: 'start_enrollment', status: 'CONFIRMED_EXECUTED', summary: 'done', domains: ['people', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-enrol', tool: 'start_enrollment', outcome: 'approved' },
          { type: 'handoff', call_id: 'conf-enrol', kind: 'enrolment', route: '/people/4471?enrol=1', label: 'Open the enrolment screen for John Okafor' },
          { type: 'assistant.message', text: 'Reception is waiting for his finger.' },
          { type: 'turn.completed', turn_id: 't3', stop_reason: 'end_turn' },
        ],
      ],
    })
    renderShell('/people')
    await screen.findByText('Ada Okonkwo')
    const peopleReads = reads('/console/people?')
    const before = peopleReads()

    const panel = await openPanel(user)
    await say(user, panel, 'Create John Okafor with ID 4471, give him Reception access, and start fingerprint enrollment.')

    // Step 1 ran on its own and refreshed the list; step 2 is a card; step 3
    // has not been mentioned yet.
    const first = await within(panel).findByRole('region', { name: 'Let John Okafor in at Reception?' })
    expect(within(panel).getByText('Added a person')).toBeInTheDocument()
    await waitFor(() => expect(peopleReads()).toBeGreaterThan(before))
    expect(within(panel).queryByRole('region', { name: /fingerprint enrollment/ })).not.toBeInTheDocument()
    expect(state.requests.filter((r) => r.url.includes('/confirmations'))).toHaveLength(0)

    // Approving the rule runs it and brings the next card, and only then.
    await user.click(within(first).getByRole('button', { name: 'Approve' }))
    const second = await within(panel).findByRole('region', { name: 'Ready to start fingerprint enrollment at Reception (Lagos)?' })
    expect(within(first).getByText('Approved')).toBeInTheDocument()
    expect(state.requests.filter((r) => r.url.includes('/confirmations'))).toHaveLength(1)
    expect(within(panel).queryByRole('link', { name: /enrolment screen/ })).not.toBeInTheDocument()

    await user.click(within(second).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('Reception is waiting for his finger.')
    expect(within(second).getByText('Approved')).toBeInTheDocument()
    expect(state.requests.filter((r) => r.url.includes('/confirmations'))).toHaveLength(2)
    expect(within(panel).getByRole('link', { name: 'Open the enrolment screen for John Okafor' })).toHaveAttribute(
      'href',
      '/people/4471?enrol=1',
    )
  })

  it('stops after a rejected step: nothing later is asked for', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson()],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c2', tool: 'grant_access', arguments: { external_id: '4471' } },
          { type: 'tool.result', call_id: 'c2', tool: 'grant_access', status: 'CONFIRMATION_REQUESTED', summary: 'waiting for your approval' },
          grantCard,
          { type: 'assistant.message', text: 'Approve the Reception rule to continue.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'confirmation.settled', confirmation_id: 'conf-grant', tool: 'grant_access', outcome: 'rejected' },
          { type: 'assistant.message', text: 'Understood — I have not started the enrolment.' },
          { type: 'turn.completed', turn_id: 't2', stop_reason: 'end_turn' },
        ],
      ],
    })
    renderShell('/people')
    const panel = await openPanel(user)
    await say(user, panel, 'give John Reception access and enrol him')
    const card = await within(panel).findByRole('region', { name: 'Let John Okafor in at Reception?' })
    await user.click(within(card).getByRole('button', { name: 'Reject' }))
    await within(panel).findByText('Understood — I have not started the enrolment.')
    expect(within(card).getByText('Rejected')).toBeInTheDocument()
    expect(within(panel).queryByRole('region', { name: /fingerprint enrollment/ })).not.toBeInTheDocument()
    expect(within(panel).queryByRole('link', { name: /enrolment screen/ })).not.toBeInTheDocument()
  })
})
