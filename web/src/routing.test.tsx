import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, type RouteObject } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import { App, createQueryClient } from './App'
import { TITLE_SUFFIX, type RouteHandle } from './layout/DocumentTitle'
import { routes } from './router'
import { makeSession } from './test/fixtures'
import { resetServerState } from './test/server'

/**
 * The REAL route table, driven through a memory router inside the real App:
 * what an address resolves to, what the tab is called, and that the public
 * site still knows every address the console used to answer on it.
 */

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<App queryClient={createQueryClient()} router={router} />)
  return router
}

describe('an address that matches nothing', () => {
  it('is "Page not found" for somebody signed out, not a sign-in prompt', async () => {
    resetServerState(null)
    const router = renderAt('/no-such-page')

    expect(await screen.findByRole('heading', { name: 'Page not found', level: 1 })).toBeInTheDocument()
    // The address is kept: this is an answer about it, not a redirect away.
    expect(router.state.location.pathname).toBe('/no-such-page')
    expect(screen.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/login')
    expect(screen.getByRole('link', { name: 'AccessLink home' })).toHaveAttribute('href', 'https://accesslink.store/')
    await waitFor(() => expect(document.title).toBe(`Page not found · ${TITLE_SUFFIX}`))
  })

  it('is the console page, inside the shell, for an operator', async () => {
    resetServerState(makeSession())
    renderAt('/settings/no-such-thing')

    expect(await screen.findByRole('heading', { name: 'Page not found', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to the overview' })).toHaveAttribute('href', '/')
    // The shell is there: navigation and the way out of the dead end.
    expect(screen.getAllByRole('navigation').length).toBeGreaterThan(0)
  })

  it('does not stop a real screen from asking a signed-out visitor to sign in', async () => {
    resetServerState(null)
    const router = renderAt('/people')

    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(router.state.location.search).toBe(`?next=${encodeURIComponent('/people')}`)
  })
})

describe('the browser tab', () => {
  it('is named after the page', async () => {
    resetServerState(null)
    renderAt('/login')
    await waitFor(() => expect(document.title).toBe(`Sign in · ${TITLE_SUFFIX}`))
  })

  it('names the screen an operator is on', async () => {
    resetServerState(makeSession())
    renderAt('/people')
    await waitFor(() => expect(document.title).toBe(`People · ${TITLE_SUFFIX}`))
  })

  it('has a title declared for every screen in the table', () => {
    // A leaf is a route that renders a page rather than a frame for others.
    const untitled: string[] = []
    const walk = (list: RouteObject[], base: string) => {
      for (const route of list) {
        const path = route.index ? base : route.path ? (route.path.startsWith('/') ? route.path : `${base.replace(/\/$/, '')}/${route.path}`) : base
        if (route.children) walk(route.children, path)
        else if (!(route.handle as RouteHandle | undefined)?.title) untitled.push(path || '(index)')
      }
    }
    walk(routes, '')
    expect(untitled).toEqual([])
  })
})

describe('the console moving to app.accesslink.store', () => {
  it('leaves no console address on accesslink.store without a forward', () => {
    // render.yaml's accesslink-site rewrites each of the console's old paths
    // to console-redirect.html. A top-level route added here without a rule
    // there would 404 on the public site for anybody holding an old link.
    const yaml = readFileSync(resolve(__dirname, '..', '..', 'render.yaml'), 'utf8')
    const forwarded = new Set(
      [...yaml.matchAll(/source: (\S+), destination: \/console-redirect\.html/g)].map((m) => m[1]),
    )

    const missing = new Set<string>()
    const walk = (list: RouteObject[], base: string) => {
      for (const route of list) {
        const path = route.path?.startsWith('/') ? route.path : route.path ? `${base}/${route.path}` : base
        const first = path.split('/').filter(Boolean)[0]
        if (first && first !== '*') {
          const exact = `/${first}`
          const nested = path.replace(/\/$/, '') !== exact
          if (!forwarded.has(nested ? `${exact}/*` : exact)) missing.add(nested ? `${exact}/*` : exact)
        }
        if (route.children) walk(route.children, path === '/' ? '' : path)
      }
    }
    walk(routes, '')
    expect([...missing]).toEqual([])
  })
})
