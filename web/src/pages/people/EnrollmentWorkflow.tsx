import { useMemo, useState } from 'react'

import { ApiError } from '../../api/client'
import type { Enrollment, Person, Terminal } from '../../api/types'
import { Badge, TerminalStatusBadge } from '../../components/Badge'
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
import {
  describeEnrollment,
  enrollableTerminals,
  phaseOf,
  type EnrollmentPhase,
} from './enrollment'

/**
 * The fingerprint enrolment workflow, as a hook and two pieces of UI.
 *
 * ONE WORKFLOW, TWO DOORS INTO IT. It runs as the second step of "Add a
 * person" -- the moment somebody has been created is the moment they are most
 * likely to be standing at a terminal -- and it runs on its own from the
 * person's page for everybody added before that, or added and skipped. Both
 * render THIS: the same picker, the same states in the same words, the same
 * retry. Two copies would have been two places for "which terminal" to go
 * wrong.
 *
 * ---------------------------------------------------------------------------
 * THE OPERATOR PICKS THE DOOR. THAT IS THE WHOLE FEATURE.
 * ---------------------------------------------------------------------------
 *
 * A person is created with no credential -- deliberately, because biometric
 * material is captured at a sensor and never travels. The choice of terminal
 * is made here, where the operator can see the list, and it is MANDATORY.
 * There is no "any terminal", no default, and no "the one that happens to be
 * online": the operator is the only person who knows which door the customer
 * is standing at, and a platform that guessed would arm a reader in a
 * different room.
 *
 * ---------------------------------------------------------------------------
 * THE STATES, AND WHAT EACH OFFERS
 * ---------------------------------------------------------------------------
 *
 *   choose      the picker, and Start
 *   queued      "waiting for <terminal>", and Cancel
 *   scanning    "<terminal> is ready -- place finger", and Cancel
 *   succeeded   "fingerprint enrolled", and Done
 *   failed      the terminal's own words, the picker again, and Try again
 *   cancelled   "no fingerprint was recorded", the picker again, and Try again
 *
 * NOTHING HERE MENTIONS FIRMWARE, SERIAL PORTS OR COMMANDS. The customer-facing
 * workflow does not touch any of that, and naming it would suggest it does.
 *
 * ---------------------------------------------------------------------------
 * WHAT THE PROTOCOL STILL IS
 * ---------------------------------------------------------------------------
 *
 * Unchanged. POST /console/terminals/:serial/enrollments addresses a job to
 * one terminal; GET /console/people/:id/enrollment is polled while it is live;
 * DELETE cancels. The person exists before any of that starts and is left
 * exactly as they were by every outcome but success -- that is a server
 * guarantee, and this layer relies on it rather than restating it.
 */

export interface EnrollmentWorkflow {
  person: Person
  phase: EnrollmentPhase
  /** The most recent enrolment, whatever its state, or null for never. */
  current: Enrollment | null
  /** True while the enrolment is still expected to happen. */
  live: boolean
  /** Terminals worth offering. */
  eligible: Terminal[]
  terminalsLoading: boolean
  terminalsError: Error | null
  refetchTerminals: () => void
  serial: string
  setSerial: (serial: string) => void
  error: string | null
  starting: boolean
  cancelling: boolean
  start: () => Promise<void>
  stop: () => Promise<void>
  /** Sets the last outcome aside so the picker leads again. */
  retry: () => void
}

export function useEnrollmentWorkflow(
  person: Person,
  options: { enabled?: boolean } = {},
): EnrollmentWorkflow {
  const notifications = useNotifications()

  const terminals = useTerminals()
  const enrollment = useEnrollment(person.external_id, { enabled: options.enabled })
  const startMutation = useStartEnrollment(person.external_id)
  const cancelMutation = useCancelEnrollment(person.external_id)

  const [serial, setSerial] = useState('')
  const [error, setError] = useState<string | null>(null)
  // Set by `retry` after an outcome: the outcome panel steps aside so the
  // picker leads. Cleared as soon as a new enrolment exists.
  const [retrying, setRetrying] = useState(false)

  const current = enrollment.data?.enrollment ?? null
  const live = current !== null && describeEnrollment(current).live
  const phase = retrying && !live ? 'choose' : phaseOf(current)

  const eligible = useMemo(
    () => enrollableTerminals(terminals.data?.terminals ?? []),
    [terminals.data],
  )

  async function start() {
    setError(null)
    if (!serial) {
      // Never auto-picked, and never silently defaulted to the only one in the
      // list either. Choosing the door is the operator's decision even when
      // there is only one -- the habit is what keeps it correct at the site
      // that later has three.
      setError('Select a terminal before starting the enrolment.')
      return
    }
    try {
      const created = await startMutation.mutateAsync({
        serial,
        body: { external_id: person.external_id },
      })
      setRetrying(false)
      // The choice was spent on this attempt. If the picker comes back after
      // an outcome, it comes back with nothing selected -- a retry at the
      // same door is a decision, not a default.
      setSerial('')
      notifications.success(
        `${person.full_name || person.external_id} can now place their finger on ${
          created.terminal_name || created.terminal_serial
        }.`,
      )
    } catch (caught) {
      setError(
        caught instanceof ApiError ? caught.message : 'The enrolment could not be started.',
      )
    }
  }

  async function stop() {
    setError(null)
    try {
      await cancelMutation.mutateAsync()
      notifications.success('Enrolment cancelled.')
    } catch (caught) {
      setError(
        caught instanceof ApiError ? caught.message : 'The enrolment could not be cancelled.',
      )
    }
  }

  function retry() {
    setError(null)
    setSerial('')
    setRetrying(true)
  }

  return {
    person,
    phase,
    current,
    live,
    eligible,
    terminalsLoading: terminals.isPending,
    terminalsError: terminals.isError ? terminals.error : null,
    refetchTerminals: () => void terminals.refetch(),
    serial,
    setSerial,
    error,
    starting: startMutation.isPending,
    cancelling: cancelMutation.isPending,
    start,
    stop,
    retry,
  }
}

/** What a dialog around the workflow should say under its title. */
export function describeWorkflowLead(workflow: EnrollmentWorkflow): string {
  switch (workflow.phase) {
    case 'queued':
    case 'scanning':
      return 'The terminal below is waiting. Ask the person to place their finger on it.'
    case 'succeeded':
      return 'The fingerprint has been captured and this person is enrolled.'
    case 'failed':
    case 'cancelled':
      return 'The person is unchanged. Choose a terminal to try again, at the same door or a different one.'
    default:
      return 'Choose the terminal the person is standing at. Only that terminal will run the enrolment.'
  }
}

/**
 * The body of the workflow: the outcome or progress, and the picker when the
 * next action is to choose.
 */
export function EnrollmentWorkflowBody({ workflow }: { workflow: EnrollmentWorkflow }) {
  const showOutcome = workflow.current !== null && workflow.phase !== 'choose'
  const showPicker = !workflow.live && workflow.phase !== 'succeeded'

  return (
    <>
      <FormError message={workflow.error} />

      {showOutcome && workflow.current ? <EnrollmentProgress enrollment={workflow.current} /> : null}

      {showPicker ? (
        <TerminalPicker
          terminals={workflow.eligible}
          loading={workflow.terminalsLoading}
          error={workflow.terminalsError}
          onRetry={workflow.refetchTerminals}
          selected={workflow.serial}
          onSelect={workflow.setSerial}
        />
      ) : null}
    </>
  )
}

/**
 * The dialog footer for each state.
 *
 * `closeLabel` is what leaving the workflow is called where it is embedded:
 * "Close" on the person's page, "Skip for now" inside Add a person, where
 * leaving means "they are not at a terminal; enrol them later". The action
 * is the same; the word says what the operator is deciding.
 */
export function EnrollmentWorkflowActions({
  workflow,
  onClose,
  closeLabel = 'Close',
}: {
  workflow: EnrollmentWorkflow
  onClose: () => void
  closeLabel?: string
}) {
  if (workflow.live) {
    return (
      <>
        <button
          type="button"
          className="button"
          onClick={onClose}
          disabled={workflow.cancelling}
        >
          Leave it running
        </button>
        <button
          type="button"
          className="button button--danger"
          onClick={() => void workflow.stop()}
          disabled={workflow.cancelling}
        >
          {workflow.cancelling ? 'Cancelling…' : 'Cancel enrolment'}
        </button>
      </>
    )
  }

  if (workflow.phase === 'succeeded') {
    return (
      <>
        {/*
          Re-enrolling is offered here rather than hidden behind closing and
          reopening: the wrong finger, or a finger that reads badly, is found
          out at the moment of success, and the picker is one click away.
        */}
        <button type="button" className="button" onClick={workflow.retry}>
          Enrol again
        </button>
        <button type="button" className="button button--primary" onClick={onClose}>
          Done
        </button>
      </>
    )
  }

  const outcome = workflow.phase === 'failed' || workflow.phase === 'cancelled'
  return (
    <>
      <button type="button" className="button" onClick={onClose}>
        {closeLabel}
      </button>
      <button
        type="button"
        className="button button--primary"
        onClick={() => void workflow.start()}
        disabled={workflow.starting || workflow.eligible.length === 0}
      >
        {workflow.starting ? 'Starting…' : outcome ? 'Try again' : 'Start enrolment'}
      </button>
    </>
  )
}

/**
 * Where the enrolment has got to, in words a non-technical operator can act on.
 *
 * THE STATE IS NEVER A BARE CODE. "IN_PROGRESS" tells somebody at a front desk
 * nothing; "Ready -- ask them to place their finger on Front Door" tells them
 * what to do next. describeEnrollment owns that mapping and is tested on its
 * own, so the words cannot drift from the states they describe.
 */
function EnrollmentProgress({ enrollment }: { enrollment: Enrollment }) {
  const described = describeEnrollment(enrollment)

  return (
    <section
      className="panel"
      aria-labelledby="enrolment-progress-heading"
      // The phase is stated on the element for anything reading structure
      // rather than words -- and for the tests that assert which of the six
      // states is being shown without matching prose.
      data-enrolment-phase={phaseOf(enrollment)}
    >
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
          neither. A finger that is already enrolled to somebody else is
          reported here too, in the terminal's words, because the fix -- find
          out who -- is nothing this screen can do for them.
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
 * a handful of rows -- name, serial, site, whether it is up -- and a select
 * collapses each option to one line of text. This is a choice about physical
 * hardware in a building, and it should read like one.
 *
 * OFFLINE TERMINALS ARE OFFERED, MARKED. A terminal that is merely unreachable
 * right now will pick the job up when it reconnects, and hiding it would make
 * the feature unusable at exactly the sites that most need it. Terminals that
 * CANNOT run an enrolment at all -- disabled, retired, never provisioned -- are
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
        been provisioned cannot capture a fingerprint. The person has been kept;
        enrol them from their page once a terminal is available.
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
