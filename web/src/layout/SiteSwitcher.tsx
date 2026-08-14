import { ALL_SITES, useSiteContext } from '../context/SiteContext'

/**
 * Chooses which site the console is looking at.
 *
 * "All sites" is offered only when the operator actually reaches all of them --
 * for a site-scoped operator it would be an option the API refuses. The choice
 * is remembered per company in localStorage, and dropped on the way back in if
 * the grant behind it has since been revoked.
 *
 * Nothing consumes the selection yet: the screens that will are Phase 2. It is
 * built now because the context it depends on is part of the shell.
 */
export function SiteSwitcher() {
  const { grants, allSites, selected, selectSite, singleSite } = useSiteContext()

  if (singleSite) {
    const only = grants[0]
    return (
      <span className="site-switcher site-switcher--static" title="Your only site">
        {only ? only.site_name : 'No site access'}
      </span>
    )
  }

  return (
    <label className="site-switcher">
      <span className="site-switcher__label">Site</span>
      <select
        className="site-switcher__select"
        value={selected}
        onChange={(event) => selectSite(event.target.value)}
      >
        {allSites ? <option value={ALL_SITES}>All sites</option> : null}
        {grants.map((grant) => (
          <option key={grant.site_id} value={grant.site_id}>
            {grant.site_name}
          </option>
        ))}
      </select>
    </label>
  )
}
