import { Navigate, createBrowserRouter } from 'react-router-dom'

import { ForgotPasswordPage } from './auth/ForgotPasswordPage'
import { LoginPage } from './auth/LoginPage'
import { RedeemPage } from './auth/RedeemPage'
import { RequireAuth, RequireRole } from './auth/guards'
import { AppShell } from './layout/AppShell'
import { DashboardPage } from './pages/DashboardPage'
import { ApplicationPlaceholder, NotImplemented } from './pages/NotImplemented'
import { ActivityPage } from './pages/activity/ActivityPage'
import { ApplicationDetailPage } from './pages/applications/ApplicationDetailPage'
import { ApplicationsPage } from './pages/applications/ApplicationsPage'
import { OperatorDetailPage } from './pages/operators/OperatorDetailPage'
import { OperatorsListPage } from './pages/operators/OperatorsListPage'
import { PeopleListPage } from './pages/people/PeopleListPage'
import { CompaniesPage } from './platform/CompaniesPage'
import { CompanyDetailPage } from './platform/CompanyDetailPage'
import { PlatformLoginPage } from './platform/PlatformLoginPage'
import { PlatformSessionProvider } from './platform/PlatformSessionProvider'
import { PlatformShell } from './platform/PlatformShell'
import { SettingsPage } from './pages/settings/SettingsPage'
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

  // Credential handover, OUTSIDE the authenticated tree by necessity. Somebody
  // redeeming an invitation has never had a password and somebody who has
  // forgotten theirs cannot sign in to ask — neither can be behind RequireAuth.
  { path: '/forgot-password', element: <ForgotPasswordPage /> },
  { path: '/redeem', element: <RedeemPage /> },

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
      // ADMIN, matching the server's route group. RequireRole is a courtesy --
      // the API refuses every one of these regardless of what the router allows.
      {
        path: 'operators',
        element: (
          <RequireRole minimum="ADMIN">
            <OperatorsListPage />
          </RequireRole>
        ),
      },
      {
        path: 'operators/:operatorId',
        element: (
          <RequireRole minimum="ADMIN">
            <OperatorDetailPage />
          </RequireRole>
        ),
      },
      // ADMIN, matching the server's route group: an audit trail names which
      // operators did what, which is administrative information rather than
      // something every viewer needs.
      {
        path: 'activity',
        element: (
          <RequireRole minimum="ADMIN">
            <ActivityPage />
          </RequireRole>
        ),
      },

      // Your own account and your company, plus signposts to the other
      // configuration scopes. No role gate: it holds your own password.
      { path: 'settings', element: <SettingsPage /> },

      // ADMIN to READ, OWNER to change -- the write gate lives on the controls
      // rather than the route, so an administrator can see what the company is
      // configured for without being able to decide it. The API is looser still
      // (any operator may read), so this is the stricter of the two.
      {
        path: 'settings/applications',
        element: (
          <RequireRole minimum="ADMIN">
            <ApplicationsPage />
          </RequireRole>
        ),
      },
      {
        path: 'settings/applications/:slug',
        element: (
          <RequireRole minimum="ADMIN">
            <ApplicationDetailPage />
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

  /*
    PLATFORM ADMINISTRATION, a separate tree for a separate identity.

    Not nested under the console's RequireAuth, and not reachable from its
    navigation: this authenticates a different table with a different cookie, and
    a tenant operator has no business seeing that the surface exists. Its
    provider is mounted here rather than at the app root so that a console user
    never issues a request to /api/v1/platform/me at all.
  */
  {
    path: '/platform/login',
    element: (
      <PlatformSessionProvider>
        <PlatformLoginPage />
      </PlatformSessionProvider>
    ),
  },
  {
    path: '/platform',
    element: (
      <PlatformSessionProvider>
        <PlatformShell />
      </PlatformSessionProvider>
    ),
    children: [
      { index: true, element: <CompaniesPage /> },
      { path: 'companies/:companyId', element: <CompanyDetailPage /> },
    ],
  },

  { path: '*', element: <Navigate to="/" replace /> },
])
