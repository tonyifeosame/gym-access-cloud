import { humaniseCode } from '../../components/Badge'

/**
 * How an audit record READS.
 *
 * The server's action names are verbs in the past tense —
 * `TERMINAL_CREDENTIAL_REVOKED` — which is exactly right for a stored column and
 * unreadable in a table an operator is scanning under pressure. This module is
 * the one place that turns them into sentences.
 *
 * TWO RULES, AND BOTH EXIST BECAUSE THE ALTERNATIVE IS SILENT DATA LOSS:
 *
 *   1. AN UNKNOWN ACTION STILL RENDERS. The column is deliberately
 *      unconstrained server-side — an application may define its own events —
 *      so a console that only understood a fixed list would show blank rows for
 *      everything it had not been taught. Anything unrecognised is humanised
 *      and shown plainly.
 *
 *   2. NOTHING IS EVER FILTERED OUT for being unfamiliar. An audit trail that
 *      quietly omits records is worse than no audit trail, because it looks
 *      complete.
 *
 * The tone is about SEVERITY OF CONSEQUENCE, not about success or failure —
 * every record here describes something that happened. Revoking a credential and
 * retiring a terminal are marked because they are the ones somebody scanning for
 * "what broke the door" is looking for.
 */

export type AuditTone = 'neutral' | 'notable' | 'destructive'

interface ActionDefinition {
  label: string
  tone: AuditTone
}

const ACTIONS: Record<string, ActionDefinition> = {
  // Sites
  SITE_CREATED: { label: 'Site created', tone: 'notable' },
  SITE_UPDATED: { label: 'Site updated', tone: 'neutral' },
  SITE_RETIRED: { label: 'Site retired', tone: 'destructive' },
  SITE_KEY_ROTATED: { label: 'Site key rotated', tone: 'destructive' },
  SITE_SETTINGS_UPDATED: { label: 'Site settings updated', tone: 'neutral' },

  // Terminals
  TERMINAL_DISABLED: { label: 'Terminal disabled', tone: 'notable' },
  TERMINAL_ENABLED: { label: 'Terminal re-enabled', tone: 'neutral' },
  TERMINAL_CREDENTIAL_REVOKED: { label: 'Terminal credential revoked', tone: 'destructive' },
  TERMINAL_RETIRED: { label: 'Terminal retired', tone: 'destructive' },
  TERMINAL_MOVED: { label: 'Terminal moved to another site', tone: 'notable' },
  TERMINAL_RESYNCED: { label: 'Terminal resynced', tone: 'neutral' },
  TERMINAL_MODE_SET: { label: 'Terminal application changed', tone: 'neutral' },

  // People
  PERSON_CREATED: { label: 'Person added', tone: 'neutral' },
  PERSON_UPDATED: { label: 'Person updated', tone: 'neutral' },
  PERSON_DELETED: { label: 'Person removed', tone: 'destructive' },

  // Credentials
  CREDENTIAL_ISSUED: { label: 'Credential issued', tone: 'notable' },
  CREDENTIAL_REVOKED: { label: 'Credential revoked', tone: 'destructive' },

  // Operators
  OPERATOR_CREATED: { label: 'Operator created', tone: 'notable' },
  OPERATOR_INVITED: { label: 'Operator invited', tone: 'notable' },
  OPERATOR_UPDATED: { label: 'Operator updated', tone: 'neutral' },
  OPERATOR_ROLE_CHANGED: { label: 'Operator role changed', tone: 'notable' },
  OPERATOR_DELETED: { label: 'Operator removed', tone: 'destructive' },
  OPERATOR_SITES_CHANGED: { label: 'Operator site access changed', tone: 'notable' },
  OPERATOR_PASSWORD_RESET: { label: 'Operator password set by an administrator', tone: 'notable' },
  OPERATOR_RESET_ISSUED: { label: 'Password reset link issued', tone: 'notable' },
  OPERATOR_RESET_REQUESTED: { label: 'Password reset requested', tone: 'neutral' },
  OPERATOR_CREDENTIAL_REDEEMED: { label: 'Password set from a link', tone: 'notable' },

  // Applications and configuration
  APPLICATION_CONFIGURED: { label: 'Application configured', tone: 'notable' },
  PERMISSION_CREATED: { label: 'Permission created', tone: 'notable' },
  PERMISSION_DELETED: { label: 'Permission removed', tone: 'destructive' },
  SCHEDULE_CONFIGURED: { label: 'Schedule configured', tone: 'neutral' },

  // Firmware
  FIRMWARE_PUBLISHED: { label: 'Firmware published', tone: 'neutral' },
  FIRMWARE_TARGET_SET: { label: 'Firmware target changed', tone: 'notable' },

  // Company. Written by the PLATFORM surface against the tenant's own trail, so
  // a customer can answer "who created this company" without asking their vendor.
  COMPANY_CREATED: { label: 'Company created', tone: 'notable' },
  COMPANY_UPDATED: { label: 'Company updated', tone: 'neutral' },
  COMPANY_DEACTIVATED: { label: 'Company suspended', tone: 'destructive' },
  COMPANY_REACTIVATED: { label: 'Company reactivated', tone: 'notable' },
  COMPANY_FIRST_OPERATOR_CREATED: { label: 'First operator created', tone: 'notable' },
}

export function describeAction(action: string): ActionDefinition {
  return ACTIONS[action] ?? { label: humaniseCode(action), tone: 'neutral' }
}

/** Whether this build recognises an action, for marking the ones it does not. */
export function isKnownAction(action: string): boolean {
  return action in ACTIONS
}

/**
 * The actions offered in the filter.
 *
 * A CONVENIENCE OVER THE KNOWN SET, not a claim about what exists. The server
 * accepts any string, and a record whose action is absent from this list is
 * still returned by an unfiltered query — which is why the filter has an "any"
 * option and why nothing here narrows the default view.
 */
export function filterableActions(): { value: string; label: string }[] {
  return Object.entries(ACTIONS)
    .map(([value, definition]) => ({ value, label: definition.label }))
    .sort((a, b) => a.label.localeCompare(b.label))
}

const TARGET_TYPES = [
  'SITE',
  'TERMINAL',
  'PERSON',
  'CREDENTIAL',
  'OPERATOR',
  'APPLICATION',
  'PERMISSION',
  'SCHEDULE',
  'FIRMWARE',
  'COMPANY',
] as const

export function filterableTargets(): { value: string; label: string }[] {
  return TARGET_TYPES.map((value) => ({ value, label: humaniseCode(value) }))
}

/**
 * `changes` as readable pairs.
 *
 * The column is free-form JSON, redacted server-side, and it is rendered as DATA
 * rather than interpreted: this returns pairs for display and never decides what
 * a key means. A console that special-cased particular keys would silently drop
 * the ones it had not been taught, which is the same failure as a closed action
 * list and matters more here — the changes are usually the reason somebody
 * opened the record.
 */
export function readChanges(changes: unknown): { key: string; value: string }[] {
  if (!changes || typeof changes !== 'object' || Array.isArray(changes)) return []

  return Object.entries(changes as Record<string, unknown>).map(([key, value]) => ({
    key: humaniseCode(key),
    value:
      typeof value === 'string'
        ? value
        : typeof value === 'number' || typeof value === 'boolean'
          ? String(value)
          : value === null
            ? '—'
            : JSON.stringify(value),
  }))
}
