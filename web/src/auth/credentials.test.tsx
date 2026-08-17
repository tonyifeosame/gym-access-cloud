import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import { makeSession } from '../test/fixtures'
import { makeTestQueryClient, renderWithQuery, renderWithSession } from '../test/render'
import { resetServerState, state } from '../test/server'
import { ForgotPasswordPage } from './ForgotPasswordPage'
import { RedeemPage } from './RedeemPage'
import { RequireAuth } from './guards'

/**
 * Credential handover at the unauthenticated end (PPL-02, SEC-10).
 *
 * Three screens that sit OUTSIDE the session by necessity: somebody who has
 * forgotten their password cannot sign in to ask for a new one, somebody
 * redeeming an invitation has never had one, and somebody whose password was
 * chosen by an administrator has to be stopped before they browse anywhere.
 *
 * What is asserted here is mostly about DISCLOSURE — what these screens say,
 * what they refuse to say, and what they leave lying around — because that is
 * where an unauthenticated, permanently exposed surface goes wrong.
 */

function renderAt(path: string) {
  const router = createMemoryRouter(
    [
      { path: '/forgot-password', element: <ForgotPasswordPage /> },
      { path: '/redeem', element: <RedeemPage /> },
      { path: '/login', element: <h1>Sign in</h1> },
    ],
    { initialEntries: [path] },
  )
  return renderWithQuery(<RouterProvider router={router} />)
}

beforeEach(() => {
  setCsrfToken(null)
  resetServerState(null)
})

// ---------------------------------------------------------------------------
// Forgot password
// ---------------------------------------------------------------------------

describe('requesting a password reset', () => {
  it('answers identically whether or not the address exists', async () => {
    // The server refuses to be an enumeration oracle; a console that branched on
    // the response would reintroduce one in the browser. Both addresses below
    // must produce the SAME screen.
    const user = userEvent.setup()

    renderAt('/forgot-password')
    await user.type(screen.getByLabelText('Email'), 'nobody@example.com')
    await user.click(screen.getByRole('button', { name: /request a reset/i }))
    const unknown = (await screen.findByRole('heading', { level: 1 })).textContent

    resetServerState(null)
    renderAt('/forgot-password')
    const forms = screen.getAllByLabelText('Email')
    await user.type(forms[forms.length - 1] as HTMLElement, 'ops@example.com')
    const buttons = screen.getAllByRole('button', { name: /request a reset/i })
    await user.click(buttons[buttons.length - 1] as HTMLElement)

    const known = (await screen.findAllByRole('heading', { level: 1 })).pop()?.textContent
    expect(known).toBe(unknown)
  })

  it('says plainly that the platform cannot deliver the link', async () => {
    // An operator told "check your inbox" would wait indefinitely for a message
    // this platform has no way to send.
    const user = userEvent.setup()
    renderAt('/forgot-password')

    await user.type(screen.getByLabelText('Email'), 'ops@example.com')
    await user.click(screen.getByRole('button', { name: /request a reset/i }))

    expect(await screen.findByText(/does not send email/i)).toBeInTheDocument()
    expect(screen.getByText(/ask them to issue you a reset link/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Redeem
// ---------------------------------------------------------------------------

describe('redeeming an invitation or reset link', () => {
  it('STRIPS THE TOKEN FROM THE ADDRESS BAR on arrival', async () => {
    // A secret in a URL leaks into history, into Referer headers, into
    // screenshots and over the shoulder of whoever is typing. It cannot be kept
    // out of the link — that is the only thing a recipient can be sent — so it
    // is removed the moment it has been read.
    state.redeemable['good-token'] = 'ok'
    renderAt('/redeem?token=good-token')

    await screen.findByRole('heading', { name: /set your password/i })
    await waitFor(() => expect(window.location.search).toBe(''))
  })

  it('does not put the token back on screen in a field', async () => {
    state.redeemable['good-token'] = 'ok'
    renderAt('/redeem?token=good-token')

    await screen.findByRole('heading', { name: /set your password/i })
    // Stripping the URL and then rendering the value in a visible input would
    // defeat the point.
    expect(screen.queryByLabelText('Code')).not.toBeInTheDocument()
    expect(document.body.textContent).not.toContain('good-token')
  })

  it('offers a code field when the page is opened without a link', async () => {
    renderAt('/redeem')
    expect(await screen.findByLabelText('Code')).toBeInTheDocument()
  })

  it('sets the password and sends them to sign in rather than signing them in', async () => {
    // The API answers 204, not a session. Redeeming sets a password; the new
    // credential is then exercised once, immediately, while the person who set
    // it is still there to notice if it did not work.
    const user = userEvent.setup()
    state.redeemable['good-token'] = 'ok'
    renderAt('/redeem?token=good-token')

    await user.type(await screen.findByLabelText('New password'), 'a-long-enough-password')
    await user.type(screen.getByLabelText('Confirm password'), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: /set password/i }))

    expect(await screen.findByRole('heading', { name: /password set/i })).toBeInTheDocument()
    expect(screen.getByText(/signed out/i)).toBeInTheDocument()
  })

  it('refuses to submit until the two passwords agree', async () => {
    const user = userEvent.setup()
    state.redeemable['good-token'] = 'ok'
    renderAt('/redeem?token=good-token')

    await user.type(await screen.findByLabelText('New password'), 'a-long-enough-password')
    await user.type(screen.getByLabelText('Confirm password'), 'a-long-enough-passwerd')

    expect(screen.getByRole('button', { name: /set password/i })).toBeDisabled()
    expect(screen.getByText(/do not match/i)).toBeInTheDocument()
  })

  it('tells an EXPIRED link apart from an unknown one and from a used one', async () => {
    // Not an enumeration risk — whoever holds the token holds the secret — and
    // each says something different about what to do next. Branched on STATUS,
    // because the API's error strings are explicitly not stable.
    const cases: [string, 'expired' | 'used', RegExp][] = [
      ['expired-token', 'expired', /has expired/i],
      ['used-token', 'used', /already been used/i],
    ]

    for (const [token, verdict, expected] of cases) {
      const user = userEvent.setup()
      resetServerState(null)
      state.redeemable[token] = verdict
      const view = renderAt(`/redeem?token=${token}`)

      await user.type(await screen.findByLabelText('New password'), 'a-long-enough-password')
      await user.type(screen.getByLabelText('Confirm password'), 'a-long-enough-password')
      await user.click(screen.getByRole('button', { name: /set password/i }))

      expect(await screen.findByRole('alert')).toHaveTextContent(expected)
      view.unmount()
    }
  })

  it('treats an unknown link as one that may simply have been superseded', async () => {
    const user = userEvent.setup()
    renderAt('/redeem?token=never-issued')

    await user.type(await screen.findByLabelText('New password'), 'a-long-enough-password')
    await user.type(screen.getByLabelText('Confirm password'), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: /set password/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/newer link may have replaced it/i)
  })
})

// ---------------------------------------------------------------------------
// Forced first change
// ---------------------------------------------------------------------------

describe('a password somebody else chose', () => {
  function signInNeedingChange() {
    const session = makeSession({ must_change_password: true })
    resetServerState(session)
    setCsrfToken(session.csrf_token)
    return session
  }

  /**
   * The guard inside a router, because RequireAuth reads the location to
   * remember where an anonymous visitor was headed.
   */
  function renderGuarded() {
    const router = createMemoryRouter(
      [
        {
          path: '/',
          element: (
            <RequireAuth>
              <h1>The console</h1>
            </RequireAuth>
          ),
        },
        { path: '/login', element: <h1>Sign in</h1> },
      ],
      { initialEntries: ['/'] },
    )
    return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
  }

  it('BLOCKS the console rather than suggesting a change', async () => {
    // The server reports must_change_password and deliberately does not enforce
    // it — an account refused every request could not reach /auth/password
    // either. So the block lives here, and it is a block: a dismissible banner
    // gets dismissed and the credential somebody else chose stays live.
    signInNeedingChange()
    renderGuarded()

    expect(
      await screen.findByRole('heading', { name: /choose your own password/i }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'The console' })).not.toBeInTheDocument()

    // No way past it except changing the password or leaving.
    expect(screen.queryByRole('button', { name: /later|skip|dismiss/i })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign out instead/i })).toBeInTheDocument()
  })

  it('says WHY, rather than presenting it as a policy', async () => {
    signInNeedingChange()
    renderGuarded()

    expect(await screen.findByText(/They still know it/)).toBeInTheDocument()
  })

  it('refuses the password they were given as the new one', async () => {
    const user = userEvent.setup()
    signInNeedingChange()
    renderGuarded()

    await screen.findByRole('heading', { name: /choose your own password/i })
    await user.type(screen.getByLabelText('Current password'), 'the-one-they-gave-me')
    await user.type(screen.getByLabelText('New password'), 'the-one-they-gave-me')
    await user.type(screen.getByLabelText('Confirm new password'), 'the-one-they-gave-me')

    expect(screen.getByText(/Choose a different one/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /set my password/i })).toBeDisabled()
  })

  it('lets the console through once the flag is clear', async () => {
    const session = makeSession({ must_change_password: false })
    resetServerState(session)
    setCsrfToken(session.csrf_token)

    renderGuarded()

    expect(await screen.findByRole('heading', { name: 'The console' })).toBeInTheDocument()
  })
})
