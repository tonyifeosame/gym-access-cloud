import type { ReactNode } from 'react'

import { MULTI_PURPOSE, type ApplicationCode, type TerminalStatus } from '../api/types'
import { describeApplication } from '../applications/registry'

/**
 * Status pills.
 *
 * COLOUR IS NEVER THE ONLY SIGNAL. Every badge carries its own text, so the
 * information survives a monochrome display, a projector, and the ~8% of men
 * with a colour vision deficiency. A red dot that means "error" and nothing else
 * is a decoration, not a status.
 */

export type BadgeTone = 'neutral' | 'positive' | 'warning' | 'danger' | 'info'

export function Badge({ tone = 'neutral', children }: { tone?: BadgeTone; children: ReactNode }) {
  return <span className={`badge badge--${tone}`}>{children}</span>
}

/**
 * How each terminal state reads.
 *
 * OFFLINE is a WARNING rather than an error: a terminal is expected to be
 * offline sometimes — power cycled, network down for a moment — and it keeps
 * working at the door while it is, because the roster is local. ERROR is the
 * state that means something needs a person. Overstating offline would train
 * operators to ignore the colour that matters.
 *
 * An unknown status still renders, humanised and neutral. Firmware may report a
 * state this build predates, and hiding it would be worse than showing it plain.
 */
const STATUS_TONES: Record<string, BadgeTone> = {
  ONLINE: 'positive',
  OFFLINE: 'warning',
  UPDATING: 'info',
  ERROR: 'danger',
  DISABLED: 'neutral',
  PROVISIONING: 'info',
}

/**
 * The customer's word for each state.
 *
 * "Provisioning" and "Error" are the server's enum values and were shown as
 * written. To somebody running a site, a terminal that is "provisioning" is
 * one that is not set up yet, and "Error" says less than "Fault" -- a fault is
 * something on the unit that needs a person, which is what ERROR means here.
 * NOTHING ABOUT THE STATES CHANGED: the values, the tones and the filters are
 * keyed on the server's words; only the label read by a person is different.
 */
const STATUS_LABELS: Record<string, string> = {
  ONLINE: 'Online',
  OFFLINE: 'Offline',
  UPDATING: 'Updating…',
  ERROR: 'Fault',
  DISABLED: 'Disabled',
  PROVISIONING: 'Not set up yet',
}

export function humaniseCode(code: string): string {
  return code
    .toLowerCase()
    .split('_')
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

/**
 * The label the badge shows, for anywhere that names a status without drawing
 * one -- the terminals list's status filter reads it, so the option a person
 * picks is worded exactly as the badge in the rows it selects.
 */
export function terminalStatusLabel(status: string): string {
  return STATUS_LABELS[status] ?? humaniseCode(status)
}

export function TerminalStatusBadge({ status }: { status: TerminalStatus }) {
  return <Badge tone={STATUS_TONES[status] ?? 'neutral'}>{terminalStatusLabel(status)}</Badge>
}

/**
 * Whether a record is active.
 *
 * "Inactive" rather than "deleted": these are soft states the operator can
 * reverse, and calling an inactive person deleted would misdescribe what
 * happened to them.
 */
export function ActiveBadge({ active }: { active: boolean }) {
  return <Badge tone={active ? 'positive' : 'neutral'}>{active ? 'Active' : 'Inactive'}</Badge>
}

/**
 * Whether a person has a biometric credential enrolled.
 *
 * A BOOLEAN, AND NOTHING MORE. No template, no locator, no sensor or vendor
 * detail — the credential is an abstraction the backend owns, and the console
 * reports only whether one exists. This component is the only place the console
 * says anything about biometrics at all, which is what keeps that boundary from
 * eroding one convenient field at a time.
 */
export function BiometricBadge({ enrolled }: { enrolled: boolean }) {
  return (
    <Badge tone={enrolled ? 'positive' : 'neutral'}>{enrolled ? 'Enrolled' : 'Not enrolled'}</Badge>
  )
}

/**
 * What a terminal is assigned to do, and whether that resolves to anything.
 *
 * The two can disagree: a terminal keeps its assignment when the company turns
 * the feature off, and `effective` goes empty. That case is called out rather
 * than hidden, because a terminal that looks configured and does nothing is
 * precisely the situation an operator needs to be told about.
 *
 * THE NAME COMES FROM THE REGISTRY, NOT FROM `humaniseCode`. The two agree on
 * ACCESS_CONTROL and disagree where it matters: humanising gives "Check In" and
 * "Multi Purpose" where the registry and the terminal page say "Check-in" and
 * "Multi-purpose". A general code-humaniser is the right tool for a status enum
 * this console does not name; feature names ARE named, in one place, and reading
 * them from anywhere else is how a second set of them starts.
 */
export function ApplicationModeBadge({
  mode,
  effective,
}: {
  mode: string
  effective: string[]
}) {
  const resolves = effective.length > 0
  const label =
    mode === MULTI_PURPOSE ? 'Multi-purpose' : describeApplication(mode as ApplicationCode).label

  return (
    <span className="badge-group">
      <Badge tone={resolves ? 'info' : 'warning'}>{label}</Badge>
      {!resolves ? <span className="badge__note">not turned on for this company</span> : null}
    </span>
  )
}
