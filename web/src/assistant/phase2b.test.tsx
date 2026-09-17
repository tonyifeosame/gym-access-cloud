import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import type { AssistantEvent, Role, Session } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { AppShell } from '../layout/AppShell'
import { SchedulesPage } from '../pages/access/SchedulesPage'
import { ActivityPage } from '../pages/activity/ActivityPage'
import { TerminalsListPage } from '../pages/terminals/TerminalsListPage'
import { makePendingTerminal, makeSchedule, makeSession, makeSite, makeTerminal } from '../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { resetServerState, seed, state } from '../test/server'
import { describeTool } from './useAssistantChat'

/**
 * Phase 2b's half of the console.
 *
 * NOTHING NEW IS DRAWN, and that is the result rather than a shortcut. The
 * four new consequential tools render through the same card the Phase 2a
 * ones do, because the card holds no wording of its own -- the title, the
 * body and the warnings are the server's, which is what keeps the assistant's
 * words and the console dialogs' words one text. The screens behind the panel
 * refresh by the DOMAINS the server names, so approving a terminal refreshes
 * the waiting list, the fleet, the sites and the audit trail without this
 * file knowing what the tool was called.
 *
 * What is under test here: the cards say what will be affected before the
 * operator approves, the right caches move afterwards, a rejection moves
 * none, a read moves none, and the panel keeps its roles and labels.
 */

function signIn(role: Role = 'ADMIN', overrides: Partial<Session> = {}) {
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
          { path: 'terminals', element: <TerminalsListPage /> },
          { path: 'access/schedules', element: <SchedulesPage /> },
          { path: 'activity', element: <ActivityPage /> },
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

/** A confirmation.required event with the server's own wording. */
function card(
  overrides: Partial<Extract<AssistantEvent, { type: 'confirmation.required' }>>,
): AssistantEvent {
  return {
    type: 'confirmation.required',
    call_id: 'c1',
    confirmation_id: 'conf-1',
    token: 'v1.conf-1.sig',
    tool: 'run_device_test',
    arguments: {},
    consequence: { title: 'Title?', body: 'Body.' },
    phrase_required: '',
    expires_at: '2026-09-17T12:00:00Z',
    ...overrides,
  } as AssistantEvent
}

beforeEach(() => setCsrfToken(null))

describe('the Phase 2b tool chips', () => {
  it('names every new tool in words an operator reads, not the tool name', () => {
    expect(describeTool('list_audit')).toBe('Read who changed what')
    expect(describeTool('withdraw_command')).toBe('Cancelled a command')
    expect(describeTool('run_device_test')).toBe('Hardware test')
    expect(describeTool('delete_schedule')).toBe('Removed a schedule')
    expect(describeTool('approve_pending_terminal')).toBe('Terminal set-up')
    expect(describeTool('reject_pending_terminal')).toBe('Terminal set-up')
    // Anything unknown still reads as words rather than an identifier.
    expect(describeTool('something_new')).toBe('something new')
  })
})

describe('the device-test card', () => {
  it('says which terminal and where before approval, and refreshes the fleet after it', async () => {
    const user = userEvent.setup()
    signIn('MANAGER')
    seed({
      assistantEnabled: true,
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [
        makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' }),
      ],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'run_device_test', arguments: { serial: 'AT-1', target: 'buzzer' } },
          { type: 'tool.result', call_id: 'c1', tool: 'run_device_test', status: 'CONFIRMATION_REQUESTED', summary: 'waiting for your approval' },
          card({
            tool: 'run_device_test',
            arguments: { serial: 'AT-1', target: 'buzzer' },
            consequence: {
              title: 'Have Reception (Lagos) sound its buzzer?',
              body: 'Reception will sound its buzzer when it next checks in, which is usually within a minute. Anybody standing at it will notice. It admits nobody, refuses nobody and opens nothing.',
              warnings: ['This terminal is offline rather than online, so it may not collect the test before it lapses.'],
            },
          }),
          { type: 'assistant.message', text: 'Waiting for your approval.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'run_device_test', status: 'CONFIRMED_EXECUTED', summary: 'queued', domains: ['terminals', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'run_device_test', outcome: 'approved' },
          { type: 'handoff', call_id: 'conf-1', kind: 'terminal', route: '/terminals/AT-1', label: 'Open AT-1' },
          { type: 'assistant.message', text: 'Reception will sound its buzzer shortly.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('Reception')
    const terminalReads = reads('/console/terminals')
    const before = terminalReads()

    const panel = await openPanel(user)
    await say(user, panel, 'make reception beep')

    // The affected resource, and where it is, are on the card before the
    // operator has agreed to anything.
    const confirmation = await within(panel).findByRole('region', { name: 'Have Reception (Lagos) sound its buzzer?' })
    expect(within(confirmation).getByText(/Anybody standing at it will notice/)).toBeInTheDocument()
    expect(within(confirmation).getByText(/offline rather than online/)).toBeInTheDocument()
    expect(within(panel).getByText('Hardware test')).toBeInTheDocument()
    // Nothing has been refreshed, because nothing has happened.
    expect(terminalReads()).toBe(before)

    await user.click(within(confirmation).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('Reception will sound its buzzer shortly.')
    expect(within(confirmation).getByText('Approved')).toBeInTheDocument()
    await waitFor(() => expect(terminalReads()).toBeGreaterThan(before))
    expect(within(panel).getByRole('link', { name: 'Open AT-1' })).toHaveAttribute('href', '/terminals/AT-1')
  })

  it('refreshes nothing when the operator rejects it', async () => {
    const user = userEvent.setup()
    signIn('MANAGER')
    seed({
      assistantEnabled: true,
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [
        makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' }),
      ],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'run_device_test', arguments: { serial: 'AT-1', target: 'display' } },
          { type: 'tool.result', call_id: 'c1', tool: 'run_device_test', status: 'CONFIRMATION_REQUESTED', summary: 'waiting' },
          card({ consequence: { title: 'Have Reception (Lagos) light its display?', body: 'Anybody standing at it will notice.' } }),
          { type: 'assistant.message', text: 'Waiting for your approval.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'run_device_test', outcome: 'rejected' },
          { type: 'assistant.message', text: 'Left it alone.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('Reception')
    const terminalReads = reads('/console/terminals')
    const before = terminalReads()

    const panel = await openPanel(user)
    await say(user, panel, 'light the display')
    const confirmation = await within(panel).findByRole('region', { name: 'Have Reception (Lagos) light its display?' })
    await user.click(within(confirmation).getByRole('button', { name: 'Reject' }))
    await within(panel).findByText('Left it alone.')
    expect(within(confirmation).getByText('Rejected')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(terminalReads()).toBe(before)
  })
})

describe('the delete-schedule card', () => {
  it('states that nothing uses it, and refreshes the schedules once it ran', async () => {
    const user = userEvent.setup()
    signIn('MANAGER')
    seed({
      assistantEnabled: true,
      schedules: [makeSchedule({ id: 'sch-1', name: 'Weekend', permission_count: 0 })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'delete_schedule', arguments: { schedule_id: 'sch-1' } },
          { type: 'tool.result', call_id: 'c1', tool: 'delete_schedule', status: 'CONFIRMATION_REQUESTED', summary: 'waiting' },
          card({
            tool: 'delete_schedule',
            arguments: { schedule_id: 'sch-1' },
            consequence: {
              title: 'Delete Weekend?',
              body: 'Nothing uses it: 0 access rules refer to this schedule, so no one’s access changes.',
              warnings: ['If an access rule starts using it between now and your approval, the deletion is refused rather than quietly widening that rule.'],
            },
          }),
          { type: 'assistant.message', text: 'Waiting for your approval.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'delete_schedule', status: 'CONFIRMED_EXECUTED', summary: 'deleted', domains: ['schedules', 'permissions', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'delete_schedule', outcome: 'approved' },
          { type: 'assistant.message', text: 'Weekend is gone.' },
        ],
      ],
    })
    renderShell('/access/schedules')
    await screen.findByText('Weekend')
    const scheduleReads = reads('/console/schedules')
    const before = scheduleReads()

    const panel = await openPanel(user)
    await say(user, panel, 'delete the Weekend schedule')
    const confirmation = await within(panel).findByRole('region', { name: 'Delete Weekend?' })
    expect(within(confirmation).getByText(/0 access rules refer to this schedule/)).toBeInTheDocument()
    expect(within(confirmation).getByText(/refused rather than quietly widening/)).toBeInTheDocument()
    expect(scheduleReads()).toBe(before)

    await user.click(within(confirmation).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('Weekend is gone.')
    // Two chips: the call that asked, and the approval that ran.
    expect(within(panel).getAllByText('Removed a schedule').length).toBeGreaterThan(0)
    await waitFor(() => expect(scheduleReads()).toBeGreaterThan(before))
  })
})

describe('the terminal set-up cards', () => {
  it('names the serial and the site, then refreshes the waiting list, the fleet and the trail', async () => {
    const user = userEvent.setup()
    signIn('ADMIN')
    seed({
      assistantEnabled: true,
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [
        makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' }),
      ],
      pendingTerminals: [makePendingTerminal({ id: 'pend-1', serial_number: 'AT-NEW-1' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'approve_pending_terminal', arguments: { pending_id: 'pend-1', site_id: 'site-1', device_name: 'Side Gate' } },
          { type: 'tool.result', call_id: 'c1', tool: 'approve_pending_terminal', status: 'CONFIRMATION_REQUESTED', summary: 'waiting' },
          card({
            tool: 'approve_pending_terminal',
            arguments: { pending_id: 'pend-1', site_id: 'site-1', device_name: 'Side Gate' },
            consequence: {
              title: 'Set AT-NEW-1 up at Lagos?',
              body: 'This authorises the unit to join your company as Side Gate at Lagos. It collects its credential the next time it checks in. Nothing is issued now and no credential is shown here.',
              warnings: ['Undoing this is reject_pending_terminal, which releases the serial so the unit can be set up again.'],
            },
          }),
          { type: 'assistant.message', text: 'Waiting for your approval.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'approve_pending_terminal', status: 'CONFIRMED_EXECUTED', summary: 'approved', domains: ['pending_terminals', 'terminals', 'sites', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'approve_pending_terminal', outcome: 'approved' },
          { type: 'handoff', call_id: 'conf-1', kind: 'pending_terminals', route: '/terminals?pending=1', label: 'Open terminals waiting to be set up' },
          { type: 'assistant.message', text: 'AT-NEW-1 is approved for Lagos.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('AT-NEW-1')
    const pendingReads = reads('terminal-announcements')
    const terminalReads = reads('/console/terminals?')
    const siteReads = reads('/console/sites')
    const pendingBefore = pendingReads()
    const terminalsBefore = terminalReads()
    const sitesBefore = siteReads()

    const panel = await openPanel(user)
    await say(user, panel, 'approve the new terminal into Lagos')

    // WHAT IS AFFECTED IS ON THE CARD: which unit, which site, what it will
    // be called, and that no credential is shown.
    const confirmation = await within(panel).findByRole('region', { name: 'Set AT-NEW-1 up at Lagos?' })
    expect(within(confirmation).getByText(/as Side Gate at Lagos/)).toBeInTheDocument()
    expect(within(confirmation).getByText(/no credential is shown here/)).toBeInTheDocument()
    expect(within(confirmation).getByText(/releases the serial/)).toBeInTheDocument()
    expect(pendingReads()).toBe(pendingBefore)

    await user.click(within(confirmation).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('AT-NEW-1 is approved for Lagos.')
    expect(within(panel).getAllByText('Terminal set-up').length).toBeGreaterThan(0)
    await waitFor(() => expect(pendingReads()).toBeGreaterThan(pendingBefore))
    await waitFor(() => expect(terminalReads()).toBeGreaterThan(terminalsBefore))
    await waitFor(() => expect(siteReads()).toBeGreaterThan(sitesBefore))
    expect(within(panel).getByRole('link', { name: 'Open terminals waiting to be set up' })).toHaveAttribute(
      'href',
      '/terminals?pending=1',
    )
  })

  it('shows the undo wording when the unit was already approved', async () => {
    const user = userEvent.setup()
    signIn('ADMIN')
    seed({
      assistantEnabled: true,
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [
        makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' }),
      ],
      pendingTerminals: [makePendingTerminal({ id: 'pend-1', serial_number: 'AT-NEW-1', state: 'APPROVED' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'reject_pending_terminal', arguments: { pending_id: 'pend-1' } },
          { type: 'tool.result', call_id: 'c1', tool: 'reject_pending_terminal', status: 'CONFIRMATION_REQUESTED', summary: 'waiting' },
          card({
            tool: 'reject_pending_terminal',
            arguments: { pending_id: 'pend-1' },
            consequence: {
              title: 'Undo the approval of AT-NEW-1?',
              body: 'AT-NEW-1 was approved and has not collected its credential yet. This withdraws that approval and releases the serial.',
              warnings: ['If the unit is already installed on a door, it will not come into service until somebody approves it again.'],
            },
          }),
          { type: 'assistant.message', text: 'Waiting for your approval.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
        ],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'reject_pending_terminal', status: 'CONFIRMED_EXECUTED', summary: 'rejected', domains: ['pending_terminals', 'audit'] },
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'reject_pending_terminal', outcome: 'approved' },
          { type: 'assistant.message', text: 'The approval is withdrawn.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('AT-NEW-1')
    const pendingReads = reads('terminal-announcements')
    const before = pendingReads()

    const panel = await openPanel(user)
    await say(user, panel, 'undo that approval')
    const confirmation = await within(panel).findByRole('region', { name: 'Undo the approval of AT-NEW-1?' })
    expect(within(confirmation).getByText(/releases the serial/)).toBeInTheDocument()
    expect(within(confirmation).getByText(/will not come into service/)).toBeInTheDocument()

    await user.click(within(confirmation).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('The approval is withdrawn.')
    await waitFor(() => expect(pendingReads()).toBeGreaterThan(before))
  })
})

describe('the safe write and the read', () => {
  it('withdraws a command without a card and still refreshes the fleet', async () => {
    const user = userEvent.setup()
    signIn('MANAGER')
    seed({
      assistantEnabled: true,
      sites: [makeSite({ id: 'site-1', name: 'Lagos' })],
      terminals: [
        makeTerminal({ serial_number: 'AT-1', device_name: 'Reception', site_public_id: 'site-1', site_name: 'Lagos' }),
      ],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'withdraw_command', arguments: { serial: 'AT-1', command_id: 'cmd-1' } },
          { type: 'tool.result', call_id: 'c1', tool: 'withdraw_command', status: 'EXECUTED', summary: 'withdrawn', domains: ['terminals', 'audit'] },
          { type: 'assistant.message', text: 'That command is cancelled.' },
        ],
      ],
    })
    renderShell('/terminals')
    await screen.findByText('Reception')
    const terminalReads = reads('/console/terminals')
    const before = terminalReads()

    const panel = await openPanel(user)
    await say(user, panel, 'cancel that diagnostic')
    await within(panel).findByText('That command is cancelled.')
    expect(within(panel).getByText('Cancelled a command')).toBeInTheDocument()
    // No card: a safe write runs on the operator's sentence.
    expect(within(panel).queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    await waitFor(() => expect(terminalReads()).toBeGreaterThan(before))
  })

  it('reads the audit trail without refreshing anything', async () => {
    const user = userEvent.setup()
    signIn('ADMIN')
    seed({
      assistantEnabled: true,
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'list_audit', arguments: { action: 'TERMINAL_APPROVED' } },
          { type: 'tool.result', call_id: 'c1', tool: 'list_audit', status: 'EXECUTED', summary: '3 rows' },
          { type: 'assistant.message', text: 'Three terminals were approved this week.' },
        ],
      ],
    })
    renderShell('/activity')
    await screen.findByRole('heading', { name: /Activity/i })
    const auditReads = reads('/console/audit')
    const before = auditReads()

    const panel = await openPanel(user)
    await say(user, panel, 'who approved terminals this week')
    await within(panel).findByText('Three terminals were approved this week.')
    expect(within(panel).getByText('Read who changed what')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(auditReads()).toBe(before)
  })
})
