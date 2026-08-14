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
  /** Makes a whole row activatable. Rows stay keyboard reachable when set. */
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
              // A clickable row must also be reachable and activatable from the
              // keyboard, or the interaction simply does not exist for anyone
              // not using a pointer.
              {...(onRowClick
                ? {
                    tabIndex: 0,
                    role: 'button' as const,
                    onClick: () => onRowClick(row),
                    onKeyDown: (event: React.KeyboardEvent) => {
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault()
                        onRowClick(row)
                      }
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
