import { useEffect } from 'react'
import { Outlet, useMatches } from 'react-router-dom'

/** What a route may declare about itself in its `handle`. */
export interface RouteHandle {
  /** The page's name in the browser tab, before the product suffix. */
  title?: string
}

export const TITLE_SUFFIX = 'AccessLink Console'

/** "People · AccessLink Console", or the suffix alone when nothing is named. */
export function documentTitle(title: string | undefined): string {
  return title ? `${title} · ${TITLE_SUFFIX}` : TITLE_SUFFIX
}

/**
 * Names the browser tab after the page, at the root of the route tree.
 *
 * DECLARED ON THE ROUTE, NOT IN EACH PAGE. A title belongs to where you are, and
 * router.tsx is the one file that lists every place there is -- a route added
 * without one shows the suffix alone, which is wrong in an obvious way rather
 * than in a stale one. The deepest match that names a title wins, so a detail
 * page can be more specific than the list it sits under.
 */
export function DocumentTitle() {
  const matches = useMatches()
  const title = [...matches]
    .reverse()
    .map((match) => (match.handle as RouteHandle | undefined)?.title)
    .find(Boolean)

  useEffect(() => {
    document.title = documentTitle(title)
  }, [title])

  return <Outlet />
}
