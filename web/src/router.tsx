import { Navigate, createBrowserRouter } from 'react-router-dom'

import { LoginPage } from './auth/LoginPage'
import { RequireAuth, RequireRole } from './auth/guards'
import { AppShell } from './layout/AppShell'
import { DashboardPage } from './pages/DashboardPage'
import { ApplicationPlaceholder, NotImplemented } from './pages/NotImplemented'
import { PeopleListPage } from './pages/people/PeopleListPage'
import { PersonDetailPage } from './pages/people/PersonDetailPage'
import { SiteDetailPage } from './pages/sites/SiteDetailPage'
import { TerminalDetailPage } from './pages/terminals/TerminalDetailPage'
import { TerminalsListPage } from './pages/terminals/TerminalsListPage'
import { SitesListPage } from './pages/sites/SitesListPage'

/**
 * Routes.
 *
 * Platform resources have fixed paths because they exist for every deployment.
 * Application modules share ONE parameterised route -- /applications/:slug --
 * because the set of capabilities is configuration, and a route per module would
 * mean a frontend release every time the platform gained one.
 *
 * Phase 1: everything except the overview renders a placeholder. The routes,
 * guards and navigation are what is being built here; the screens come later.
 */
export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },

  {
    path: '/',
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <DashboardPage /> },

      // People are addressed by external_id -- the identifier terminals hold
      // and sync against, and the one an operator already knows.
      { path: 'people', element: <PeopleListPage /> },
      { path: 'people/:externalId', element: <PersonDetailPage /> },
      // Terminals are a PLATFORM resource. The serial is the path parameter
      // because it is what the API addresses a terminal by, and what is printed
      // on the hardware an operator is standing in front of.
      { path: 'terminals', element: <TerminalsListPage /> },
      { path: 'terminals/:serial', element: <TerminalDetailPage /> },
      // Sites are a PLATFORM resource: every deployment has locations,
      // whatever it uses the platform for. The lifecycle writes behind these
      // screens are ADMIN-gated in the UI and enforced by the API regardless.
      { path: 'sites', element: <SitesListPage /> },
      { path: 'sites/:siteId', element: <SiteDetailPage /> },
      {
        path: 'operators',
        element: (
          <RequireRole minimum="ADMIN">
            <NotImplemented
              title="Operators"
              description="Who can sign in to this console, and what they may do."
            />
          </RequireRole>
        ),
      },
      {
        path: 'settings/applications',
        element: (
          <RequireRole minimum="OWNER">
            <NotImplemented
              title="Applications"
              description="Which capabilities this company has enabled."
            />
          </RequireRole>
        ),
      },

      { path: 'applications/:slug', element: <ApplicationPlaceholder /> },

      {
        path: 'forbidden',
        element: (
          <NotImplemented
            title="Not available to you"
            description="Your role does not include this area."
            detail="If you need access, ask an owner or administrator of your company to change your role."
          />
        ),
      },

      {
        path: '*',
        element: (
          <NotImplemented
            title="Page not found"
            detail="That address does not match anything in the console."
          />
        ),
      },
    ],
  },

  { path: '*', element: <Navigate to="/" replace /> },
])
