import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import type { Terminal } from '../../api/types'
import { TerminalStatusBadge, humaniseCode } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { SearchInput } from '../../components/Pagination'
import { PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useSites, useTerminalSummary, useTerminals } from '../../data/console'
import { filterTerminals, presentStatuses, type TerminalFilter } from './health'

/**
 * The fleet.
 *
 * WHAT THE OPERATOR IS ACTUALLY ASKING is "is anything wrong, and where" — so
 * the summary tiles come first and the table answers the follow-up. The tiles
 * come from the summary endpoint rather than being counted from the rows,
 * because both are narrowed by the same grants server-side and computing one
 * from the other would drift the moment either changes.
 *
 * SEARCH AND FILTER ARE CLIENT-SIDE HERE, deliberately, and that is the
 * opposite of the rule for people. This endpoint returns the caller's whole
 * scoped fleet in one response — no limit, no offset, no `q` — so narrowing in
 * the browser narrows the complete set. See health.ts for why that stops being
 * true if the endpoint is ever paginated.
 */
export function TerminalsListPage() {
  const navigate = useNavigate()
  const terminals = useTerminals()
  const summary = useTerminalSummary()
  const sites = useSites()

  const [filter, setFilter] = useState<TerminalFilter>({
    search: '',
    status: 'ALL',
    siteId: 'ALL',
    outdatedOnly: false,
  })

  const all = useMemo(() => terminals.data?.terminals ?? [], [terminals.data])
  const visible = useMemo(() => filterTerminals(all, filter), [all, filter])
  const statuses = useMemo(() => presentStatuses(all), [all])

  const filtering =
    Boolean(filter.search?.trim()) ||
    filter.status !== 'ALL' ||
    filter.siteId !== 'ALL' ||
    Boolean(filter.outdatedOnly)

  const columns: Column<Terminal>[] = [
    {
      id: 'serial',
      header: 'Serial',
      primary: true,
      render: (terminal) => <code className="mono">{terminal.serial_number}</code>,
    },
    {
      id: 'name',
      header: 'Name',
      render: (terminal) => terminal.device_name || <span className="muted">—</span>,
    },
    {
      id: 'site',
      header: 'Site',
      render: (terminal) => terminal.site_name,
    },
    {
      id: 'status',
      header: 'Status',
      render: (terminal) => <TerminalStatusBadge status={terminal.status} />,
    },
    {
      id: 'heartbeat',
      header: 'Last seen',
      render: (terminal) =>
        terminal.last_heartbeat_at ? (
          <Timestamp value={terminal.last_heartbeat_at} relative />
        ) : (
          <span className="muted">Never</span>
        ),
    },
    {
      id: 'firmware',
      header: 'Firmware',
      secondary: true,
      render: (terminal) => (
        <span className="firmware">
          <code className="mono">{terminal.firmware_version || '—'}</code>
          {terminal.firmware_outdated ? (
            <span className="badge badge--warning">Outdated</span>
          ) : null}
        </span>
      ),
    },
  ]

  return (
    <div className="page">
      <PageHeader
        title="Terminals"
        lead="The devices installed across your sites."
      />

      {/* Health first: the counts are what a fleet view is for. */}
      <section className="tiles" aria-label="Fleet health">
        <Tile label="Total" value={summary.data?.total} />
        <Tile label="Online" value={summary.data?.online} tone="positive" />
        <Tile label="Offline" value={summary.data?.offline} tone="warning" />
        <Tile label="Error" value={summary.data?.error} tone="danger" />
        <Tile label="Firmware outdated" value={summary.data?.firmware_outdated} tone="warning" />
      </section>

      <div className="toolbar">
        <SearchInput
          label="Search terminals"
          placeholder="Serial, name or site"
          value={filter.search ?? ''}
          onChange={(search) => setFilter((current) => ({ ...current, search }))}
        />

        <label className="field">
          <span className="field__label" id="filter-status-label">
            Status
          </span>
          <select
            className="field__input field__select"
            aria-labelledby="filter-status-label"
            value={filter.status}
            onChange={(event) =>
              setFilter((current) => ({
                ...current,
                status: event.target.value as TerminalFilter['status'],
              }))
            }
          >
            <option value="ALL">All statuses</option>
            {statuses.map((status) => (
              <option key={status} value={status}>
                {humaniseCode(status)}
              </option>
            ))}
          </select>
        </label>

        <label className="field">
          <span className="field__label" id="filter-site-label">
            Site
          </span>
          <select
            className="field__input field__select"
            aria-labelledby="filter-site-label"
            value={filter.siteId}
            onChange={(event) =>
              setFilter((current) => ({ ...current, siteId: event.target.value }))
            }
          >
            <option value="ALL">All sites</option>
            {/*
              Keyed by PUBLIC id, which is what a terminal carries as
              site_public_id. Matching on site_name would quietly include
              another site's hardware when two are named alike.
            */}
            {(sites.data?.sites ?? []).map((site) => (
              <option key={site.id} value={site.id}>
                {site.name}
              </option>
            ))}
          </select>
        </label>

        <label className="checkbox toolbar__toggle">
          <input
            type="checkbox"
            className="checkbox__input"
            checked={Boolean(filter.outdatedOnly)}
            onChange={(event) =>
              setFilter((current) => ({ ...current, outdatedOnly: event.target.checked }))
            }
          />
          <span className="checkbox__label">Outdated firmware only</span>
        </label>

        <RefreshingIndicator active={terminals.isFetching && !terminals.isPending} />
      </div>

      {/* Announced, because filtering changes the table without moving focus. */}
      <p className="filter-status" aria-live="polite">
        {filtering
          ? `Showing ${visible.length} of ${all.length} terminals`
          : `${all.length} terminal${all.length === 1 ? '' : 's'}`}
      </p>

      <DataTable<Terminal>
        caption="Terminals"
        columns={columns}
        rows={visible}
        rowKey={(terminal) => terminal.serial_number}
        isLoading={terminals.isPending}
        isFetching={terminals.isFetching}
        error={terminals.isError ? terminals.error : null}
        onRetry={() => void terminals.refetch()}
        onRowClick={(terminal) => navigate(`/terminals/${encodeURIComponent(terminal.serial_number)}`)}
        // Two genuinely different empties, and conflating them would tell a
        // company with hardware that it has none.
        emptyTitle={filtering ? 'No terminals match those filters' : 'No terminals yet'}
        emptyDescription={
          filtering
            ? 'Try a different search, status or site.'
            : 'Terminals appear here once they have been registered against one of your sites. Registration happens on the device, using the site’s provisioning key.'
        }
        emptyAction={
          filtering ? (
            <button
              type="button"
              className="button"
              onClick={() =>
                setFilter({ search: '', status: 'ALL', siteId: 'ALL', outdatedOnly: false })
              }
            >
              Clear filters
            </button>
          ) : null
        }
      />
    </div>
  )
}

function Tile({
  label,
  value,
  tone,
}: {
  label: string
  value: number | undefined
  tone?: 'positive' | 'warning' | 'danger'
}) {
  return (
    <article className={`tile${tone ? ` tile--${tone}` : ''}`}>
      <p className="tile__label">{label}</p>
      {/* An em dash while loading rather than a zero: "0 online" is a claim,
          and it is the alarming one to make by accident. */}
      <p className="tile__value">{value === undefined ? '—' : value}</p>
    </article>
  )
}
