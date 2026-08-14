import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '../api/client'
import { NotificationsProvider, useNotifications } from './Notifications'
import { Pagination, SearchInput } from './Pagination'

function Harness() {
  const { success, failure, notify } = useNotifications()
  return (
    <div>
      <button type="button" onClick={() => success('Person saved')}>
        Succeed
      </button>
      <button
        type="button"
        onClick={() =>
          failure(
            'Could not save the person.',
            new ApiError(409, 'A person with that external_id already exists', 'req-9', null),
          )
        }
      >
        Fail
      </button>
      <button type="button" onClick={() => notify({ tone: 'info', message: 'Sync queued' })}>
        Inform
      </button>
    </div>
  )
}

describe('Notifications', () => {
  it('announces a success politely and a failure assertively', async () => {
    // An operator should be interrupted when a change did NOT take effect, and
    // not when it did.
    const user = userEvent.setup()
    const { container } = render(
      <NotificationsProvider>
        <Harness />
      </NotificationsProvider>,
    )

    await user.click(screen.getByRole('button', { name: 'Succeed' }))
    const polite = container.querySelector('[aria-live="polite"]')
    expect(polite).toHaveTextContent('Person saved')

    await user.click(screen.getByRole('button', { name: 'Fail' }))
    const assertive = container.querySelector('[aria-live="assertive"]')
    expect(assertive).toHaveTextContent('Could not save the person.')
  })

  it('appends the API’s own message and carries the request id', async () => {
    // The API's strings are for humans and are never parsed; showing ours plus
    // theirs gives the operator the context and the cause.
    const user = userEvent.setup()
    render(
      <NotificationsProvider>
        <Harness />
      </NotificationsProvider>,
    )

    await user.click(screen.getByRole('button', { name: 'Fail' }))
    expect(
      screen.getByText(/Could not save the person\. A person with that external_id already exists/),
    ).toBeInTheDocument()
    expect(screen.getByText('req-9')).toBeInTheDocument()
  })

  it('auto-dismisses a success but NEVER a failure', async () => {
    // A success that disappears is fine — the screen behind already shows the
    // new state. A failure that disappears leaves somebody who looked away
    // believing their change went through.
    const user = userEvent.setup()

    render(
      <NotificationsProvider autoDismissMs={30}>
        <Harness />
      </NotificationsProvider>,
    )

    await user.click(screen.getByRole('button', { name: 'Succeed' }))
    await user.click(screen.getByRole('button', { name: 'Fail' }))

    await waitFor(() => expect(screen.queryByText('Person saved')).not.toBeInTheDocument())
    expect(screen.getByText(/Could not save the person/)).toBeInTheDocument()
  })

  it('can be dismissed by hand', async () => {
    const user = userEvent.setup()
    render(
      <NotificationsProvider>
        <Harness />
      </NotificationsProvider>,
    )

    await user.click(screen.getByRole('button', { name: 'Fail' }))
    await user.click(screen.getByRole('button', { name: 'Dismiss notification' }))

    await waitFor(() =>
      expect(screen.queryByText(/Could not save the person/)).not.toBeInTheDocument(),
    )
  })
})

describe('Pagination', () => {
  const base = {
    count: 50,
    total: 130,
    offset: 0,
    limit: 50,
    hasMore: true,
    onOffsetChange: vi.fn(),
    noun: 'people',
  }

  it('renders nothing when everything fits on one page', () => {
    // Controls that can only be disabled are noise.
    const { container } = render(
      <Pagination {...base} count={3} total={3} hasMore={false} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing at all when there is no data', () => {
    const { container } = render(<Pagination {...base} count={0} total={0} hasMore={false} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('states the range and the page, and announces changes', () => {
    render(<Pagination {...base} offset={50} count={50} />)

    const status = screen.getByText(/Showing/)
    expect(status).toHaveTextContent('Showing 51–100 of 130 people')
    expect(status).toHaveAttribute('aria-live', 'polite')
    expect(screen.getByText('Page 2 of 3')).toBeInTheDocument()
  })

  it('disables Previous on the first page and Next on the last', () => {
    const { rerender } = render(<Pagination {...base} />)
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Next' })).toBeEnabled()

    rerender(<Pagination {...base} offset={100} count={30} hasMore={false} />)
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
  })

  it('moves by exactly one page and never below zero', async () => {
    const user = userEvent.setup()
    const onOffsetChange = vi.fn()
    render(<Pagination {...base} offset={50} onOffsetChange={onOffsetChange} />)

    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(onOffsetChange).toHaveBeenLastCalledWith(100)

    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(onOffsetChange).toHaveBeenLastCalledWith(0)
  })
})

describe('SearchInput', () => {
  it('updates locally at once but reports only after typing stops', async () => {
    // Each reported change is a request AND a cache entry; firing per keystroke
    // turns a six-letter name into six round trips.
    const onChange = vi.fn()
    const user = userEvent.setup()

    render(<SearchInput value="" onChange={onChange} label="Search people" debounceMs={40} />)

    const input = screen.getByLabelText('Search people')
    await user.type(input, 'ada')

    // Typing is never laggy: the local value is already there.
    expect(input).toHaveValue('ada')
    // ...but three keystrokes must not have become three requests.
    expect(onChange).not.toHaveBeenCalled()

    await waitFor(() => expect(onChange).toHaveBeenCalledExactlyOnceWith('ada'))
  })

  it('adopts a value cleared from outside', async () => {
    const { rerender } = render(
      <SearchInput value="ada" onChange={vi.fn()} label="Search people" />,
    )
    expect(screen.getByLabelText('Search people')).toHaveValue('ada')

    rerender(<SearchInput value="" onChange={vi.fn()} label="Search people" />)
    await waitFor(() => expect(screen.getByLabelText('Search people')).toHaveValue(''))
  })
})
