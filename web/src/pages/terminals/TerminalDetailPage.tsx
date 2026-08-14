import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { MULTI_PURPOSE } from '../../api/types'
import { describeApplication } from '../../applications/registry'
import { can } from '../../auth/permissions'
import { Badge, TerminalStatusBadge, humaniseCode } from '../../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useTerminal } from '../../data/console'
import { useSession } from '../../session/useSession'
import { ApplicationModeDialog } from './ApplicationModeDialog'
import { readHealth } from './health'

/**
 * One terminal.
 *
 * Three questions, in the order an operator asks them: is it working, what is
 * it for, and what is it running.
 *
 * WHAT IS DELIBERATELY ABSENT. There is no remove, no move-to-another-site, no
 * force-resync and no enable/disable button, because the console API has no such
 * route — those exist only behind the site provisioning key, which a browser
 * must never hold. Inventing a client-side approximation of any of them would be
 * a control that 404s, so the gaps are recorded as product requirements instead
 * and named on the page so an operator is not left hunting for a button that was
 * never built.
 */
export function TerminalDetailPage() {
  const { serial } = useParams<{ serial: string }>()
  const { session } = useSession()
  const query = useTerminal(serial)
  const [configuring, setConfiguring] = useState(false)

  const mayConfigure = can(session, 'configureTerminals')

  if (query.isPending) return <LoadingState label="Loading terminal…" />

  if (query.isError) {
    const error = query.error
    // 404 and 403 mean different things and the difference is worth showing:
    // one is "no such terminal in your company", the other is "your account is
    // not scoped to the site it stands at". The API never conflates them, and
    // neither does this.
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader title="Terminal not found" breadcrumb={<Link to="/terminals">Terminals</Link>} />
          <InfoNote title="Nothing here">
            No terminal with that serial is registered to your company. It may have
            been registered elsewhere, or its site may have been retired.
          </InfoNote>
        </div>
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="page">
          <PageHeader title="Terminal" breadcrumb={<Link to="/terminals">Terminals</Link>} />
          <InfoNote title="Not one of your sites">
            This terminal belongs to your company, but it stands at a site your
            account is not scoped to. An administrator can grant you access to that
            site.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Terminal" breadcrumb={<Link to="/terminals">Terminals</Link>} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const terminal = query.data
  const health = readHealth(terminal)
  const resolves = terminal.effective_applications.length > 0

  return (
    <div className="page">
      <PageHeader
        title={terminal.device_name || terminal.serial_number}
        breadcrumb={<Link to="/terminals">Terminals</Link>}
        lead={
          <>
            <code className="mono">{terminal.serial_number}</code> at{' '}
            {/* Linked by PUBLIC id — the only identifier a browser can join on. */}
            <Link to={`/sites/${terminal.site_public_id}`}>{terminal.site_name}</Link>
          </>
        }
        actions={
          mayConfigure ? (
            <button type="button" className="button" onClick={() => setConfiguring(true)}>
              Change application mode
            </button>
          ) : null
        }
      />

      {health.note ? (
        <InfoNote tone={health.tone === 'danger' ? 'warning' : 'muted'} title="Health">
          {health.note}
        </InfoNote>
      ) : null}

      <section className="cards" aria-label="Status">
        <article className="card">
          <h2 className="card__title">Status</h2>
          <p className="card__value">
            <TerminalStatusBadge status={terminal.status} />
          </p>
          <p className="card__detail">as last reported to the platform</p>
        </article>

        <article className="card">
          <h2 className="card__title">Last heartbeat</h2>
          <p className="card__value">
            {terminal.last_heartbeat_at ? (
              <Timestamp value={terminal.last_heartbeat_at} relative />
            ) : (
              <span className="muted">Never</span>
            )}
          </p>
          <p className="card__detail">
            {terminal.last_seen_at ? (
              <>
                last seen <Timestamp value={terminal.last_seen_at} relative />
              </>
            ) : (
              'no contact recorded'
            )}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Last sync</h2>
          <p className="card__value">
            {terminal.last_sync_at ? (
              <Timestamp value={terminal.last_sync_at} relative />
            ) : (
              <span className="muted">Never</span>
            )}
          </p>
          <p className="card__detail">when it last collected changes</p>
        </article>

        <article className="card">
          <h2 className="card__title">Active</h2>
          <p className="card__value">
            <Badge tone={terminal.active ? 'positive' : 'neutral'}>
              {terminal.active ? 'Yes' : 'No'}
            </Badge>
          </p>
        </article>
      </section>

      {/* --- what it is for ------------------------------------------------ */}
      <section className="panel" aria-labelledby="terminal-application-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="terminal-application-heading">
            Application
          </h2>
          <p className="field__hint">
            What this terminal is assigned to do, and what that resolves to now.
            Assignment is per terminal; which capabilities exist at all is a
            company-level setting.
          </p>
        </div>

        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Assigned mode</dt>
            <dd>
              <Badge tone={resolves ? 'info' : 'warning'}>
                {terminal.application_mode === MULTI_PURPOSE
                  ? 'Multi-purpose'
                  : describeApplication(terminal.application_mode).label}
              </Badge>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Resolves to</dt>
            <dd>
              {resolves ? (
                <span className="badge-group">
                  {terminal.effective_applications.map((code) => (
                    <Badge key={code} tone="info">
                      {describeApplication(code).label}
                    </Badge>
                  ))}
                </span>
              ) : (
                <span className="muted">Nothing</span>
              )}
            </dd>
          </div>
        </dl>

        {/*
          The case the two fields exist to make visible: the assignment is
          retained when a company disables a capability, and effective goes
          empty. A screen showing only one of them would be misleading in
          exactly the situation that matters.
        */}
        {!resolves ? (
          <InfoNote tone="warning" title="This terminal resolves to nothing">
            {terminal.application_mode === MULTI_PURPOSE
              ? 'It is multi-purpose, but your company has no applications enabled, so there is nothing for it to serve. An owner can enable capabilities from Applications.'
              : 'It is assigned to a capability your company does not currently have enabled. The assignment is kept, and will take effect again if that capability is switched back on.'}
          </InfoNote>
        ) : null}
      </section>

      {/* --- what it is running -------------------------------------------- */}
      <section className="panel" aria-labelledby="terminal-firmware-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="terminal-firmware-heading">
            Hardware and firmware
          </h2>
        </div>

        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Firmware</dt>
            <dd>
              <span className="firmware">
                <code className="mono">{terminal.firmware_version || '—'}</code>
                {terminal.firmware_outdated ? (
                  <Badge tone="warning">Outdated</Badge>
                ) : (
                  <Badge tone="positive">Current</Badge>
                )}
              </span>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Current build for its channel</dt>
            <dd>
              <code className="mono">{terminal.current_firmware_version || '—'}</code>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Release channel</dt>
            <dd>{humaniseCode(terminal.release_channel)}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Device type</dt>
            <dd>{humaniseCode(terminal.device_type)}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Hardware revision</dt>
            <dd>{terminal.hardware_revision || <span className="muted">—</span>}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Build number</dt>
            <dd>{terminal.build_number || <span className="muted">—</span>}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Boot count</dt>
            <dd>{terminal.boot_count ?? <span className="muted">—</span>}</dd>
          </div>
        </dl>

        {terminal.firmware_outdated ? (
          <InfoNote title="Behind the current build">
            This terminal is not running the build marked current for its release
            channel. AccessLink does not push firmware over the air — updating is a
            separate, deliberate operation.
          </InfoNote>
        ) : null}
      </section>

      {/*
        Named rather than silently missing. An operator who needs to move or
        remove a terminal should learn that it is not possible here, not spend
        time looking for the button.
      */}
      <InfoNote title="Not available from the console">
        A terminal cannot be moved to another site, removed, or forced to resync
        from here — the platform has no operator API for those yet. Registering a
        terminal, and re-registering one that has been reset, happens on the device
        using its site’s provisioning key.
      </InfoNote>

      {configuring ? (
        <ApplicationModeDialog
          open
          terminal={terminal}
          onClose={() => setConfiguring(false)}
        />
      ) : null}
    </div>
  )
}
