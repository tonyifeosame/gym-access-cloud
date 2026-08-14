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

  it('makes a clickable row reachable and activatable from the keyboard', async () => {
    // A row that only responds to a pointer does not exist for a keyboard user.
    const user = userEvent.setup()
    const onRowClick = vi.fn()
    renderTable({ onRowClick })

    const rows = screen.getAllByRole('button')
    const first = rows[0] as HTMLElement
    first.focus()
    expect(first).toHaveFocus()

    await user.keyboard('{Enter}')
    expect(onRowClick).toHaveBeenCalledWith(ROWS[0])

    await user.keyboard(' ')
    expect(onRowClick).toHaveBeenCalledTimes(2)
  })

  it('still responds to a pointer', async () => {
    const user = userEvent.setup()
    const onRowClick = vi.fn()
    renderTable({ onRowClick })

    await user.click(screen.getByText('AT-0002'))
    expect(onRowClick).toHaveBeenCalledWith(ROWS[1])
  })
})
