import { renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'

import { ApiError } from '../api/client'
import { setCsrfToken } from '../api/csrf'
import { invitationOf, operatorOf } from '../api/types'
import {
  makeApplication,
  makeOperatorAccount,
  makePerson,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
  SITE_B,
} from '../test/fixtures'
import { makeTestQueryClient, queryWrapper } from '../test/render'
import { failNext, resetServerState, resetTerminalModes, seed, state } from '../test/server'
import {
  useApplications,
  useCreateOperator,
  useCreatePerson,
  useDeletePerson,
  usePeople,
  useSetOperatorSites,
  useSites,
  useTerminal,
  useTerminalSummary,
  useTerminals,
  useUpdateApplication,
  useUpdatePerson,
  useUpdateSiteSettings,
  useUpdateTerminalMode,
} from './console'
import { keys } from './keys'

/**
 * The data layer.
 *
 * Exercised against the mock API rather than against stubbed hooks, because the
 * behaviours worth protecting are the ones BETWEEN the client and the contract:
 * that a mutation invalidates what it invalidated, that the CSRF token is sent,
 * that a scoped operator's list is narrowed, and that nothing secret is ever in
 * a response the console holds.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name, terminal_count: 1 }),
]

const TERMINALS = [
  makeTerminal({ serial_number: 'AT-0001', site_public_id: SITE_A.site_id, site_name: SITE_A.site_name }),
  makeTerminal({
    id: 2,
    public_id: 'terminal-public-2',
    serial_number: 'AT-0002',
    site_public_id: SITE_B.site_id,
    site_name: SITE_B.site_name,
    status: 'OFFLINE',
    firmware_outdated: true,
  }),
]

function signIn(session = makeSession()) {
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({ sites: SITES, terminals: TERMINALS })
  return session
}

beforeEach(() => {
  setCsrfToken(null)
})

describe('reads', () => {
  it('fetches sites and terminals for an unscoped operator', async () => {
    signIn()
    const wrapper = queryWrapper()

    const sites = renderHook(() => useSites(), { wrapper })
    await waitFor(() => expect(sites.result.current.isSuccess).toBe(true))
    expect(sites.result.current.data?.sites).toHaveLength(2)

    const terminals = renderHook(() => useTerminals(), { wrapper })
    await waitFor(() => expect(terminals.result.current.isSuccess).toBe(true))
    expect(terminals.result.current.data?.terminals).toHaveLength(2)
  })

  it('NARROWS sites, terminals and the summary to a scoped operator’s grants', async () => {
    // The console must not show what the API would then refuse. The summary in
    // particular: company-wide counts above a narrowed list read as a bug and
    // disclose the size of a fleet the operator was not given.
    signIn(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A] }))
    const wrapper = queryWrapper()

    const sites = renderHook(() => useSites(), { wrapper })
    await waitFor(() => expect(sites.result.current.isSuccess).toBe(true))
    expect(sites.result.current.data?.sites.map((site) => site.id)).toEqual([SITE_A.site_id])

    const terminals = renderHook(() => useTerminals(), { wrapper })
    await waitFor(() => expect(terminals.result.current.isSuccess).toBe(true))
    expect(terminals.result.current.data?.terminals.map((t) => t.serial_number)).toEqual(['AT-0001'])

    const summary = renderHook(() => useTerminalSummary(), { wrapper })
    await waitFor(() => expect(summary.result.current.isSuccess).toBe(true))
    expect(summary.result.current.data?.total).toBe(1)
  })

  it('refuses a terminal at a site the operator is not granted', async () => {
    signIn(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A] }))
    const wrapper = queryWrapper()

    const { result } = renderHook(() => useTerminal('AT-0002'), { wrapper })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect((result.current.error as ApiError).status).toBe(403)
  })

  it('filters to outdated terminals with a separate cache entry', async () => {
    // Distinct keys, so the filtered list does not overwrite the full one.
    signIn()
    const wrapper = queryWrapper()

    const outdated = renderHook(() => useTerminals({ outdated: true }), { wrapper })
    await waitFor(() => expect(outdated.result.current.isSuccess).toBe(true))
    expect(outdated.result.current.data?.terminals.map((t) => t.serial_number)).toEqual(['AT-0002'])

    expect(keys.terminals.list({ outdated: true })).not.toEqual(keys.terminals.list())
  })

  it('surfaces a failure as an error rather than as empty data', async () => {
    signIn()
    failNext('people', 500)

    const { result } = renderHook(() => usePeople(), { wrapper: queryWrapper() })
    await waitFor(() => expect(result.current.isError).toBe(true))
    expect(result.current.data).toBeUndefined()
  })
})

describe('people: search and paging happen on the server', () => {
  const roster = Array.from({ length: 7 }, (_, index) =>
    makePerson({
      id: `person-${index}`,
      external_id: `P-000${index}`,
      full_name: index % 2 === 0 ? `Ada Number ${index}` : `Bola Number ${index}`,
    }),
  )

  it('asks the API for a page rather than slicing one locally', async () => {
    signIn()
    seed({ sites: SITES, terminals: TERMINALS, people: roster })

    const { result } = renderHook(() => usePeople({ limit: 3, offset: 3 }), {
      wrapper: queryWrapper(),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    expect(result.current.data?.count).toBe(3)
    expect(result.current.data?.total).toBe(7)
    expect(result.current.data?.has_more).toBe(true)
    expect(result.current.data?.people[0]?.external_id).toBe('P-0003')

    const asked = state.requests.filter((request) => request.url.includes('/console/people'))
    expect(asked.at(-1)?.url).toContain('offset=3')
  })

  it('sends the search term to the API, and searches the ROSTER not the page', async () => {
    signIn()
    seed({ sites: SITES, terminals: TERMINALS, people: roster })

    const { result } = renderHook(() => usePeople({ search: 'bola', limit: 2 }), {
      wrapper: queryWrapper(),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    // Three Bolas exist; the page holds two and the total reports all three.
    expect(result.current.data?.total).toBe(3)
    expect(result.current.data?.count).toBe(2)
    expect(state.requests.at(-1)?.url).toContain('q=bola')
  })

  it('keeps a query’s pages and searches as separate cache entries', () => {
    expect(keys.people.list({ offset: 0 })).not.toEqual(keys.people.list({ offset: 50 }))
    expect(keys.people.list({ search: 'ada' })).not.toEqual(keys.people.list({ search: 'bola' }))
    // ...but normalises the ways of saying "no search".
    expect(keys.people.list({})).toEqual(keys.people.list({ search: '' }))
  })
})

describe('writes', () => {
  it('sends the CSRF token on every unsafe request', async () => {
    // The API refuses an unsafe request without it, so this is the difference
    // between a console that works and one that 403s on every save.
    signIn()

    const { result } = renderHook(() => useCreatePerson(), { wrapper: queryWrapper() })
    await result.current.mutateAsync({ external_id: 'P-NEW', full_name: 'New Person' })

    const post = state.requests.find(
      (request) => request.method === 'POST' && request.url.includes('/console/people'),
    )
    expect(post?.headers.get('X-CSRF-Token')).toBe('csrf-token-value')
  })

  it('invalidates every page of people after a create', async () => {
    signIn()
    const client = makeTestQueryClient()
    const wrapper = queryWrapper(client)

    const list = renderHook(() => usePeople(), { wrapper })
    await waitFor(() => expect(list.result.current.isSuccess).toBe(true))
    expect(list.result.current.data?.total).toBe(0)

    const create = renderHook(() => useCreatePerson(), { wrapper })
    await create.result.current.mutateAsync({ external_id: 'P-NEW', full_name: 'New Person' })

    // The new person may belong on any page and every total is off by one, so
    // the whole subtree has to go.
    await waitFor(() => expect(list.result.current.data?.total).toBe(1))
  })

  it('drops a deleted person’s own cache entry rather than refetching a 404', async () => {
    signIn()
    seed({ sites: SITES, terminals: TERMINALS, people: [makePerson({ external_id: 'P-GONE' })] })

    const client = makeTestQueryClient()
    const wrapper = queryWrapper(client)
    client.setQueryData(keys.people.detail('P-GONE'), makePerson({ external_id: 'P-GONE' }))

    const remove = renderHook(() => useDeletePerson(), { wrapper })
    await remove.result.current.mutateAsync('P-GONE')

    expect(client.getQueryData(keys.people.detail('P-GONE'))).toBeUndefined()
  })

  it('reports a conflict as an error the caller can branch on by STATUS', async () => {
    // The API's error strings are explicitly not stable; the status is.
    signIn()
    seed({ sites: SITES, terminals: TERMINALS, people: [makePerson({ external_id: 'P-TAKEN' })] })

    const { result } = renderHook(() => useCreatePerson(), { wrapper: queryWrapper() })
    await expect(
      result.current.mutateAsync({ external_id: 'P-TAKEN', full_name: 'Duplicate' }),
    ).rejects.toMatchObject({ status: 409 })
  })

  it('updates a person without touching their credential', async () => {
    signIn()
    seed({
      sites: SITES,
      terminals: TERMINALS,
      people: [makePerson({ external_id: 'P-1', biometric_enrolled: true })],
    })

    const { result } = renderHook(() => useUpdatePerson('P-1'), { wrapper: queryWrapper() })
    const updated = await result.current.mutateAsync({ full_name: 'Corrected Name' })

    expect(updated.full_name).toBe('Corrected Name')
    // Correcting a spelling must never unenrol somebody.
    expect(updated.biometric_enrolled).toBe(true)
  })

  it('writes the terminal detail response straight into the cache', async () => {
    // The response IS the detail object; refetching what we were just handed
    // would be a wasted round trip.
    signIn(makeSession({ applications: [{ code: 'ATTENDANCE', settings: {} }] }))
    const client = makeTestQueryClient()

    const { result } = renderHook(() => useUpdateTerminalMode('AT-0001'), {
      wrapper: queryWrapper(client),
    })
    await result.current.mutateAsync({ application_mode: 'ATTENDANCE' })

    expect(client.getQueryData(keys.terminals.detail('AT-0001'))).toMatchObject({
      application_mode: 'ATTENDANCE',
      effective_applications: ['ATTENDANCE'],
    })
  })

  it('refuses to point a terminal at a capability the company has not enabled', async () => {
    signIn(makeSession({ applications: [] }))

    const { result } = renderHook(() => useUpdateTerminalMode('AT-0001'), {
      wrapper: queryWrapper(),
    })
    await expect(
      result.current.mutateAsync({ application_mode: 'ATTENDANCE' }),
    ).rejects.toMatchObject({ status: 409 })
  })

  it('replaces site settings wholesale and invalidates terminals', async () => {
    // The write queues a SETTINGS job for every terminal at the site, so the
    // fleet view is now potentially stale.
    signIn()
    const client = makeTestQueryClient()
    const wrapper = queryWrapper(client)

    const terminals = renderHook(() => useTerminals(), { wrapper })
    await waitFor(() => expect(terminals.result.current.isSuccess).toBe(true))

    const update = renderHook(() => useUpdateSiteSettings(SITE_A.site_id), { wrapper })
    const saved = await update.result.current.mutateAsync({ unlock_duration_seconds: 8 })

    expect(saved.settings).toEqual({ unlock_duration_seconds: 8 })
    expect(saved.settings_version).toBe(2)
    expect(client.getQueryState(keys.terminals.list())?.isInvalidated).toBe(true)
  })

  it('surfaces a failed settings write rather than reporting success', async () => {
    signIn()
    failNext('site-settings', 500)

    const { result } = renderHook(() => useUpdateSiteSettings(SITE_A.site_id), {
      wrapper: queryWrapper(),
    })
    await expect(result.current.mutateAsync({ unlock_duration_seconds: 8 })).rejects.toBeInstanceOf(
      ApiError,
    )
  })
})

describe('applications', () => {
  it('invalidates the SESSION when a capability is toggled', async () => {
    // Navigation is derived from session.applications. Without this the console
    // changes everywhere except its own menu.
    signIn(makeSession({ role: 'OWNER', applications: [] }))
    seed({ sites: SITES, terminals: TERMINALS, applications: [] })

    // A gcTime window: the session entry below has no observer, and the
    // terminals query is awaited before the assertions.
    const client = makeTestQueryClient({ gcTime: 60_000 })
    const wrapper = queryWrapper(client)
    client.setQueryData(['session'], makeSession())

    // A REAL terminals query, so the assertion below is about an entry that
    // exists rather than passing vacuously on a missing one.
    const terminals = renderHook(() => useTerminals(), { wrapper })
    await waitFor(() => expect(terminals.result.current.isSuccess).toBe(true))

    const update = renderHook(() => useUpdateApplication(), { wrapper })
    await update.result.current.mutateAsync({ code: 'ATTENDANCE', body: { enabled: true } })

    expect(client.getQueryState(['session'])?.isInvalidated).toBe(true)
    // effective_applications is computed against what the company has enabled,
    // so every terminal's resolution just changed.
    expect(client.getQueryState(keys.terminals.list())?.isInvalidated).toBe(true)
  })

  it('reports the catalog so a new capability appears without a frontend release', async () => {
    signIn()
    seed({
      sites: SITES,
      terminals: TERMINALS,
      applications: [makeApplication({ code: 'ATTENDANCE' })],
      available: ['ATTENDANCE', 'A_BRAND_NEW_CAPABILITY'],
    })

    const { result } = renderHook(() => useApplications(), { wrapper: queryWrapper() })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    expect(result.current.data?.available).toContain('A_BRAND_NEW_CAPABILITY')
    expect(result.current.data?.enabled).toEqual(['ATTENDANCE'])
  })

  it('refuses a non-OWNER, as the server does', async () => {
    signIn(makeSession({ role: 'ADMIN' }))

    const { result } = renderHook(() => useUpdateApplication(), { wrapper: queryWrapper() })
    await expect(
      result.current.mutateAsync({ code: 'ATTENDANCE', body: { enabled: true } }),
    ).rejects.toMatchObject({ status: 403 })
  })
})

describe('operators', () => {
  it('creates an operator and refreshes the list', async () => {
    signIn()
    const client = makeTestQueryClient()
    const wrapper = queryWrapper(client)

    const create = renderHook(() => useCreateOperator(), { wrapper })
    const response = await create.result.current.mutateAsync({
      email: 'new@example.com',
      full_name: 'New Operator',
      password: 'a-long-enough-password',
      role: 'VIEWER',
      site_ids: [SITE_A.site_id],
    })

    // A supplied password means a bare account and NO invitation — the union's
    // other arm. Narrowed through the helpers so the shape stays checked.
    const created = operatorOf(response)
    expect(invitationOf(response)).toBeNull()
    expect(created.role).toBe('VIEWER')
    expect(created.all_sites).toBe(false)
    expect(created.sites?.[0]?.site_id).toBe(SITE_A.site_id)
  })

  it('creates an operator BY INVITATION when no password is supplied', async () => {
    // The preferred path (PPL-02): the account exists with a credential nobody
    // holds, and the caller is handed a single-use link instead of choosing a
    // password it would then know indefinitely.
    signIn()
    const wrapper = queryWrapper(makeTestQueryClient())

    const create = renderHook(() => useCreateOperator(), { wrapper })
    const response = await create.result.current.mutateAsync({
      email: 'invited@example.com',
      full_name: 'Invited Operator',
      role: 'MANAGER',
    })

    const invitation = invitationOf(response)
    expect(invitation).not.toBeNull()
    expect(invitation?.purpose).toBe('INVITE')
    expect(invitation?.shown_once).toBe(true)
    expect(operatorOf(response).email).toBe('invited@example.com')
  })

  it('treats an EMPTY grant list as unscoped, not as no access', async () => {
    // The two readings are opposite and the destructive one looks like the safe
    // one, so this is pinned at the data layer as well as in the UI.
    signIn()
    const operator = makeOperatorAccount({ id: 'op-9', all_sites: false, sites: [SITE_A] })
    seed({ sites: SITES, terminals: TERMINALS, operators: [operator] })

    const { result } = renderHook(() => useSetOperatorSites('op-9'), { wrapper: queryWrapper() })
    const saved = await result.current.mutateAsync({ site_ids: [] })

    expect(saved.sites).toEqual([])
    expect(saved.all_sites).toBe(true)
  })
})

describe('nothing secret ever reaches the browser', () => {
  it('carries no site API key, device key or biometric template in any response', async () => {
    // The site key is the PROVISIONING SECRET — it registers terminals and
    // rotates their credentials. A biometric template is credential material.
    // Neither has any business in a browser, and this asserts it against every
    // console response the data layer holds rather than trusting the projection.
    signIn()
    seed({
      sites: SITES,
      terminals: TERMINALS,
      people: [makePerson({ biometric_enrolled: true })],
      operators: [makeOperatorAccount()],
      applications: [makeApplication()],
    })

    const wrapper = queryWrapper()
    const hooks = [
      renderHook(() => useSites(), { wrapper }),
      renderHook(() => useTerminals(), { wrapper }),
      renderHook(() => usePeople(), { wrapper }),
      renderHook(() => useApplications(), { wrapper }),
      renderHook(() => useTerminal('AT-0001'), { wrapper }),
    ]

    for (const hook of hooks) {
      await waitFor(() => expect(hook.result.current.isSuccess).toBe(true))
    }

    const serialised = JSON.stringify(hooks.map((hook) => hook.result.current.data))
    for (const forbidden of [
      'api_key',
      'apiKey',
      'device_key',
      'api_key_hash',
      'fingerprint_template',
      'password_hash',
      'token_hash',
    ]) {
      expect(serialised, `${forbidden} must never reach the browser`).not.toContain(forbidden)
    }

    // What the console DOES get about biometrics is a boolean, and only that.
    expect(serialised).toContain('biometric_enrolled')
  })

  it('never sends the site API key header', async () => {
    signIn()
    const { result } = renderHook(() => useCreatePerson(), { wrapper: queryWrapper() })
    await result.current.mutateAsync({ external_id: 'P-X', full_name: 'X' })

    for (const request of state.requests) {
      expect(request.headers.get('X-API-Key')).toBeNull()
    }
  })
})
