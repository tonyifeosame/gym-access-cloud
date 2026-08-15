import { useState } from 'react'
import { Link } from 'react-router-dom'

import { ApiError } from '../../api/client'
import type { AccessDecision, TerminalDetail } from '../../api/types'
import { Badge } from '../../components/Badge'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, TextField } from '../../components/Form'
import { InfoNote } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useEvaluateAccess } from '../../data/console'
import { describeReason } from '../access/accessVocabulary'

/**
 * "Would this person get in at this terminal, right now, and why."
 *
 * THE QUESTION AN OPERATOR ACTUALLY ASKS after configuring a rule, and one that
 * previously could only be answered by sending somebody to stand at the door.
 * The access model has scopes, effects, schedules in timezones, validity windows
 * and a deny that beats every allow — nobody can reliably evaluate that in their
 * head, and a console that only showed the rules would leave them guessing.
 *
 * THE PREVIEW WRITES NO EVENT, and that is a property of the endpoint rather
 * than a promise made here: the server evaluates without recording, precisely so
 * that a presentation which never happened cannot end up in the trail an
 * attendance report is built from. Worth stating on the screen, because an
 * operator testing a rule twenty times would otherwise be right to worry.
 *
 * THE `at` PARAMETER IS THE OTHER HALF OF ITS VALUE. A schedule is testable
 * without waiting until Tuesday — "would the night shift get in at 23:00 on
 * Saturday" is a question with an answer now.
 */

interface Values extends Record<string, unknown> {
  external_id: string
  at: string
}

export function EvaluateAccessDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const evaluate = useEvaluateAccess(terminal.serial_number)
  const [decision, setDecision] = useState<AccessDecision | null>(null)
  const [askedAbout, setAskedAbout] = useState('')

  const form = useForm<Values>({
    initialValues: { external_id: '', at: '' },
    validate: (values) => ({
      external_id: validators.required(values.external_id, 'An identifier'),
    }),
    onSubmit: async (values) => {
      const result = await evaluate.mutateAsync({
        external_id: values.external_id.trim(),
        // A local datetime, converted to an instant. Absent means now, which is
        // what the field being blank should mean rather than "midnight".
        at: values.at ? new Date(values.at).toISOString() : undefined,
      })
      setAskedAbout(values.external_id.trim())
      setDecision(result)
    },
  })

  const name = terminal.device_name || terminal.serial_number
  const error = form.submitError

  return (
    <Dialog
      open={open}
      title={`Would somebody get in at ${name}?`}
      description="Checks the rules as they stand. Nothing is recorded and no door moves."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Person identifier"
          required
          mono
          value={form.values.external_id}
          error={form.errors.external_id}
          onChange={(value) => {
            // A previous answer must not sit under a changed question. It was
            // true for a different person and would be read as true for this one.
            setDecision(null)
            form.setValue('external_id', value)
          }}
          onBlur={() => form.touch('external_id')}
          disabled={form.submitting}
          hint="The external id the terminal would read — what People calls Identifier."
        />

        <TextField
          label="At what moment"
          type="text"
          value={form.values.at}
          onChange={(value) => {
            setDecision(null)
            form.setValue('at', value)
          }}
          disabled={form.submitting}
          placeholder="2026-08-18T23:00"
          hint="Optional, in your own timezone. Leave blank for now. This is how a night-shift schedule is tested without waiting until Saturday."
        />

        <FormError
          message={submitErrorMessage(error)}
          requestId={error instanceof ApiError ? error.requestId : null}
        />

        <FormActions>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={form.submitting}
          >
            Close
          </button>
          <button type="submit" className="button button--primary" disabled={form.submitting}>
            {form.submitting ? 'Checking…' : 'Check'}
          </button>
        </FormActions>
      </form>

      {decision ? <DecisionResult decision={decision} askedAbout={askedAbout} /> : null}

      <InfoNote title="Nothing was recorded">
        This preview does not write a door event. It cannot appear in Events or
        be counted by anything built on that history — which is why it is safe to
        run repeatedly while working out a rule.
      </InfoNote>
    </Dialog>
  )
}

/**
 * The answer, and what to do about it.
 *
 * THE REASON IS THE ENTIRE POINT. A bare "denied" tells an operator standing
 * next to a confused person nothing they cannot already see. "No rule covers
 * this terminal" and "outside the schedule on the rule that does" send them to
 * two different screens, and the remedy names which.
 */
function DecisionResult({
  decision,
  askedAbout,
}: {
  decision: AccessDecision
  askedAbout: string
}) {
  const reason = describeReason(decision.reason)

  return (
    <section className="decision" aria-live="polite" aria-label="Result">
      <h3 className="decision__verdict">
        <Badge tone={decision.granted ? 'positive' : 'danger'}>
          {decision.granted ? 'Would be let in' : 'Would be refused'}
        </Badge>{' '}
        {decision.person_name ? (
          <Link to={`/people/${encodeURIComponent(decision.external_id ?? askedAbout)}`}>
            {decision.person_name}
          </Link>
        ) : (
          <span className="event__unknown">
            <code className="mono">{askedAbout}</code>
            <Badge tone="warning">Not recognised</Badge>
          </span>
        )}
      </h3>

      <dl className="detail-list">
        <div className="detail-list__row">
          <dt>Because</dt>
          <dd>
            <p className="rule__detail">
              <strong>{reason.label}.</strong> {reason.meaning}
            </p>
            {reason.remedy ? (
              <p className="rule__detail">
                <strong>What to do:</strong> {reason.remedy}
              </p>
            ) : null}
          </dd>
        </div>

        <div className="detail-list__row">
          <dt>Rule that decided it</dt>
          <dd>
            {decision.matched_permission ? (
              <code className="mono">{decision.matched_permission}</code>
            ) : (
              // Empty is the answer rather than missing data: nothing matched,
              // and on this platform nothing matching means refused.
              <span className="muted">
                None — no rule matched, and absence of permission is not permission
              </span>
            )}
          </dd>
        </div>

        {decision.application ? (
          <div className="detail-list__row">
            <dt>Acting as</dt>
            <dd>{decision.application}</dd>
          </div>
        ) : null}

        <div className="detail-list__row">
          <dt>Evaluated at</dt>
          <dd>
            <Timestamp value={decision.decided_at} />
          </dd>
        </div>
      </dl>
    </section>
  )
}
