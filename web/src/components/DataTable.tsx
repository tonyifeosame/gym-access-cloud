import type { ReactNode } from 'react'

import { EmptyState, ErrorState, LoadingState } from './states'

/**
 * The console's table.
 *
 * ONE COMPONENT OWNS THE FOUR STATES every list has — loading, failed, empty,
 * populated — because the failure mode otherwise is a screen that renders an
 * empty table when a request errored, telling the operator there are no
 * terminals when in fact nobody asked successfully. Callers pass the query's
 * state and cannot skip a case.
 *
 * RESPONSIVENESS IS A REAL REQUIREMENT HERE, not a nicety: this console is used
 * standing in front of a door with a phone. A table cannot shrink below its
 * columns, so below the breakpoint each ROW BECOMES A CARD and every cell
 * carries its column name as a label. That is done with a `data-label` attribute
 * and CSS rather than by rendering a different tree, so there is exactly one DOM
 * for tests and assistive technology to agree on, and no viewport-dependent
 * behaviour to get wrong.
 *
 * Sorting is deliberately absent. The lists that need ordering are paginated
 * server-side, and a client-side sort over one page silently sorts the page
 * rather than the data — the same trap as filtering a page and calling it search.
 */

export interface Column<Row> {
  /** Stable key, also used for the responsive label. */
  id: string
  header: ReactNode
  /** Short form used as the card label below the breakpoint. Defaults to header. */
  label?: string
  render: (row: Row) => ReactNode
  /** Right-aligns, for counts and actions. */
  align?: 'start' | 'end'
  /** Hidden below the breakpoint, for detail that does not earn phone space. */
  secondary?: boolean
  /** Marks the column whose cell is the row's heading in card form. */
  primary?: boolean
}

export interface DataTableProps<Row> {
  columns: Column<Row>[]
  rows: Row[] | undefined
  rowKey: (row: Row) => string
  /** Accessible name for the table. Required — a bare grid announces nothing. */
  caption: string
  isLoading?: boolean
  isFetching?: boolean
  error?: unknown
  onRetry?: () => void
  emptyTitle?: string
  emptyDescription?: string
  emptyAction?: ReactNode
  /**
   * Makes a whole row clickable WITH A POINTER, as a convenience.
   *
   * IT IS NOT THE KEYBOARD OR SCREEN-READER AFFORDANCE, and must not be the
   * only way to reach the row's destination. Every table using this renders a
   * real link in its `primary` column, and that link is what a keyboard user
   * tabs to and what a screen reader announces. See the note on the row itself
   * for why the obvious implementation of this prop was wrong.
   */
  onRowClick?: (row: Row) => void
}

export function DataTable<Row>({
  columns,
  rows,
  rowKey,
  caption,
  isLoading = false,
  isFetching = false,
  error = null,
  onRetry,
  emptyTitle = 'Nothing here yet',
  emptyDescription,
  emptyAction,
  onRowClick,
}: DataTableProps<Row>) {
  if (error) {
    return <ErrorState error={error} onRetry={onRetry} />
  }

  // Only the FIRST load blanks the area. A background refetch keeps the table
  // on screen and shows a quiet indicator instead, so paging or searching does
  // not flash an empty state between two populated ones.
  if (isLoading || rows === undefined) {
    return <LoadingState label={`Loading ${caption.toLowerCase()}…`} />
  }

  if (rows.length === 0) {
    return <EmptyState title={emptyTitle} description={emptyDescription} action={emptyAction} />
  }

  return (
    <div className="table-wrap">
      {/* aria-busy on the TABLE rather than its scroll container: the table is
          the thing whose contents are being replaced, and that is what a
          screen reader should be told is in flux. */}
      <table className="table" aria-busy={isFetching || undefined}>
        <caption className="table__caption">{caption}</caption>
        <thead>
          <tr>
            {columns.map((column) => (
              <th
                key={column.id}
                scope="col"
                className={cellClass(column)}
              >
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={rowKey(row)}
              className={onRowClick ? 'table__row table__row--clickable' : 'table__row'}
              /*
                A POINTER CONVENIENCE ONLY, and deliberately invisible to
                assistive technology. This used to carry role="button" and
                tabIndex=0 on the reasoning that a clickable row must be
                keyboard-reachable, which sounds right and was wrong twice:

                  - role="button" on a <tr> REPLACES its row semantics. A
                    screen reader stops announcing the table's structure
                    entirely for that row -- no "row 3 of 40", no column
                    headers read with the cells -- which is most of what makes
                    a data table usable without sight.
                  - The row contains a link, so it was an interactive control
                    nested inside another. Screen readers disagree about what
                    to announce, and axe reports it as a serious violation.

                The keyboard path was never missing: every table using this
                renders a real <a> in its primary column, which tabs, announces
                its destination, and works with middle-click and
                open-in-new-tab as a row handler never could. So the row keeps
                the pointer shortcut and nothing else.

                A row is NOT given a click handler when a cell click should
                mean something else -- the caller decides that by whether it
                passes onRowClick at all.
              */
              {...(onRowClick
                ? {
                    onClick: (event: React.MouseEvent) => {
                      // Let a real control inside the row win. Without this,
                      // clicking a row's own button also navigates away from
                      // the page it was pressed on.
                      const target = event.target as HTMLElement
                      if (target.closest('a, button, input, select, textarea')) return
                      onRowClick(row)
                    },
                  }
                : {})}
            >
              {columns.map((column) => {
                const content = column.render(row)
                const label = column.label ?? textOf(column.header)
                return column.primary ? (
                  <th key={column.id} scope="row" className={cellClass(column)} data-label={label}>
                    {content}
                  </th>
                ) : (
                  <td key={column.id} className={cellClass(column)} data-label={label}>
                    {content}
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function cellClass<Row>(column: Column<Row>): string {
  const classes = ['table__cell']
  if (column.align === 'end') classes.push('table__cell--end')
  if (column.secondary) classes.push('table__cell--secondary')
  return classes.join(' ')
}

/** Best-effort text for a header used as a card label. */
function textOf(header: ReactNode): string {
  return typeof header === 'string' ? header : ''
}
