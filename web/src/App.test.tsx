import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, createMemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import { App, createQueryClient } from './App'
import { useNotifications } from './components/Notifications'
import { useSession } from './session/useSession'
import { makeSession } from './test/fixtures'
import { resetServerState } from './test/server'

/**
 * The provider tree itself.
 *
 * Rendering the REAL App rather than a reconstruction of it: a test that rebuilt
 * this nesting would assert against a copy, and the failure worth catching here
 * is precisely a divergence between what ships and what was intended — a
 * duplicated provider, or notifications mounted inside the router where they
 * would be destroyed by the navigation they are meant to survive.
 */

/** Raises a notification, then links away from the page that raised it. */
function Raiser() {
  const { success } = useNotifications()
  const { session } = useSession()

  return (
    <div>
      <h1>Origin page</h1>
      <p data-testid="who">{session?.operator.full_name ?? 'nobody'}</p>
      <button type="button" onClick={() => success('Person removed from every terminal')}>
        Do the thing
      </button>
      <Link to="/elsewhere">Go elsewhere</Link>
    </div>
  )
}

function Destination() {
  return <h1>Destination page</h1>
}

function testRouter(initialPath = '/') {
  return createMemoryRouter(
    [
      { path: '/', element: <Raiser /> },
      { path: '/elsewhere', element: <Destination /> },
    ],
    { initialEntries: [initialPath] },
  )
}

describe('App provider tree', () => {
  it('mounts exactly one of each provider', async () => {
    // Two QueryClientProviders would give half the app a different cache; two
    // SessionProviders would each fetch /auth/me and could disagree about who
    // is signed in; two NotificationsProviders would render two lists, one of
    // which nothing can reach.
    resetServerState(makeSession())
    render(<App queryClient={createQueryClient()} router={testRouter()} />)

    await screen.findByRole('heading', { name: 'Origin page' })

    // One session, resolved once: the page reads it through the context.
    await waitFor(() => expect(screen.getByTestId('who')).toHaveTextContent('Ops Person'))

    // One notification region pair, not two.
    expect(document.querySelectorAll('.notifications')).toHaveLength(1)
    expect(document.querySelectorAll('[aria-live="assertive"]')).toHaveLength(1)
    expect(document.querySelectorAll('[aria-live="polite"]')).toHaveLength(1)

    // One router: the destination is not already mounted alongside the origin.
    expect(screen.queryByRole('heading', { name: 'Destination page' })).not.toBeInTheDocument()
  })

  it('KEEPS A NOTIFICATION ACROSS NAVIGATION', async () => {
    // The reason NotificationsProvider sits above RouterProvider. An operator
    // deletes someone on a detail page and is returned to the list; the
    // confirmation has to survive that or it is never read.
    resetServerState(makeSession())
    const user = userEvent.setup()
    render(<App queryClient={createQueryClient()} router={testRouter()} />)

    await screen.findByRole('heading', { name: 'Origin page' })
    await user.click(screen.getByRole('button', { name: 'Do the thing' }))
    expect(screen.getByText('Person removed from every terminal')).toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: 'Go elsewhere' }))

    // The page that raised it is gone...
    await screen.findByRole('heading', { name: 'Destination page' })
    expect(screen.queryByRole('heading', { name: 'Origin page' })).not.toBeInTheDocument()
    // ...and the message it raised is still on screen.
    expect(screen.getByText('Person removed from every terminal')).toBeInTheDocument()
  })

  it('routes normally, so the tree is not merely inert', async () => {
    resetServerState(makeSession())
    const user = userEvent.setup()
    render(<App queryClient={createQueryClient()} router={testRouter()} />)

    await screen.findByRole('heading', { name: 'Origin page' })
    await user.click(screen.getByRole('link', { name: 'Go elsewhere' }))
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: 'Destination page' })).toBeInTheDocument(),
    )
  })
})
