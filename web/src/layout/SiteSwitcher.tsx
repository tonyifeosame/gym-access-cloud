import { ALL_SITES, useSiteContext } from '../context/SiteContext'

/**
 * The site scope: what it governs, and where it is therefore allowed to sit.
 *
 * ---------------------------------------------------------------------------
 * WHY THE SELECT IS NO LONGER IN THE TOP BAR
 * ---------------------------------------------------------------------------
 *
 * A control in the shell's top bar, above the navigation and present on every
 * screen, makes exactly one claim: it governs the console. This one did not.
 * `SiteContext.selected` has one reader in the entire application — the
 * overview — and every other screen ignores it completely. So a scoped operator
 * who picked a site and then opened Terminals, People, Events or Activity saw
 * the whole company, with a control still sitting at the top of the page
 * reading the name of one site.
 *
 * That is worse than having no control at all, because the failure is silent
 * and confident: nothing errors, the pages render, and the numbers are simply
 * about a different set of doors than the reader believes. On Events it is a
 * safety question — "was anybody refused here today" answered over every site.
 *
 * ---------------------------------------------------------------------------
 * WHY IT WAS NOT MADE GLOBAL INSTEAD
 * ---------------------------------------------------------------------------
 *
 * Because it cannot honestly be, with the APIs that exist:
 *
 *   PEOPLE HAVE NO SITE. The schema carries no person-to-site relationship —
 *   `PeopleListPage` says so on its own screen — so there is nothing to narrow
 *   that list by. A global scope would have to silently do nothing there.
 *
 *   ACTIVITY IS NOT SITE-FILTERABLE. The audit endpoint takes an actor, an
 *   action, a target type and a date range. No site.
 *
 *   TERMINALS AND EVENTS ALREADY HAVE THEIR OWN SITE FILTER, in their own
 *   toolbars, over their own rows. Driving those from the top bar would put two
 *   site controls on one screen — which is the exact confusion this component's
 *   previous fix was written to remove.
 *
 * Inventing a scope that means "narrow these two pages, silently do nothing on
 * those two, and be unrepresentable on the fifth" would be a new filtering
 * semantic rather than a fix. So the control moved to the one screen that reads
 * it, and now claims precisely what it does.
 *
 * ---------------------------------------------------------------------------
 * WHAT DID NOT CHANGE
 * ---------------------------------------------------------------------------
 *
 * The SELECTION and everything under it: `SiteProvider` still defaults an
 * unscoped operator to ALL_SITES, still remembers the choice per company, and
 * still drops a remembered site whose grant has been revoked. Authorization is
 * untouched — the options are still built from the operator's own grants, and
 * "All sites" is still offered only to somebody who reaches all of them.
 */

/**
 * WHERE THIS OPERATOR IS, for somebody who has no choice about it.
 *
 * A site-scoped operator holding exactly one grant has no scope to set, but the
 * fact of it is real context and belongs in the chrome: every screen they open
 * genuinely is that one site, because the API enforces the grant on every
 * request. This states it and offers no control, which is the honest shape for
 * a scope somebody cannot change.
 *
 * Renders nothing for everybody else. An owner or administrator reaches every
 * site, so a label naming one would be false, and a label reading "all sites"
 * on every screen is chrome that says nothing.
 */
export function SiteIndicator() {
  const { grants, singleSite } = useSiteContext()

  if (!singleSite) return null

  const only = grants[0]
  return (
    <span className="site-switcher site-switcher--static" title="Your only site">
      {only ? only.site_name : 'No site access'}
    </span>
  )
}

/**
 * The scope control for the overview, rendered by the overview.
 *
 * IT IS RENDERED ONLY WHEN IT IS A CHOICE. `all_sites` is true for every OWNER
 * and ADMIN and their `grants` array is empty — they reach every site by role
 * rather than by grant — so the options came to exactly one, and the shell used
 * to put a single-option select box in the top bar of every screen for every
 * owner of every company. A selection that can only have one value was never a
 * selection.
 *
 * Labelled for what it narrows rather than "Site", so that the one screen it
 * governs says so out loud instead of leaving the reader to infer a scope from
 * a control's position.
 */
export function SiteScopeSelect() {
  const { grants, allSites, selected, selectSite } = useSiteContext()

  // What the operator could actually pick between: "all of them", when they
  // reach all of them, plus each site they are explicitly granted.
  const choices = (allSites ? 1 : 0) + grants.length
  if (choices <= 1) return null

  return (
    <label className="site-switcher">
      <span className="site-switcher__label">Showing</span>
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
