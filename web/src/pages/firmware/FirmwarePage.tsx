import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import { ApiError } from '../../api/client'
import type { FirmwareVersion, Terminal } from '../../api/types'
import { can } from '../../auth/permissions'
import { Badge, humaniseCode } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { CheckboxField, FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCreateFirmware, useFirmware, useSetCurrentFirmware, useTerminals } from '../../data/console'
import { useSession } from '../../session/useSession'
import { firmwareOfferability, terminalsOffered } from './offerability'

/**
 * The firmware catalogue.
 *
 * WHAT MARKING A BUILD "CURRENT" ACTUALLY DOES, because it is the one thing an
 * operator will assume wrongly and the assumption is now expensive in the other
 * direction:
 *
 *   IT STARTS A ROLLOUT. Every terminal of that device type on that release
 *   channel is offered the build on its next heartbeat, and a terminal that
 *   takes an offer downloads the image, verifies its digest, writes it to flash
 *   and reboots into a trial boot that rolls itself back only if the new
 *   firmware cannot reach its sensor or the platform.
 *
 * THIS PAGE PREVIOUSLY SAID THE OPPOSITE. It said AccessLink had no
 * over-the-air update and that promoting a build changed nothing but a report —
 * which was true when it was written and stopped being true when the heartbeat
 * began carrying `firmware_update`. A screen that tells an operator a dangerous
 * action is safe is worse than one that says nothing, so the sentence is now the
 * warning it should be, and it appears on the page, in the publish dialog and in
 * the confirmation rather than once at the top where it can be scrolled past.
 *
 * WHAT THE CONSOLE ADDS THAT THE API DOES NOT. The server refuses to offer a
 * build it could not populate — no digest, no size, a plaintext URL, a string
 * longer than the device's buffer — and logs the reason server-side where no
 * operator will ever see it. So each row here states whether the platform would
 * actually offer it, and promotion of an undeliverable build is confirmed as
 * what it is: a change of target that will update nothing. See `offerability.ts`.
 *
 * WHY THIS SCREEN IS ADMIN. These routes used to live in the site-key tree,
 * where any provisioning secret — a value that lives on hardware bolted to a
 * wall — could add a build and move the deployment target. That was a leaked
 * door credential with control of the firmware target; now that the target
 * drives an actual rollout, it would have been a leaked door credential with
 * control of what every terminal runs.
 *
 * THE STRUCTURE MIRRORS THE MODEL. "Current" is per company, device type AND
 * release channel: promoting a build offers it to that pair and nothing else.
 * Grouping the catalogue that way is what makes "make current" unambiguous — a
 * flat list invites somebody to believe there is one current build for
 * everything.
 */
export function FirmwarePage() {
  const { session } = useSession()
  const firmware = useFirmware()
  // The fleet, so each group can say what it is actually describing AND how many
  // terminals a promotion would reach. Scoped by the operator's grants exactly
  // as the Terminals page is, which is stated rather than left to be inferred
  // from a number that looks company-wide.
  const terminals = useTerminals()

  const [publishing, setPublishing] = useState(false)
  const [promoting, setPromoting] = useState<FirmwareVersion | null>(null)

  const mayManage = can(session, 'manageFirmware')

  const groups = useMemo(
    () => groupByTarget(firmware.data?.firmware_versions ?? [], terminals.data?.terminals ?? []),
    [firmware.data, terminals.data],
  )

  if (firmware.isPending) return <LoadingState label="Loading firmware…" />

  if (firmware.isError) {
    return (
      <div className="page">
        <PageHeader title="Firmware" />
        <ErrorState error={firmware.error} onRetry={() => void firmware.refetch()} />
      </div>
    )
  }

  const promotingGroup = promoting
    ? (groups.find((group) => group.key === targetKey(promoting)) ?? null)
    : null

  return (
    <div className="page">
      <PageHeader
        title="Firmware"
        lead="The builds this company knows about, and which one each part of the fleet is being offered."
        actions={
          mayManage ? (
            <button
              type="button"
              className="button button--primary"
              onClick={() => setPublishing(true)}
            >
              Publish a build
            </button>
          ) : null
        }
      />

      {/*
        THE LOAD-BEARING SENTENCE. Every other honest thing on this page is a
        restatement of it, and it is the exact opposite of what this notice said
        before over-the-air updates existed.
      */}
      <InfoNote tone="warning" title="Making a build current updates terminals">
        <p>
          AccessLink <strong>does</strong> update terminals over the air. Marking a
          build current offers it to every terminal of that device type on that
          release channel, on its next heartbeat — and a terminal that takes the
          offer <strong>downloads it, writes it to flash and reboots</strong>,
          without anybody visiting the site.
        </p>
        <p>
          Publishing a build only records that it exists; nothing is offered until
          it is made current. That second step is the one to be careful with, and
          it is confirmed separately. Which terminals are behind is on{' '}
          <Link to="/terminals">Terminals</Link>.
        </p>
      </InfoNote>

      {groups.length === 0 ? (
        <InfoNote title="No builds recorded">
          <p>
            Nothing has been published, so every terminal reports as up to date —
            not because it is, but because there is nothing to compare it with, and
            nothing is being offered to anything.
          </p>
          <p>
            Publishing the first build for a device type and channel is what makes
            “outdated” mean anything, and it may mark terminals outdated the moment
            it lands. Nothing about those terminals will have changed until a build
            is made current.
          </p>
        </InfoNote>
      ) : (
        groups.map((group) => (
          <section
            className="panel"
            key={group.key}
            aria-labelledby={`firmware-${group.key}`}
          >
            <div className="panel__header">
              <h2 className="panel__title" id={`firmware-${group.key}`}>
                {humaniseCode(group.deviceType)} · {humaniseCode(group.releaseChannel)}
              </h2>
              <p className="field__hint">
                {/*
                  "Current" is scoped to this pair, and saying so here is what
                  stops somebody reading the badge as fleet-wide.
                */}
                One build is current per device type and release channel.{' '}
                {group.terminalCount === 0 ? (
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
              <InfoNote title="No current build for this combination">
                Nothing is being offered to terminals here, and they all report as up
                to date because there is nothing to compare them with. Making a build
                current changes both.
              </InfoNote>
            ) : null}

            <ul className="rule-list">
              {group.versions.map((version) => (
                <VersionRow
                  key={version.id}
                  version={version}
                  terminals={group.terminals}
                  mayManage={mayManage}
                  onPromote={() => setPromoting(version)}
                />
              ))}
            </ul>
          </section>
        ))
      )}

      {/*
        THERE IS NO READ-ONLY STATE HERE, and its absence is deliberate rather
        than an omission. Unlike Applications — which any operator may read and
        only an OWNER may change — the catalogue's READ is ADMIN too
        (`admin.GET("/firmware")`), so anybody who can open this page can also
        publish and promote. The `mayManage` guards are kept as defence in depth
        and as documentation of the role, but a "you can look but not touch"
        notice would describe a state no operator can be in.
      */}

      {publishing ? (
        <PublishFirmwareDialog open onClose={() => setPublishing(false)} />
      ) : null}
      {promoting ? (
        <MakeCurrentDialog
          open
          version={promoting}
          previous={promotingGroup?.current ?? null}
          terminals={promotingGroup?.terminals ?? []}
          onClose={() => setPromoting(null)}
        />
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// One build
// ---------------------------------------------------------------------------

/**
 * A catalogue row.
 *
 * The row says three separate things and keeps them separate: what the build is,
 * whether it is the target, and whether the platform would actually send it. The
 * third is the one that did not exist before and is the one that turns "OTA does
 * not work" into a line naming the field to fix.
 */
function VersionRow({
  version,
  terminals,
  mayManage,
  onPromote,
}: {
  version: FirmwareVersion
  /** The fleet on this device type and channel, for the "would reach" count. */
  terminals: Terminal[]
  mayManage: boolean
  onPromote: () => void
}) {
  const offerability = firmwareOfferability(version)
  const wouldReach = terminalsOffered(version, terminals).length

  return (
    <li className="rule" data-effect={version.is_current ? 'ALLOW' : undefined}>
      <div className="rule__main">
        <h3 className="rule__title">
          <code className="mono">{version.version}</code>
          {version.is_current ? (
            <Badge tone="positive">Current target</Badge>
          ) : (
            <Badge>Recorded</Badge>
          )}
          {version.is_mandatory ? <Badge tone="warning">Mandatory</Badge> : null}
          {offerability.deliverable ? null : <Badge tone="danger">Cannot be sent</Badge>}
        </h3>

        {version.release_notes ? (
          <p className="rule__detail">{version.release_notes}</p>
        ) : null}

        <p className="rule__detail">
          Published{' '}
          <Timestamp value={version.published_at ?? version.created_at} relative />
          {version.size_bytes ? <> · {formatBytes(version.size_bytes)}</> : null}
        </p>

        {version.checksum_sha256 ? (
          <p className="rule__detail">
            Checksum <code className="mono">{version.checksum_sha256}</code>
          </p>
        ) : null}

        {/*
          Shown as text and never as a link. Terminals fetch this; an operator
          should not, and offering it as something to click invites somebody to
          download a firmware image into their browser by accident.
        */}
        {version.download_url ? (
          <p className="rule__detail">
            Terminals download it from <code className="mono">{version.download_url}</code>
          </p>
        ) : null}

        {version.is_mandatory ? (
          <p className="rule__detail">
            <strong>“Mandatory” changes when, not whether.</strong> A terminal treats
            it as a signal to apply the update sooner rather than at a quiet moment.
            Every other check it makes is unchanged.
          </p>
        ) : null}

        {/*
          THE PART THE SERVER KNOWS AND NOBODY COULD SEE. An undeliverable build
          can be published and promoted and will silently never be sent; the
          reason is logged where only an operator with server access could read
          it.
        */}
        {offerability.deliverable ? (
          version.is_current ? (
            <p className="rule__detail">
              <strong>
                {wouldReach === 0
                  ? 'Every terminal you can see on this combination is already running it.'
                  : `Being offered to ${wouldReach} terminal${wouldReach === 1 ? '' : 's'} you can see.`}
              </strong>{' '}
              A terminal takes the offer on its next heartbeat.
            </p>
          ) : (
            <p className="rule__detail">
              Making this current would offer it to{' '}
              <strong>
                {wouldReach} terminal{wouldReach === 1 ? '' : 's'}
              </strong>{' '}
              you can see.
            </p>
          )
        ) : (
          <div className="rule__detail">
            <p>
              <strong>The platform will not send this build to anything</strong>, whether
              or not it is the current target:
            </p>
            <ul>
              {offerability.problems.map((problem) => (
                <li key={problem}>{problem}</li>
              ))}
            </ul>
            <p>
              A published build cannot be edited. Publish a corrected entry and make
              that one current.
            </p>
          </div>
        )}
      </div>

      {mayManage && !version.is_current ? (
        <button
          type="button"
          className={offerability.deliverable ? 'button button--danger' : 'button'}
          onClick={onPromote}
        >
          Make current
        </button>
      ) : null}
    </li>
  )
}

// ---------------------------------------------------------------------------
// Grouping
// ---------------------------------------------------------------------------

interface TargetGroup {
  key: string
  deviceType: string
  releaseChannel: string
  versions: FirmwareVersion[]
  current: FirmwareVersion | null
  /** Terminals the OPERATOR CAN SEE on this combination. */
  terminals: Terminal[]
  terminalCount: number
  /** How many of those are already running the current build. */
  onCurrent: number
}

function targetKey(version: { device_type: string; release_channel: string }): string {
  return `${version.device_type}--${version.release_channel}`
}

/**
 * Groups builds by the pair that "current" is actually scoped to.
 *
 * The terminal counts come from the fleet list the console already holds, and
 * are therefore narrowed by the operator's site grants — which the page states,
 * because a number that looks company-wide and is not is worse than no number.
 *
 * Exported for the tests: the grouping is where an off-by-one in "how many are
 * already on this build" would hide, and that number is now the difference
 * between an update that reaches nobody and one that reaches a fleet.
 */
export function groupByTarget(
  versions: FirmwareVersion[],
  terminals: Terminal[],
): TargetGroup[] {
  const groups = new Map<string, TargetGroup>()

  for (const version of versions) {
    const key = targetKey(version)
    const group = groups.get(key) ?? {
      key,
      deviceType: version.device_type,
      releaseChannel: version.release_channel,
      versions: [],
      current: null,
      terminals: [],
      terminalCount: 0,
      onCurrent: 0,
    }
    group.versions.push(version)
    if (version.is_current) group.current = version
    groups.set(key, group)
  }

  for (const group of groups.values()) {
    const matching = terminals.filter(
      (terminal) =>
        terminal.device_type === group.deviceType &&
        terminal.release_channel === group.releaseChannel,
    )
    group.terminals = matching
    group.terminalCount = matching.length
    group.onCurrent = group.current
      ? matching.filter((terminal) => terminal.firmware_version === group.current?.version).length
      : 0

    // Newest first, with the current build pinned to the top: it is the one the
    // page is about, and hunting for a badge in a version-sorted list is work
    // the screen can do instead.
    group.versions.sort((a, b) => {
      if (a.is_current !== b.is_current) return a.is_current ? -1 : 1
      return (b.published_at ?? b.created_at).localeCompare(a.published_at ?? a.created_at)
    })
  }

  return [...groups.values()].sort((a, b) => a.key.localeCompare(b.key))
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
 * Adding a build to the catalogue.
 *
 * PUBLISHING DOES NOT START A ROLLOUT. Two decisions, two calls, on the server
 * as well as here: recording that a build exists and deciding a fleet should
 * install it are different, and collapsing them would mean every upload silently
 * began updating hardware.
 *
 * THE THREE DELIVERY FIELDS ARE ASKED FOR AS THOUGH THEY WERE REQUIRED, because
 * in practice they are. The API accepts a build without a digest, a size or a
 * URL; the platform then withholds every offer for it, so promoting it would
 * move the target and update nothing. They are validated as optional — a
 * catalogue entry for a build distributed some other way is legitimate — and the
 * form says plainly what omitting them costs.
 */
function PublishFirmwareDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const publish = useCreateFirmware()
  const notifications = useNotifications()

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
      // symptom would be a rollout that fails verification after every download.
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
        `${created.version} recorded. Nothing has been sent to any terminal — it is not the current target until you make it one.`,
      )
      onClose()
    },
  })

  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

  return (
    <Dialog
      open={open}
      title="Publish a build"
      description="Records that a build exists. It does not become the deployment target, and no terminal is offered it."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Version"
          required
          mono
          value={form.values.version}
          error={form.errors.version}
          onChange={(value) => form.setValue('version', value)}
          onBlur={() => form.touch('version')}
          disabled={form.submitting}
          hint="Exactly as the firmware reports itself. A terminal is offered this build when the string differs from what it runs, so a mismatch in formatting reads as a whole fleet being behind — and would offer every one of them an update."
        />

        <TextField
          label="Device type"
          required
          value={form.values.device_type}
          error={form.errors.device_type}
          onChange={(value) => form.setValue('device_type', value)}
          onBlur={() => form.touch('device_type')}
          disabled={form.submitting}
          hint="Must match what the terminals report. Terminals of another type are never offered this build."
        />

        <TextField
          label="Release channel"
          required
          value={form.values.release_channel}
          error={form.errors.release_channel}
          onChange={(value) => form.setValue('release_channel', value)}
          onBlur={() => form.touch('release_channel')}
          disabled={form.submitting}
          hint="Terminals are only ever offered builds on their own channel."
        />

        <TextField
          label="Download address"
          value={form.values.download_url}
          error={form.errors.download_url}
          onChange={(value) => form.setValue('download_url', value)}
          onBlur={() => form.touch('download_url')}
          disabled={form.submitting}
          hint="Where the terminal fetches the image from. Must be https and at most 127 characters. Without it the platform never offers the build."
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
          hint="The terminal uses it to size the flash write, and trusts it over whatever the download server claims. Without it the platform never offers the build."
        />

        <TextField
          label="Release notes"
          multiline
          rows={4}
          value={form.values.release_notes}
          onChange={(value) => form.setValue('release_notes', value)}
          disabled={form.submitting}
          hint="Optional."
        />

        <CheckboxField
          label="Mark as mandatory"
          checked={form.values.is_mandatory}
          onChange={(checked) => form.setValue('is_mandatory', checked)}
          disabled={form.submitting}
          hint="Changes WHEN a terminal applies the update, not whether. It still verifies the digest and can still refuse — a mandatory build is not a trusted one."
        />

        <InfoNote title="This does not update anything yet">
          A published build sits in the catalogue until somebody makes it current.
          That is the separate, deliberate decision that starts terminals
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

        <FormActions>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={form.submitting}
          >
            Cancel
          </button>
          <button type="submit" className="button button--primary" disabled={form.submitting}>
            {form.submitting ? 'Publishing…' : 'Publish build'}
          </button>
        </FormActions>
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
// Make current
// ---------------------------------------------------------------------------

/**
 * Moving the deployment target — which is to say, starting a rollout.
 *
 * THE CONFIRMATION IS THE SAFETY CONTROL, so it does four things rather than
 * ask a question:
 *
 *   - it states, first, that terminals will download and install the build;
 *   - it counts the terminals that would be offered it, using the server's own
 *     narrowing (same device type, same channel, not already running it);
 *   - it names the build being demoted, because "current" is a single slot and
 *     the previous occupant leaves it silently;
 *   - it requires the version to be TYPED when a rollout would actually start.
 *
 * The typed phrase is deliberately conditional. Asking for it when nothing would
 * be sent — an undeliverable build, or a fleet already on this version — trains
 * operators to type it without reading, which costs the protection exactly when
 * it matters. It appears when, and only when, hardware is about to change.
 *
 * AN UNDELIVERABLE BUILD IS STILL PROMOTABLE. Refusing here would be the console
 * inventing a rule the platform does not have, and there are legitimate reasons
 * to move the target for a build distributed some other way. It is confirmed as
 * what it is instead: a change of report that will update nothing.
 */
function MakeCurrentDialog({
  open,
  version,
  previous,
  terminals,
  onClose,
}: {
  open: boolean
  version: FirmwareVersion
  previous: FirmwareVersion | null
  /** The fleet on this device type and channel, as the operator can see it. */
  terminals: Terminal[]
  onClose: () => void
}) {
  const promote = useSetCurrentFirmware()
  const notifications = useNotifications()

  const offerability = firmwareOfferability(version)
  const affected = terminalsOffered(version, terminals).length
  const wouldUpdate = offerability.deliverable && affected > 0

  return (
    <ConfirmDialog
      open={open}
      tone={offerability.deliverable ? 'danger' : 'default'}
      title={`Make ${version.version} the current build for ${humaniseCode(version.device_type)} · ${humaniseCode(version.release_channel)}?`}
      consequence={
        offerability.deliverable ? (
          <>
            <strong>This can update terminals.</strong>{' '}
            {affected === 0 ? (
              <>
                No terminal you can see would be offered it right now — they are all
                already running {version.version} — but any terminal on this device
                type and channel that reports an older version will be offered it on
                its next heartbeat.
              </>
            ) : (
              <>
                {affected} terminal{affected === 1 ? '' : 's'} you can see{' '}
                {affected === 1 ? 'is' : 'are'} not running{' '}
                <code className="mono">{version.version}</code> and{' '}
                {affected === 1 ? 'will be offered it' : 'will be offered it'} on{' '}
                {affected === 1 ? 'its' : 'their'} next heartbeat. A terminal that
                takes the offer <strong>downloads the image, writes it to flash and
                reboots</strong>.
              </>
            )}
          </>
        ) : (
          <>
            <strong>Nothing will be updated.</strong> The platform will not offer this
            build to any terminal, so making it current changes only what the fleet is
            reported against.
          </>
        )
      }
      detail={
        <>
          {offerability.deliverable ? null : (
            <>
              It cannot be sent because: {offerability.problems.join(' ')} Publish a
              corrected entry and make that one current instead.{' '}
            </>
          )}
          {previous ? (
            <>
              <code className="mono">{previous.version}</code> stops being the current
              build — there is one per device type and channel, and this replaces it.
              A terminal already running it is not rolled back.{' '}
            </>
          ) : null}
          Terminals on other device types or other channels are unaffected.
          {offerability.deliverable ? (
            <>
              {' '}
              There is no undo: making the previous build current again stops further
              offers, but a terminal that has already installed this one has already
              installed it.
            </>
          ) : null}
        </>
      }
      // Typed only when hardware would actually change. See the note above.
      confirmPhrase={wouldUpdate ? version.version : undefined}
      confirmLabel={wouldUpdate ? 'Start the rollout' : 'Make it the current build'}
      onConfirm={async () => {
        await promote.mutateAsync(version.id)
        notifications.success(
          offerability.deliverable
            ? `${version.version} is now the current build for ${humaniseCode(version.device_type)} terminals on ${humaniseCode(version.release_channel)}. Terminals not running it will be offered it on their next heartbeat.`
            : `${version.version} is now the current build for ${humaniseCode(version.device_type)} terminals on ${humaniseCode(version.release_channel)}. It cannot be sent to anything, so no terminal will change.`,
        )
      }}
      onClose={onClose}
    />
  )
}
