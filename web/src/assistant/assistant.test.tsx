import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import { parseSSEFrame } from '../api/endpoints'
import type { AssistantEvent, Role, Session } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { AppShell } from '../layout/AppShell'
import { PeopleListPage } from '../pages/people/PeopleListPage'
import { PersonDetailPage } from '../pages/people/PersonDetailPage'
import { expectNoViolations } from '../test/axe'
import { makePerson, makeSession } from '../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { failNext, resetServerState, seed, state } from '../test/server'

/**
 * The assistant panel, against the mock's scripted turns.
 *
 * What is under test is the console's half: the launcher is absent until
 * the deployment enables the assistant; a turn's events become readable
 * entries; a confirmation card shows the SERVER'S wording and sends the
 * token back only when the operator chooses; a hand-off card opens the
 * screen it names; and a write the assistant made refreshes the screens
 * behind it. The server's half -- that a tool runs as the operator, roles,
 * tenancy, the token itself -- is tested against the real API in Go.
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

function renderShell(path = '/people', client = makeTestQueryClient()) {
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

beforeEach(() => setCsrfToken(null))

describe('the launcher', () => {
  it('is absent until the deployment enables the assistant', async () => {
    signIn()
    seed({ assistantEnabled: false, people: [makePerson()] })
    renderShell()
    await screen.findByRole('heading', { name: 'People' })
    expect(screen.queryByRole('button', { name: 'Assistant' })).not.toBeInTheDocument()
  })

  it('opens and closes the panel without leaving the page', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true, people: [makePerson()] })
    renderShell()
    const panel = await openPanel(user)
    expect(within(panel).getByRole('textbox', { name: 'Message the assistant' })).toHaveFocus()
    // The page is still there behind it: not a modal.
    expect(screen.getByRole('heading', { name: 'People' })).toBeInTheDocument()
    await user.click(within(panel).getByRole('button', { name: 'Close the assistant' }))
    expect(screen.queryByRole('dialog', { name: 'Assistant' })).not.toBeInTheDocument()
  })
})

describe('a turn', () => {
  it('shows the reply, the tools used, and refreshes what a write changed', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'search_people', arguments: { query: 'Ada' } },
          { type: 'tool.result', call_id: 'c1', tool: 'search_people', status: 'EXECUTED', summary: 'ok' },
          { type: 'assistant.delta', text: 'I found ' },
          { type: 'assistant.delta', text: 'Ada Okonkwo.' },
          { type: 'assistant.message', text: 'I found Ada Okonkwo.' },
        ],
      ],
    })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'find Ada{Enter}')

    expect(await within(panel).findByText('I found Ada Okonkwo.')).toBeInTheDocument()
    expect(within(panel).getByText('Searched people')).toBeInTheDocument()
    expect(within(panel).getByText('find Ada')).toBeInTheDocument()
    // The message went with the operator's CSRF token, like every console write.
    const post = state.requests.find((r) => r.method === 'POST' && r.url.includes('/assistant/conversations/conv-1/messages'))
    expect(post?.headers.get('X-CSRF-Token')).toBe('csrf-token-value')
  })

  it('refreshes the people list after the assistant adds somebody', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })],
      assistantTurns: [
        [
          { type: 'tool.call', call_id: 'c1', tool: 'create_person', arguments: {} },
          { type: 'tool.result', call_id: 'c1', tool: 'create_person', status: 'EXECUTED', summary: 'ok' },
          { type: 'handoff', call_id: 'c1', kind: 'person', route: '/people/P-NEW', label: 'Open New Person' },
          { type: 'assistant.message', text: 'Added New Person.' },
        ],
      ],
    })
    renderShell()
    await screen.findByText('Ada Okonkwo')
    const listReads = () => state.requests.filter((r) => r.method === 'GET' && r.url.includes('/console/people?')).length
    const before = listReads()

    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'add New Person{Enter}')
    await within(panel).findByText('Added New Person.')

    // The list behind the panel asked again, without anybody reloading.
    await waitFor(() => expect(listReads()).toBeGreaterThan(before))
    expect(within(panel).getByRole('link', { name: 'Open New Person' })).toHaveAttribute('href', '/people/P-NEW')
  })

  it('reports a failure the server sent rather than hanging', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson()],
      assistantTurns: [[{ type: 'turn.failed', code: 'budget_exhausted', message: 'Allowance used.', retryable: false }]],
    })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'hi{Enter}')
    expect(await within(panel).findByText('Assistant allowance used up')).toBeInTheDocument()
    expect(within(panel).getByText('Allowance used.')).toBeInTheDocument()
    // And the composer is usable again.
    expect(within(panel).getByRole('textbox', { name: 'Message the assistant' })).toBeEnabled()
  })
})

describe('a confirmation', () => {
  const grantTurn: AssistantEvent[] = [
    { type: 'tool.call', call_id: 'c1', tool: 'grant_access', arguments: { external_id: 'P-0001' } },
    { type: 'tool.result', call_id: 'c1', tool: 'grant_access', status: 'CONFIRMATION_REQUESTED', summary: 'waiting for your approval' },
    {
      type: 'confirmation.required',
      call_id: 'c1',
      confirmation_id: 'conf-1',
      token: 'v1.conf-1.signature',
      tool: 'grant_access',
      arguments: { external_id: 'P-0001', effect: 'DENY', scope_type: 'COMPANY' },
      consequence: {
        title: 'Keep Ada Okonkwo out of everywhere?',
        body: 'Adds a rule that keeps Ada Okonkwo out of everywhere, at any time. Keep out always wins, even if another rule lets them in.',
        warnings: ['This covers terminals that do not exist yet: A rule for everywhere applies to every terminal you have and every one installed later.'],
      },
      phrase_required: '',
      expires_at: '2026-09-15T12:00:00Z',
    },
    { type: 'assistant.message', text: 'Waiting for your approval.' },
    { type: 'turn.completed', turn_id: 't1', stop_reason: 'confirmation' },
  ]

  it('shows the server wording and sends the token back only on approval', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true, people: [makePerson()], assistantTurns: [[...grantTurn]] })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')

    const card = await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })
    expect(within(card).getByText(/Keep out always wins/)).toBeInTheDocument()
    expect(within(card).getByText(/terminals that do not exist yet/)).toBeInTheDocument()
    // Nothing was sent until a button was pressed.
    expect(state.requests.some((r) => r.url.includes('/confirmations'))).toBe(false)

    await user.click(within(card).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('Done.')
    const settle = state.requests.find((r) => r.method === 'POST' && r.url.includes('/confirmations'))
    expect(settle).toBeDefined()
    expect(within(card).getByText('Approved')).toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })

  it('marks the card approved only when the server says it ran, not when the click was sent', async () => {
    const user = userEvent.setup()
    signIn()
    // The settlement stream carries a result and a reply but NO
    // confirmation.settled: the server never said what became of it.
    seed({
      assistantEnabled: true,
      people: [makePerson()],
      assistantTurns: [
        [...grantTurn],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'grant_access', status: 'FAILED', summary: 'That is temporarily unavailable.' },
          { type: 'assistant.message', text: 'It was attempted but did not succeed.' },
        ],
      ],
    })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')
    const card = await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })
    await user.click(within(card).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('It was attempted but did not succeed.')
    await within(card).findByText('Not done')
    expect(within(card).queryByText('Approved')).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })

  it('shows a failed run as not done, in the server words', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      people: [makePerson()],
      assistantTurns: [
        [...grantTurn],
        [
          { type: 'tool.result', call_id: 'conf-1', tool: 'grant_access', status: 'REFUSED_SCOPE', summary: 'Not permitted: site' },
          { type: 'confirmation.settled', confirmation_id: 'conf-1', tool: 'grant_access', outcome: 'failed', message: 'Not permitted: that site is outside your access.' },
          { type: 'assistant.message', text: 'I could not add the rule.' },
        ],
      ],
    })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')
    const card = await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })
    await user.click(within(card).getByRole('button', { name: 'Approve' }))
    await within(panel).findByText('I could not add the rule.')
    expect(within(card).getByText('Not done')).toBeInTheDocument()
    expect(within(card).getByText('Not permitted: that site is outside your access.')).toBeInTheDocument()
    expect(within(card).queryByText('Approved')).not.toBeInTheDocument()
  })

  it('never shows an expired or spent confirmation as approved', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true, people: [makePerson()], assistantTurns: [[...grantTurn]] })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')
    const card = await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })

    // The server refuses the token before any stream opens: 410 Gone.
    failNext('assistant-confirm', 410)
    const peopleReadsBefore = state.requests.filter((r) => r.method === 'GET' && r.url.includes('/console/people')).length
    await user.click(within(card).getByRole('button', { name: 'Approve' }))
    await within(card).findByText('Not done')
    expect(within(card).getByText('That confirmation has expired.')).toBeInTheDocument()
    expect(within(card).queryByText('Approved')).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    // The refusal is also reported as a failure the operator can read.
    expect(within(panel).getByText('The assistant hit a problem')).toBeInTheDocument()
    // Nothing behind the panel was refreshed as if a write had happened.
    expect(state.requests.filter((r) => r.method === 'GET' && r.url.includes('/console/people')).length).toBe(peopleReadsBefore)
  })

  it('locks the card while the answer is in flight', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true, people: [makePerson()], assistantTurns: [[...grantTurn]] })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')
    const card = await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })
    await user.click(within(card).getByRole('button', { name: 'Approve' }))
    // Between the click and the server's word the card says so, and offers
    // no second click.
    expect(within(card).queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    await within(card).findByText('Approved')
  })

  it('rejects without running anything, and says so', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true, people: [makePerson()], assistantTurns: [[...grantTurn]] })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')
    const card = await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })
    await user.click(within(card).getByRole('button', { name: 'Reject' }))
    await within(panel).findByText('Understood, nothing was changed.')
    expect(within(card).getByText('Rejected')).toBeInTheDocument()
  })

  it('has no accessibility violations with a card open', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true, people: [makePerson()], assistantTurns: [[...grantTurn]] })
    renderShell()
    const panel = await openPanel(user)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'keep Ada out{Enter}')
    await within(panel).findByRole('region', { name: 'Keep Ada Okonkwo out of everywhere?' })
    await expectNoViolations()
  })
})

describe('a full conversation', () => {
  it('closes the composer when the server closes the conversation, until a new one starts', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      assistantEnabled: true,
      assistantTurns: [
        [
          { type: 'assistant.message', text: 'That was the last one.' },
          { type: 'turn.completed', turn_id: 't1', stop_reason: 'end_turn', conversation_closed: true },
        ],
      ],
    })
    renderShell()
    const panel = await openPanel(user)
    const input = within(panel).getByRole('textbox', { name: 'Message the assistant' })
    await user.type(input, 'hi{Enter}')
    await within(panel).findByText('That was the last one.')
    expect(await within(panel).findByText('Start a new conversation')).toBeInTheDocument()
    expect(within(panel).getByText(/reached its limit/)).toBeInTheDocument()
    expect(within(panel).getByRole('textbox', { name: 'Message the assistant' })).toBeDisabled()
    expect(within(panel).getByRole('button', { name: 'Send' })).toBeDisabled()

    await user.click(within(panel).getByRole('button', { name: 'New conversation' }))
    expect(within(panel).getByRole('textbox', { name: 'Message the assistant' })).toBeEnabled()
    expect(within(panel).queryByText(/reached its limit/)).not.toBeInTheDocument()
  })

  it('reports a closed conversation the server refuses as not retryable', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ assistantEnabled: true })
    renderShell()
    const panel = await openPanel(user)
    failNext('assistant-message', 409)
    await user.type(within(panel).getByRole('textbox', { name: 'Message the assistant' }), 'hi{Enter}')
    expect(await within(panel).findByText('Start a new conversation')).toBeInTheDocument()
    expect(within(panel).getByRole('textbox', { name: 'Message the assistant' })).toBeDisabled()
  })
})

describe('the enrolment hand-off', () => {
  it('opens the existing enrolment dialog from ?enrol=1, once, for an operator who may enrol', async () => {
    signIn('MANAGER')
    seed({ people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })] })
    renderShell('/people/P-0001?enrol=1')
    // The dialog the person page already had, opened on arrival.
    const dialog = await screen.findByRole('dialog', { name: /Enrol a fingerprint for Ada Okonkwo/ })
    expect(dialog).toBeInTheDocument()
  })

  it('opens nothing for a VIEWER, who could not press the button either', async () => {
    signIn('VIEWER')
    seed({ people: [makePerson({ external_id: 'P-0001', full_name: 'Ada Okonkwo' })] })
    renderShell('/people/P-0001?enrol=1')
    await screen.findByRole('heading', { name: 'Ada Okonkwo', level: 1 })
    expect(screen.queryByRole('dialog', { name: /Enrol a fingerprint/ })).not.toBeInTheDocument()
  })
})

describe('the event stream parser', () => {
  it('reads one frame into a typed event and ignores comments', () => {
    expect(parseSSEFrame('event: assistant.delta\nid: 3\ndata: {"text":"hi"}')).toEqual({
      type: 'assistant.delta',
      text: 'hi',
    })
    expect(parseSSEFrame(': keepalive')).toBeNull()
    expect(parseSSEFrame('event: tool.call\ndata: not json')).toBeNull()
  })
})
