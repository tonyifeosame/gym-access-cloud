import type { BadgeTone } from '../../components/Badge'
import type { Enrollment, EnrollmentStatus, Terminal } from '../../api/types'

/**
 * Reading a fingerprint enrolment.
 *
 * SEPARATED FROM THE DIALOG BECAUSE THE WORDS ARE THE PART WORTH TESTING. Which
 * sentence a gym operator reads is the whole of what makes this workflow usable
 * by somebody who has never heard of a sensor, and asserting it through a
 * rendered dialog would test the dialog. It lives here, it is pure, and it is
 * covered directly.
 */

export interface DescribedEnrollment {
  /** The short state label for a badge. */
  label: string
  tone: BadgeTone
  /** What is happening, as a heading. */
  headline: string
  /** What the operator should do or expect next. */
  detail: string
  /** True while the enrolment is still expected to happen. */
  live: boolean
}

/**
 * The six states, in the words an operator needs.
 *
 * NEVER A BARE CODE. "IN_PROGRESS" tells somebody at a front desk nothing.
 * Every sentence below says what is happening and what to do about it, and
 * three of them name the terminal, because "which door" is the question the
 * whole feature exists to answer.
 *
 * NO FIRMWARE VOCABULARY. Nothing here says sensor, serial port, job, sync or
 * command. The operator's model is: I asked that door to enrol this person; it
 * is waiting, or it worked, or it did not.
 */
export function describeEnrollment(enrollment: Enrollment): DescribedEnrollment {
  const terminal = enrollment.terminal_name || enrollment.terminal_serial || 'the terminal'
  const status: EnrollmentStatus = enrollment.status

  switch (status) {
    case 'PENDING':
      return {
        label: 'Waiting for terminal',
        tone: 'info',
        headline: `Waiting for ${terminal}`,
        detail:
          `${terminal} has not picked the request up yet. Terminals check in every ` +
          'minute or so, and it will start as soon as it does.',
        live: true,
      }

    case 'IN_PROGRESS':
      return {
        label: 'Ready — place finger',
        tone: 'positive',
        headline: `${terminal} is ready`,
        detail:
          `${terminal} is showing this person's name and waiting for a finger. Ask ` +
          'them to place the same finger on the reader when it prompts them.',
        live: true,
      }

    case 'COMPLETED':
      return {
        label: 'Enrolled',
        tone: 'positive',
        headline: 'Fingerprint enrolled',
        detail:
          `${terminal} captured the fingerprint and this person's credential is now ` +
          'enrolled. Whether they may actually come in is a separate question, ' +
          'decided by their access rules.',
        live: false,
      }

    case 'FAILED':
      return {
        label: 'Failed',
        tone: 'danger',
        headline: 'The enrolment did not work',
        detail:
          `${terminal} could not capture the fingerprint. The person is unchanged ` +
          'and still has no credential — you can try again here, or at a ' +
          'different terminal.',
        live: false,
      }

    case 'EXPIRED':
      return {
        label: 'Expired',
        tone: 'warning',
        headline: 'Nobody came to the terminal',
        detail:
          `The time window closed before a finger was placed on ${terminal}. Nothing ` +
          'was changed — start it again when the person is at a terminal.',
        live: false,
      }

    case 'CANCELLED':
      return {
        label: 'Cancelled',
        tone: 'neutral',
        headline: 'Enrolment cancelled',
        detail:
          'This enrolment was stopped, and no fingerprint was recorded. You can ' +
          'start a new one whenever the person is at a terminal.',
        live: false,
      }

    default:
      // A state this build predates. Shown plainly rather than crashing or
      // pretending it is finished — an unknown state is not a safe one to treat
      // as "you may start another".
      return {
        label: String(status),
        tone: 'neutral',
        headline: 'Enrolment state not recognised',
        detail:
          'This console does not know what that state means. Refresh, and if it ' +
          'persists, it is worth reporting.',
        live: false,
      }
  }
}

/**
 * The terminals that may be offered as a place to stand.
 *
 * THE SAME RULE THE SERVER ENFORCES, mirrored here so an operator is not offered
 * a choice that comes back as a 409. The server is authoritative — this decides
 * only what is worth showing, and the two are written to agree:
 *
 *   inactive       taken out of service. It refuses every authenticated call,
 *                  including the one that would fetch the enrolment request.
 *   DISABLED       the same thing, as the terminal's reported state.
 *   PROVISIONING   created but never registered, so it holds no credential of
 *                  its own and cannot authenticate to collect the request at
 *                  all. This is the clause the server spells as "has an
 *                  api_key_hash"; the console cannot see that column, and
 *                  PROVISIONING is exactly the state of a terminal without one.
 *
 * A RETIRED TERMINAL NEEDS NO CLAUSE. Retirement is a soft delete, so it is not
 * in this list — or in any other — to be filtered out.
 *
 * OFFLINE IS DELIBERATELY STILL OFFERED. A terminal that is merely unreachable
 * right now will pick the request up when it reconnects, and a site whose
 * terminals go offline between heartbeats is exactly the site that most needs
 * this feature. The dialog marks it, and the operator decides.
 */
export function enrollableTerminals(terminals: Terminal[]): Terminal[] {
  return terminals.filter(
    (terminal) =>
      terminal.active &&
      terminal.status !== 'DISABLED' &&
      terminal.status !== 'PROVISIONING',
  )
}
