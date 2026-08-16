import { useEffect, useMemo, useState } from 'react'

import { ApiError } from '../../api/client'
import { can } from '../../auth/permissions'
import { CheckboxField, FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState } from '../../components/states'
import { useSiteSettings, useUpdateSiteSettings } from '../../data/console'
import { useSession } from '../../session/useSession'
import {
  REFUSED_SETTINGS,
  SETTING_DEFINITIONS,
  composeSettings,
  isRefusedSetting,
  partitionSettings,
  preservableSettings,
  supersededSetting,
  toFieldValue,
  validateGuided,
  type SettingIssue,
} from './settingsSchema'

/**
 * A site's device configuration.
 *
 * TWO EDITORS OVER ONE OBJECT, and the relationship between them is the whole
 * design. The guided form covers the settings this build can explain; the raw
 * editor covers the entire object including keys it cannot. Whichever is used,
 * NOTHING IS DISCARDED — the guided save merges its fields into everything it
 * did not touch, because `PUT` replaces the object wholesale and a merge the
 * client forgets is configuration the customer silently loses.
 *
 * SAVING RECONFIGURES HARDWARE. The write queues a SETTINGS job to every
 * terminal at the site, so this is not editing a record; it is changing what
 * physical units do. The panel says so before the button rather than after.
 */
export function SiteSettingsPanel({ siteId, siteName }: { siteId: string; siteName: string }) {
  const { session } = useSession()
  const mayEdit = can(session, 'manageSiteSettings')

  const query = useSiteSettings(siteId)
  const save = useUpdateSiteSettings(siteId)
  const notifications = useNotifications()

  const [mode, setMode] = useState<'guided' | 'advanced'>('guided')
  const [guided, setGuided] = useState<Record<string, string>>({})
  const [raw, setRaw] = useState('')
  const [rawError, setRawError] = useState<string | null>(null)
  const [issues, setIssues] = useState<SettingIssue[]>([])

  const settings = useMemo(() => query.data?.settings ?? {}, [query.data])
  const { known, superseded, unknown } = useMemo(() => partitionSettings(settings), [settings])
  const unknownKeys = Object.keys(unknown)
  const supersededKeys = Object.keys(superseded)

  // Everything the guided form does not touch AND may legally be written back.
  // `PUT` replaces wholesale, so anything omitted is deleted from the site and
  // from every terminal at it on the next sync — but the two reserved keys
  // CANNOT be sent at all, and a save that kept them faithfully would 400.
  const preserved = useMemo(
    () => preservableSettings(superseded, unknown),
    [superseded, unknown],
  )

  // Reserved keys actually present in this site's stored object. A guided save
  // drops them, so the panel has to say so first.
  const staleReserved = supersededKeys.filter(isRefusedSetting)

  // Adopt whatever the server last returned. Keyed on settings_version so a save
  // (which increments it) reloads the form from the authoritative copy rather
  // than leaving the operator editing what they typed over a stale base.
  const version = query.data?.settings_version
  useEffect(() => {
    if (!query.data) return
    const next: Record<string, string> = {}
    for (const definition of SETTING_DEFINITIONS) {
      next[definition.key] = toFieldValue(known[definition.key])
    }
    setGuided(next)
    setRaw(JSON.stringify(settings, null, 2))
    setRawError(null)
    setIssues([])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version, query.data])

  if (query.isPending) return <LoadingState label="Loading site settings…" />
  if (query.isError) {
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />
  }

  async function persist(payload: Record<string, unknown>) {
    try {
      await save.mutateAsync(payload)
      notifications.success(`Settings saved for ${siteName}. Terminals will pick them up on their next sync.`)
    } catch (error) {
      notifications.failure('Could not save the settings.', error)
    }
  }

  async function saveGuided() {
    const found = validateGuided(guided)
    setIssues(found)
    if (found.length > 0) return

    const typed: Record<string, unknown> = {}
    for (const definition of SETTING_DEFINITIONS) {
      const value = guided[definition.key] ?? ''
      if (value.trim() === '') continue
      typed[definition.key] =
        definition.kind === 'boolean' ? value === 'true' : Number(value)
    }

    // The merge that keeps unrecognised AND superseded keys alive.
    await persist(composeSettings(typed, preserved))
  }

  async function saveRaw() {
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch (error) {
      setRawError(error instanceof Error ? error.message : 'That is not valid JSON.')
      return
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      setRawError('Settings must be a JSON object.')
      return
    }

    // THE RESERVED KEYS ARE REFUSED BY THE SERVER, so this refuses them here —
    // not to duplicate the rule, but because the server's message arrives as a
    // failed save on a form the operator has to reconstruct, and this one
    // arrives while the text is still in front of them and names the control
    // that does what they were trying to do.
    const offending = REFUSED_SETTINGS.filter((key) => key in (parsed as Record<string, unknown>))
    if (offending.length > 0) {
      setRawError(
        `${offending.join(' and ')} cannot be set here — the platform refuses it. ` +
          'It is a validated field on the site, set under “Behaviour during an outage” ' +
          'above. Remove it from this object and set it there.',
      )
      return
    }

    setRawError(null)
    await persist(parsed as Record<string, unknown>)
  }

  const issueFor = (key: string) => issues.find((issue) => issue.key === key)?.message

  return (
    <section className="panel" aria-labelledby="site-settings-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="site-settings-heading">
          Terminal settings
        </h2>
        <p className="field__hint">
          Applied to every terminal at this site. Version {query.data?.settings_version}.
          {query.isFetching ? ' Refreshing…' : ''}
        </p>
        <p className="field__hint">
          What terminals do during an outage is <strong>not</strong> here. It is a
          validated field of its own, set under “Behaviour during an outage” above —
          a value written into this object under the same name is ignored.
        </p>
      </div>

      {!mayEdit ? (
        <InfoNote title="Read only">
          Your role can view these settings but not change them. A manager, administrator
          or owner in your company can.
        </InfoNote>
      ) : null}

      {/*
        THE KEYS THIS BUILD DELIBERATELY WILL NOT EDIT, named with the reason.
        Both of these used to be controls in the form below, and both did
        nothing — which is the failure this note exists to close rather than
        quietly tidy away.
      */}
      {supersededKeys.length > 0 ? (
        <InfoNote tone="warning" title="Settings this console no longer edits">
          <p>
            This site still carries {supersededKeys.length} setting
            {supersededKeys.length === 1 ? '' : 's'} that the console used to offer a
            control for. <strong>None of them has any effect.</strong>
          </p>
          <ul>
            {supersededKeys.map((key) => {
              const definition = supersededSetting(key)
              return (
                <li key={key}>
                  <code>{key}</code> — {definition?.reason}
                </li>
              )
            })}
          </ul>
          {staleReserved.length > 0 ? (
            <p>
              <strong>
                Saving from this panel will remove{' '}
                {staleReserved.map((key, index) => (
                  <span key={key}>
                    {index > 0 ? ' and ' : ''}
                    <code>{key}</code>
                  </span>
                ))}
                .
              </strong>{' '}
              The platform rejects a write that contains{' '}
              {staleReserved.length === 1 ? 'it' : 'them'}, so the stale copy cannot be
              kept — and it was never being read, so nothing your terminals do will
              change.
            </p>
          ) : null}
        </InfoNote>
      ) : null}

      {unknownKeys.length > 0 ? (
        <InfoNote tone="warning" title="Settings this console does not recognise">
          <p>
            This site carries {unknownKeys.length} setting
            {unknownKeys.length === 1 ? '' : 's'} newer than this version of the console:{' '}
            {unknownKeys.map((key, index) => (
              <span key={key}>
                {index > 0 ? ', ' : ''}
                <code>{key}</code>
              </span>
            ))}
            .
          </p>
          <p>
            <strong>They are preserved.</strong> Saving from the guided form keeps them
            exactly as they are; edit them under Advanced if you need to.
          </p>
        </InfoNote>
      ) : null}

      <div className="tabs" role="tablist" aria-label="Settings editor">
        <button
          type="button"
          role="tab"
          id="tab-guided"
          aria-selected={mode === 'guided'}
          aria-controls="panel-guided"
          className={`tab${mode === 'guided' ? ' tab--active' : ''}`}
          onClick={() => setMode('guided')}
        >
          Guided
        </button>
        <button
          type="button"
          role="tab"
          id="tab-advanced"
          aria-selected={mode === 'advanced'}
          aria-controls="panel-advanced"
          className={`tab${mode === 'advanced' ? ' tab--active' : ''}`}
          onClick={() => setMode('advanced')}
        >
          Advanced (JSON)
        </button>
      </div>

      {mode === 'guided' ? (
        <div className="form" role="tabpanel" id="panel-guided" aria-labelledby="tab-guided">
          {SETTING_DEFINITIONS.map((definition) =>
            definition.kind === 'boolean' ? (
              <CheckboxField
                key={definition.key}
                label={definition.label}
                hint={definition.description}
                checked={guided[definition.key] === 'true'}
                disabled={!mayEdit || save.isPending}
                onChange={(checked) =>
                  setGuided((current) => ({ ...current, [definition.key]: String(checked) }))
                }
              />
            ) : (
              <TextField
                key={definition.key}
                label={definition.label}
                type="number"
                value={guided[definition.key] ?? ''}
                error={issueFor(definition.key)}
                disabled={!mayEdit || save.isPending}
                hint={
                  <>
                    {definition.description}
                    {definition.min !== undefined && definition.max !== undefined ? (
                      <>
                        {' '}
                        Between {definition.min} and {definition.max}
                        {definition.unit ? ` ${definition.unit}` : ''}. Leave blank to use
                        the firmware default.
                      </>
                    ) : null}
                  </>
                }
                onChange={(value) =>
                  setGuided((current) => ({ ...current, [definition.key]: value }))
                }
              />
            ),
          )}

          {mayEdit ? (
            <>
              <p className="field__hint">
                Saving queues a settings update to every terminal at this site.
              </p>
              <FormError
                message={
                  save.error
                    ? save.error instanceof ApiError
                      ? save.error.message
                      : 'The settings could not be saved.'
                    : null
                }
                requestId={save.error instanceof ApiError ? save.error.requestId : null}
              />
              <FormActions>
                <button
                  type="button"
                  className="button button--primary"
                  onClick={() => void saveGuided()}
                  disabled={save.isPending}
                >
                  {save.isPending ? 'Saving…' : 'Save settings'}
                </button>
              </FormActions>
            </>
          ) : null}
        </div>
      ) : (
        <div className="form" role="tabpanel" id="panel-advanced" aria-labelledby="tab-advanced">
          <TextField
            label="Settings JSON"
            multiline
            mono
            rows={14}
            value={raw}
            error={rawError ?? undefined}
            disabled={!mayEdit || save.isPending}
            hint="The complete settings object, including anything this console does not recognise. Saving replaces the object exactly as written."
            onChange={(value) => {
              setRaw(value)
              setRawError(null)
            }}
          />

          {mayEdit ? (
            <>
              <p className="field__hint">
                This replaces the whole object — a key you remove here is removed from the
                site, and from every terminal at it on the next sync.
              </p>
              <FormActions>
                <button
                  type="button"
                  className="button button--primary"
                  onClick={() => void saveRaw()}
                  disabled={save.isPending}
                >
                  {save.isPending ? 'Saving…' : 'Save JSON'}
                </button>
                <button
                  type="button"
                  className="button button--quiet"
                  onClick={() => setRaw(JSON.stringify(settings, null, 2))}
                  disabled={save.isPending}
                >
                  Reset to saved
                </button>
              </FormActions>
            </>
          ) : null}
        </div>
      )}
    </section>
  )
}
