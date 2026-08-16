import { MAX_OFFLINE_GRACE_MINUTES, type OfflinePolicy } from '../../api/types'

/**
 * What each offline policy means for the people standing at the door.
 *
 * THIS IS A SAFETY DECISION AND THE PLATFORM HAS NO DEFAULT IT CAN PICK, which
 * is why the wording matters more than usual. Both directions are dangerous and
 * the danger is not symmetrical:
 *
 *   DENY_ALL wrong  →  a site is locked out during a network outage. Highly
 *                      visible, immediately reported, nobody gets in who should
 *                      not.
 *   CACHED_* wrong  →  a door keeps admitting whoever the terminal last heard
 *                      about, including somebody dismissed this morning, for as
 *                      long as the outage lasts. Invisible, and nobody reports
 *                      it because nothing appears to be broken.
 *
 * So each option below states the exposure in the operator's own terms rather
 * than naming the constant, and the consequence sits BESIDE the control rather
 * than in help text somewhere else. Somebody choosing between these is making a
 * decision about their building, not configuring a device.
 *
 * DOMAIN-NEUTRAL, deliberately. A terminal might hold a door, a turnstile, a
 * barrier or a locker; the policy says what it does when it cannot ask, not what
 * it is attached to.
 */

export interface OfflinePolicyDefinition {
  value: OfflinePolicy
  label: string
  /** One line, for a list. */
  summary: string
  /** What actually happens, and what it exposes. Shown beside the control. */
  consequence: string
  /** Whether the grace period means anything for this policy. */
  usesGrace: boolean
}

export const OFFLINE_POLICY_DEFINITIONS: OfflinePolicyDefinition[] = [
  {
    value: 'DENY_ALL',
    label: 'Refuse everybody',
    summary: 'Nothing opens while a terminal cannot reach the platform.',
    consequence:
      'The moment a terminal loses contact it refuses every presentation, including ' +
      'people who are plainly permitted. Nobody who has been withdrawn can get in, ' +
      'and nobody else can either — a network fault becomes a lockout that somebody ' +
      'will notice within minutes. Choose this where letting the wrong person ' +
      'through is worse than letting nobody through.',
    usesGrace: false,
  },
  {
    value: 'CACHED_GRACE',
    label: 'Keep working for a limited time',
    summary: 'Decides from the records it already holds, for a bounded period.',
    consequence:
      'The terminal keeps deciding from the people it already knows about, for the ' +
      'grace period below, and then refuses everybody. Somebody withdrawn during an ' +
      'outage may still be admitted until the grace period runs out, because the ' +
      'terminal has not been told. This is the middle answer: a short outage is ' +
      'invisible to the people using the building, and a long one closes it.',
    usesGrace: true,
  },
  {
    value: 'CACHED_INDEFINITE',
    label: 'Keep working indefinitely',
    summary: 'Decides from the records it already holds, for as long as the outage lasts.',
    consequence:
      'The terminal keeps deciding from the people it already knows about with no ' +
      'time limit. A site stays open through an outage of any length — and somebody ' +
      'withdrawn while a terminal is offline keeps getting in until it reconnects, ' +
      'however long that is. Choose this only where a lockout would be more ' +
      'dangerous than a stale roster.',
    usesGrace: false,
  },
]

export function offlinePolicyDefinition(policy: OfflinePolicy): OfflinePolicyDefinition {
  // Non-null: the union is closed and the list above is exhaustive over it, which
  // the type checker enforces at the call sites that build this list.
  return (
    OFFLINE_POLICY_DEFINITIONS.find((entry) => entry.value === policy) ??
    OFFLINE_POLICY_DEFINITIONS[0]!
  )
}

/**
 * Whether a policy uses the grace period.
 *
 * The control is hidden rather than disabled for the other two, because a number
 * shown beside a policy that ignores it reads as a setting that applies. An
 * operator who sets DENY_ALL and sees "720 minutes" underneath has been told
 * something false about their building.
 */
export function usesGracePeriod(policy: OfflinePolicy | ''): boolean {
  return policy === 'CACHED_GRACE'
}

/**
 * Validates a grace period against THE PLATFORM'S bound.
 *
 * 43,200 minutes is 30 days, and it is the number `models.MaxOfflineGraceMinutes`
 * carries, the number 016's `CHECK` constraint carries, and the number the
 * firmware's `kMaxOfflineGraceMinutes` carries. The console previously enforced
 * 10,080 — seven days — which was this build's own invention: it refused
 * configurations the platform accepts, with a message that named no real rule.
 *
 * Zero is legitimate and is NOT the same as DENY_ALL: it means the terminal
 * stops trusting its cache as soon as contact is lost, under a policy that could
 * later be widened without changing the policy itself.
 */
export function graceError(raw: string): string | undefined {
  const trimmed = raw.trim()
  if (trimmed === '') return 'A grace period is required when terminals keep working for a limited time.'

  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed)) return 'The grace period must be a whole number of minutes.'
  if (parsed < 0) return 'The grace period cannot be negative.'
  if (parsed > MAX_OFFLINE_GRACE_MINUTES) {
    return `The grace period must be at most ${MAX_OFFLINE_GRACE_MINUTES.toLocaleString()} minutes (30 days).`
  }
  return undefined
}

/**
 * A grace period as something a person can weigh.
 *
 * "43200 minutes" is not a duration anybody can reason about, and the whole
 * point of the control is that somebody decides how long their building should
 * keep running on stale information.
 */
export function describeGrace(minutes: number): string {
  if (minutes === 0) return 'no grace at all — it refuses as soon as contact is lost'
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'}`

  const hours = minutes / 60
  if (minutes % 60 === 0 && hours < 48) {
    return `${hours} hour${hours === 1 ? '' : 's'}`
  }

  const days = minutes / (60 * 24)
  if (minutes % (60 * 24) === 0) {
    return `${days} day${days === 1 ? '' : 's'}`
  }

  return `${Math.floor(hours)} hours ${minutes % 60} minutes`
}
