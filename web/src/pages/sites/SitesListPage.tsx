import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { Site } from '../../api/types'
import { can } from '../../auth/permissions'
import { ActiveBadge, Badge } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { SearchInput } from '../../components/Pagination'
import { PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useSites } from '../../data/console'
import { useSession } from '../../session/useSession'
import { offlinePolicyDefinition } from './offlinePolicy'
import { SiteFormDialog } from './SiteFormDialog'

/**
 * Every site this operator can reach.
 *
 * NARROWED BY THE API, not here — twice over.
 *
 * A site-scoped operator is served only their granted sites, so this renders
 * whatever came back rather than filtering a fuller list client-side, which
 * would mean the browser had briefly held sites the operator is not entitled to.
 *
 * SEARCH IS SERVER-SIDE FOR THE SAME REASON IT IS ON THE PEOPLE LIST: matching
 * the fetched array would search what one response happened to carry rather than
 * the estate. That is silently wrong for a customer with more locations than the
 * screen was built around, and silently right in every test with two fixtures.
 * The scope predicate and the search predicate are applied in the same
 * statement, so a term narrows within the grant and can never widen it.
 *
 * A site is domain-neutral: a location with terminals at it. An office, a
 * campus, a warehouse, a venue. Nothing here assumes which.
 */
export function SitesListPage() {
  const { session } = useSession()
  const navigate = useNavigate()
  const [search, setSearch] = useState('')
  const [creating, setCreating] = useState(false)

  const query = useSites({ search })

  // ADMIN, matching the server. The gate is a courtesy: the API refuses
  // anything this wrongly permitted.
  const mayManage = can(session, 'manageSites')
  const searching = search.trim() !== ''

  const columns: Column<Site>[] = [
    {
      id: 'name',
      header: 'Site',
      primary: true,
      render: (site) => (
        <Link to={`/sites/${site.id}`} className="table__link">
          {site.name}
        </Link>
      ),
    },
    {
      id: 'address',
      header: 'Address',
      secondary: true,
      render: (site) => site.address || <span className="muted">—</span>,
    },
    {
      id: 'timezone',
      header: 'Time zone',
      secondary: true,
      render: (site) => <code className="mono">{site.timezone}</code>,
    },
    {
      id: 'status',
      header: 'Status',
      render: (site) => <ActiveBadge active={site.active} />,
    },
    {
      id: 'terminals',
      header: 'Terminals',
      align: 'end',
      render: (site) => site.terminal_count,
    },
    {
      id: 'offline',
      header: 'During an outage',
      // The one column here that is about what a DOOR does rather than about a
      // record, and the reason it is worth a column at all: "which of our
      // locations keeps opening when the network goes" is a question somebody
      // asks about a whole estate, and answering it by visiting each site in
      // turn is how it stops being asked.
      //
      // DENY_ALL reads as the neutral, cautious state; both cached policies are
      // marked, because both mean a door can admit somebody who has been
      // withdrawn. That is not an error — it is frequently the right choice —
      // so it is a warning tone and never a danger one.
      render: (site) => (
        <Badge tone={site.offline_policy === 'DENY_ALL' ? 'info' : 'warning'}>
          {offlinePolicyDefinition(site.offline_policy).label}
        </Badge>
      ),
    },
    /*
      NO "KEY" COLUMN. It rendered the first characters of a site's provisioning
      key, and it could never render anything: `models.ConsoleSite` has no such
      field and `consoleSiteColumns` does not select one, so no read carries a
      prefix and no cache write puts one there. Every row showed an em dash, in
      every session, for ever.

      It looked alive in development only because the test mock stored a prefix
      on the site after a create or a rotation, which the real API does not.
      The mock no longer does either.
    */
    {
      id: 'created',
      header: 'Added',
      secondary: true,
      render: (site) => <Timestamp value={site.created_at} />,
    },
  ]

  return (
    <div className="page">
      <PageHeader
        title="Sites"
        lead="Locations with terminals installed at them."
        actions={
          mayManage ? (
            <button type="button" className="button button--primary" onClick={() => setCreating(true)}>
              Add a site
            </button>
          ) : null
        }
      />

      <div className="toolbar">
        <SearchInput
          label="Search sites"
          placeholder="Name or address"
          value={search}
          onChange={setSearch}
          busy={query.isFetching && !query.isPending}
        />
        <RefreshingIndicator active={query.isFetching && !query.isPending} />
      </div>

      <DataTable<Site>
        caption="Sites"
        columns={columns}
        rows={query.data?.sites}
        rowKey={(site) => site.id}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        error={query.isError ? query.error : null}
        onRetry={() => void query.refetch()}
        onRowClick={(site) => navigate(`/sites/${site.id}`)}
        // "Nothing matched" and "no sites yet" are different facts, and telling a
        // company with a full estate that it has no locations is the worse one
        // to get wrong.
        emptyTitle={searching ? 'No sites match that search' : 'No sites yet'}
        emptyDescription={
          searching
            ? 'Search matches a site name or an address, anywhere in the value.'
            : mayManage
              ? // NO SECOND DEFINITION. What a site is, is the page lead directly
                // above; saying it again here — and a third time in the dialog this
                // button opens — was the same sentence three times in two clicks.
                // What is left is the next step, which is the part somebody
                // standing on an empty page does not know.
                'Add your first one, then add terminals to it.'
              : 'No sites have been set up for your company yet, or none have been shared with you.'
        }
        emptyAction={
          searching ? (
            <button type="button" className="button" onClick={() => setSearch('')}>
              Clear search
            </button>
          ) : mayManage ? (
            <button type="button" className="button button--primary" onClick={() => setCreating(true)}>
              Add a site
            </button>
          ) : null
        }
      />

      {creating ? <SiteFormDialog open onClose={() => setCreating(false)} /> : null}
    </div>
  )
}
