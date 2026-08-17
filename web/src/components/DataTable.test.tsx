import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '../api/client'
import { DataTable, type Column } from './DataTable'

interface Row {
  serial: string
  site: string
  status: string
}

const ROWS: Row[] = [
  { serial: 'AT-0001', site: 'Lagos Depot', status: 'Online' },
  { serial: 'AT-0002', site: 'Abuja Depot', status: 'Offline' },
]

const COLUMNS: Column<Row>[] = [
  { id: 'serial', header: 'Serial', render: (row) => row.serial, primary: true },
  { id: 'site', header: 'Site', render: (row) => row.site },
  { id: 'status', header: 'Status', render: (row) => row.status, secondary: true },
]

function renderTable(props: Partial<Parameters<typeof DataTable<Row>>[0]> = {}) {
  return render(
    <DataTable<Row>
      columns={COLUMNS}
      rows={ROWS}
      rowKey={(row) => row.serial}
      caption="Terminals"
      {...props}
    />,
  )
}

describe('DataTable', () => {
  it('renders rows with an accessible name for the table', () => {
    renderTable()
    expect(screen.getByRole('table', { name: 'Terminals' })).toBeInTheDocument()
    expect(screen.getByText('AT-0001')).toBeInTheDocument()
    expect(screen.getByText('AT-0002')).toBeInTheDocument()
  })

  it('marks the primary column as the row heading', () => {
    // Which is what lets a screen reader say which ROW a cell belongs to.
    renderTable()
    expect(screen.getByRole('rowheader', { name: 'AT-0001' })).toBeInTheDocument()
  })

  it('carries the column name on every cell for the card layout', () => {
    // Below 680px the header row is hidden and these labels are what identify
    // each value. Asserted on the DOM because the layout itself is CSS.
    renderTable()
    const cell = screen.getByText('Lagos Depot')
    expect(cell).toHaveAttribute('data-label', 'Site')
  })

  it('shows a loading state on the FIRST load only', () => {
    const { rerender } = renderTable({ rows: undefined, isLoading: true })
    expect(screen.getByRole('status')).toHaveTextContent(/loading terminals/i)

    // A background refetch keeps the data on screen rather than blanking it,
    // so paging does not flash an empty state between two populated ones.
    rerender(
      <DataTable<Row>
        columns={COLUMNS}
        rows={ROWS}
        rowKey={(row) => row.serial}
        caption="Terminals"
        isFetching
      />,
    )
    expect(screen.getByText('AT-0001')).toBeInTheDocument()
    expect(screen.getByRole('table')).toHaveAttribute('aria-busy', 'true')
  })

  it('shows an error state INSTEAD of an empty table when the request failed', () => {
    // The failure this component exists to prevent: rendering "no terminals"
    // when in fact nobody asked successfully.
    renderTable({
      rows: undefined,
      error: new ApiError(500, 'Failed to retrieve terminals', 'req-1', null),
    })

    expect(screen.getByRole('alert')).toHaveTextContent('Failed to retrieve terminals')
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    // And the request id, which is what makes the failure findable in the log.
    expect(screen.getByText('req-1')).toBeInTheDocument()
  })

  it('offers a retry when one is given', async () => {
    const user = userEvent.setup()
    const onRetry = vi.fn()
    renderTable({ rows: undefined, error: new Error('boom'), onRetry })

    await user.click(screen.getByRole('button', { name: /try again/i }))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('shows an empty state, with an action, when there is genuinely nothing', () => {
    renderTable({
      rows: [],
      emptyTitle: 'No terminals yet',
      emptyDescription: 'Register a terminal to see it here.',
      emptyAction: <button type="button">Register a terminal</button>,
    })

    expect(screen.getByText('No terminals yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Register a terminal' })).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('KEEPS ROW SEMANTICS, and does not present a row as a button', async () => {
    // This used to put role="button" and tabIndex=0 on the <tr>, on the
    // reasoning that a clickable row must be keyboard-reachable. It sounds
    // right and was wrong twice, and axe reported the second one as a serious
    // violation:
    //
    //   - role="button" REPLACES a row's semantics. A screen reader stops
    //     announcing the table structure for that row entirely -- no position,
    //     no headers read with the cells -- which is most of what makes a data
    //     table usable without sight.
    //   - Every such row contains a link, so it was an interactive control
    //     nested inside another.
    //
    // The keyboard path was never missing: the primary cell holds a real link,
    // which tabs, announces its destination, and supports middle-click and
    // open-in-new-tab as a row handler never could.
    const onRowClick = vi.fn()
    renderTable({ onRowClick })

    for (const row of screen.getAllByRole('row')) {
      expect(row).not.toHaveAttribute('role', 'button')
      expect(row).not.toHaveAttribute('tabindex')
    }
    // The rows are still rows.
    expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
  })

  it('lets a control inside a row win over the row itself', async () => {
    // Without this, pressing a row's own button also navigates away from the
    // page it was pressed on.
    const user = userEvent.setup()
    const onRowClick = vi.fn()
    const onInner = vi.fn()

    renderTable({
      onRowClick,
      columns: [
        { id: 'serial', header: 'Serial', primary: true, render: (row) => row.serial },
        {
          id: 'action',
          header: 'Action',
          render: () => (
            <button type="button" onClick={onInner}>
              Inner
            </button>
          ),
        },
      ],
    })

    await user.click(screen.getAllByRole('button', { name: 'Inner' })[0] as HTMLElement)
    expect(onInner).toHaveBeenCalled()
    expect(onRowClick).not.toHaveBeenCalled()
  })

  it('still responds to a pointer', async () => {
    const user = userEvent.setup()
    const onRowClick = vi.fn()
    renderTable({ onRowClick })

    await user.click(screen.getByText('AT-0002'))
    expect(onRowClick).toHaveBeenCalledWith(ROWS[1])
  })
})
