import { useId, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import { ApiError } from '../../api/client'
import type { FirmwareVersion, Terminal } from '../../api/types'
import { can } from '../../auth/permissions'
import { Badge, humaniseCode } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { CheckboxField, FormError, TextField } from '../../components/Form'
import { Meter } from '../../components/Meter'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCreateFirmware, useFirmware, useSetCurrentFirmware, useTerminals } from '../../data/console'
import { useSession } from '../../session/useSession'
import { firmwareOfferability, terminalsOffered } from './offerability'
import {
  fleetStanding,
  findings,
  groupByTarget,
  standingOf,
  targetKey,
  type FleetKnowledge,
  type Standing,
  type TargetGroup,
  type UpdateFinding,
} from './standing'

/**
 * Firmware.
 *
 * ---------------------------------------------------------------------------
 * THE QUESTION THIS SCREEN ANSWERS, AND THE ONE IT USED TO ANSWER
 * ---------------------------------------------------------------------------
 *
 * A customer opens this page to ask ONE thing: are my terminals all right, and
 * do I need to do anything. The screen used to answer a different question — it
 * was the firmware CATALOGUE, organised as builds grouped by device type and
 * release channel, with a red "Make current" beside each of them. That is the
 * shape of the data and the transpose of the question, so a page measured at
 * 1440 and 390 spent its first screen on a 91-word warning and its next two on
 * combinations the customer owned no hardware for, and never once said how many
 * of their terminals needed anything.
 *
 * So the order is now: where the fleet stands, then the one update if there is
 * one, then — behind a disclosure — the catalogue, publishing, and every
 * version that is not the answer.
 *
 * ---------------------------------------------------------------------------
 * FOUR THINGS ARE LOAD-BEARING AND NONE OF THEM MOVED BEHIND THE DISCLOSURE
 * ---------------------------------------------------------------------------
 *
 *   1. WHERE THE FLEET STANDS, including the distinction between a terminal
 *      that is behind and one that has never said what it runs. See
 *      `standing.ts` — the API cannot tell those apart and a customer must not
 *      be sent looking for an update that does not exist.
 *
 *   2. THE UPDATE, when one exists, with the number of terminals it reaches,
 *      what happens when it is applied, and what happens if it is not.
 *
 *   3. "CANNOT BE INSTALLED", whenever a RELEVANT version cannot be sent. The
 *      server withholds an offer it could not populate and logs the reason
 *      where no operator will ever read it; without this the symptom is a fleet
 *      that silently never moves. It is the only actionable fault this screen
 *      can report and it stays in the open.
 *
 *   4. THE FACT THAT SETTING A TARGET UPDATES HARDWARE. It is no longer a
 *      standing banner over a page with nothing to press — it lives with the
 *      action, and inside Advanced where the same action is also available.
 *
 * ---------------------------------------------------------------------------
 * WHAT AN OLDER VERSION ACTUALLY DOES, AND WHY IT IS NOT AN UPDATE
 * ---------------------------------------------------------------------------
 *
 * Setting an OLDER version as the target does not roll a fleet back. The server
 * offers it (`database/firmware_offer.go` compares versions only for exact
 * equality) and every terminal REFUSES it — `firmware_update.cpp` returns
 * `kUpToDate` for anything not strictly newer than what it runs. Nothing
 * installs, ever. What does happen is that `firmware_outdated` flips true for
 * the whole fleet, because it is an exact string mismatch, so every terminal
 * reports as needing an update it will never accept, until somebody sets a
 * newer target.
 *
 * The old screen offered that as a red "Make current" button beside a
 * confirmation WORD FOR WORD IDENTICAL to the one for a genuine upgrade,
 * promising the terminals would download and install it. So an older version is
 * now never presented as an update: it exists only under Advanced, and its
 * confirmation says what actually happens. NOTHING ON THE DEVICE OR THE SERVER
 * CHANGED — this is the console stopping describing it wrongly.
 *
 * WHY THE CONSOLE NEVER ORDERS VERSION STRINGS: see the note in `standing.ts`.
 */

const FIRMWARE_LEAD = 'The software your terminals run, and whether it is up to date.'

export function FirmwarePage() {
  const { session } = useSession()
  const firmware = useFirmware()
  // The fleet, so the screen can say what it is actually describing AND how many
  // terminals an update would reach. Scoped by the operator's grants exactly as
  // the Terminals page is, which is stated rather than left to be inferred from
  // a number that looks company-wide.
  const terminals = useTerminals()

  const [publishing, setPublishing] = useState(false)
  const [retargeting, setRetargeting] = useState<FirmwareVersion | null>(null)

  const mayManage = can(session, 'manageFirmware')

  /*
    WHETHER THE CONSOLE KNOWS WHAT AN UPDATE WOULD REACH — and it is a THREE-WAY
    answer, which is the whole of this guard.

    Every count on this page comes from a second request. Its failure was once
    unchecked, so an empty terminal list meant two irreconcilable things at once:
    "no terminal is affected" and "we could not find out". The page chose the
    first, and the typed-phrase safeguard — armed by `affected > 0` — disarmed
    itself at exactly that moment.

    So the fleet is `known` only when the request has actually succeeded.
    Loading and failed are both "unknown", they read differently, and neither
    permits a target change.
  */
  // MEMOISED, because it is an object literal that `findings` depends on: rebuilt
  // every render it would defeat the memo below entirely, recomputing the
  // findings on every keystroke anywhere on the page.
  const fleet = useMemo<FleetKnowledge>(
    () =>
      terminals.isSuccess
        ? { known: true, terminals: terminals.data.terminals }
        : { known: false, reason: terminals.isError ? 'unavailable' : 'loading' },
    [terminals.isSuccess, terminals.isError, terminals.data],
  )

  const groups = useMemo(
    () => groupByTarget(firmware.data?.firmware_versions ?? [], terminals.data?.terminals ?? []),
    [firmware.data, terminals.data],
  )

  const found = useMemo(() => findings(groups, fleet), [groups, fleet])

  /*
    THE HEADING SURVIVES THE WAIT. This once returned a bare `LoadingState`, so
    while the catalogue loaded the document contained no `<h1>` at all — an axe
    `page-has-heading-one` violation, reproduced at both widths, and a screen
    whose entire main region read "Loading firmware…" with nothing saying which
    screen it was.
  */
  if (firmware.isPending) {
    return (
      <div className="page">
        <PageHeader title="Firmware" lead={FIRMWARE_LEAD} />
        <LoadingState label="Loading firmware…" />
      </div>
    )
  }

  if (firmware.isError) {
    return (
      <div className="page">
        <PageHeader title="Firmware" lead={FIRMWARE_LEAD} />
        <ErrorState error={firmware.error} onRetry={() => void firmware.refetch()} />
      </div>
    )
  }

  const retargetingGroup = retargeting
    ? (groups.find((group) => group.key === targetKey(retargeting)) ?? null)
    : null

  return (
    <div className="page">
      <PageHeader title="Firmware" lead={FIRMWARE_LEAD} />

      {/* --- 1 · where the fleet stands ------------------------------------ */}
      <YourTerminals fleet={fleet} onRetry={() => void terminals.refetch()} />

      {/* --- 2 · the one thing to do, if there is one ---------------------- */}
      {found.map((finding) => {
        switch (finding.kind) {
          case 'UPDATE':
            return (
              <UpdateAvailable
                key={`update-${finding.key}`}
                finding={finding}
                mayManage={mayManage}
                onApply={() => setRetargeting(finding.version)}
              />
            )
          case 'UPDATE_BLOCKED':
          case 'TARGET_BLOCKED':
            return (
              <CannotBeInstalled
                key={`${finding.kind}-${finding.key}`}
                kind={finding.kind}
                version={finding.version}
                problems={finding.problems}
                terminalCount={finding.terminalCount}
              />
            )
          case 'NO_TARGET':
            return (
              <InfoNote
                key={`no-target-${finding.key}`}
                tone="warning"
                title="No version is set for these terminals"
              >
                <p>
                  {finding.terminalCount} terminal{finding.terminalCount === 1 ? '' : 's'} you can
                  see {finding.terminalCount === 1 ? 'has' : 'have'} no version set as their
                  target, so nothing is being offered to{' '}
                  {finding.terminalCount === 1 ? 'it' : 'them'} and{' '}
                  {finding.terminalCount === 1 ? 'it reports' : 'they report'} as up to date
                  whatever {finding.terminalCount === 1 ? 'it is' : 'they are'} running.
                </p>
                <p>
                  {/*
                    THE CHOICE IS NOT MADE HERE, DELIBERATELY. Picking one of
                    several recorded versions requires knowing which is newest,
                    and the console does not order version strings -- the device
                    does. So it says what is true and sends the decision to
                    somebody who knows the answer.
                  */}
                  {finding.versionCount === 1
                    ? 'One version is recorded for them.'
                    : `${finding.versionCount} versions are recorded for them.`}{' '}
                  Choose one under <strong>Advanced: all versions</strong>, below.
                </p>
              </InfoNote>
            )
        }
      })}

      {/* --- 3 · everything else ------------------------------------------- */}
      <AllVersions
        groups={groups}
        fleet={fleet}
        mayManage={mayManage}
        hasFindings={found.length > 0}
        onPublish={() => setPublishing(true)}
        onRetarget={setRetargeting}
      />

      {publishing ? (
        <PublishFirmwareDialog open onClose={() => setPublishing(false)} />
      ) : null}
      {retargeting ? (
        <SetTargetDialog
          open
          version={retargeting}
          previous={retargetingGroup?.current ?? null}
          standing={standingOf(retargeting, retargetingGroup?.current ?? null)}
          terminals={retargetingGroup?.terminals ?? []}
          fleet={fleet}
          onClose={() => setRetargeting(null)}
        />
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 1 · Your terminals
// ---------------------------------------------------------------------------

/**
 * Where the fleet stands, first, before anything about the catalogue.
 *
 * THIS IS THE ANSWER TO THE QUESTION SOMEBODY OPENED THE PAGE WITH, and it did
 * not exist. The old screen's first mention of the customer's own hardware was
 * a sub-clause 372px down on a desktop and 676px down on a phone — "4 terminals
 * you can see are on it, of which 3 are already running 1.2.0" — inside a
 * paragraph explaining the catalogue's data model.
 *
 * THE SAME METER THE OVERVIEW DRAWS, from the same function, so the two screens
 * cannot come apart on a fact a customer can read on both.
 */
function YourTerminals({
  fleet,
  onRetry,
}: {
  fleet: FleetKnowledge
  onRetry: () => void
}) {
  return (
    <section className="panel" aria-labelledby="firmware-fleet-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="firmware-fleet-heading">
          Your terminals
        </h2>
      </div>

      {!fleet.known ? (
        /*
          NOT A METER OF ZEROES. Every number here comes from the terminal list,
          and drawing an empty bar when that list failed would report "0 update
          available" — which is the good news, stated on no evidence.
        */
        fleet.reason === 'unavailable' ? (
          <>
            <p className="field__hint">
              The list of terminals could not be loaded, so this page cannot say where
              your terminals stand or how many an update would reach. Everything under
              Advanced is accurate; only the terminal numbers are missing.
            </p>
            <button type="button" className="button" onClick={onRetry}>
              Try again
            </button>
          </>
        ) : (
          <p className="field__hint">Checking where your terminals stand…</p>
        )
      ) : fleet.terminals.length === 0 ? (
        <p className="field__hint">
          No terminals yet, so there is nothing to keep up to date. Add one from{' '}
          <Link to="/terminals">Terminals</Link>.
        </p>
      ) : (
        <FleetMeter terminals={fleet.terminals} />
      )}
    </section>
  )
}

function FleetMeter({ terminals }: { terminals: Terminal[] }) {
  const standing = fleetStanding(terminals)

  return (
    <>
      <Meter
        segments={[
          { id: 'up-to-date', label: 'Up to date', value: standing.upToDate, tone: 'positive' },
          {
            id: 'update-available',
            label: 'Update available',
            value: standing.updateAvailable,
            tone: 'warning',
          },
          {
            /*
              A THIRD SEGMENT, AND THE REASON THIS FUNCTION EXISTS.

              `firmware_outdated` is TRUE for a terminal that has never reported
              a version — see `standing.ts`. Counting those as "update
              available" sends somebody hunting for an update when what actually
              happened is that a terminal has not been switched on at its door
              yet. Neutral tone: it is not a fault, it is a terminal nobody has
              plugged in.
            */
            id: 'never-reported',
            label: 'Hasn’t reported a version yet',
            value: standing.neverReported,
            tone: 'neutral',
          },
        ]}
      />
      <p className="field__hint">
        Counted across the terminals you can see.{' '}
        <Link to="/terminals">Terminals</Link> lists them one by one.
      </p>
    </>
  )
}

// ---------------------------------------------------------------------------
// 2 · Update available
// ---------------------------------------------------------------------------

/**
 * The one decision, with everything needed to take it.
 *
 * ONE CARD PER GROUP THAT HAS ONE, AND ONLY THE NEWEST VERSION. The old screen
 * put a red button on every non-current row: three of them on a typical page,
 * one of which was a DOWNGRADE and one of which would have reached nobody. A
 * customer choosing between three identical destructive buttons is a customer
 * being asked to do the platform's reasoning.
 *
 * THE WARNING LIVES HERE. It used to be a 91-word banner at the top of the
 * page, rendered whenever the catalogue was non-empty — including on a fleet
 * that was entirely up to date with nothing to press. A caution attached to no
 * action is a caution people learn to scroll past. Attached to the button, it is
 * read by the person about to use it.
 *
 * AND WHAT HAPPENS IF THEY DO NOTHING, which the old screen never said at all.
 * "Nothing" is the honest answer and it is worth printing: an update that can
 * wait is a decision a customer is allowed to make, and a screen that only
 * describes the consequences of acting reads as one that is pressing them to.
 */
function UpdateAvailable({
  finding,
  mayManage,
  onApply,
}: {
  finding: UpdateFinding
  mayManage: boolean
  onApply: () => void
}) {
  const { version, wouldReach } = finding
  const one = wouldReach === 1

  return (
    <section className="panel" aria-labelledby={`firmware-update-${finding.key}`}>
      <div className="panel__header">
        <h2 className="panel__title" id={`firmware-update-${finding.key}`}>
          Update available
        </h2>
        <p className="field__hint">
          <code className="mono">{version.version}</code> · published{' '}
          <Timestamp value={version.published_at ?? version.created_at} relative />{' '}
          <span className="muted">
            · <Timestamp value={version.published_at ?? version.created_at} dateOnly />
          </span>
        </p>
      </div>

      {version.release_notes ? <p>{version.release_notes}</p> : null}

      <p>
        <strong>
          {wouldReach} terminal{one ? '' : 's'} you can see
        </strong>{' '}
        {one ? 'is' : 'are'} not running it.
      </p>

      {/*
        TWO SENTENCES, NOT A DEFINITION LIST.

        This was a `.detail-list`, which stacks each pair into a bordered block
        below the breakpoint: 215px at 390 and 235px at 360, for two facts that
        are one line each. Measured, that block alone pushed the primary button
        below the fold on both phones. The content did not need cutting; the
        furniture around it did.
      */}
      <p className="rule__detail">
        <strong>If you update:</strong> each terminal downloads it automatically at its
        next check-in, <strong>installs it and restarts once</strong>, with nobody
        visiting the site. It cannot be undone.
      </p>
      <p className="rule__detail">
        {/*
          THE PART THE OLD SCREEN NEVER SAID. Nothing breaks, and saying so is
          what makes the other half believable — a screen that only describes the
          consequences of acting reads as one that is pressing you to.
        */}
        <strong>If you don’t:</strong> nothing. {one ? 'It keeps' : 'They keep'} running
        the version {one ? 'it has' : 'they have'} and {one ? 'goes' : 'go'} on working.
      </p>

      {mayManage ? (
        <div className="panel__actions">
          <button type="button" className="button button--primary" onClick={onApply}>
            Update {wouldReach} terminal{one ? '' : 's'} to {version.version}
          </button>
        </div>
      ) : null}
    </section>
  )
}

// ---------------------------------------------------------------------------
// 3 · Cannot be installed
// ---------------------------------------------------------------------------

/**
 * The fault only this console can see.
 *
 * The server refuses to offer a version it could not populate — no digest, no
 * size, a plaintext address, a string longer than the device's buffer — and
 * logs the reason server-side where no operator will ever read it. So a version
 * can be published, set as the target, look entirely normal, and update nothing
 * for ever.
 *
 * NEVER BEHIND THE DISCLOSURE, in either of its two shapes. `TARGET_BLOCKED` is
 * a live fault — the fleet is pointed at something that will never be sent, so
 * it is frozen wherever it happens to be. `UPDATE_BLOCKED` is the newer version
 * a customer is entitled to expect, not arriving; hiding it would make the
 * newest version simply vanish from the screen with no explanation.
 *
 * IT NAMES THE FIELD TO FIX, which is the whole value: it turns "the update
 * does not work" into one line somebody can act on.
 */
function CannotBeInstalled({
  kind,
  version,
  problems,
  terminalCount,
}: {
  kind: 'TARGET_BLOCKED' | 'UPDATE_BLOCKED'
  version: FirmwareVersion
  problems: string[]
  terminalCount: number
}) {
  const target = kind === 'TARGET_BLOCKED'

  return (
    <InfoNote tone="warning" title="Cannot be installed">
      <p>
        <code className="mono">{version.version}</code>{' '}
        {target ? (
          <>
            is the version your terminals are pointed at, and{' '}
            <strong>the platform will not send it to any of them</strong>. Nothing is
            being offered to{' '}
            {terminalCount === 1
              ? 'the one terminal you can see here'
              : `the ${terminalCount} terminals you can see here`}
            , so {terminalCount === 1 ? 'it stays' : 'they stay'} on whatever{' '}
            {terminalCount === 1 ? 'it is' : 'they are'} running.
          </>
        ) : (
          <>
            is newer than the version your terminals are pointed at, but{' '}
            <strong>the platform will not send it to anything</strong> — so it is not
            offered as an update.
          </>
        )}
      </p>
      <ul>
        {problems.map((problem) => (
          <li key={problem}>{problem}</li>
        ))}
      </ul>
      <p>
        A published version cannot be edited. Publish a corrected one under{' '}
        <strong>Advanced: all versions</strong> and set that as the target.
      </p>
    </InfoNote>
  )
}

// ---------------------------------------------------------------------------
// 4 · Advanced: all versions
// ---------------------------------------------------------------------------

/**
 * Everything that is not the answer.
 *
 * NOTHING WAS REMOVED, AND THAT IS THE POINT OF THE DISCLOSURE RATHER THAN A
 * DELETION. Every version, including superseded and older ones; every group,
 * including combinations no terminal is on; publishing; the mandatory flag;
 * device type; release channel; publication dates; and setting any of them as
 * the target. All of it is one press away and all of it still works.
 *
 * WHY IT IS CLOSED BY DEFAULT. On a phone the catalogue was 2.2 of the page's
 * 3.05 screens, and two of its three panels described combinations the customer
 * owned no hardware for. For the ordinary visit — "is anything wrong" — none of
 * it is the answer.
 *
 * THE CAUTION IS REPEATED INSIDE, because the same fleet-updating action is
 * available in here and somebody who opened the disclosure may not have read
 * the card above it.
 */
function AllVersions({
  groups,
  fleet,
  mayManage,
  hasFindings,
  onPublish,
  onRetarget,
}: {
  groups: TargetGroup[]
  fleet: FleetKnowledge
  mayManage: boolean
  /** Whether the main view said anything, which changes what "open" means. */
  hasFindings: boolean
  onPublish: () => void
  onRetarget: (version: FirmwareVersion) => void
}) {
  return (
    <section className="panel" aria-labelledby="firmware-advanced-title">
      <details className="disclosure">
        <summary className="disclosure__summary" id="firmware-advanced-title">
          Advanced: all versions
        </summary>

        <div className="disclosure__body">
          <p className="field__hint">
            Every firmware version recorded for this company, including ones no
            terminal is on. Setting a version as the target is what starts an
            update: every terminal of that hardware type on that update channel is
            offered it at its next check-in, and a terminal that takes the offer
            installs it and restarts once, without anybody visiting the site.
          </p>

          {mayManage ? (
            <div className="panel__actions">
              {/*
                PUBLISHING LIVES IN HERE NOW. It records that a version exists
                and sends nothing to anything, and for most customers it is not
                their job at all -- it means hosting an image, computing its
                SHA-256 and counting its bytes. Keeping it on the main screen
                put a seven-field engineering form beside the question "is
                anything wrong". The capability, the form and the ADMIN
                permission are unchanged.
              */}
              <button type="button" className="button" onClick={onPublish}>
                Publish a version
              </button>
            </div>
          ) : null}

          {groups.length === 0 ? (
            <InfoNote title="No versions recorded">
              <p>
                Nothing has been published, so every terminal reports as up to date —
                not because it is, but because there is nothing to compare it with, and
                nothing is being offered to anything.
              </p>
              <p>
                Publishing the first version for a device type and channel is what makes
                “update available” mean anything, and it may mark terminals as needing an
                update the moment it lands. Nothing about those terminals will have
                changed until a version is set as their target.
              </p>
            </InfoNote>
          ) : (
            groups.map((group) => (
              <section
                className="panel panel--nested"
                key={group.key}
                aria-labelledby={`firmware-${group.key}`}
              >
                <div className="panel__header">
                  <h3 className="panel__title" id={`firmware-${group.key}`}>
                    {humaniseCode(group.deviceType)} · {humaniseCode(group.releaseChannel)}
                  </h3>
                  <p className="field__hint">
                    {/*
                      "Current" is scoped to this pair, and saying so here is
                      what stops somebody reading the badge as fleet-wide.
                    */}
                    One version is the target per device type and release channel.{' '}
                    {!fleet.known ? (
                      <>Terminal numbers are unavailable.</>
                    ) : group.terminalCount === 0 ? (
                      <>No terminal you can see is on this combination.</>
                    ) : (
                      <>
                        {group.terminalCount} terminal{group.terminalCount === 1 ? '' : 's'} you can
                        see {group.terminalCount === 1 ? 'is' : 'are'} on it
                        {group.current ? (
                          <>
                            , of which <strong>{group.onCurrent}</strong>{' '}
                            {group.onCurrent === 1 ? 'is' : 'are'} already running{' '}
                            <code className="mono">{group.current.version}</code>
                          </>
                        ) : null}
                        .
                      </>
                    )}
                  </p>
                </div>

                {!group.current ? (
                  <InfoNote title="No version is the target here">
                    Nothing is being offered to terminals on this combination, and they
                    all report as up to date because there is nothing to compare them
                    with. Setting a version as the target changes both.
                  </InfoNote>
                ) : null}

                <ul className="rule-list">
                  {group.versions.map((version) => (
                    <VersionRow
                      key={version.id}
                      version={version}
                      standing={standingOf(version, group.current)}
                      terminals={group.terminals}
                      fleet={fleet}
                      mayManage={mayManage}
                      onRetarget={() => onRetarget(version)}
                    />
                  ))}
                </ul>
              </section>
            ))
          )}

          {hasFindings ? null : (
            <p className="field__hint">
              Nothing above needs attention, so nothing in here is urgent.
            </p>
          )}
        </div>
      </details>
    </section>
  )
}

/**
 * A catalogue row, inside Advanced.
 *
 * The row says three separate things and keeps them separate: what the version
 * is, how it stands against the target, and whether the platform would actually
 * send it. The third is the one that turns "the update does not work" into a
 * line naming the field to fix.
 */
function VersionRow({
  version,
  standing,
  terminals,
  fleet,
  mayManage,
  onRetarget,
}: {
  version: FirmwareVersion
  standing: Standing
  /** The fleet on this device type and channel, for the "would reach" count. */
  terminals: Terminal[]
  fleet: FleetKnowledge
  mayManage: boolean
  onRetarget: () => void
}) {
  const offerability = firmwareOfferability(version)
  const wouldReach = fleet.known ? terminalsOffered(version, terminals).length : null
  const label = STANDING[standing]

  // Setting a target is refused while the console cannot say what it would do.
  // The dialog refuses too — see the note there — but a button that opens onto
  // a refusal is worse than one that explains itself in place.
  const settable = mayManage && standing !== 'CURRENT'

  return (
    <li className="rule" data-effect={version.is_current ? 'ALLOW' : undefined}>
      <div className="rule__main">
        <h4 className="rule__title">
          <code className="mono">{version.version}</code>
          {/*
            FOUR STATES, NOT TWO. Everything that was not the target used to
            wear one neutral badge, so a version published two months BEFORE the
            one in use and a version published six days AFTER it were
            indistinguishable — and the second is the entire reason somebody
            opens this page. "Cannot be installed" is orthogonal to all of them
            and stays as its own badge.
          */}
          <Badge tone={label.tone}>{label.text}</Badge>
          {version.is_mandatory ? <Badge tone="warning">Install sooner</Badge> : null}
          {offerability.deliverable ? null : <Badge tone="danger">Cannot be installed</Badge>}
        </h4>

        {version.release_notes ? (
          <p className="rule__detail">{version.release_notes}</p>
        ) : null}

        <p className="rule__detail">
          {/*
            BOTH TIMES. Relative reads at a glance; the date is what goes into a
            change record, and there is no hover to reveal it on a phone.
          */}
          Published <Timestamp value={version.published_at ?? version.created_at} relative />{' '}
          <span className="muted">
            · <Timestamp value={version.published_at ?? version.created_at} dateOnly />
          </span>
        </p>

        {version.is_mandatory ? (
          <p className="rule__detail">
            <strong>“Install sooner” changes when, not whether.</strong> A terminal
            treats it as a signal to apply the update promptly rather than at a quiet
            moment. Every other check it makes is unchanged.
          </p>
        ) : null}

        {standing === 'OLDER' ? (
          <p className="rule__detail">
            {/*
              THE SENTENCE THAT WAS MISSING, AND THE REASON A FLEET COULD BE PUT
              INTO A PERMANENT FALSE ALARM.

              A terminal refuses anything not strictly newer than what it runs,
              so this installs on nothing. Saying it on the row means somebody
              never reaches the confirmation expecting a rollback.
            */}
            <strong>Older than the version in use.</strong> Terminals refuse firmware
            older than what they already run, so setting this as the target installs
            nothing.
          </p>
        ) : null}

        {offerability.deliverable ? (
          <p className="rule__detail">
            {wouldReach === null ? (
              <>
                <strong>How many terminals this would reach is unavailable</strong>{' '}
                while the terminal list cannot be read.
              </>
            ) : version.is_current ? (
              <>
                <strong>
                  {wouldReach === 0
                    ? terminals.length === 0
                      ? 'No terminal you can see is on this combination.'
                      : 'Every terminal you can see on this combination is already running it.'
                    : `Being offered to ${wouldReach} terminal${wouldReach === 1 ? '' : 's'} you can see.`}
                </strong>{' '}
                {wouldReach === 0 ? null : 'A terminal takes the offer at its next check-in.'}
              </>
            ) : (
              <>
                Setting this as the target would offer it to{' '}
                <strong>
                  {wouldReach} terminal{wouldReach === 1 ? '' : 's'}
                </strong>{' '}
                you can see.
              </>
            )}
          </p>
        ) : (
          <div className="rule__detail">
            <p>
              <strong>The platform will not send this version to anything</strong>,
              whether or not it is the target:
            </p>
            <ul>
              {offerability.problems.map((problem) => (
                <li key={problem}>{problem}</li>
              ))}
            </ul>
            <p>
              A published version cannot be edited. Publish a corrected one and set that
              as the target.
            </p>
          </div>
        )}

        {/*
          THE VERIFICATION MATERIAL, FOLDED AWAY AND NOT DELETED.

          A 64-character digest and a 127-character address were the visual bulk
          of every row, and neither is read by eye — a digest is copied and
          compared, and the address is fetched by a terminal rather than by
          anybody here. Behind a disclosure they cost nothing and are one press
          away. The same `technical` disclosure the terminal page uses.
        */}
        {version.checksum_sha256 || version.download_url || version.size_bytes ? (
          <details className="technical">
            <summary>Image details</summary>
            <dl className="detail-list">
              {version.size_bytes ? (
                <div className="detail-list__row">
                  <dt>Size</dt>
                  <dd>{formatBytes(version.size_bytes)}</dd>
                </div>
              ) : null}
              {version.checksum_sha256 ? (
                <div className="detail-list__row">
                  <dt>SHA-256 checksum</dt>
                  <dd>
                    <code className="mono rule__code">{version.checksum_sha256}</code>
                  </dd>
                </div>
              ) : null}
              {/*
                Shown as text and never as a link. Terminals fetch this; an
                operator should not, and offering it as something to click
                invites somebody to download a firmware image into their browser
                by accident.
              */}
              {version.download_url ? (
                <div className="detail-list__row">
                  <dt>Terminals download it from</dt>
                  <dd>
                    <code className="mono rule__code">{version.download_url}</code>
                  </dd>
                </div>
              ) : null}
            </dl>
          </details>
        ) : null}
      </div>

      {settable ? (
        <div className="rule__actions">
          {/*
            NEVER `button--danger` IN HERE, and never the primary action.

            The old screen put a red button on every non-current row — including
            downgrades and versions that would reach nobody — so three identical
            destructive controls competed for one decision. The genuine update
            is a primary button in its own card above; this is the deliberate
            override, and it is styled as one.
          */}
          <button type="button" className="button" onClick={onRetarget} disabled={!fleet.known}>
            Set as target version
          </button>
          {fleet.known ? null : (
            <p className="field__hint">
              {fleet.reason === 'unavailable'
                ? 'Unavailable while terminal numbers cannot be read.'
                : 'Available once terminal numbers load.'}
            </p>
          )}
        </div>
      ) : null}
    </li>
  )
}

const STANDING: Record<Standing, { text: string; tone: 'positive' | 'info' | 'neutral' }> = {
  CURRENT: { text: 'In use now', tone: 'positive' },
  NEWER: { text: 'Update available', tone: 'info' },
  OLDER: { text: 'Older than the version in use', tone: 'neutral' },
  // No target in this group, so there is nothing to be newer or older than.
  // Saying "older" here would be inventing a comparison.
  UNCOMPARED: { text: 'Not in use', tone: 'neutral' },
}

/** Bytes as something readable. Decimal units, as storage is sold in. */
function formatBytes(bytes: number): string {
  if (bytes < 1000) return `${bytes} B`
  if (bytes < 1000 * 1000) return `${(bytes / 1000).toFixed(0)} kB`
  return `${(bytes / (1000 * 1000)).toFixed(1)} MB`
}

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

interface PublishValues extends Record<string, unknown> {
  version: string
  device_type: string
  release_channel: string
  download_url: string
  checksum_sha256: string
  size_bytes: string
  release_notes: string
  is_mandatory: boolean
}

/**
 * Adding a version to the catalogue.
 *
 * PUBLISHING DOES NOT START AN UPDATE. Two decisions, two calls, on the server
 * as well as here: recording that a version exists and deciding a fleet should
 * install it are different, and collapsing them would mean every upload
 * silently began updating hardware. UNCHANGED by the move into Advanced — the
 * form, its validation, its hints and its ADMIN permission are exactly as they
 * were; only where the button lives has changed.
 *
 * THE THREE DELIVERY FIELDS ARE ASKED FOR AS THOUGH THEY WERE REQUIRED, because
 * in practice they are. The API accepts a version without a digest, a size or an
 * address; the platform then withholds every offer for it, so setting it as the
 * target would move the target and update nothing. They are validated as
 * optional — a catalogue entry for a version distributed some other way is
 * legitimate — and the form says plainly what omitting them costs.
 */
function PublishFirmwareDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const publish = useCreateFirmware()
  const notifications = useNotifications()
  const formId = useId()

  const form = useForm<PublishValues>({
    initialValues: {
      version: '',
      device_type: 'TERMINAL',
      release_channel: 'STABLE',
      download_url: '',
      checksum_sha256: '',
      size_bytes: '',
      release_notes: '',
      is_mandatory: false,
    },
    validate: (values) => ({
      version: validators.required(values.version, 'A version'),
      device_type: validators.required(values.device_type, 'A device type'),
      release_channel: validators.required(values.release_channel, 'A release channel'),
      // LOWER CASE, matching the server and the device exactly. Accepting an
      // upper-case digest here would store one that never matches, and the
      // symptom would be an update that fails verification after every download.
      checksum_sha256:
        values.checksum_sha256.trim() && !/^[0-9a-f]{64}$/.test(values.checksum_sha256.trim())
          ? 'A SHA-256 is 64 lower-case hexadecimal characters. The platform and the terminal compare it exactly.'
          : undefined,
      download_url:
        values.download_url.trim() && !/^https:\/\//i.test(values.download_url.trim())
          ? 'The download address must be https. A terminal refuses a plaintext download.'
          : undefined,
      size_bytes: sizeError(values.size_bytes),
    }),
    onSubmit: async (values) => {
      const size = values.size_bytes.trim()
      const created = await publish.mutateAsync({
        version: values.version.trim(),
        device_type: values.device_type.trim(),
        release_channel: values.release_channel.trim(),
        download_url: values.download_url.trim() || undefined,
        checksum_sha256: values.checksum_sha256.trim() || undefined,
        size_bytes: size ? Number(size) : undefined,
        release_notes: values.release_notes.trim() || undefined,
        is_mandatory: values.is_mandatory,
      })
      notifications.success(
        `${created.version} recorded. Nothing has been sent to any terminal — it is not the target until you set it as one.`,
      )
      onClose()
    },
  })

  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

  return (
    <Dialog
      open={open}
      title="Publish a version"
      description="Records that a firmware version exists. It does not become the target, and no terminal is offered it."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
      /*
        THE ACTIONS SIT IN THE DIALOG'S FOOTER RATHER THAN AT THE END OF THE FORM.

        `.dialog__body` is the scrolling box and `.dialog__footer` is its sibling,
        so anything in the footer stays put. These buttons were the last thing
        inside the form, which is inside the body — measured at 1440x900 the
        submit was 400px BELOW the fold on open, and 680px below at 390. The form
        is seven fields with a hint apiece; that length is the point of the form
        and is not the thing to cut.

        The submit keeps working from out here through `form=`, which is what
        that attribute is for, and Enter still submits from any field.
      */
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={form.submitting}
          >
            Cancel
          </button>
          <button
            type="submit"
            form={formId}
            className="button button--primary"
            disabled={form.submitting}
          >
            {form.submitting ? 'Publishing…' : 'Publish version'}
          </button>
        </>
      }
    >
      <form
        id={formId}
        className="form"
        onSubmit={(event) => void form.handleSubmit(event)}
        noValidate
      >
        <TextField
          label="Version"
          required
          mono
          value={form.values.version}
          error={form.errors.version}
          onChange={(value) => form.setValue('version', value)}
          onBlur={() => form.touch('version')}
          disabled={form.submitting}
          hint="Exactly as the firmware reports itself. A terminal is offered this version when the string differs from what it runs, so a mismatch in formatting reads as a whole fleet being behind — and would offer every one of them an update."
        />

        <TextField
          label="Device type"
          required
          value={form.values.device_type}
          error={form.errors.device_type}
          onChange={(value) => form.setValue('device_type', value)}
          onBlur={() => form.touch('device_type')}
          disabled={form.submitting}
          hint="Must match what the terminals report. Terminals of another type are never offered this version."
        />

        <TextField
          label="Release channel"
          required
          value={form.values.release_channel}
          error={form.errors.release_channel}
          onChange={(value) => form.setValue('release_channel', value)}
          onBlur={() => form.touch('release_channel')}
          disabled={form.submitting}
          hint="Terminals are only ever offered versions on their own channel."
        />

        <TextField
          label="Download address"
          value={form.values.download_url}
          error={form.errors.download_url}
          onChange={(value) => form.setValue('download_url', value)}
          onBlur={() => form.touch('download_url')}
          disabled={form.submitting}
          hint="Where the terminal fetches the image from. Must be https and at most 127 characters. Without it the platform never offers the version."
        />

        <TextField
          label="SHA-256 checksum"
          mono
          value={form.values.checksum_sha256}
          error={form.errors.checksum_sha256}
          onChange={(value) => form.setValue('checksum_sha256', value)}
          onBlur={() => form.touch('checksum_sha256')}
          disabled={form.submitting}
          hint="64 lower-case hexadecimal characters. The terminal verifies the downloaded image against it and refuses an update without one."
        />

        <TextField
          label="File size in bytes"
          type="number"
          value={form.values.size_bytes}
          error={form.errors.size_bytes}
          onChange={(value) => form.setValue('size_bytes', value)}
          onBlur={() => form.touch('size_bytes')}
          disabled={form.submitting}
          hint="The terminal uses it to size the flash write, and trusts it over whatever the download server claims. Without it the platform never offers the version."
        />

        <TextField
          label="Release notes"
          multiline
          rows={4}
          value={form.values.release_notes}
          onChange={(value) => form.setValue('release_notes', value)}
          disabled={form.submitting}
          hint="Optional. Shown to the customer beside the update, so plain language is worth more here than a changelog."
        />

        <CheckboxField
          label="Mark as install sooner"
          checked={form.values.is_mandatory}
          onChange={(checked) => form.setValue('is_mandatory', checked)}
          disabled={form.submitting}
          hint="Changes WHEN a terminal applies the update, not whether. It still verifies the digest and can still refuse — a mandatory version is not a trusted one."
        />

        <InfoNote title="This does not update anything yet">
          A published version sits in the catalogue until somebody sets it as the
          target. That is the separate, deliberate decision that starts terminals
          downloading it.
        </InfoNote>

        <FormError
          message={
            conflict
              ? 'That version already exists for this device type. Versions are unique per device type, so either it is already recorded or the version string needs to differ.'
              : submitErrorMessage(error)
          }
          requestId={error instanceof ApiError ? error.requestId : null}
        />

      </form>
    </Dialog>
  )
}

/** A size is optional, and a nonsense one is refused before the server sees it. */
function sizeError(raw: string): string | undefined {
  const trimmed = raw.trim()
  if (trimmed === '') return undefined
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed <= 0) {
    return 'A file size is a whole number of bytes, greater than zero.'
  }
  return undefined
}

// ---------------------------------------------------------------------------
// Setting the target
// ---------------------------------------------------------------------------

/**
 * Moving the target — which, for a newer version, is to say starting an update.
 *
 * THE CONFIRMATION IS THE SAFETY CONTROL, so it does four things rather than
 * ask a question:
 *
 *   - it states, first, what will actually happen — and that is now THREE
 *     different statements rather than one, because there are three cases;
 *   - it counts the terminals affected, using the server's own narrowing;
 *   - it names the version being replaced, because the target is a single slot
 *     and the previous occupant leaves it silently;
 *   - it requires the version to be TYPED when, and only when, terminals would
 *     actually install something.
 *
 * ---------------------------------------------------------------------------
 * THE THREE CASES, AND WHY THEY MUST NOT SHARE COPY
 * ---------------------------------------------------------------------------
 *
 *   NEWER      terminals download, install and restart. The dangerous one, and
 *              the only one that asks for a typed version.
 *
 *   OLDER      NOTHING INSTALLS, EVER. A terminal refuses anything not strictly
 *              newer than what it runs (`firmware_update.cpp`), so the offer is
 *              made and declined for ever. What DOES happen is that
 *              `firmware_outdated` — an exact string mismatch — flips true for
 *              the whole fleet, so every terminal reports as needing an update
 *              it will never accept until a newer version is set.
 *
 *              This dialog previously showed the NEWER copy for this case, word
 *              for word: "3 terminals will be offered it… downloads the image,
 *              writes it to flash and reboots". Two clicks and a typed string
 *              put an entire fleet into a permanent false alarm under a promise
 *              of the opposite.
 *
 *   UNDELIVERABLE  the platform will not send it at all. Confirmed as what it
 *              is: a change of what the fleet is reported against.
 *
 * AN OLDER OR UNDELIVERABLE VERSION IS STILL SETTABLE. Refusing would be the
 * console inventing a rule the platform does not have, and there are legitimate
 * reasons to move the target — pinning a fleet before a staged rollout, or a
 * version distributed some other way. It is confirmed as what it is instead.
 */
function SetTargetDialog({
  open,
  version,
  previous,
  standing,
  terminals,
  fleet,
  onClose,
}: {
  open: boolean
  version: FirmwareVersion
  previous: FirmwareVersion | null
  /** How it compares to the target it would replace. */
  standing: Standing
  /** The fleet on this device type and channel, as the operator can see it. */
  terminals: Terminal[]
  fleet: FleetKnowledge
  onClose: () => void
}) {
  const retarget = useSetCurrentFirmware()
  const notifications = useNotifications()

  const offerability = firmwareOfferability(version)
  const older = standing === 'OLDER'

  /*
    NULL MEANS "NOT KNOWN", AND IT IS NEVER TREATED AS ZERO.

    `affected` was once a number derived from a possibly-empty list, so a failed
    fleet request produced 0 — and 0 drove three things wrong at once: the dialog
    claimed they were all already running it, `wouldInstall` went false, and the
    typed-phrase safeguard removed itself.
  */
  const affected = fleet.known ? terminalsOffered(version, terminals).length : null

  // Hardware changes only when the version is newer AND deliverable AND there
  // is somebody to send it to. An older version fails the first test, which is
  // the whole of this fix.
  const wouldInstall = offerability.deliverable && !older && affected !== null && affected > 0

  /*
    THE SECOND LOCK. The list already disables the control that opens this, so
    this state should be unreachable through the UI. It is enforced here anyway
    because this dialog is the last thing between an operator and a fleet-wide
    flash write, and a safeguard that depends on every caller getting the guard
    right is not a safeguard.

    NOT BLOCKED for an older or undeliverable version: neither installs anything,
    so an unknown fleet size cannot make either more dangerous than it is.
  */
  const blocked =
    fleet.known || !offerability.deliverable || older ? undefined : (
      <p>
        {fleet.reason === 'unavailable'
          ? 'The list of terminals could not be loaded, so nobody can say how many terminals this would update.'
          : 'The list of terminals is still loading, so how many terminals this would update is not yet known.'}{' '}
        This version <strong>can</strong> be installed, so setting it as the target
        could start an update — and starting one without knowing its reach is not a
        decision this screen will let you take. Close this, let the terminal numbers
        load, and try again.
      </p>
    )

  const where = `${humaniseCode(version.device_type)} · ${humaniseCode(version.release_channel)}`

  return (
    <ConfirmDialog
      open={open}
      tone={wouldInstall ? 'danger' : 'default'}
      title={`Set ${version.version} as the target version for ${where}?`}
      consequence={
        !offerability.deliverable ? (
          <>
            <strong>Nothing will be installed.</strong> The platform will not send this
            version to any terminal, so setting it as the target changes only what the
            fleet is reported against.
          </>
        ) : older ? (
          <>
            {/*
              THE CASE THIS DIALOG USED TO DESCRIBE AS AN UPDATE.
            */}
            <strong>Nothing will be installed.</strong> This version is older than the
            one your terminals are pointed at, and{' '}
            <strong>a terminal refuses firmware older than what it already runs</strong> —
            so every terminal will decline the offer and keep running what it has.
          </>
        ) : affected === null ? (
          <>
            <strong>This can update terminals.</strong> How many it would reach is{' '}
            <strong>not known</strong> — the terminal list could not be read. Every
            terminal on this hardware type and channel that reports a different version
            would be offered it at its next check-in, and a terminal that takes the
            offer installs it and restarts once.
          </>
        ) : affected === 0 ? (
          <>
            {/*
              "NOBODY IS HERE" AND "EVERYBODY ALREADY HAS IT" ARE DIFFERENT
              FACTS, and this once printed the second for both — telling somebody
              that an empty combination was "all already running" the version.
            */}
            <strong>Nothing will be installed right now.</strong>{' '}
            {terminals.length === 0
              ? 'No terminal you can see is on this hardware type and channel at all.'
              : `Every terminal you can see on this combination is already running ${version.version}.`}{' '}
            Any terminal on this combination that later reports a different version will
            be offered it at its next check-in.
          </>
        ) : (
          <>
            <strong>This can update terminals.</strong> {affected} terminal
            {affected === 1 ? '' : 's'} you can see {affected === 1 ? 'is' : 'are'} not
            running <code className="mono">{version.version}</code> and will be offered
            it at {affected === 1 ? 'its' : 'their'} next check-in. A terminal that takes
            the offer <strong>installs it and restarts once</strong>.
          </>
        )
      }
      detail={
        <>
          {offerability.deliverable ? null : (
            <>
              It cannot be installed because: {offerability.problems.join(' ')} Publish a
              corrected version and set that as the target instead.{' '}
            </>
          )}
          {older && offerability.deliverable ? (
            <>
              {/*
                WHAT DOES CHANGE, AND WHY IT IS WORTH KNOWING. Nothing installs,
                but the whole fleet starts reporting as needing an update — and
                cannot stop until a newer version is set.
              */}
              What does change is the report:{' '}
              {affected === null ? (
                <>every terminal not already running it</>
              ) : (
                <>
                  <strong>
                    {affected} terminal{affected === 1 ? '' : 's'}
                  </strong>{' '}
                  you can see
                </>
              )}{' '}
              will show as <strong>Update available</strong> against{' '}
              <code className="mono">{version.version}</code> — a state{' '}
              {affected === 1 ? 'it' : 'they'} cannot leave until a newer version is set
              as the target.{' '}
            </>
          ) : null}
          {previous ? (
            <>
              <code className="mono">{previous.version}</code> stops being the target —
              there is one per device type and channel, and this replaces it. A terminal
              already running it is not rolled back.{' '}
            </>
          ) : null}
          Terminals on other device types or other channels are unaffected.
          {wouldInstall ? (
            <>
              {' '}
              There is no undo: setting the previous version as the target again stops
              further offers, but a terminal that has already installed this one has
              already installed it.
            </>
          ) : null}
        </>
      }
      // Typed only when hardware would actually change — which an older version
      // never does. Asking for it where nothing installs is how operators learn
      // to type phrases without reading them.
      confirmPhrase={wouldInstall ? version.version : undefined}
      blocked={blocked}
      confirmLabel={wouldInstall ? 'Start the update' : 'Set as target version'}
      onConfirm={async () => {
        await retarget.mutateAsync(version.id)
        notifications.success(
          wouldInstall
            ? `${version.version} is now the target for ${where}. Terminals not running it will be offered it at their next check-in.`
            : older && offerability.deliverable
              ? `${version.version} is now the target for ${where}. It is older than what your terminals run, so none of them will install it.`
              : `${version.version} is now the target for ${where}. No terminal will change.`,
        )
      }}
      onClose={onClose}
    />
  )
}
