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

/**
 * The firmware catalogue.
 *
 * WHAT MARKING A BUILD "CURRENT" ACTUALLY DOES, because it is the one thing an
 * operator will assume wrongly and the assumption is expensive:
 *
 *   It changes what the fleet is COMPARED TO. Nothing is sent anywhere. No
 *   terminal downloads, installs or restarts. The platform has no OTA (FW-02),
 *   so `is_current` is the value every "is this terminal outdated" report is
 *   measured against and is nothing else.
 *
 * Somebody who publishes a build, marks it current, and walks away believing the
 * fleet is updating has been misled by a screen — so the sentence appears on the
 * page, in the publish dialog, and in the confirmation, rather than once at the
 * top where it can be scrolled past.
 *
 * WHY THIS SCREEN EXISTS AT ALL. These routes used to live in the site-key tree,
 * where any provisioning secret — a value that lives on hardware bolted to a wall
 * — could add a build and move the deployment target every outdated report is
 * measured against. They are ADMIN console routes now, and this is the surface
 * for them.
 *
 * THE STRUCTURE MIRRORS THE MODEL. "Current" is per company, device type AND
 * release channel: promoting a build demotes only its own peers. Grouping the
 * catalogue that way is what makes "make current" unambiguous — a flat list
 * invites somebody to believe there is one current build for everything.
 */
export function FirmwarePage() {
  const { session } = useSession()
  const firmware = useFirmware()
  // The fleet, so each group can say what it is actually describing. Scoped by
  // the operator's grants exactly as the Terminals page is, which is stated
  // rather than left to be inferred from a number that looks company-wide.
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

  return (
    <div className="page">
      <PageHeader
        title="Firmware"
        lead="The builds this company knows about, and which one each part of the fleet is measured against."
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
        restatement of it.
      */}
      <InfoNote tone="warning" title="Nothing here updates a terminal">
        <p>
          AccessLink has <strong>no over-the-air update</strong>. Publishing a
          build records that it exists; marking one current sets the version the
          fleet is <strong>compared to</strong>. No terminal downloads anything,
          and none will change its firmware as a result of anything on this page.
        </p>
        <p>
          Installing firmware is a separate, physical operation. What this
          catalogue gives you is an accurate answer to “which terminals are
          behind”, on <Link to="/terminals">Terminals</Link>.
        </p>
      </InfoNote>

      {groups.length === 0 ? (
        <InfoNote title="No builds recorded">
          <p>
            Nothing has been published, so every terminal reports as up to date —
            not because it is, but because there is nothing to compare it with.
          </p>
          <p>
            Publishing the first build for a device type and channel is what makes
            “outdated” mean anything, and it may mark terminals outdated the
            moment it lands. Nothing about those terminals will have changed.
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
                    see {group.terminalCount === 1 ? 'is' : 'are'} measured against it
                    {group.current ? (
                      <>
                        , of which <strong>{group.onCurrent}</strong>{' '}
                        {group.onCurrent === 1 ? 'is' : 'are'} running{' '}
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
                Terminals here are compared with nothing, so they all report as up
                to date. Mark a build current to change that.
              </InfoNote>
            ) : null}

            <ul className="rule-list">
              {group.versions.map((version) => (
                <li key={version.id} className="rule" data-effect={version.is_current ? 'ALLOW' : undefined}>
                  <div className="rule__main">
                    <h3 className="rule__title">
                      <code className="mono">{version.version}</code>
                      {version.is_current ? (
                        <Badge tone="positive">Current target</Badge>
                      ) : (
                        <Badge>Recorded</Badge>
                      )}
                      {version.is_mandatory ? <Badge tone="warning">Marked mandatory</Badge> : null}
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
                      Shown as text and never as a link. There is no OTA, so this
                      is a note about where a human can find the file — offering
                      it as something to click would suggest the platform fetches
                      it, which is exactly the belief this page exists to prevent.
                    */}
                    {version.download_url ? (
                      <p className="rule__detail">
                        Stored at <code className="mono">{version.download_url}</code>
                      </p>
                    ) : null}

                    {version.is_mandatory ? (
                      <p className="rule__detail">
                        <strong>“Mandatory” is recorded, not enforced.</strong> Nothing
                        in the platform acts on it, because nothing installs firmware.
                      </p>
                    ) : null}
                  </div>

                  {mayManage && !version.is_current ? (
                    <button
                      type="button"
                      className="button"
                      onClick={() => setPromoting(version)}
                    >
                      Make current
                    </button>
                  ) : null}
                </li>
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
        publish and promote. The `mayManage` guards below are kept as
        defence in depth and as documentation of the role, but a "you can look
        but not touch" notice would describe a state no operator can be in.
      */}

      {publishing ? (
        <PublishFirmwareDialog open onClose={() => setPublishing(false)} />
      ) : null}
      {promoting ? (
        <MakeCurrentDialog
          open
          version={promoting}
          previous={
            groups.find((group) => group.key === targetKey(promoting))?.current ?? null
          }
          affected={groups.find((group) => group.key === targetKey(promoting))?.terminalCount ?? 0}
          onClose={() => setPromoting(null)}
        />
      ) : null}
    </div>
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
 * already on this build" would hide, and that number is what an operator reads
 * before deciding whether an update is worth doing.
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
  release_notes: string
  is_mandatory: boolean
}

/**
 * Adding a build to the catalogue.
 *
 * PUBLISHING DOES NOT MAKE IT CURRENT. Two decisions, two calls, on the server
 * as well as here: recording that a build exists and deciding a fleet should be
 * measured against it are different, and collapsing them would mean every
 * upload silently moved the target.
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
      release_notes: '',
      is_mandatory: false,
    },
    validate: (values) => ({
      version: validators.required(values.version, 'A version'),
      device_type: validators.required(values.device_type, 'A device type'),
      release_channel: validators.required(values.release_channel, 'A release channel'),
      checksum_sha256:
        values.checksum_sha256.trim() && !/^[0-9a-f]{64}$/i.test(values.checksum_sha256.trim())
          ? 'A SHA-256 is 64 hexadecimal characters.'
          : undefined,
    }),
    onSubmit: async (values) => {
      const created = await publish.mutateAsync({
        version: values.version.trim(),
        device_type: values.device_type.trim(),
        release_channel: values.release_channel.trim(),
        download_url: values.download_url.trim() || undefined,
        checksum_sha256: values.checksum_sha256.trim() || undefined,
        release_notes: values.release_notes.trim() || undefined,
        is_mandatory: values.is_mandatory,
      })
      notifications.success(
        `${created.version} recorded. It is not the current target until you make it one.`,
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
      description="Records that a build exists. It does not become the deployment target, and nothing is sent to any terminal."
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
          hint="Exactly as the firmware reports itself. A terminal is counted as outdated when this string differs from what it reports, so a mismatch in formatting reads as a whole fleet being behind."
        />

        <TextField
          label="Device type"
          required
          value={form.values.device_type}
          error={form.errors.device_type}
          onChange={(value) => form.setValue('device_type', value)}
          onBlur={() => form.touch('device_type')}
          disabled={form.submitting}
          hint="Must match what the terminals report. Terminals of another type are not compared with this build at all."
        />

        <TextField
          label="Release channel"
          required
          value={form.values.release_channel}
          error={form.errors.release_channel}
          onChange={(value) => form.setValue('release_channel', value)}
          onBlur={() => form.touch('release_channel')}
          disabled={form.submitting}
          hint="Terminals are compared only with builds on their own channel."
        />

        <TextField
          label="Where the file is stored"
          value={form.values.download_url}
          onChange={(value) => form.setValue('download_url', value)}
          disabled={form.submitting}
          hint="Optional, and a note for people rather than an instruction to the platform — nothing fetches it."
        />

        <TextField
          label="SHA-256 checksum"
          mono
          value={form.values.checksum_sha256}
          error={form.errors.checksum_sha256}
          onChange={(value) => form.setValue('checksum_sha256', value)}
          onBlur={() => form.touch('checksum_sha256')}
          disabled={form.submitting}
          hint="Optional. Recorded so whoever installs the build by hand can verify what they are installing."
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
          hint="RECORDED, NOT ENFORCED. Nothing in the platform acts on this, because nothing installs firmware. It is a note to whoever plans the rollout."
        />

        <InfoNote title="This does not become the target">
          A published build sits in the catalogue until somebody marks it current.
          That is a separate, deliberate decision — and even then, nothing is sent
          to a terminal.
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

// ---------------------------------------------------------------------------
// Make current
// ---------------------------------------------------------------------------

/**
 * Moving the deployment target.
 *
 * The confirmation says the two things an operator would otherwise assume
 * wrongly: that this pushes something (it does not), and that it affects the
 * whole fleet (it affects one device type on one channel). It also names the
 * build being demoted, because "current" is a single slot and the previous
 * occupant leaves it silently.
 */
function MakeCurrentDialog({
  open,
  version,
  previous,
  affected,
  onClose,
}: {
  open: boolean
  version: FirmwareVersion
  previous: FirmwareVersion | null
  affected: number
  onClose: () => void
}) {
  const promote = useSetCurrentFirmware()
  const notifications = useNotifications()

  return (
    <ConfirmDialog
      open={open}
      tone="default"
      title={`Make ${version.version} the target for ${humaniseCode(version.device_type)} · ${humaniseCode(version.release_channel)}?`}
      consequence={
        <>
          {affected === 0 ? (
            <>No terminal you can see is on this device type and channel.</>
          ) : (
            <>
              The {affected} terminal{affected === 1 ? '' : 's'} you can see on this
              device type and channel will be reported as up to date or behind{' '}
              <strong>by comparison with {version.version}</strong>.
            </>
          )}{' '}
          <strong>Nothing is sent to any of them.</strong>
        </>
      }
      detail={
        <>
          {previous ? (
            <>
              <code className="mono">{previous.version}</code> stops being the
              target — there is one current build per device type and channel, and
              this replaces it.{' '}
            </>
          ) : null}
          Terminals on other device types or other channels are unaffected.
          AccessLink has no over-the-air update: changing this changes what the
          fleet is measured against, and installing firmware remains a separate,
          physical operation.
        </>
      }
      confirmLabel="Make it the target"
      onConfirm={async () => {
        await promote.mutateAsync(version.id)
        notifications.success(
          `${version.version} is now what ${humaniseCode(version.device_type)} terminals on ${humaniseCode(version.release_channel)} are compared with. Nothing was sent to them.`,
        )
      }}
      onClose={onClose}
    />
  )
}
