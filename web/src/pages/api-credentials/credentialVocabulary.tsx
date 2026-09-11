import type { APICredential, APICredentialScope, APICredentialStatus } from '../../api/types'
import { Badge, type BadgeTone } from '../../components/Badge'

/**
 * The console's words for an integration credential.
 *
 * ONE PLACE, because the list, the detail page and the dialogs all describe the
 * same four states and the same seven scopes, and a reader moving between them
 * must not meet a different name for the same thing on each screen.
 */

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

const STATUS: Record<APICredentialStatus, { label: string; tone: BadgeTone; explain: string }> = {
  ACTIVE: { label: 'Active', tone: 'positive', explain: 'Accepted by the public API.' },
  IN_GRACE: {
    label: 'In grace',
    tone: 'warning',
    explain:
      'Rotated. This key still works until its grace period ends, then the replacement is the only one accepted.',
  },
  EXPIRED: {
    label: 'Expired',
    tone: 'neutral',
    explain: 'No longer accepted. Either it reached its expiry or its rotation grace ended.',
  },
  REVOKED: { label: 'Revoked', tone: 'danger', explain: 'Turned off by an administrator.' },
}

export function statusLabel(status: APICredentialStatus): string {
  return STATUS[status]?.label ?? status
}

export function statusExplanation(status: APICredentialStatus): string {
  return STATUS[status]?.explain ?? ''
}

export function CredentialStatusBadge({ status }: { status: APICredentialStatus }) {
  const spec = STATUS[status] ?? { label: status, tone: 'neutral' as BadgeTone }
  return <Badge tone={spec.tone}>{spec.label}</Badge>
}

/** Whether the key is still accepted somewhere, i.e. whether rotate/revoke mean anything. */
export function isLive(credential: Pick<APICredential, 'status'>): boolean {
  return credential.status === 'ACTIVE' || credential.status === 'IN_GRACE'
}

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

export interface ScopeDefinition {
  scope: APICredentialScope
  label: string
  /** The server registry's own description, so the two cannot disagree. */
  description: string
  /**
   * WHETHER THE PUBLIC API HAS A ROUTE FOR IT TODAY. The server registry lists
   * every scope it can express and will issue a key carrying any of them, but
   * only members:read and sites:read have endpoints in this version. A key
   * issued with the others answers 403 or 404 to everything, which is a
   * credential that cannot do anything -- so the issue form does not offer
   * them, and says why.
   */
  available: boolean
}

export const SCOPES: ScopeDefinition[] = [
  {
    scope: 'members:read',
    label: 'Read members',
    description: 'Read the people on your roster.',
    available: true,
  },
  {
    scope: 'sites:read',
    label: 'Read sites',
    description: 'Read your sites.',
    available: true,
  },
  {
    scope: 'members:write',
    label: 'Change members',
    description: 'Add, change and remove people on your roster.',
    available: false,
  },
  {
    scope: 'terminals:read',
    label: 'Read terminals',
    description: 'Read your terminals and whether they are online.',
    available: false,
  },
  {
    scope: 'events:read',
    label: 'Read events',
    description: 'Read the record of who was admitted and refused.',
    available: false,
  },
  {
    scope: 'access:read',
    label: 'Read access standing',
    description: "Read a person's access standing and where it applies.",
    available: false,
  },
  {
    scope: 'webhooks:manage',
    label: 'Manage webhooks',
    description: 'Register and manage endpoints that receive your events.',
    available: false,
  },
]

export const AVAILABLE_SCOPES = SCOPES.filter((definition) => definition.available)
export const UNAVAILABLE_SCOPES = SCOPES.filter((definition) => !definition.available)

export function scopeLabel(scope: string): string {
  return SCOPES.find((definition) => definition.scope === scope)?.label ?? scope
}

export function ScopeChips({ scopes }: { scopes: string[] }) {
  if (scopes.length === 0) return <span className="muted">None</span>
  return (
    <ul className="chip-list">
      {scopes.map((scope) => (
        <li key={scope}>
          <span className="chip" title={scope}>
            {scopeLabel(scope)}
          </span>
        </li>
      ))}
    </ul>
  )
}
