import { useMemo, useState } from 'react'

import { ApiError } from '../../api/client'
import type { Enrollment, Person, Terminal } from '../../api/types'
import { Badge, TerminalStatusBadge } from '../../components/Badge'
import { Dialog } from '../../components/Dialog'
import { FormError, RadioGroup } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import {
  useCancelEnrollment,
  useEnrollment,
  useStartEnrollment,
  useTerminals,
} from '../../data/console'
import { describeEnrollment, enrollableTerminals } from './enrollment'

/**
 * Enrolling a fingerprint, from the console.
 *
 * ---------------------------------------------------------------------------
 * THE OPERATOR PICKS THE DOOR. THAT IS THE WHOLE FEATURE.
 * ---------------------------------------------------------------------------
 *
 * A person is created with no credential — deliberately, because biometric
 * material is captured at a sensor and never travels. Binding a finger to them
 * used to mean a technician with a USB cable typing a command into the serial
 * console of the right terminal, and at a site with four doors there is nothing
 * on a unit that says which of them it is.
 *
 * So the choice is made here, where the operator can see the list, and it is
 * MANDATORY. There is no "any terminal", no default, and no "the one that
 * happens to be online" — the operator is the only person who knows which door
 * the customer is standing at, and a platform that guessed would arm a reader in
 * a different room.
 *
 * ---------------------------------------------------------------------------
 * WHAT AN OPERATOR IS TOLD, AND WHY EACH PART IS THERE
 * ---------------------------------------------------------------------------
 *
 *   the terminal's NAME     what it is called on the wall
 *   its SERIAL              what is printed on the unit itself
 *   its SITE                which building
 *   its STATUS              whether it is reachable right now
 *
 * A name alone is not enough to identify hardware — two sites can both have a
 * "Front Door" — and a serial alone is not enough for a human to recognise.
 *
 * NOTHING HERE MENTIONS FIRMWARE, SERIAL PORTS OR COMMANDS. The customer-facing
 * workflow does not touch any of that, and naming it in this dialog would
 * suggest it does.
 */
export function EnrollFingerprintDialog({
  open,
  person,
  onClose,
}: {
  open: boolean
  person: Person
  onClose: () => void
}) {
  const notifications = useNotifications()

  const terminals = useTerminals()
  const enrollment = useEnrollment(person.external_id, { enabled: open })
  const start = useStartEnrollment(person.external_id)
  const cancel = useCancelEnrollment(person.external_id)

  const [serial, setSerial] = useState('')
  const [error, setError] = useState<string | null>(null)

  const current = enrollment.data?.enrollment ?? null
  const live = current !== null && describeEnrollment(current).live

  const eligible = useMemo(
    () => enrollableTerminals(terminals.data?.terminals ?? []),
    [terminals.data],
  )

  async function submit() {
    setError(null)
    if (!serial) {
      // Never auto-picked, and never silently defaulted to the only one in the
      // list either. Choosing the door is the operator's decision even when
      // there is only one — the habit is what keeps it correct at the site that
      // later has three.
      setError('Select a terminal before starting the enrolment.')
      return
    }
    try {
      const created = await start.mutateAsync({
        serial,
        body: { external_id: person.external_id },
      })
      notifications.success(
        `${person.full_name || person.external_id} can now place their finger on ${
          created.terminal_name || created.terminal_serial
        }.`,
      )
    } catch (caught) {
      setError(
        caught instanceof ApiError
          ? caught.message
          : 'The enrolment could not be started.',
      )
    }
  }

  async function stop() {
    setError(null)
    try {
      await cancel.mutateAsync()
      notifications.success('Enrolment cancelled.')
    } catch (caught) {
      setError(
        caught instanceof ApiError ? caught.message : 'The enrolment could not be cancelled.',
      )
    }
  }

  return (
    <Dialog
      open={open}
      size="wide"
      title={`Enrol a fingerprint for ${person.full_name || person.external_id}`}
      description={
        live
          ? 'The terminal below is waiting. Ask the person to place their finger on it.'
          : 'Choose the terminal the person is standing at. Only that terminal will run the enrolment.'
      }
      onClose={onClose}
      dismissible={!start.isPending && !cancel.isPending}
      footer={
        live ? (
          <>
            <button
              type="button"
              className="button"
              onClick={onClose}
              disabled={cancel.isPending}
            >
              Leave it running
            </button>
            <button
              type="button"
              className="button button--danger"
              onClick={() => void stop()}
              disabled={cancel.isPending}
            >
              {cancel.isPending ? 'Cancelling…' : 'Cancel enrolment'}
            </button>
          </>
        ) : (
          <>
            <button type="button" className="button" onClick={onClose}>
              Close
            </button>
            <button
              type="button"
              className="button button--primary"
              onClick={() => void submit()}
              disabled={start.isPending || eligible.length === 0}
            >
              {start.isPending ? 'Starting…' : 'Start enrolment'}
            </button>
          </>
        )
      }
    >
      <FormError message={error} />

      {current ? <EnrollmentProgress enrollment={current} /> : null}

      {live ? null : (
        <TerminalPicker
          terminals={eligible}
          loading={terminals.isPending}
          error={terminals.isError ? terminals.error : null}
          onRetry={() => void terminals.refetch()}
          selected={serial}
          onSelect={setSerial}
        />
      )}
    </Dialog>
  )
}

/**
 * Where the enrolment has got to, in words a non-technical operator can act on.
 *
 * THE STATE IS NEVER A BARE CODE. "IN_PROGRESS" tells somebody at a front desk
 * nothing; "Ready — ask them to place their finger on Front Door" tells them
 * what to do next. describeEnrollment owns that mapping and is tested on its
 * own, so the words cannot drift from the states they describe.
 */
function EnrollmentProgress({ enrollment }: { enrollment: Enrollment }) {
  const described = describeEnrollment(enrollment)

  return (
    <section className="panel" aria-labelledby="enrolment-progress-heading">
      <div className="panel__header">
        <h3 className="panel__title" id="enrolment-progress-heading">
          {described.headline}
        </h3>
        <Badge tone={described.tone}>{described.label}</Badge>
      </div>

      <p>{described.detail}</p>

      <dl className="detail-list">
        <div className="detail-list__row">
          <dt>Terminal</dt>
          <dd>
            {enrollment.terminal_name || <span className="muted">Unnamed terminal</span>}{' '}
            <code className="mono">{enrollment.terminal_serial}</code>
            {enrollment.terminal_status ? (
              <> <TerminalStatusBadge status={enrollment.terminal_status} /></>
            ) : null}
          </dd>
        </div>

        {enrollment.site_name ? (
          <div className="detail-list__row">
            <dt>Location</dt>
            <dd>{enrollment.site_name}</dd>
          </div>
        ) : null}

        {enrollment.expires_at && described.live ? (
          <div className="detail-list__row">
            <dt>Window closes</dt>
            <dd>
              <Timestamp value={enrollment.expires_at} relative />
            </dd>
          </div>
        ) : null}

        {enrollment.completed_at && !described.live ? (
          <div className="detail-list__row">
            <dt>Finished</dt>
            <dd>
              <Timestamp value={enrollment.completed_at} relative />
            </dd>
          </div>
        ) : null}

        {/*
          THE TERMINAL'S OWN WORDS, not a platform summary. "Sensor error" and
          "the window closed with nobody at the door" send somebody to two
          different places, and flattening both to "failed" would send them to
          neither.
        */}
        {enrollment.error_message && !described.live ? (
          <div className="detail-list__row">
            <dt>What the terminal reported</dt>
            <dd>{enrollment.error_message}</dd>
          </div>
        ) : null}

        {enrollment.requested_by_email ? (
          <div className="detail-list__row">
            <dt>Started by</dt>
            <dd>{enrollment.requested_by_email}</dd>
          </div>
        ) : null}
      </dl>

      {described.live && enrollment.terminal_status === 'OFFLINE' ? (
        <InfoNote tone="warning" title="That terminal is offline">
          The request is queued and will reach it as soon as it reconnects. If the
          person cannot wait, cancel this and start again at a terminal that is
          online.
        </InfoNote>
      ) : null}
    </section>
  )
}

/**
 * The terminal list, as somewhere a person could be standing.
 *
 * A RADIO GROUP AND NOT A DROPDOWN. The operator is comparing four facts across
 * a handful of rows — name, serial, site, whether it is up — and a select
 * collapses each option to one line of text. This is a choice about physical
 * hardware in a building, and it should read like one.
 *
 * OFFLINE TERMINALS ARE OFFERED, MARKED. A terminal that is merely unreachable
 * right now will pick the job up when it reconnects, and hiding it would make
 * the feature unusable at exactly the sites that most need it. Terminals that
 * CANNOT run an enrolment at all — disabled, retired, never provisioned — are
 * not in this list, because the server refuses them and offering a choice that
 * gets rejected is worse than not offering it.
 */
function TerminalPicker({
  terminals,
  loading,
  error,
  onRetry,
  selected,
  onSelect,
}: {
  terminals: Terminal[]
  loading: boolean
  error: Error | null
  onRetry: () => void
  selected: string
  onSelect: (serial: string) => void
}) {
  if (loading) return <LoadingState label="Loading terminals…" />
  if (error) return <ErrorState error={error} onRetry={onRetry} />

  if (terminals.length === 0) {
    return (
      <InfoNote tone="warning" title="No terminal can run an enrolment">
        Enrolling a fingerprint needs a terminal that is in service and has been
        set up. Check your terminals list — one that is disabled or has never
        been provisioned cannot capture a fingerprint.
      </InfoNote>
    )
  }

  return (
    <RadioGroup
      legend="Which terminal is the person standing at?"
      hint="Only the terminal you choose will enter enrolment mode. No other terminal is affected."
      name="enrolment-terminal"
      value={selected}
      onChange={onSelect}
      options={terminals.map((terminal) => ({
        value: terminal.serial_number,
        // The name is what it is called on the wall. The serial is what is
        // printed on the unit, and the site is which building -- a name alone
        // is not enough (two sites can both have a "Front Door") and a serial
        // alone is not something a human recognises.
        label: terminal.device_name || terminal.serial_number,
        description: (
          <>
            <code className="mono">{terminal.serial_number}</code>
            {terminal.site_name ? <> · {terminal.site_name}</> : null}{' '}
            <TerminalStatusBadge status={terminal.status} />
            {terminal.status === 'OFFLINE' ? (
              <> — the request will wait until it reconnects</>
            ) : null}
          </>
        ),
      }))}
    />
  )
}
