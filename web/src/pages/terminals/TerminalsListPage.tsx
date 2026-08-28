import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { Terminal } from '../../api/types'
import { TerminalStatusBadge, humaniseCode } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { SearchInput } from '../../components/Pagination'
import { PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { can } from '../../auth/permissions'
import { useSites, useTerminalSummary, useTerminals } from '../../data/console'
import { useSession } from '../../session/useSession'
import { AddTerminalDialog } from './AddTerminalDialog'
import { filterTerminals, presentStatuses, type TerminalFilter } from './health'
import { PendingTerminals } from './PendingTerminals'

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
  const { session } = useSession()

  const mayAdd = can(session, 'addTerminals')
  const [adding, setAdding] = useState(false)

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

  /*
    "This company has no terminals", which is NOT the same as "the table is
    showing none".

    It is deliberately false while the first read is in flight and false on an
    error, because both of those are states where the answer is unknown — and
    the page hides its statistics and filters on the strength of this, which is
    not something to do to somebody whose fleet is merely still loading. A
    filtered-to-nothing fleet is not empty either: the filters have to stay on
    screen for the operator to undo them.
  */
  const fleetEmpty = !terminals.isPending && !terminals.isError && all.length === 0

  const columns: Column<Terminal>[] = [
    {
      id: 'serial',
      header: 'Serial',
      primary: true,
      /*
        A REAL LINK, WHICH IS THE ONLY WAY INTO THIS PAGE THAT IS NOT A MOUSE.

        `onRowClick` is documented on DataTable as a pointer convenience that
        "must not be the only way to reach the row's destination… every table
        using this renders a real link in its primary column". This one did not:
        it rendered a bare <code>, so tabbing through the fleet went from the
        toolbar straight back to the top of the page and a terminal could not be
        opened by keyboard at all. People and Sites had it right; this was the
        outlier.

        THE SERIAL RATHER THAN THE NAME carries the link because it is the one
        field guaranteed to be there — a terminal added without a name renders
        an em dash in the Name column, and a link with no text is worse than no
        link. It is also what the row is keyed and routed by.

        No automated check caught this: axe cannot see a missing keyboard path
        when there is no ARIA to contradict, and the row deliberately carries no
        role. The regression test added alongside this asserts the link exists.
      */
      render: (terminal) => (
        <Link
          to={`/terminals/${encodeURIComponent(terminal.serial_number)}`}
          className="table__link mono"
        >
          {terminal.serial_number}
        </Link>
      ),
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
        // THE PRIMARY ACTION ON THE PAGE, because for a new customer it is the
        // only thing on this screen worth doing: the fleet is empty until they
        // do it. It used to live on the site detail page, one level down, which
        // is where somebody looks after they already know how this works.
        //
        // IT STAYS ON AN EMPTY FLEET, deliberately. Suppressing it in favour of
        // the empty state's own button would leave one primary action instead of
        // two, but this is the affordance that is in the same place on every
        // visit — and it is the one somebody reaches for when they have come
        // back to add a second terminal, or when the empty copy is below the
        // fold on a phone.
        actions={
          mayAdd ? (
            <button
              type="button"
              className="button button--primary"
              onClick={() => setAdding(true)}
            >
              Add a terminal
            </button>
          ) : null
        }
      />

      {/* Above the fleet: somebody standing next to a unit that is not working
          yet is more urgent than the health of the ones that are. Renders
          nothing at all when nothing is waiting. */}
      <PendingTerminals canApprove={mayAdd} />

      {adding ? <AddTerminalDialog open onClose={() => setAdding(false)} /> : null}

      {/*
        HEALTH FIRST — BUT ONLY WHERE THERE IS HEALTH TO REPORT.

        The counts are what a fleet view is for, and they are the first thing an
        operator with a fleet wants. They are also the first thing a customer
        with NO fleet saw: five tiles reading zero, a search box that can match
        nothing, two filters over an empty set and the line "0 terminals", and
        only underneath all of it the one paragraph that says what to do next.
        That was the opening screen of the product for every new account.

        Both blocks are suppressed together, and only when the fleet is
        genuinely empty rather than merely loading — a page that flickered its
        controls in and out during a refetch would be its own bug. Nothing is
        removed: the moment there is one terminal, the tiles and the toolbar are
        exactly as they were.
      */}
      {fleetEmpty ? null : (
        <>
          <section className="tiles" aria-label="Fleet health">
            <Tile label="Total" value={summary.data?.total} />
            <Tile label="Online" value={summary.data?.online} tone="positive" />
            <Tile label="Offline" value={summary.data?.offline} tone="warning" />
            <Tile label="Error" value={summary.data?.error} tone="danger" />
            <Tile
              label="Firmware outdated"
              value={summary.data?.firmware_outdated}
              tone="warning"
            />
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
        </>
      )}

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
        // THE OLD TEXT SENT PEOPLE TO THE SITE PROVISIONING KEY, which is the
        // credential that registers every terminal at a site for ever and is
        // exactly what a customer should never be handling. It also described a
        // procedure needing a cable. Both are now wrong as well as unsafe.
        //
        // AND THE TEXT THAT REPLACED IT PROMISED SOMETHING NO BUILD DID. It
        // described a terminal showing a pairing code at a point when no
        // firmware anywhere implemented announce, so a new customer followed it
        // to an empty list and waited for a code that was never coming. The
        // firmware half now exists — but it is a FIRMWARE version, not a
        // platform one, so a unit shipped or flashed before it still cannot do
        // this. Saying which version, and what to do otherwise, is the whole of
        // the fix: the instruction is right for the fleet it is right for, and
        // it no longer strands the fleet it is wrong for.
        emptyDescription={
          filtering
            ? 'Try a different search, status or site.'
            : 'Power a terminal on and connect it to Wi-Fi from your phone. On firmware 1.2.0 or newer it then shows a code on its screen — add it here with that code. An older terminal needs a claim code from its site instead.'
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
          ) : mayAdd ? (
            // NAMED DIFFERENTLY FROM THE HEADER BUTTON on purpose, and not only
            // to avoid two controls with one accessible name on the same screen.
            // This one is only ever seen by somebody who has no terminals at
            // all, and "your first" is what the dashboard's onboarding item
            // calls the same act — so the two places a new customer might start
            // agree with each other.
            <button
              type="button"
              className="button button--primary"
              onClick={() => setAdding(true)}
            >
              Add your first terminal
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
