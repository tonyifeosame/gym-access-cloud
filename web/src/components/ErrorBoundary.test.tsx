import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { describe, expect, it, vi } from 'vitest'

import { ErrorBoundary } from './ErrorBoundary'

/**
 * The last line of defence against a blank page.
 *
 * A white page is the worst available failure for this product: the console is
 * used standing in front of a door, often to deal with something that has
 * already gone wrong, and somebody who needs to revoke a stolen terminal and
 * gets a blank screen cannot tell whether the platform is down, their session
 * ended, or they should try another browser.
 */

function Boom({ message = 'cannot read properties of undefined' }: { message?: string }): never {
  throw new Error(message)
}

/** React logs every caught error; the noise is not the thing under test. */
function silenceReact() {
  return vi.spyOn(console, 'error').mockImplementation(() => {})
}

describe('a rendering failure', () => {
  it('shows a usable page instead of nothing', () => {
    const spy = silenceReact()
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )

    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
      /could not be displayed/i,
    )
    spy.mockRestore()
  })

  it('says the session and the data are intact, because that is the operator’s first question', () => {
    const spy = silenceReact()
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )

    expect(screen.getByText(/session is still active/i)).toBeInTheDocument()
    expect(screen.getByText(/not in your account or your data/i)).toBeInTheDocument()
    spy.mockRestore()
  })

  it('SHOWS the error text rather than hiding it', () => {
    // The operator reporting this is the only channel this platform has. A
    // message they cannot see is one they cannot quote.
    const spy = silenceReact()
    render(
      <ErrorBoundary>
        <Boom message="terminal.site_public_id is undefined" />
      </ErrorBoundary>,
    )

    expect(screen.getByText('terminal.site_public_id is undefined')).toBeInTheDocument()
    spy.mockRestore()
  })

  it('offers a retry that re-renders the same screen', async () => {
    const user = userEvent.setup()
    const spy = silenceReact()

    // The flag lives OUTSIDE the component and is flipped by the TEST rather
    // than by the component itself: React re-invokes a failing render to
    // recover the stack, so a component that healed itself on the way past
    // would never reach the error state at all. What is being modelled is a
    // transient failure — a race with a refetch, most plausibly — that has
    // stopped being true by the time the operator presses retry.
    let stillBroken = true

    function Flaky() {
      if (stillBroken) throw new Error('transient')
      return <p>Recovered</p>
    }

    render(
      <ErrorBoundary>
        <Flaky />
      </ErrorBoundary>,
    )

    expect(screen.getByRole('alert')).toBeInTheDocument()
    stillBroken = false

    await user.click(screen.getByRole('button', { name: /try this screen again/i }))
    expect(await screen.findByText('Recovered')).toBeInTheDocument()
    spy.mockRestore()
  })

  it('clears itself when the route changes', async () => {
    // Without this, one broken screen shows its error for the rest of the
    // session no matter where the operator navigates.
    const spy = silenceReact()

    const { rerender } = render(
      <ErrorBoundary resetKey="/terminals/AT-0001">
        <Boom />
      </ErrorBoundary>,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()

    rerender(
      <ErrorBoundary resetKey="/people">
        <p>Somewhere else</p>
      </ErrorBoundary>,
    )
    expect(screen.getByText('Somewhere else')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    spy.mockRestore()
  })

  it('LOGS THE MESSAGE AND THE STACK, never the props', () => {
    // A "helpful" dump of component props here would log exactly what the rest
    // of this codebase is careful never to log: a person's name, an email
    // address, a site key.
    const spy = silenceReact()

    render(
      <ErrorBoundary>
        <Boom message="boom" />
      </ErrorBoundary>,
    )

    const logged = spy.mock.calls
      .filter((call) => String(call[0]).includes('AccessLink console'))
      .flat()
      .map(String)
      .join(' ')

    expect(logged).toContain('boom')
    expect(logged).not.toMatch(/props/i)
    spy.mockRestore()
  })

  it('passes children through untouched when nothing fails', () => {
    render(
      <ErrorBoundary>
        <p>The actual page</p>
      </ErrorBoundary>,
    )
    expect(screen.getByText('The actual page')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
