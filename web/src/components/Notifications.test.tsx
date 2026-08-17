import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '../api/client'
import { NotificationsProvider, useNotifications } from './Notifications'
import { Pagination, SearchInput } from './Pagination'

/**
 * TWO OF THE TESTS BELOW ARE ABOUT ELAPSED TIME, AND THEY USE A FAKE CLOCK.
 *
 * They did not, and that was a real defect rather than a style point. The
 * auto-dismissal test rendered with `autoDismissMs={30}` and the debounce test
 * with `debounceMs={40}`, then used real timers and `waitFor`. Both then assert
 * something has NOT happened yet — that a success is still on screen, that three
 * keystrokes have not become three requests — and under parallel load a 30ms
 * window elapses between the click that starts the timer and the assertion that
 * the timer has not fired. The test fails, the code is fine, and the failure does
 * not reproduce.
 *
 * Raising the constants would have hidden it rather than fixed it: the race is
 * that the test cannot say WHEN it is, not that the window is small. With a fake
 * clock the window is exactly as long as the test says and nothing else can
 * advance it, so both directions become assertable — that the message is still
 * there one millisecond before the deadline, and gone one millisecond after. The
 * first of those is the assertion the original could not make at all.
 *
 * THE TWO FAKE-CLOCK TESTS DRIVE THE DOM WITH `fireEvent` RATHER THAN
 * `userEvent`, and that is not a preference either.
 *
 * user-event awaits between actions, and Testing Library's async wrapper drains
 * the microtask queue through a real `setTimeout(…, 0)` which it only advances
 * when it detects fake timers — by looking for a global `jest`. Under Vitest
 * there is no such global, so the detection returns false, the drain never
 * resolves, and every test in the block hangs until the runner kills it. That is
 * a five-second timeout in place of an assertion, which is a worse failure than
 * the flake being fixed.
 *
 * `fireEvent` is synchronous and already wrapped in `act`, so no async wrapper is
 * involved and the clock only moves when the test moves it. Nothing is lost here:
 * these two tests are about WHEN a timer fires, and the realistic input
 * simulation they would otherwise provide is covered by the user-event tests
 * above, which run on the real clock.
 *
 * `toFake` is narrowed to the timer functions the components under test actually
 * use. Faking everything — the default — also replaces `setImmediate` and
 * `queueMicrotask`, which React's `act` schedules through.
 */
const TIMERS_TO_FAKE = ['setTimeout', 'clearTimeout'] as const

function useFakeClock() {
  vi.useFakeTimers({ toFake: [...TIMERS_TO_FAKE] })
}

/** Moves the fake clock and lets React flush what that produced. */
function elapse(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms)
  })
}

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

describe('Notifications: auto-dismissal', () => {
  // Scoped to this block rather than the file, so the tests that are not about
  // elapsed time keep running on the real clock and stay simple.
  beforeEach(() => {
    useFakeClock()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('auto-dismisses a success at its deadline and NEVER a failure', () => {
    // A success that disappears is fine — the screen behind already shows the
    // new state. A failure that disappears leaves somebody who looked away
    // believing their change went through.
    render(
      <NotificationsProvider autoDismissMs={6000}>
        <Harness />
      </NotificationsProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Succeed' }))
    fireEvent.click(screen.getByRole('button', { name: 'Fail' }))

    expect(screen.getByText('Person saved')).toBeInTheDocument()

    // STILL THERE ONE TICK BEFORE THE DEADLINE. This is the half a real-timer
    // test cannot assert: it proves the message survives until its own timer
    // fires, rather than that it happened to still be there when the assertion
    // ran.
    elapse(5999)
    expect(screen.getByText('Person saved')).toBeInTheDocument()

    elapse(1)
    expect(screen.queryByText('Person saved')).not.toBeInTheDocument()

    // And the failure outlives any amount of time.
    elapse(60_000)
    expect(screen.getByText(/Could not save the person/)).toBeInTheDocument()
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

describe('SearchInput debouncing', () => {
  beforeEach(() => {
    useFakeClock()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('updates locally at once but reports only after typing stops', () => {
    // Each reported change is a request AND a cache entry; firing per keystroke
    // turns a six-letter name into six round trips.
    //
    // ON A FAKE CLOCK, because the load-bearing assertion is a negative one —
    // that three keystrokes have NOT yet become three requests — and a negative
    // about elapsed time is only meaningful if the test controls how much has
    // elapsed. With real timers this passed because the debounce usually had not
    // fired yet, which is not the same as proving it had not.
    const onChange = vi.fn()

    render(<SearchInput value="" onChange={onChange} label="Search people" debounceMs={300} />)

    const input = screen.getByLabelText('Search people')

    // Three keystrokes with time between them, so the test also proves each one
    // RESTARTS the debounce rather than the first one simply winning.
    fireEvent.change(input, { target: { value: 'a' } })
    elapse(200)
    fireEvent.change(input, { target: { value: 'ad' } })
    elapse(200)
    fireEvent.change(input, { target: { value: 'ada' } })

    // Typing is never laggy: the local value is already there.
    expect(input).toHaveValue('ada')
    // ...but 400ms and three keystrokes must not have become three requests, or
    // any requests at all.
    expect(onChange).not.toHaveBeenCalled()

    // Still silent one tick short of the debounce.
    elapse(299)
    expect(onChange).not.toHaveBeenCalled()

    elapse(1)
    expect(onChange).toHaveBeenCalledExactlyOnceWith('ada')
  })
})

describe('SearchInput', () => {
  // Real timers: nothing here is about elapsed time, and adopting an external
  // value is a render-time effect rather than a debounced one.
  it('adopts a value cleared from outside', async () => {
    const { rerender } = render(
      <SearchInput value="ada" onChange={vi.fn()} label="Search people" />,
    )
    expect(screen.getByLabelText('Search people')).toHaveValue('ada')

    rerender(<SearchInput value="" onChange={vi.fn()} label="Search people" />)
    await waitFor(() => expect(screen.getByLabelText('Search people')).toHaveValue(''))
  })
})
