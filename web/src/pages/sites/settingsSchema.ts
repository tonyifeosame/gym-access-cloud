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
 *
 * TWO KEYS WERE REMOVED FROM THE GUIDED FORM AND ARE NOT COMING BACK, because a
 * control that changes nothing is worse than no control: the operator makes a
 * decision, sees it saved, and is never told it had no effect.
 *
 *   offline_grace_minutes  Superseded by a validated field on the site itself.
 *                          A value here was IGNORED — the platform layers the
 *                          column over this object on the way to a terminal — so
 *                          the form was collecting a number nothing read, and the
 *                          server now refuses the key outright. Its bound was
 *                          wrong too: this build enforced 10,080 minutes where
 *                          the platform and the firmware both allow 43,200. See
 *                          OfflinePolicyPanel.
 *   tamper_alarm           The firmware has no tamper detection at all: no
 *                          input, no event, nothing that reads the value. It was
 *                          a switch for hardware behaviour that does not exist.
 *
 * What happens to each on the next save differs, and SUPERSEDED_SETTINGS below
 * is where that is decided.
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
]

/**
 * Keys this console DELIBERATELY NO LONGER EDITS, and why.
 *
 * A third category, distinct from both of the others, because collapsing it into
 * either would mislead. Left in `SETTING_DEFINITIONS` they would be controls that
 * do nothing; dropped into the unrecognised bucket they would be described as
 * "newer than this version of the console", which is the opposite of true.
 *
 * THE TWO ARE NOT THE SAME KIND OF DEAD, and the difference decides what happens
 * to them on the next save:
 *
 *   inert    The platform accepts the key and nothing reads it. Preserved, as
 *            every unrecognised key is — removing configuration somebody wrote,
 *            because this build has decided it is pointless, is not the
 *            console's call.
 *   refused  The platform REJECTS a write containing the key, with a 400 and
 *            `code: "RESERVED_SETTINGS_KEY"`. It cannot be preserved: sending it
 *            back would fail the whole save. So it is dropped, and the panel says
 *            so before the operator presses anything.
 */
export type SettingDisposition = 'inert' | 'refused'

export interface SupersededSetting {
  key: string
  label: string
  disposition: SettingDisposition
  /** Why there is no longer a control for it. Shown to the operator. */
  reason: string
}

export const SUPERSEDED_SETTINGS: SupersededSetting[] = [
  {
    key: 'offline_policy',
    label: 'Offline policy',
    disposition: 'refused',
    reason:
      'The platform refuses a write containing this key. It is a validated field on ' +
      'the site itself, and a copy here would be ignored — the real value is layered ' +
      'over this object on its way to a terminal. Set it under “Behaviour during an ' +
      'outage” above. Saving from this panel will drop the stale copy, which changes ' +
      'nothing about what your terminals do.',
  },
  {
    key: 'offline_grace_minutes',
    label: 'Offline grace period',
    disposition: 'refused',
    reason:
      'The platform refuses a write containing this key, for the same reason as the ' +
      'policy it belongs to: the real value is a validated field on the site and this ' +
      'copy was never read. Set it under “Behaviour during an outage” above, where it ' +
      'is bounded and reaches the hardware. Saving from this panel will drop the stale ' +
      'copy.',
  },
  {
    key: 'tamper_alarm',
    label: 'Tamper alarm',
    disposition: 'inert',
    reason:
      'The firmware does not implement tamper detection. There is no tamper input, no ' +
      'tamper event and nothing that reads this value, so switching it on raised no ' +
      'alert and switching it off disabled nothing. It is not offered as a control ' +
      'because presenting unbuilt hardware behaviour as configuration is how a ' +
      'building ends up relying on protection it does not have. It is left in place ' +
      'rather than deleted.',
  },
]

const RECOGNISED = new Set(SETTING_DEFINITIONS.map((definition) => definition.key))
const SUPERSEDED = new Set(SUPERSEDED_SETTINGS.map((definition) => definition.key))

/** Keys the platform rejects outright. Mirrors `models.RejectReservedSettingsKeys`. */
export const REFUSED_SETTINGS = SUPERSEDED_SETTINGS.filter(
  (definition) => definition.disposition === 'refused',
).map((definition) => definition.key)

export function isRecognisedSetting(key: string): boolean {
  return RECOGNISED.has(key)
}

export function isRefusedSetting(key: string): boolean {
  return REFUSED_SETTINGS.includes(key)
}

export function supersededSetting(key: string): SupersededSetting | undefined {
  return SUPERSEDED_SETTINGS.find((definition) => definition.key === key)
}

/**
 * Splits stored settings three ways.
 *
 *   known       this build offers a control for it
 *   superseded  this build deliberately does not, and can say why
 *   unknown     this build has never heard of it
 *
 * `unknown` IS CARRIED THROUGH EVERY SAVE UNTOUCHED, which is the original
 * reason this function exists — `PUT` replaces the object wholesale, so a guided
 * form that wrote back only what it recognised would delete the rest invisibly,
 * and from every terminal at the site on the next sync.
 *
 * `superseded` is split again at save time by disposition: see
 * `preservableSettings` below.
 */
export function partitionSettings(settings: Record<string, unknown>): {
  known: Record<string, unknown>
  superseded: Record<string, unknown>
  unknown: Record<string, unknown>
} {
  const known: Record<string, unknown> = {}
  const superseded: Record<string, unknown> = {}
  const unknown: Record<string, unknown> = {}

  for (const [key, value] of Object.entries(settings)) {
    if (RECOGNISED.has(key)) {
      known[key] = value
    } else if (SUPERSEDED.has(key)) {
      superseded[key] = value
    } else {
      unknown[key] = value
    }
  }
  return { known, superseded, unknown }
}

/**
 * Everything the guided form does not touch AND may legally be written back.
 *
 * THE ONE THING THIS DROPS IS THE ONE THING THAT CANNOT BE KEPT. A stored
 * `offline_policy` or `offline_grace_minutes` is refused by the server, so a save
 * that faithfully preserved it would fail — and the failure would be a 400 on a
 * form the operator had only used to change a relay timing, with a message about
 * a key they never typed.
 *
 * Dropping it loses nothing: the value was already being ignored in favour of the
 * validated column. The panel says the save will remove it rather than letting it
 * happen quietly.
 */
export function preservableSettings(
  superseded: Record<string, unknown>,
  unknown: Record<string, unknown>,
): Record<string, unknown> {
  const keepable: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(superseded)) {
    if (!isRefusedSetting(key)) keepable[key] = value
  }
  return { ...keepable, ...unknown }
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
