import { render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import { Forbidden, NotFound } from './ErrorPage'

/**
 * The two dead ends.
 *
 * BOTH OF THESE USED TO SAY "Not implemented yet". They shared a placeholder
 * component built for a phase of the project when most screens were stubs, so
 * an operator who mistyped a URL, or opened a page their role does not cover,
 * was told the console had not been written. Neither statement was true, and on
 * a product somebody is paying for the second one is alarming.
 *
 * These tests are as much about what the pages must NOT say as what they do.
 */

const DEV_STATUS = [
  /not implemented/i,
  /not built/i,
  /partly built/i,
  /coming soon/i,
  /under construction/i,
  /in development/i,
  /placeholder/i,
]

function renderAt(element: React.ReactElement) {
  const router = createMemoryRouter(
    [
      { path: '/', element: <p>Overview</p> },
      { path: '/where', element },
    ],
    { initialEntries: ['/where'] },
  )
  return render(<RouterProvider router={router} />)
}

describe('403 — a page this operator may not open', () => {
  it('says what happened and who can change it', async () => {
    renderAt(<Forbidden />)

    expect(screen.getByRole('heading', { name: 'Not available to you', level: 1 })).toBeInTheDocument()
    expect(screen.getByText(/Your role does not include this area/)).toBeInTheDocument()
    // Actionable: the operator cannot fix this themselves, so name who can.
    expect(screen.getByText(/ask an owner or administrator/i)).toBeInTheDocument()
  })

  it('offers a way back rather than stranding them', () => {
    renderAt(<Forbidden />)
    expect(screen.getByRole('link', { name: /back to the overview/i })).toHaveAttribute('href', '/')
  })

  it('says nothing about the state of the build', () => {
    renderAt(<Forbidden />)
    const text = document.body.textContent ?? ''
    for (const pattern of DEV_STATUS) {
      expect(text, `403 must not say ${pattern}`).not.toMatch(pattern)
    }
  })
})

describe('404 — an address that matches nothing', () => {
  it('says the address is wrong, not that the page is unwritten', () => {
    renderAt(<NotFound />)

    expect(screen.getByRole('heading', { name: 'Page not found', level: 1 })).toBeInTheDocument()
    expect(screen.getByText(/does not match anything in the console/i)).toBeInTheDocument()
  })

  it('offers a way back', () => {
    renderAt(<NotFound />)
    expect(screen.getByRole('link', { name: /back to the overview/i })).toHaveAttribute('href', '/')
  })

  it('says nothing about the state of the build', () => {
    renderAt(<NotFound />)
    const text = document.body.textContent ?? ''
    for (const pattern of DEV_STATUS) {
      expect(text, `404 must not say ${pattern}`).not.toMatch(pattern)
    }
  })

  it('gives each page exactly one h1, as every other screen does', () => {
    const { unmount } = renderAt(<NotFound />)
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    unmount()

    renderAt(<Forbidden />)
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
  })
})
