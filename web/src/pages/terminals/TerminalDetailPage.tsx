import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { MULTI_PURPOSE, type ProvisioningSource } from '../../api/types'
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
import { ChangeWifiDialog } from './ChangeWifiDialog'
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
  const [changingWifi, setChangingWifi] = useState(false)

  const mayConfigure = can(session, 'configureTerminals')
  // ADMIN, matching the server's route group: revoking a credential stops a door
  // working and retiring is one-way. Neither is day-to-day work.
  const mayAdminister = can(session, 'manageTerminalLifecycle')
  // ADMIN as well, and named separately from the lifecycle gate so the two can
  // be argued about independently — the server gates them on the same role today
  // but for different reasons.
  const mayChangeWifi = can(session, 'changeTerminalWifi')

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

  /*
    WHAT THIS TERMINAL SERVES, AND THE DISTINCTION THAT USED TO BE MISSING.

    This page read `effective_applications.length > 0` and called the empty case
    "resolves to nothing" — for BOTH of the two ways it can be empty. They are
    not the same, and the engine treats them as opposites:

      MULTI-PURPOSE with nothing enabled     database/authorization.go clears the
                                             application and SKIPS the capability
                                             gate entirely. The terminal admits
                                             people normally.

      ASSIGNED to a feature since turned off  the gate runs and refuses, with
                                             APPLICATION_NOT_ENABLED. Nobody
                                             gets in.

    Signup enables no features and every terminal defaults to multi-purpose, so
    the FIRST terminal of every new customer landed in the first case — and was
    told, in warning styling, that it served nothing. A customer standing at
    hardware they had just paired was being told it was inert while it worked.

    `notServing` is therefore the second case only. Multi-purpose is never a
    fault; it is the default, and the copy below explains it rather than
    flagging it.
  */
  const multiPurpose = terminal.application_mode === MULTI_PURPOSE
  const serving = terminal.effective_applications.length > 0
  const notServing = !multiPurpose && !serving

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
              Change feature
            </button>
          ) : null
        }
      />

      {/*
        WARNING TONE FOR THE WARNING STATES TOO, not only for ERROR. An offline
        terminal is not receiving anything queued for it, which is worth the
        same visual weight as a fault; it was previously drawn in the same quiet
        box as "checking in, but not yet reporting as a working terminal".
      */}
      {health.note ? (
        <InfoNote
          tone={health.tone === 'danger' || health.tone === 'warning' ? 'warning' : 'muted'}
          title="Health"
        >
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

        {/*
          ONE CARD, NOT TWO, AND THIS IS WHY. It used to read "Last heartbeat:
          1 minute ago" over "last seen 1 minute ago", which is one instant
          printed twice: `HeartbeatDevice` sets last_heartbeat_at and
          last_seen_at to CURRENT_TIMESTAMP in the same statement, and nothing
          else on `devices` writes either. Two labels for one fact is not extra
          information, it is a reader wondering which of them to believe.

          "Check-in" rather than "heartbeat": the operator reading this did not
          design the protocol.
        */}
        <article className="card">
          <h2 className="card__title">Last check-in</h2>
          <p className="card__value">
            {terminal.last_heartbeat_at ? (
              <Timestamp value={terminal.last_heartbeat_at} relative />
            ) : (
              <span className="muted">Never</span>
            )}
          </p>
          <p className="card__detail">when it last contacted the platform</p>
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
          <p className="card__detail">when it last collected the people and settings it holds</p>
        </article>

        {/*
          KEPT, AND GIVEN A SENTENCE. `active` and `status` are set
          independently on the server -- see the note in RegisterDevice: "either
          one is an operator saying no" -- so this is not a restatement of the
          status badge and cannot be folded into it. What it lacked was any
          statement of what the word meant, so a card reading "Active: Yes" sat
          beside "Status: Error" and explained nothing.
        */}
        <article className="card">
          <h2 className="card__title">In service</h2>
          <p className="card__value">
            <Badge tone={terminal.active ? 'positive' : 'neutral'}>
              {terminal.active ? 'Yes' : 'No'}
            </Badge>
          </p>
          <p className="card__detail">
            {terminal.active
              ? 'it is allowed to let people in'
              : 'an operator has taken it out of service'}
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
            What this terminal does
          </h2>
          <p className="field__hint">
            What this terminal is assigned to do, and what it is doing now.
            The assignment is per terminal; which features your company has at all
            is a company-wide setting.
          </p>
        </div>

        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Assigned to</dt>
            <dd>
              {/*
                MULTI-PURPOSE IS NEVER A WARNING. It is the state every terminal
                starts in and a perfectly good one to stay in. Only an
                assignment that has stopped working is marked.
              */}
              <Badge tone={notServing ? 'warning' : 'info'}>
                {multiPurpose
                  ? 'Multi-purpose'
                  : describeApplication(terminal.application_mode).label}
              </Badge>
            </dd>
          </div>
          <div className="detail-list__row">
            {/* "Currently serving", not "Resolves to". Resolution is what the
                platform does with the setting; what an operator wants to know
                is what the terminal is doing. */}
            <dt>Currently serving</dt>
            <dd>
              {serving ? (
                <span className="badge-group">
                  {terminal.effective_applications.map((code) => (
                    <Badge key={code} tone="info">
                      {describeApplication(code).label}
                    </Badge>
                  ))}
                </span>
              ) : multiPurpose ? (
                // NOT "Nothing". A multi-purpose terminal at a company with no
                // features on is working — it simply has no named feature to
                // list, because its company has not chosen one.
                <span className="muted">Anything your company turns on</span>
              ) : (
                <span className="muted">Nothing</span>
              )}
            </dd>
          </div>
        </dl>

        {/*
          THE TWO NOTICES ARE OPPOSITES AND MUST NOT SHARE A BRANCH.

          One reassures somebody whose terminal is working and looks unconfigured;
          the other warns somebody whose terminal is genuinely refusing people.
          They were a single `!resolves` block with a ternary inside, which is how
          the working case came to be rendered in warning styling under a heading
          saying it served nothing.
        */}
        {multiPurpose && !serving ? (
          <InfoNote title="Multi-purpose, which is ready to use">
            This terminal is not tied to one feature — it serves whatever your
            company turns on, and follows your access rules either way. Your company
            has no features turned on yet, which is the normal place to start and
            does not stop this terminal working. Turning one on later needs no change
            here.
          </InfoNote>
        ) : null}

        {notServing ? (
          <InfoNote tone="warning" title="This terminal is not letting anyone in">
            It is set up for a feature your company has since turned off, so people
            are refused at it. The setting is kept and starts working again the
            moment that feature is switched back on — or you can set this terminal
            to multi-purpose, which is not tied to any one feature.
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
          {/*
            THE THIRD ANSWER, WHICH THIS ROW USED TO REFUSE TO GIVE. The badge
            was a two-way branch on `firmware_outdated`, so a terminal that has
            never reported a version — no heartbeat yet, or one that predates
            version reporting — rendered an em dash next to a green "Current".
            The console was asserting a terminal was up to date while showing
            that it had no idea what it was running.

            The fleet list has always been honest about the same terminal, which
            is how the two screens came to disagree.
          */}
          <div className="detail-list__row">
            <dt>Firmware</dt>
            <dd>
              {terminal.firmware_version ? (
                <span className="firmware">
                  <code className="mono">{terminal.firmware_version}</code>
                  {terminal.firmware_outdated ? (
                    <Badge tone="warning">Outdated</Badge>
                  ) : (
                    <Badge tone="positive">Current</Badge>
                  )}
                </span>
              ) : (
                <span className="muted">Not reported</span>
              )}
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Current build for its channel</dt>
            <dd>
              <code className="mono">{terminal.current_firmware_version || '—'}</code>
            </dd>
          </div>
          {/*
            HOW THIS DOOR GOT HERE. The audit trail records the provisioning
            action, but joining a terminal back to it means knowing which of
            three action names to look for and searching a window nobody wrote
            down — so the row carries the answer.

            "Not recorded" rather than a guess for terminals that predate the
            column. Backfilling them all to the site key would be probably-true
            and occasionally false, and a console cannot un-say "site key".
          */}
          <div className="detail-list__row">
            <dt>Set up using</dt>
            <dd>{describeProvisioning(terminal.provisioned_via)}</dd>
          </div>
        </dl>

        {/*
          FOLDED AWAY, NOT DELETED. Release channel, device type, hardware
          revision, build number and boot count are what somebody reads to a
          support engineer; none of them is why an operator opened this page,
          and "Device type: Terminal" on a page about a terminal is a row that
          can only ever say one thing. Behind a disclosure they cost nothing and
          are still one click away — deleting them would have cost a real
          diagnostic.
        */}
        <details className="technical">
          <summary>Technical details</summary>
          <dl className="detail-list">
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
        </details>

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

      {/*
        --- how it reaches the platform -------------------------------------

        A SECTION OF ITS OWN rather than a sixth entry in Lifecycle, and the
        split is the point. Everything in Lifecycle answers "should this terminal
        be in service"; this answers "how does it get to us", which is the
        question somebody has when the answer to the first one is yes and the
        door still does not work.

        It also has a different audience. Disable, revoke and retire are things
        an administrator decides. Changing the Wi-Fi is something that HAPPENS TO
        a customer — the router was replaced, the password rotated — and they
        arrive here looking for the word "Wi-Fi" rather than for a lifecycle
        operation.
      */}
      <section className="panel" aria-labelledby="terminal-network-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="terminal-network-heading">
            Network
          </h2>
          <p className="field__hint">
            How this terminal reaches the platform. AccessLink never stores your
            Wi-Fi password — the network is joined at the terminal itself.
          </p>
        </div>

        {/*
          NO STATUS BADGE AND NO HEARTBEAT HERE, deliberately. Both are already
          on the cards at the top of this page, and a second copy is not extra
          information — it is a second thing to keep in agreement, and the first
          time they disagree an operator has to work out which one to believe.
          This section is the ACTION and the sentence that goes with it.
        */}

        {!mayChangeWifi ? (
          <InfoNote title="Read only">
            Moving a terminal to a different Wi-Fi network is an administrator
            action. Ask an administrator or owner of your company, or use the
            terminal&apos;s own recovery: hold the button on the unit for five
            seconds and it returns to Wi-Fi setup mode.
          </InfoNote>
        ) : null}

        <ul className="lifecycle">
          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Change Wi-Fi</h3>
              <p className="lifecycle__detail">
                For a new router, or a Wi-Fi password that has changed. The
                terminal returns to setup mode and somebody joins it to the new
                network from a phone standing next to it. Its people, credential
                and settings are untouched.
              </p>
            </div>
            <button
              type="button"
              className="button"
              disabled={!mayChangeWifi}
              onClick={() => setChangingWifi(true)}
            >
              Change Wi-Fi
            </button>
          </li>
        </ul>

        {/*
          STATED WHERE IT IS NEEDED, not only inside the dialog. A terminal that
          cannot be reached cannot be sent anything, and that is the state most
          people reading this section are in — the Wi-Fi broke, which is why they
          are here. Telling them only after they press the button would be the
          console making them ask.

          GATED ON `reachable`, NOT ON `status !== 'ONLINE'`, and the difference
          is the whole correction. The old test made this box appear over four
          states it was false for: it told an operator that a terminal reporting
          a fault twelve minutes ago was offline, and that a terminal installing
          an update it had just been offered was offline, in both cases directly
          beneath a card showing the check-in that disproved it. Nothing about
          the server's status semantics changed — `reachable` is read from the
          same status and heartbeat, it just stops claiming more than they say.
        */}
        {!health.reachable ? (
          <InfoNote tone="warning" title="This terminal cannot be reached right now">
            {/* NOT A RESTATEMENT of the health note at the top of the page,
                which already says this terminal has never checked in. This box
                is about the consequence for the thing beside it: nothing can be
                sent. */}
            {health.neverReported ? (
              <>
                Nothing can be sent to it until it has checked in for the first time.
                Connect it to the network, or use the terminal&apos;s local Wi-Fi
                recovery procedure — hold the button on the unit for five seconds and
                it returns to Wi-Fi setup mode without needing the network at all.
              </>
            ) : (
              <>
                The terminal is offline. Connect it to the network again or use the
                terminal&apos;s local Wi-Fi recovery procedure — hold the button on the
                unit for five seconds and it returns to Wi-Fi setup mode without
                needing the network at all.
              </>
            )}
          </InfoNote>
        ) : null}

        {/*
          MID-UPDATE IS NOT UNREACHABLE, and it is not nothing either: the unit
          is downloading a build and will restart itself. Saying so is what stops
          somebody sending it back to setup mode in the middle of that.
        */}
        {health.reachable && terminal.status === 'UPDATING' ? (
          <InfoNote title="It is installing an update">
            The terminal is in contact and is writing a new build to itself. It
            restarts on its own when it finishes; it is worth waiting for that before
            sending it back to Wi-Fi setup.
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

      {/*
        --- checks and maintenance ------------------------------------------

        SPLIT OUT OF LIFECYCLE, and every control is the one it always was — the
        gates, the handlers and the dialogs are untouched. Lifecycle answers
        "should this terminal be in service", and its own hint says two of the
        things in it cannot be undone. Neither of these two belongs to that
        question: one asks the access engine a hypothetical and records nothing,
        the other queues a snapshot. Sitting in the same list as Revoke and
        Retire, under a warning about irreversible actions, made them read as
        more dangerous than they are — and made the warning read as less.
      */}
      <section className="panel" aria-labelledby="terminal-maintenance-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="terminal-maintenance-heading">
            Checks and maintenance
          </h2>
          <p className="field__hint">
            Neither of these changes what this terminal is or what it holds for long
            — they are safe to run while it is in service.
          </p>
        </div>

        {!mayConfigure ? (
          <InfoNote title="Read only">
            Checking access and queueing a resync need a manager or above. Ask an
            administrator or owner of your company if one of these is needed.
          </InfoNote>
        ) : null}

        <ul className="lifecycle">
          <li className="lifecycle__option">
            <div className="lifecycle__text">
              <h3 className="lifecycle__title">Check who would get in</h3>
              <p className="lifecycle__detail">
                Asks the access engine what it would decide for one person at this
                terminal, and why. Records nothing and changes nothing at the
                access point — the question to ask after changing a rule, instead
                of sending somebody to stand at it.
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
      </section>

      {changingWifi ? (
        <ChangeWifiDialog open terminal={terminal} onClose={() => setChangingWifi(false)} />
      ) : null}

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

/**
 * How this terminal's CURRENT credential was issued.
 *
 * PLAIN LANGUAGE RATHER THAN THE CODE, because the audience is a customer
 * wondering how a door got onto their account, not somebody reading the schema.
 * The three answers have genuinely different meanings for them: one says a
 * secret that provisions the whole site was in play, one says a single-use code
 * was, and one says they approved it from this console.
 */
function describeProvisioning(source: ProvisioningSource | undefined) {
  switch (source) {
    case 'ANNOUNCEMENT':
      return 'Added from this console after the terminal showed a code'
    case 'CLAIM_CODE':
      return 'A single-use claim code, redeemed at the terminal'
    case 'SITE_KEY':
      return 'The site’s provisioning key'
    default:
      // Not a guess. See the note at the call site.
      return <span className="muted">Not recorded</span>
  }
}
