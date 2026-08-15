import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { MULTI_PURPOSE } from '../../api/types'
import { readinessOf } from '../../applications/readiness'
import { describeApplication, findApplicationBySlug } from '../../applications/registry'
import { can } from '../../auth/permissions'
import { Badge } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useApplications, useUpdateApplication } from '../../data/console'
import { useSession } from '../../session/useSession'
import { UNKNOWN_DESCRIPTION } from './ApplicationsPage'

/**
 * One capability.
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

  if (query.isPending) return <LoadingState label="Loading application…" />
  if (query.isError) {
    return (
      <div className="page">
        <PageHeader title="Application" breadcrumb={<Link to="/settings/applications">Applications</Link>} />
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  if (!code || code === MULTI_PURPOSE) {
    return (
      <div className="page">
        <PageHeader
          title="Not a capability"
          breadcrumb={<Link to="/settings/applications">Applications</Link>}
        />
        <InfoNote title="Nothing here">
          {slug === slugify(MULTI_PURPOSE)
            ? 'Multi-purpose is a terminal operating mode, not a capability a company enables. Assign it to a terminal under Terminals.'
            : 'This platform does not offer a capability by that name.'}
        </InfoNote>
      </div>
    )
  }

  const definition = describeApplication(code)
  const isEnabled = query.data.enabled.includes(code)
  const recognised = definition.description !== UNKNOWN_DESCRIPTION
  const readiness = readinessOf(code, query.data)

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
        breadcrumb={<Link to="/settings/applications">Applications</Link>}
        lead={definition.description}
        actions={
          mayConfigure ? (
            isEnabled ? (
              <button
                type="button"
                className="button"
                onClick={() => setConfirmingDisable(true)}
              >
                Disable
              </button>
            ) : (
              <button
                type="button"
                className="button button--primary"
                disabled={update.isPending}
                onClick={() => void setEnabled(true)}
              >
                Enable
              </button>
            )
          ) : null
        }
      />

      {!recognised ? (
        <InfoNote tone="warning" title="Newer than this console">
          The platform offers this capability but this version of the console has no
          description for it. You can still enable and configure it; the label above
          is derived from its code.
        </InfoNote>
      ) : null}

      {/*
        FOUR STATES, SHOWN SEPARATELY. Collapsing them into one "status" is how a
        configuration screen becomes a false claim: an owner who enables a
        capability, stores settings for it and assigns a terminal has done three
        real things and still has a platform that does nothing with any of them.
      */}
      <section className="cards" aria-label="Readiness">
        <article className="card">
          <h2 className="card__title">Available</h2>
          <p className="card__value">
            {readiness.available ? (
              <Badge tone="positive">Yes</Badge>
            ) : (
              <Badge tone="warning">No</Badge>
            )}
          </p>
          <p className="card__detail">offered by this platform</p>
        </article>

        <article className="card">
          <h2 className="card__title">Enabled</h2>
          <p className="card__value">
            {isEnabled ? <Badge tone="positive">Yes</Badge> : <Badge>No</Badge>}
          </p>
          <p className="card__detail">for {session?.company.name}</p>
        </article>

        <article className="card">
          <h2 className="card__title">Configured</h2>
          <p className="card__value">
            {readiness.configured ? <Badge tone="positive">Yes</Badge> : <Badge>No</Badge>}
          </p>
          <p className="card__detail">
            {record ? (
              <>
                changed <Timestamp value={record.updated_at} relative />
              </>
            ) : (
              'no settings stored'
            )}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Operational</h2>
          <p className="card__value">
            {readiness.operational ? (
              <Badge tone="positive">Yes</Badge>
            ) : readiness.implementation === 'PARTIAL' ? (
              <Badge tone="warning">Partly</Badge>
            ) : (
              <Badge tone="warning">No</Badge>
            )}
          </p>
          <p className="card__detail">does the platform actually do this</p>
        </article>
      </section>

      {!readiness.operational ? (
        <InfoNote
          tone="warning"
          title={
            readiness.implementation === 'PARTIAL'
              ? 'This capability is only partly built'
              : 'Nothing acts on this capability yet'
          }
        >
          <p>{readiness.gap}</p>
          <p>
            Enabling it records that your company intends to use it, and lets a
            terminal be assigned to it. Until the platform carries out the
            workflow, that is the whole of what it does.
          </p>
        </InfoNote>
      ) : null}

      <section className="panel" aria-labelledby="application-identity-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="application-identity-heading">
            How terminals refer to it
          </h2>
        </div>
        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Platform code</dt>
            <dd>
              <code className="mono">{code}</code>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Minimum role to open its screens</dt>
            <dd>{definition.minimumRole}</dd>
          </div>
        </dl>
      </section>

      {/*
        Dependencies and conflicts are not modelled anywhere in the platform --
        no capability declares that it needs another, or that it cannot run
        alongside one. Saying so is more useful than an empty "Dependencies:"
        heading that implies the answer is "none".
      */}
      <section className="panel" aria-labelledby="application-relationships-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="application-relationships-heading">
            Dependencies and conflicts
          </h2>
        </div>
        <p className="field__hint">
          The platform does not currently record relationships between
          capabilities. Each is enabled independently, and none is known to
          require or exclude another.
        </p>
      </section>

      {/* --- settings ------------------------------------------------------- */}
      <section className="panel" aria-labelledby="application-settings-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="application-settings-heading">
            Settings
          </h2>
          <p className="field__hint">
            An open JSON object. The platform defines no keys for any capability
            yet, so there are no guided controls to offer and nothing currently
            reads what is stored here.
          </p>
        </div>

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
      </section>

      {confirmingDisable ? (
        <ConfirmDialog
          open
          title={`Disable ${definition.label}?`}
          consequence={
            <>
              It stops being available company-wide, and any terminal assigned to it
              will <strong>resolve to nothing</strong> until it is re-enabled or the
              terminal is reassigned.
            </>
          }
          detail={
            <>
              Terminal assignments are <strong>kept, not rewritten</strong> — a
              terminal pointed at this capability stays pointed at it and starts
              working again if you switch it back on. Its stored settings are kept
              too.
            </>
          }
          confirmLabel="Disable capability"
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
