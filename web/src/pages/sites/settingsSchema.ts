/**
 * The site settings this build knows how to explain.
 *
 * THE PLATFORM DOES NOT FIX THIS KEY SET, and the console must not pretend it
 * does. `settings` is an open JSON object: firmware gains options over time, and
 * a customer's site may legitimately carry keys this build has never heard of.
 *
 * So this is a DESCRIPTION, not a schema. Recognised keys get a proper control;
 * everything else is preserved untouched and editable as raw JSON. A guided
 * editor that wrote back only what it recognised would silently delete a
 * colleague's configuration — and it would do it invisibly, because the write is
 * a full replacement rather than a merge.
 *
 * Descriptions are deliberately domain-neutral. A relay might release a door, a
 * turnstile, a barrier or a locker; the setting says how long it is held, not
 * what it is attached to.
 */

export type SettingKind = 'boolean' | 'integer'

export interface SettingDefinition {
  key: string
  label: string
  kind: SettingKind
  description: string
  /** Inclusive bounds for an integer, used for validation and the hint. */
  min?: number
  max?: number
  unit?: string
}

export const SETTING_DEFINITIONS: SettingDefinition[] = [
  {
    key: 'unlock_duration_seconds',
    label: 'Relay hold time',
    kind: 'integer',
    description:
      'How long the terminal holds its relay open after a successful check. Long values leave an opening unattended; short ones can close before somebody has passed through.',
    min: 1,
    max: 60,
    unit: 'seconds',
  },
  {
    key: 'sync_interval_seconds',
    label: 'Sync interval',
    kind: 'integer',
    description:
      'How often a terminal asks the platform for changes. Shorter means people and settings reach the hardware sooner, at the cost of more traffic from every terminal.',
    min: 10,
    max: 3600,
    unit: 'seconds',
  },
  {
    key: 'offline_grace_minutes',
    label: 'Offline grace period',
    kind: 'integer',
    description:
      'How long a terminal keeps operating on its local records after losing contact with the platform. This is what lets a site keep working through a network outage.',
    min: 0,
    max: 10_080,
    unit: 'minutes',
  },
  {
    key: 'tamper_alarm',
    label: 'Tamper alarm',
    kind: 'boolean',
    description: 'Raise an alert when a terminal detects that its enclosure has been opened.',
  },
]

const RECOGNISED = new Set(SETTING_DEFINITIONS.map((definition) => definition.key))

export function isRecognisedSetting(key: string): boolean {
  return RECOGNISED.has(key)
}

/**
 * Splits stored settings into the part this build can render and the part it
 * cannot.
 *
 * The `unknown` half is the whole reason this function exists: it is carried
 * through every save untouched, and shown to the operator so they know it is
 * there rather than discovering later that it vanished.
 */
export function partitionSettings(settings: Record<string, unknown>): {
  known: Record<string, unknown>
  unknown: Record<string, unknown>
} {
  const known: Record<string, unknown> = {}
  const unknown: Record<string, unknown> = {}

  for (const [key, value] of Object.entries(settings)) {
    if (RECOGNISED.has(key)) {
      known[key] = value
    } else {
      unknown[key] = value
    }
  }
  return { known, unknown }
}

/**
 * Rebuilds the complete settings object from the guided form plus everything the
 * form did not touch.
 *
 * THE SECOND ARGUMENT IS NOT OPTIONAL IN SPIRIT. `PUT` replaces the object
 * wholesale, so a caller that forgets to merge the unrecognised half deletes it
 * from the site — and from every terminal there, on the next sync.
 *
 * A recognised key whose form field is empty is OMITTED rather than written as
 * null: absent means "the firmware default applies", which is a different
 * instruction from "this is null" and is the one an operator clearing a field
 * intends.
 */
export function composeSettings(
  guided: Record<string, unknown>,
  preserved: Record<string, unknown>,
): Record<string, unknown> {
  const composed: Record<string, unknown> = { ...preserved }

  for (const [key, value] of Object.entries(guided)) {
    if (value === undefined || value === '') continue
    composed[key] = value
  }
  return composed
}

/** Formats a recognised value for its form control. */
export function toFieldValue(value: unknown): string {
  if (value === undefined || value === null) return ''
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  return String(value)
}

export interface SettingIssue {
  key: string
  message: string
}

/**
 * Validates the guided fields.
 *
 * Only the recognised keys are checked — the raw editor is the escape hatch for
 * everything else, and validating a value this build does not understand would
 * mean refusing configuration that is perfectly valid for the firmware.
 */
export function validateGuided(values: Record<string, string>): SettingIssue[] {
  const issues: SettingIssue[] = []

  for (const definition of SETTING_DEFINITIONS) {
    if (definition.kind !== 'integer') continue

    const raw = values[definition.key]
    if (raw === undefined || raw.trim() === '') continue

    const parsed = Number(raw)
    if (!Number.isInteger(parsed)) {
      issues.push({ key: definition.key, message: `${definition.label} must be a whole number.` })
      continue
    }
    if (definition.min !== undefined && parsed < definition.min) {
      issues.push({
        key: definition.key,
        message: `${definition.label} must be at least ${definition.min}.`,
      })
    }
    if (definition.max !== undefined && parsed > definition.max) {
      issues.push({
        key: definition.key,
        message: `${definition.label} must be at most ${definition.max}.`,
      })
    }
  }
  return issues
}
