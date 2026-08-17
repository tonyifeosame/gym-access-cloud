import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { MULTI_PURPOSE } from '../../api/types'
import { describeApplication } from '../../applications/registry'
import { can } from '../../auth/permissions'
import { Badge, TerminalStatusBadge, humaniseCode } from '../../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useSite, useTerminal } from '../../data/console'
import {
  describeGrace,
  offlinePolicyDefinition,
  usesGracePeriod,
} from '../sites/offlinePolicy'
import { useSession } from '../../session/useSession'
import { ApplicationModeDialog } from './ApplicationModeDialog'
import { EvaluateAccessDialog } from './EvaluateAccessDialog'
import {
  MoveTerminalDialog,
  ResyncTerminalDialog,
  RetireTerminalDialog,
  RevokeTerminalDialog,
  TerminalStateDialog,
} from './TerminalLifecycleDialogs'
import { readHealth } from './health'

/**
 * One terminal.
 *
 * Four questions, in the order an operator asks them: is it working, what is it
 * for, what is it running, and what can I do about it.
 *
 * THE LAST SECTION IS THE ONE THAT MATTERS MOST AND IS THE HARDEST TO GET
 * RIGHT. Disable, revoke and retire sound interchangeable and are not, and an
 * operator reaching for one of them is usually in a hurry and often standing in
 * front of the hardware. So they are presented as three separate, differently
 * weighted actions with the consequence stated on each — never as one control
 * with a mode — and each names the situation it is for rather than only what it
 * does. See TerminalLifecycleDialogs.
 */
export function TerminalDetailPage() {
  const { serial } = useParams<{ serial: string }>()
  const navigate = useNavigate()
  const { session } = useSession()
  const query = useTerminal(serial)
  // The site this terminal stands at, for its offline policy. Enabled only once
  // the terminal read has produced a site id, so a 404 on the terminal does not
  // also fire a second doomed request.
  const site = useSite(query.data?.site_public_id)
  const [configuring, setConfiguring] = useState(false)
  const [lifecycle, setLifecycle] = useState<LifecycleAction | null>(null)

  const mayConfigure = can(session, 'configureTerminals')
  // ADMIN, matching the server's route group: revoking a credential stops a door
  // working and retiring is one-way. Neither is day-to-day work.
  const mayAdminister = can(session, 'manageTerminalLifecycle')

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

        {/*
          WHAT THIS TERMINAL DOES WHEN IT CANNOT REACH THE PLATFORM, which is the
          question an operator looking at a terminal's health is one step away
          from asking — especially one looking at an OFFLINE terminal, where it
          is the only question that matters.

          READ FROM THE SITE, which is where the platform keeps it and what it
          actually sends to this terminal. Not derived, not defaulted: if the
          site read has not arrived the card says so rather than filling in a
          plausible policy, because a guess here describes what a door does.

          The terminal projection does not carry these fields and does not need
          to — `models.ConsoleTerminalHealth` models them per terminal but is not
          served by any route, and the site is the authority either way.
        */}
        <article className="card">
          <h2 className="card__title">During an outage</h2>
          <p className="card__value">
            {site.data ? (
              <Badge tone={site.data.offline_policy === 'DENY_ALL' ? 'info' : 'warning'}>
                {offlinePolicyDefinition(site.data.offline_policy).label}
              </Badge>
            ) : (
              <span className="muted">…</span>
            )}
          </p>
          <p className="card__detail">
            {site.data ? (
              <>
                {usesGracePeriod(site.data.offline_policy)
                  ? `For ${describeGrace(site.data.offline_grace_minutes)}, then it refuses everybody. `
                  : ''}
                Set for <Link to={`/sites/${terminal.site_public_id}`}>{terminal.site_name}</Link>{' '}
                and applied to every terminal there.
              </>
            ) : (
              <>
                Decided for{' '}
                <Link to={`/sites/${terminal.site_public_id}`}>{terminal.site_name}</Link>.
              </>
            )}
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
            {/*
              CORRECTED. This said AccessLink does not push firmware over the
              air, which stopped being true when the heartbeat began carrying
              update offers — so a terminal reported as behind is now usually one
              that is ABOUT TO UPDATE ITSELF, and an operator reading the old
              sentence would have gone to schedule a site visit.
            */}
            This terminal is not running the build marked current for its release
            channel, so the platform will offer that build on its next heartbeat. A
            terminal that takes the offer downloads it, writes it to flash and
            reboots on its own. If it stays behind, the catalogue entry is probably
            missing something the terminal requires — <Link to="/settings/firmware">Firmware</Link>{' '}
            says which.
          </InfoNote>
        ) : null}
      </section>

      {/* --- what can be done about it -------------------------------------- */}
      <section className="panel" aria-labelledby="terminal-lifecycle-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="terminal-lifecycle-heading">
            Lifecycle
          </h2>
          <p className="field__hint">
            These are not variants of one another. Read what each does before
            choosing — two of them cannot be undone from the console.
          </p>
        </div>

        {!mayAdminister ? (
          <InfoNote title="Read only">
            Disabling, revoking and retiring a terminal are administrator actions.
            {mayConfigure
              ? ' You can still change what this terminal is for, and queue a resync, from this page.'
              : ' Ask an administrator or owner of your company if one of these is needed.'}
          </InfoNote>
        ) : null}

        <ul className="lifecycle">
          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">
                {terminal.active ? 'Disable' : 'Re-enable'}
              </h3>
              <p className="lifecycle__detail">
                {terminal.active
                  ? 'Stops it working, keeps its credential. Reversible from this page with no site visit — for a faulty unit, or an entrance that is closed for now.'
                  : 'This terminal is disabled. Re-enabling brings it back using the credential it still holds.'}
              </p>
            </div>
            <button
              type="button"
              className={terminal.active ? 'button' : 'button button--primary'}
              disabled={!mayAdminister}
              onClick={() => setLifecycle('state')}
            >
              {terminal.active ? 'Disable' : 'Re-enable'}
            </button>
          </li>

          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Revoke credential</h3>
              <p className="lifecycle__detail">
                Destroys the device key, so the credential itself stops working.
                For hardware that is <strong>missing, stolen or out of your
                control</strong>. To bring the unit back, issue a claim code for
                its serial from{' '}
                <Link to={`/sites/${terminal.site_public_id}`}>{terminal.site_name}</Link>{' '}
                and redeem it at the terminal.
              </p>
            </div>
            <button
              type="button"
              className="button button--danger"
              disabled={!mayAdminister}
              onClick={() => setLifecycle('revoke')}
            >
              Revoke
            </button>
          </li>

          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Retire</h3>
              <p className="lifecycle__detail">
                Removes it from your fleet entirely and destroys its credential.
                For a unit that has been decommissioned, destroyed or returned.
                Cannot be undone.
              </p>
            </div>
            <button
              type="button"
              className="button button--danger"
              disabled={!mayAdminister}
              onClick={() => setLifecycle('retire')}
            >
              Retire
            </button>
          </li>

          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Move to another site</h3>
              <p className="lifecycle__detail">
                Reassigns which people this terminal knows about. It erases what it
                holds and rebuilds against the new site, so it recognises nobody
                until that finishes.
              </p>
            </div>
            <button
              type="button"
              className="button"
              disabled={!mayAdminister}
              onClick={() => setLifecycle('move')}
            >
              Move
            </button>
          </li>

          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Check who would get in</h3>
              <p className="lifecycle__detail">
                Asks the access engine what it would decide for one person at this
                terminal, and why. Records nothing and moves no door — the
                question to ask after changing a rule, instead of sending
                somebody to stand at it.
              </p>
            </div>
            {/* MANAGER: it is a preview, and the people who write rules are the
                people who need to test them. */}
            <button
              type="button"
              className="button"
              disabled={!mayConfigure}
              onClick={() => setLifecycle('evaluate')}
            >
              Check access
            </button>
          </li>

          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Force a resync</h3>
              <p className="lifecycle__detail">
                Replaces what is queued with a fresh snapshot of the people and
                settings this terminal should hold. Nothing is destroyed and it
                keeps working throughout.
              </p>
            </div>
            {/* MANAGER, not ADMIN — matching the server. A resync changes no
                state an operator has to reason about afterwards. */}
            <button
              type="button"
              className="button"
              disabled={!mayConfigure}
              onClick={() => setLifecycle('resync')}
            >
              Resync
            </button>
          </li>
        </ul>

        {/*
          THE REGISTRATION SENTENCE, CORRECTED. It used to say registration
          happens "using its site's provisioning key", which was true and is now
          the path to avoid: that key registers every terminal at the site, for
          ever, and putting it on an installer's laptop is what claim codes were
          built to stop. The console CAN take part now — it issues the code.
        */}
        <p className="field__hint">
          Registration itself happens on the device. What the console does is issue a{' '}
          <strong>claim code</strong> for one serial, from{' '}
          <Link to={`/sites/${terminal.site_public_id}`}>{terminal.site_name}</Link>;
          whoever is at the terminal redeems it there and the unit is handed its own
          credential. The site’s provisioning key does not need to leave the platform.
        </p>
      </section>

      {configuring ? (
        <ApplicationModeDialog
          open
          terminal={terminal}
          onClose={() => setConfiguring(false)}
        />
      ) : null}

      {lifecycle === 'state' ? (
        <TerminalStateDialog open terminal={terminal} onClose={() => setLifecycle(null)} />
      ) : null}
      {lifecycle === 'revoke' ? (
        <RevokeTerminalDialog open terminal={terminal} onClose={() => setLifecycle(null)} />
      ) : null}
      {lifecycle === 'retire' ? (
        <RetireTerminalDialog
          open
          terminal={terminal}
          onClose={() => setLifecycle(null)}
          // The page it was opened from now 404s, so leaving is part of the
          // action rather than something to let the operator discover.
          onRetired={() => navigate('/terminals', { replace: true })}
        />
      ) : null}
      {lifecycle === 'move' ? (
        <MoveTerminalDialog open terminal={terminal} onClose={() => setLifecycle(null)} />
      ) : null}
      {lifecycle === 'resync' ? (
        <ResyncTerminalDialog open terminal={terminal} onClose={() => setLifecycle(null)} />
      ) : null}
      {lifecycle === 'evaluate' ? (
        <EvaluateAccessDialog open terminal={terminal} onClose={() => setLifecycle(null)} />
      ) : null}
    </div>
  )
}

type LifecycleAction = 'state' | 'revoke' | 'retire' | 'move' | 'resync' | 'evaluate'
