import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { MULTI_PURPOSE } from '../../api/types'
import {
  describeApplication,
  findApplicationBySlug,
  UNKNOWN_DESCRIPTION,
} from '../../applications/registry'
import { can } from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useApplications, useUpdateApplication } from '../../data/console'
import { useSession } from '../../session/useSession'
import { TurnOffFeatureDialog } from './TurnOffFeatureDialog'

/**
 * One feature.
 *
 * RESOLVED FROM THE SERVER'S CATALOG, NOT FROM A ROUTE TABLE. The URL carries a
 * slug, which is matched against what the API reports as available — so a
 * capability this build has never heard of still has a working page, and one
 * that this build knows but the platform does not offer is honestly reported as
 * unavailable rather than rendered as if it existed.
 *
 * SETTINGS ARE AN OPEN JSON OBJECT WITH NO SCHEMA, exactly like a site's. There
 * is nothing to build guided controls from — the platform defines no keys for
 * any capability yet — so this offers the raw object and says so, rather than
 * inventing fields that no module reads.
 */
export function ApplicationDetailPage() {
  const { slug = '' } = useParams<{ slug: string }>()
  const { session } = useSession()
  const query = useApplications()
  const update = useUpdateApplication()
  const notifications = useNotifications()

  const [raw, setRaw] = useState('')
  const [rawError, setRawError] = useState<string | null>(null)
  const [confirmingDisable, setConfirmingDisable] = useState(false)

  const mayConfigure = can(session, 'configureApplications')

  // Resolve the slug against the SERVER's catalog. A known slug is matched by
  // the registry; anything else is matched by slugifying the available codes,
  // which is what gives an unknown capability a working page.
  const available = query.data?.available ?? []
  const known = findApplicationBySlug(slug)
  const code =
    known && available.includes(known.code)
      ? known.code
      : available.find((candidate) => slugify(candidate) === slug)

  const record = query.data?.configured.find((entry) => entry.code === code)
  const settings = record?.settings

  useEffect(() => {
    setRaw(JSON.stringify(settings ?? {}, null, 2))
    setRawError(null)
  }, [settings, record?.updated_at])

  if (query.isPending) return <LoadingState label="Loading feature…" />
  if (query.isError) {
    return (
      <div className="page">
        <PageHeader title="Feature" breadcrumb={<Link to="/settings/applications">Features</Link>} />
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  if (!code || code === MULTI_PURPOSE) {
    return (
      <div className="page">
        <PageHeader
          title="Not a feature"
          breadcrumb={<Link to="/settings/applications">Features</Link>}
        />
        <InfoNote title="Nothing here">
          {slug === slugify(MULTI_PURPOSE)
            ? 'Multi-purpose is a terminal setting, not a feature a company turns on. Assign it to a terminal under Terminals.'
            : 'This platform does not offer a feature by that name.'}
        </InfoNote>
      </div>
    )
  }

  const definition = describeApplication(code)
  const isEnabled = query.data.enabled.includes(code)
  const recognised = definition.description !== UNKNOWN_DESCRIPTION
  // What is STORED, which is a different question from whether a row exists --
  // see the note where the old "Configured" card used to be.
  const settingsKeyCount = settings ? Object.keys(settings).length : 0
  const hasStoredSettings = settingsKeyCount > 0

  async function setEnabled(next: boolean) {
    try {
      await update.mutateAsync({ code: code as string, body: { enabled: next } })
      notifications.success(next ? `${definition.label} enabled` : `${definition.label} disabled`)
    } catch (error) {
      notifications.failure(`Could not update ${definition.label}.`, error)
    }
  }

  async function saveSettings() {
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
    setRawError(null)

    try {
      await update.mutateAsync({
        code: code as string,
        // `enabled` is sent explicitly: omitting it would default to true
        // server-side, so saving settings on a disabled capability would
        // silently switch it on.
        body: { enabled: isEnabled, settings: parsed as Record<string, unknown> },
      })
      notifications.success(`${definition.label} settings saved`)
    } catch (error) {
      notifications.failure('Could not save the settings.', error)
    }
  }

  return (
    <div className="page">
      <PageHeader
        title={definition.label}
        breadcrumb={<Link to="/settings/applications">Features</Link>}
        lead={definition.description}
        actions={
          mayConfigure ? (
            isEnabled ? (
              <button
                type="button"
                className="button"
                onClick={() => setConfirmingDisable(true)}
              >
                Turn off
              </button>
            ) : (
              <button
                type="button"
                className="button button--primary"
                disabled={update.isPending}
                onClick={() => void setEnabled(true)}
              >
                Turn on
              </button>
            )
          ) : null
        }
      />

      {!recognised ? (
        <InfoNote tone="warning" title="Newer than this console">
          The platform offers this feature but this version of the console has no
          description for it. You can still turn it on and configure it; the name
          above is derived from the platform's own.
        </InfoNote>
      ) : null}

      {/*
        THE STATUS CARDS AND THE RELATIONSHIPS PANEL ARE GONE, and each for its
        own reason rather than a general wish for less.

        "AVAILABLE: YES — offered by this platform" appeared on a page the reader
        could only have reached BECAUSE it is available. The only interesting
        value of that card was "No", which is already covered: a feature the
        platform does not offer cannot be resolved from the catalogue and lands
        on the "not a feature" page instead.

        "TURNED ON: YES" was the third statement of the same fact on one screen —
        the header button reads "Turn off", the list carries an On badge, and
        this card said it again.

        "CONFIGURED: YES" WAS ACTIVELY MISLEADING. `readinessOf` defines
        configured as "a settings row exists", not "has settings" — deliberately,
        and correctly for the model. But the card rendered "Configured — Yes,
        changed 8 months ago" directly above a settings object containing `{}`.
        Two statements on one screen that contradict each other in plain reading.
        What is true and useful — whether anything is stored, and when it last
        changed — now sits with the settings themselves, where it describes what
        the reader is looking at.

        "DEPENDENCIES AND CONFLICTS" was a heading, a border and a sentence
        explaining that the platform does not model the concept. Nothing is lost
        by not raising it.
      */}

      <section className="panel" aria-labelledby="application-identity-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="application-identity-heading">
            Who can use it
          </h2>
        </div>
        {/*
          `roleLabel`, the same function every other role badge in the console
          uses. This printed the stored enum — "VIEWER" — while the operators
          screen called the same value "Viewer".

          NO RAW PLATFORM CODE. This panel used to print `ACCESS_CONTROL` under
          the heading "Platform code": an internal identifier for the device
          protocol, which an operator does nothing with.
        */}
        <p className="field__hint">
          Anyone with the <strong>{roleLabel(definition.minimumRole)}</strong> role or
          above can work with {definition.label} once it is turned on.
        </p>
      </section>

      {/* --- settings ------------------------------------------------------- */}
      {/*
        SECONDARY, NOT REMOVED.

        No feature defines any settings keys, so for almost every customer this
        is a large empty text box asking for JSON — and it was the biggest thing
        on the page, under a sentence explaining our storage model to somebody
        who came to turn a feature on.

        THE CAPABILITY IS UNCHANGED. The API accepts a settings object, a
        customer may already have one stored, and an integrator may need to put
        one there. It is one press away and says what it holds, rather than
        leading.

        The disclosure OPENS BY ITSELF when something is stored: a customer who
        has settings should not have to discover them behind a summary that gives
        no sign anything is there.
      */}
      <section className="panel" aria-labelledby="application-settings-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="application-settings-heading">
            Advanced settings
          </h2>
          <p className="field__hint">
            {/*
              THE HONEST REPLACEMENT FOR THE "Configured: Yes" CARD: what is
              actually stored, rather than whether a row exists.
            */}
            {hasStoredSettings ? (
              <>
                {settingsKeyCount} setting{settingsKeyCount === 1 ? '' : 's'} stored for
                this feature
                {record ? (
                  <>
                    , last changed <Timestamp value={record.updated_at} relative />
                  </>
                ) : null}
                .
              </>
            ) : (
              <>Nothing is stored for this feature, which is the usual case.</>
            )}
          </p>
        </div>

        <details className="technical" open={hasStoredSettings}>
          <summary>Stored configuration</summary>

        {mayConfigure ? (
          <div className="form">
            <TextField
              label="Settings JSON"
              multiline
              mono
              rows={10}
              value={raw}
              error={rawError ?? undefined}
              disabled={update.isPending}
              onChange={(value) => {
                setRaw(value)
                setRawError(null)
              }}
            />
            <FormError
              message={
                update.error
                  ? update.error instanceof ApiError
                    ? update.error.message
                    : 'The settings could not be saved.'
                  : null
              }
              requestId={update.error instanceof ApiError ? update.error.requestId : null}
            />
            <FormActions>
              <button
                type="button"
                className="button button--primary"
                onClick={() => void saveSettings()}
                disabled={update.isPending}
              >
                {update.isPending ? 'Saving…' : 'Save settings'}
              </button>
              <button
                type="button"
                className="button button--quiet"
                onClick={() => setRaw(JSON.stringify(settings ?? {}, null, 2))}
                disabled={update.isPending}
              >
                Reset to saved
              </button>
            </FormActions>
          </div>
        ) : (
          <pre className="code-block">{JSON.stringify(settings ?? {}, null, 2)}</pre>
        )}
        </details>
      </section>

      {confirmingDisable ? (
        <TurnOffFeatureDialog
          open
          label={definition.label}
          onConfirm={() => setEnabled(false)}
          onClose={() => setConfirmingDisable(false)}
        />
      ) : null}
    </div>
  )
}

function slugify(code: string): string {
  return code.toLowerCase().replace(/_/g, '-')
}
