import { useState } from 'react'

import type { TerminalDetail } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, SelectField, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import {
  useMoveTerminal,
  useResyncTerminal,
  useRetireTerminal,
  useRevokeTerminalCredential,
  useSetTerminalState,
  useSites,
} from '../../data/console'

/**
 * The terminal lifecycle operations.
 *
 * THREE OF THESE SOUND ALIKE AND ARE NOT, and the single most important job of
 * this file is that an operator standing in front of a door cannot confuse them.
 * Each is shaped differently on purpose — different tone, different confirmation
 * weight, different words for what survives:
 *
 *   DISABLE   Reversible, credential untouched. The same dialog offers to undo
 *             it. This is "it is faulty" or "this entrance is closed this month".
 *
 *   REVOKE    The device credential is destroyed. The hardware still exists and
 *             must be re-provisioned to come back — with a CLAIM CODE for its
 *             serial, not with the site's provisioning key, which is the whole
 *             point of claim codes existing. This is "it has been stolen", and it
 *             is the operation that did not exist at all before, which meant a
 *             stolen terminal could only be dealt with by retiring its entire
 *             site.
 *
 *   RETIRE    One-way. The row goes, the credential goes, the terminal leaves
 *             every list. Requires typing the serial. This is "the unit is
 *             gone".
 *
 * If these ever collapse into one control with a mode selector, somebody will
 * decommission a working terminal by reflex — which is the failure the whole
 * distinction exists to prevent.
 *
 * A REASON IS ASKED FOR ON ALL THREE. It lands in the audit trail and on the
 * terminal row, and "why is this terminal dead" is the question the next
 * operator asks. It is optional, because a required field on an urgent action
 * gets filled with a full stop.
 */

// ---------------------------------------------------------------------------
// Disable / re-enable
// ---------------------------------------------------------------------------

export function TerminalStateDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const setState = useSetTerminalState(terminal.serial_number)
  const notifications = useNotifications()
  const [reason, setReason] = useState('')

  // `active` rather than `status`: status is what the terminal last reported and
  // may be stale, while active is what the platform has decided about it. The
  // control has to reflect the decision, not the report.
  const disabling = terminal.active
  const name = terminal.device_name || terminal.serial_number

  return (
    <ConfirmDialog
      open={open}
      tone={disabling ? 'danger' : 'default'}
      title={disabling ? `Disable ${name}?` : `Re-enable ${name}?`}
      consequence={
        disabling ? (
          <>
            This terminal stops authenticating <strong>immediately</strong>. It will
            not open anything, and it will not collect changes.
          </>
        ) : (
          <>
            This terminal starts authenticating again immediately, using the
            credential it already holds.
          </>
        )
      }
      detail={
        disabling ? (
          <>
            <strong>This is reversible and does not touch the credential.</strong> The
            unit keeps its device key, so re-enabling brings it back with no site
            visit and no re-provisioning. If the hardware is missing or stolen,
            revoke its credential instead.
          </>
        ) : undefined
      }
      confirmLabel={disabling ? 'Disable terminal' : 'Re-enable terminal'}
      onConfirm={async () => {
        await setState.mutateAsync({ disabled: disabling, reason: reason.trim() || undefined })
        notifications.success(disabling ? `${name} disabled` : `${name} re-enabled`)
      }}
      onClose={onClose}
    >
      <ReasonField value={reason} onChange={setReason} />
    </ConfirmDialog>
  )
}

// ---------------------------------------------------------------------------
// Revoke the device credential
// ---------------------------------------------------------------------------

export function RevokeTerminalDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const revoke = useRevokeTerminalCredential(terminal.serial_number)
  const notifications = useNotifications()
  const [reason, setReason] = useState('')

  const name = terminal.device_name || terminal.serial_number

  return (
    <ConfirmDialog
      open={open}
      title={`Revoke the credential for ${name}?`}
      consequence={
        <>
          This terminal&apos;s device key is <strong>destroyed</strong>. It stops
          authenticating immediately and cannot be brought back by re-enabling it.
        </>
      }
      detail={
        <>
          Use this when the hardware is <strong>missing, stolen, or out of your
          control</strong> — revoking is the only operation that makes the
          credential itself stop working. Work already queued for it will never be
          delivered. If the terminal is simply faulty and still in your possession,
          disable it instead.
          <br />
          <br />
          {/*
            THE RECOVERY PATH, CORRECTED. This used to say the unit must
            re-register "using its site's provisioning key" — which is one way and
            is now the worse one. Recovering a single terminal does not need a
            credential that registers every terminal at the site for ever; it needs
            a claim code for this serial, which is one use and expires. Naming the
            key first was the console teaching the habit that claim codes exist to
            break.
          */}
          To bring this unit back afterwards, issue a <strong>claim code</strong> for{' '}
          <code className="mono">{terminal.serial_number}</code> from{' '}
          <strong>{terminal.site_name}</strong> and redeem it at the terminal. That
          gives this one unit a fresh credential without the site&apos;s provisioning
          key leaving the platform.
        </>
      }
      // Typing the serial, because this is not reversible and the serial is
      // printed on the hardware the operator is looking at — which is also the
      // check that they are revoking the unit they think they are.
      confirmPhrase={terminal.serial_number}
      confirmLabel="Revoke credential"
      onConfirm={async () => {
        const result = await revoke.mutateAsync({ reason: reason.trim() || undefined })
        const cancelled = result.pending_jobs_cancelled ?? 0
        const queued =
          cancelled > 0
            ? ` ${cancelled} queued change${cancelled === 1 ? '' : 's'} cancelled.`
            : ''
        // The SERVER'S OWN WORDS on recovery, appended rather than paraphrased.
        // It is the authority on what re-registration requires, and a console
        // sentence that drifted from it would be the one an operator followed.
        const recovery = result.recovery ? ` ${result.recovery}` : ''
        notifications.success(`${name} revoked.${queued}${recovery}`)
      }}
      onClose={onClose}
    >
      <ReasonField value={reason} onChange={setReason} />
    </ConfirmDialog>
  )
}

// ---------------------------------------------------------------------------
// Retire
// ---------------------------------------------------------------------------

export function RetireTerminalDialog({
  open,
  terminal,
  onClose,
  onRetired,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
  /** Called after success, so the caller can leave a page that now 404s. */
  onRetired: () => void
}) {
  const retire = useRetireTerminal(terminal.serial_number)
  const notifications = useNotifications()
  const [reason, setReason] = useState('')

  const name = terminal.device_name || terminal.serial_number

  return (
    <ConfirmDialog
      open={open}
      title={`Retire ${name}?`}
      consequence={
        <>
          The terminal is removed from your fleet and its credential destroyed.
          This <strong>cannot be undone</strong> from the console.
        </>
      }
      detail={
        <>
          Use this when the unit has been <strong>decommissioned, destroyed or
          returned</strong> — it disappears from every list, and its history stays
          in the audit trail. If the hardware still exists and may be reinstalled,
          revoke its credential instead: that leaves the terminal on your fleet and
          lets it re-register later.
        </>
      }
      confirmPhrase={terminal.serial_number}
      confirmLabel="Retire terminal"
      onConfirm={async () => {
        const result = await retire.mutateAsync({ reason: reason.trim() || undefined })
        notifications.success(
          result.pending_jobs_cancelled > 0
            ? `${name} retired. ${result.pending_jobs_cancelled} queued change${
                result.pending_jobs_cancelled === 1 ? '' : 's'
              } cancelled.`
            : `${name} retired`,
        )
        onRetired()
      }}
      onClose={onClose}
    >
      <ReasonField value={reason} onChange={setReason} />
    </ConfirmDialog>
  )
}

// ---------------------------------------------------------------------------
// Move to another site
// ---------------------------------------------------------------------------

interface MoveValues extends Record<string, unknown> {
  site_id: string
}

/**
 * Reassigning a terminal to another site.
 *
 * NOT A METADATA EDIT, and the dialog says so. The roster is rebuilt: queued
 * work for the old site is cancelled and the terminal re-converges against the
 * new one, being told to erase what it may no longer hold. A move that kept the
 * old roster would leave a terminal opening for the previous location's people,
 * which is exactly the outcome an operator moving hardware is trying to avoid.
 */
export function MoveTerminalDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const move = useMoveTerminal(terminal.serial_number)
  const sites = useSites()
  const notifications = useNotifications()

  const name = terminal.device_name || terminal.serial_number

  // The site it already stands at is excluded rather than disabled: "move it to
  // where it is" is not an operation, and offering it invites a no-op that
  // still cancels the terminal's queued work.
  const destinations = (sites.data?.sites ?? []).filter(
    (site) => site.id !== terminal.site_public_id,
  )

  const form = useForm<MoveValues>({
    initialValues: { site_id: '' },
    validate: (values) => ({ site_id: validators.required(values.site_id, 'A destination site') }),
    onSubmit: async (values) => {
      const result = await move.mutateAsync({ site_id: values.site_id })
      const destination = destinations.find((site) => site.id === values.site_id)
      const cancelled = result.pending_jobs_cancelled ?? 0
      notifications.success(
        cancelled > 0
          ? `${name} moved to ${destination?.name ?? 'its new site'}. ${cancelled} queued change${
              cancelled === 1 ? '' : 's'
            } cancelled while it rebuilds.`
          : `${name} moved to ${destination?.name ?? 'its new site'}`,
      )
      onClose()
    },
  })

  return (
    <Dialog
      open={open}
      title={`Move ${name}`}
      description={`Currently at ${terminal.site_name}.`}
      dismissible={!form.submitting}
      onClose={onClose}
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <SelectField
          label="Move to"
          required
          placeholder="Choose a site…"
          value={form.values.site_id}
          error={form.errors.site_id}
          onChange={(value) => form.setValue('site_id', value)}
          onBlur={() => form.touch('site_id')}
          disabled={form.submitting || destinations.length === 0}
          options={destinations.map((site) => ({
            value: site.id,
            label: site.name,
            description: site.active ? undefined : 'Deactivated — the terminal would not authenticate',
          }))}
        />

        {destinations.length === 0 ? (
          <InfoNote title="Nowhere to move it">
            Your company has no other site to move this terminal to. Create one
            first, or ask an administrator to grant you access to the site you had
            in mind.
          </InfoNote>
        ) : (
          <InfoNote tone="warning" title="The terminal rebuilds from scratch">
            Moving reassigns which people this terminal knows about. Queued changes
            for its current site are cancelled, it is told to erase what it holds,
            and it re-collects everything for the new site. Until that finishes the
            terminal will not recognise anybody.
          </InfoNote>
        )}

        <FormError message={submitErrorMessage(form.submitError)} />

        <FormActions>
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
            className="button button--primary"
            disabled={form.submitting || destinations.length === 0}
          >
            {form.submitting ? 'Moving…' : 'Move terminal'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Force a resync
// ---------------------------------------------------------------------------

/**
 * Replaces a terminal's queue with a snapshot of current state.
 *
 * The gentlest operation here, and the only one that is not destructive — which
 * is why it is a plain confirmation with no typed phrase and no danger tone. It
 * is also MANAGER rather than ADMIN, matching the server.
 */
export function ResyncTerminalDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const resync = useResyncTerminal(terminal.serial_number)
  const notifications = useNotifications()
  const name = terminal.device_name || terminal.serial_number

  return (
    <ConfirmDialog
      open={open}
      tone="default"
      title={`Resync ${name}?`}
      consequence={
        <>
          Everything currently queued for this terminal is replaced with a fresh
          snapshot of the people and settings it should hold.
        </>
      }
      detail={
        <>
          For a terminal believed to have drifted — missing somebody who should be
          there, or holding somebody who should not. Nothing is deleted and the
          terminal keeps working throughout; it collects the snapshot on its next
          poll, which may take a few minutes.
        </>
      }
      confirmLabel="Queue a full sync"
      onConfirm={async () => {
        const result = await resync.mutateAsync()
        notifications.success(
          `${name} will resync: ${result.pending_jobs} change${
            result.pending_jobs === 1 ? '' : 's'
          } queued${
            result.superseded_jobs > 0
              ? `, replacing ${result.superseded_jobs} older one${
                  result.superseded_jobs === 1 ? '' : 's'
                }`
              : ''
          }.`,
        )
      }}
      onClose={onClose}
    />
  )
}

// ---------------------------------------------------------------------------

/**
 * The reason field the three destructive dialogs share.
 *
 * Optional, and labelled as such. A required reason on an urgent action gets a
 * full stop typed into it, which is worse than an empty column because it looks
 * like an answer.
 */
function ReasonField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <TextField
      label="Reason (optional)"
      value={value}
      onChange={onChange}
      placeholder="e.g. reported stolen from the east entrance"
      hint="Recorded in the audit trail and shown on the terminal. The next person to look at this will want to know why."
    />
  )
}
