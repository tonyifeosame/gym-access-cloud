import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { Site } from '../../api/types'
import { can } from '../../auth/permissions'
import { ActiveBadge, Badge } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useSites } from '../../data/console'
import { useSession } from '../../session/useSession'
import { offlinePolicyDefinition } from './offlinePolicy'
import { SiteFormDialog } from './SiteFormDialog'

/**
 * Every site this operator can reach.
 *
 * NARROWED BY THE API, not here. A site-scoped operator is served only their
 * granted sites, so this renders whatever came back rather than filtering a
 * fuller list client-side — which would mean the browser had briefly held sites
 * the operator is not entitled to.
 *
 * A site is domain-neutral: a location with terminals at it. An office, a
 * campus, a warehouse, a venue. Nothing here assumes which.
 */
export function SitesListPage() {
  const { session } = useSession()
  const navigate = useNavigate()
  const query = useSites()
  const [creating, setCreating] = useState(false)

  // ADMIN, matching the server. The gate is a courtesy: the API refuses
  // anything this wrongly permitted.
  const mayManage = can(session, 'manageSites')

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
    {
      id: 'key',
      header: 'Key',
      secondary: true,
      // Only ever the PREFIX, and only when the API supplies one. There is no
      // path that fetches a whole key, and adding one would be a bug.
      render: (site) =>
        site.api_key_prefix ? (
          <code className="mono">{site.api_key_prefix}…</code>
        ) : (
          <span className="muted">—</span>
        ),
    },
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
        emptyTitle="No sites yet"
        emptyDescription={
          mayManage
            ? 'A site is a location with terminals at it. Add your first one to start provisioning hardware.'
            : 'No sites have been set up for your company yet, or none have been shared with you.'
        }
        emptyAction={
          mayManage ? (
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
