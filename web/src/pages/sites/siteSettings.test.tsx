import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role } from '../../api/types'
import { makeSession, SITE_A } from '../../test/fixtures'
import { renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { SiteSettingsPanel } from './SiteSettingsPanel'
import { composeSettings, partitionSettings, validateGuided } from './settingsSchema'

/**
 * Site settings.
 *
 * The assertion that matters most is that NOTHING IS SILENTLY DISCARDED. `PUT`
 * replaces the settings object wholesale, so a guided form that wrote back only
 * the keys it recognised would delete everything else — invisibly, and from
 * every terminal at the site on the next sync.
 */

function signIn(role: Role = 'ADMIN', settings: Record<string, unknown> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ settings: { [SITE_A.site_id]: { settings, settings_version: 3 } } })
  return session
}

function renderPanel() {
  return renderWithSession(<SiteSettingsPanel siteId={SITE_A.site_id} siteName="Lagos Depot" />)
}

/** The body of the last settings PUT the mock received. */
function lastSavedSettings(): Record<string, unknown> {
  return state.settings[SITE_A.site_id]?.settings ?? {}
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The pure helpers
// ---------------------------------------------------------------------------

describe('settings composition', () => {
  it('separates what this build understands from what it does not', () => {
    const { known, unknown } = partitionSettings({
      unlock_duration_seconds: 5,
      a_future_setting: 'whatever',
      tamper_alarm: true,
    })

    expect(known).toEqual({ unlock_duration_seconds: 5, tamper_alarm: true })
    expect(unknown).toEqual({ a_future_setting: 'whatever' })
  })

  it('carries unrecognised keys through a guided save untouched', () => {
    const composed = composeSettings(
      { unlock_duration_seconds: 8 },
      { a_future_setting: 'whatever', another: { nested: true } },
    )

    expect(composed).toEqual({
      unlock_duration_seconds: 8,
      a_future_setting: 'whatever',
      another: { nested: true },
    })
  })

  it('omits a cleared field rather than writing null', () => {
    // Absent means "the firmware default applies", which is a different
    // instruction from "this is null" and is what clearing a field intends.
    expect(composeSettings({ unlock_duration_seconds: '' }, {})).toEqual({})
  })

  it('bounds the values it does understand', () => {
    expect(validateGuided({ unlock_duration_seconds: '900' })).toHaveLength(1)
    expect(validateGuided({ unlock_duration_seconds: '0' })).toHaveLength(1)
    expect(validateGuided({ unlock_duration_seconds: '5.5' })).toHaveLength(1)
    expect(validateGuided({ unlock_duration_seconds: '8' })).toHaveLength(0)
    // Blank is legitimate: it means the default.
    expect(validateGuided({ unlock_duration_seconds: '' })).toHaveLength(0)
  })
})

// ---------------------------------------------------------------------------
// The panel
// ---------------------------------------------------------------------------

describe('guided settings', () => {
  it('loads the current values into proper controls', async () => {
    signIn('ADMIN', { unlock_duration_seconds: 7, tamper_alarm: true })
    renderPanel()

    await waitFor(() =>
      expect(screen.getByLabelText(/Relay hold time/)).toHaveValue(7),
    )
    expect(screen.getByLabelText('Tamper alarm')).toBeChecked()
  })

  it('says which version it is editing', async () => {
    signIn('ADMIN', { unlock_duration_seconds: 7 })
    renderPanel()
    expect(await screen.findByText(/Version 3/)).toBeInTheDocument()
  })

  it('PRESERVES SETTINGS THIS BUILD DOES NOT RECOGNISE across a guided save', async () => {
    // The single most important behaviour in this panel.
    const user = userEvent.setup()
    signIn('ADMIN', {
      unlock_duration_seconds: 5,
      a_future_setting: 'keep me',
      nested_future: { deep: [1, 2, 3] },
    })
    renderPanel()

    const relay = await screen.findByLabelText(/Relay hold time/)
    await user.clear(relay)
    await user.type(relay, '9')
    await user.click(screen.getByRole('button', { name: 'Save settings' }))

    await waitFor(() => expect(lastSavedSettings()).toMatchObject({ unlock_duration_seconds: 9 }))
    expect(lastSavedSettings()).toMatchObject({
      a_future_setting: 'keep me',
      nested_future: { deep: [1, 2, 3] },
    })
  })

  it('tells the operator those settings exist, rather than hiding them', async () => {
    signIn('ADMIN', { a_future_setting: 'x', another_future: 'y' })
    renderPanel()

    expect(
      await screen.findByText('Settings this console does not recognise'),
    ).toBeInTheDocument()
    expect(screen.getByText('a_future_setting')).toBeInTheDocument()
    expect(screen.getByText('another_future')).toBeInTheDocument()
    expect(screen.getByText(/They are preserved/)).toBeInTheDocument()
  })

  it('refuses an out-of-range value before asking the server', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', { unlock_duration_seconds: 5 })
    renderPanel()

    const relay = await screen.findByLabelText(/Relay hold time/)
    await user.clear(relay)
    await user.type(relay, '9000')
    await user.click(screen.getByRole('button', { name: 'Save settings' }))

    expect(await screen.findByText(/must be at most 60/)).toBeInTheDocument()
    expect(state.requests.some((request) => request.method === 'PUT')).toBe(false)
  })

  it('warns that saving reaches the hardware', async () => {
    signIn('ADMIN', {})
    renderPanel()
    expect(
      await screen.findByText(/queues a settings update to every terminal at this site/),
    ).toBeInTheDocument()
  })

  it('reports a failed save instead of claiming success', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', { unlock_duration_seconds: 5 })
    failNext('site-settings', 500)
    renderPanel()

    const relay = await screen.findByLabelText(/Relay hold time/)
    await user.clear(relay)
    await user.type(relay, '9')
    await user.click(screen.getByRole('button', { name: 'Save settings' }))

    expect(await screen.findByText(/Could not save the settings/)).toBeInTheDocument()
  })
})

describe('advanced JSON', () => {
  it('shows the complete object, including unrecognised keys', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', { unlock_duration_seconds: 5, a_future_setting: 'x' })
    renderPanel()

    await user.click(await screen.findByRole('tab', { name: 'Advanced (JSON)' }))
    const editor = screen.getByLabelText('Settings JSON') as HTMLTextAreaElement
    const parsed = JSON.parse(editor.value)

    expect(parsed).toEqual({ unlock_duration_seconds: 5, a_future_setting: 'x' })
  })

  it('saves exactly what was written', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', { unlock_duration_seconds: 5 })
    renderPanel()

    await user.click(await screen.findByRole('tab', { name: 'Advanced (JSON)' }))
    const editor = screen.getByLabelText('Settings JSON')
    await user.clear(editor)
    await user.type(editor, '{{"unlock_duration_seconds":12,"brand_new":"value"}')
    await user.click(screen.getByRole('button', { name: 'Save JSON' }))

    await waitFor(() =>
      expect(lastSavedSettings()).toEqual({ unlock_duration_seconds: 12, brand_new: 'value' }),
    )
  })

  it('refuses malformed JSON without sending it', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', {})
    renderPanel()

    await user.click(await screen.findByRole('tab', { name: 'Advanced (JSON)' }))
    const editor = screen.getByLabelText('Settings JSON')
    await user.clear(editor)
    await user.type(editor, 'not json at all')
    await user.click(screen.getByRole('button', { name: 'Save JSON' }))

    await waitFor(() =>
      expect(screen.getByLabelText('Settings JSON')).toHaveAttribute('aria-invalid', 'true'),
    )
    expect(state.requests.some((request) => request.method === 'PUT')).toBe(false)
  })

  it('refuses a JSON array, which the API would reject anyway', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', {})
    renderPanel()

    await user.click(await screen.findByRole('tab', { name: 'Advanced (JSON)' }))
    const editor = screen.getByLabelText('Settings JSON')
    await user.clear(editor)
    // paste rather than type: user-event reads "[" as the start of a key
    // descriptor, and escaping it would obscure what is being entered.
    await user.click(editor)
    await user.paste('[1, 2, 3]')
    await user.click(screen.getByRole('button', { name: 'Save JSON' }))

    expect(await screen.findByText('Settings must be a JSON object.')).toBeInTheDocument()
  })

  it('warns that the raw save is a full replacement', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', {})
    renderPanel()

    await user.click(await screen.findByRole('tab', { name: 'Advanced (JSON)' }))
    expect(screen.getByText(/replaces the whole object/)).toBeInTheDocument()
  })
})

describe('role restrictions', () => {
  it('lets a VIEWER read but not write', async () => {
    signIn('VIEWER', { unlock_duration_seconds: 5 })
    renderPanel()

    expect(await screen.findByText('Read only')).toBeInTheDocument()
    expect(screen.getByLabelText(/Relay hold time/)).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Save settings' })).not.toBeInTheDocument()
  })

  it('lets a MANAGER write — settings are day-to-day work', async () => {
    signIn('MANAGER', { unlock_duration_seconds: 5 })
    renderPanel()

    await waitFor(() => expect(screen.getByLabelText(/Relay hold time/)).toBeEnabled())
    expect(screen.getByRole('button', { name: 'Save settings' })).toBeInTheDocument()
    expect(screen.queryByText('Read only')).not.toBeInTheDocument()
  })
})
