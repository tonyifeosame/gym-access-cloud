import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'

import type { SiteGrant } from '../api/types'
import { useAuthenticatedSession } from '../session/useSession'

/** The selection meaning "not narrowed to one site". */
export const ALL_SITES = 'ALL' as const

export type SiteSelection = typeof ALL_SITES | string

export interface SiteContextValue {
  /** Sites the operator is explicitly granted. Empty when unscoped. */
  grants: SiteGrant[]
  /**
   * True when the operator reaches every site in the company: always for OWNER
   * and ADMIN, and for anyone holding no grants.
   *
   * AN EMPTY `grants` ARRAY WITH THIS SET MEANS EVERY SITE, NOT NONE. Getting
   * that backwards would show an operator an empty console and tell them they
   * have no access, when in fact they have all of it.
   */
  allSites: boolean
  selected: SiteSelection
  selectedSite: SiteGrant | null
  selectSite: (selection: SiteSelection) => void
  /** True when there is nothing meaningful to switch between. */
  singleSite: boolean
}

const SiteContext = createContext<SiteContextValue | null>(null)

/** Per-company, so switching companies later cannot inherit a stale selection. */
function storageKey(companyId: string): string {
  return `accesslink.site.${companyId}`
}

function readStored(companyId: string): SiteSelection | null {
  try {
    return window.localStorage.getItem(storageKey(companyId))
  } catch {
    // Private browsing, or storage disabled. A remembered site is a
    // convenience; losing it must not stop the console working.
    return null
  }
}

function writeStored(companyId: string, selection: SiteSelection): void {
  try {
    window.localStorage.setItem(storageKey(companyId), selection)
  } catch {
    /* see readStored */
  }
}

export function SiteProvider({ children }: { children: ReactNode }) {
  const session = useAuthenticatedSession()
  const companyId = session.company.id
  const grants = session.sites
  const allSites = session.all_sites

  const [selected, setSelected] = useState<SiteSelection>(() => {
    const stored = readStored(companyId)

    // A remembered site is only honoured if it is still granted. Grants can be
    // revoked between visits, and restoring one that no longer applies would
    // put the console into a scope the API would then refuse.
    if (stored && stored !== ALL_SITES && grants.some((grant) => grant.site_id === stored)) {
      return stored
    }
    if (stored === ALL_SITES && allSites) {
      return ALL_SITES
    }

    // Default: everything when unscoped, otherwise the only site they have.
    if (allSites) return ALL_SITES
    return grants[0]?.site_id ?? ALL_SITES
  })

  const selectSite = useCallback(
    (selection: SiteSelection) => {
      setSelected(selection)
      writeStored(companyId, selection)
    },
    [companyId],
  )

  const value = useMemo<SiteContextValue>(() => {
    const selectedSite =
      selected === ALL_SITES ? null : (grants.find((grant) => grant.site_id === selected) ?? null)

    return {
      grants,
      allSites,
      selected,
      selectedSite,
      selectSite,
      singleSite: !allSites && grants.length <= 1,
    }
  }, [allSites, grants, selectSite, selected])

  return <SiteContext.Provider value={value}>{children}</SiteContext.Provider>
}

export function useSiteContext(): SiteContextValue {
  const context = useContext(SiteContext)
  if (!context) {
    throw new Error('useSiteContext must be used inside a SiteProvider')
  }
  return context
}
