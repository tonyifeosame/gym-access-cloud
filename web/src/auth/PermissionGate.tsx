import type { ReactNode } from 'react'

import { useSession } from '../session/useSession'
import { can, canReachSite, type Action } from './permissions'

/**
 * Shows children only when the operator may do something.
 *
 * NOT AN AUTHORIZATION BOUNDARY — the server enforces every one of these and
 * would refuse the request regardless. The purpose is to avoid offering a
 * control that can only produce a 403, which is a courtesy, not a defence.
 *
 * `fallback` is usually nothing. Where an absent control would be confusing —
 * a page that looks broken rather than restricted — pass an explanation instead
 * of leaving a hole.
 */
export function Can({
  action,
  children,
  fallback = null,
}: {
  action: Action
  children: ReactNode
  fallback?: ReactNode
}) {
  const { session } = useSession()
  return <>{can(session, action) ? children : fallback}</>
}

/**
 * Shows children only when the operator is scoped to a site.
 *
 * Mirrors RequireSiteGrant, including that an operator with NO grants is
 * unscoped and reaches everything. Takes the site's public id.
 */
export function CanReachSite({
  siteId,
  children,
  fallback = null,
}: {
  siteId: string
  children: ReactNode
  fallback?: ReactNode
}) {
  const { session } = useSession()
  if (!session || !canReachSite(session, siteId)) return <>{fallback}</>
  return <>{children}</>
}

/**
 * Why a control is missing, where its absence would otherwise read as a bug.
 *
 * Deliberately says what to do about it. "You do not have permission" is a dead
 * end; naming who can change it is not.
 */
export function RestrictedNote({ children }: { children: ReactNode }) {
  return <p className="restricted-note">{children}</p>
}
