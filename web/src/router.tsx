import { createBrowserRouter, type RouteObject } from 'react-router-dom'

import { ForgotPasswordPage } from './auth/ForgotPasswordPage'
import { LoginPage } from './auth/LoginPage'
import { RedeemPage } from './auth/RedeemPage'
import { RegisterPage } from './auth/RegisterPage'
import { RequireAuth, RequireRole } from './auth/guards'
import { AppShell } from './layout/AppShell'
import { DocumentTitle } from './layout/DocumentTitle'
import { DashboardPage } from './pages/DashboardPage'
import { Forbidden, NotFound, PublicNotFound } from './pages/ErrorPage'
import { ActivityPage } from './pages/activity/ActivityPage'
import { SchedulesPage } from './pages/access/SchedulesPage'
import { ApiCredentialDetailPage } from './pages/api-credentials/ApiCredentialDetailPage'
import { ApiCredentialsListPage } from './pages/api-credentials/ApiCredentialsListPage'
import { EventsPage } from './pages/events/EventsPage'
import { FirmwarePage } from './pages/firmware/FirmwarePage'
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
import { useSession } from './session/useSession'

/**
 * Routes.
 *
 * Platform resources have fixed paths because they exist for every deployment.
 *
 * THERE IS NO /applications/:slug ROUTE. Capabilities are configuration, and
 * every one of them used to resolve to a shared page that said the screens for
 * it had not been written. A navigation entry leading to that is worse than no
 * entry: it invites an operator to go looking for a workflow, and then tells
 * them about the state of our build. A capability appears in the navigation once
 * it has a screen to appear for -- see `route` in applications/registry.ts.
 *
 * Enabling and configuring capabilities is unaffected and lives, as it always
 * has, under /settings/applications.
 */
const appRoutes: RouteObject[] = [
  { path: '/login', element: <LoginPage />, handle: { title: 'Sign in' } },

  // Self-service signup, OUTSIDE the authenticated tree by necessity: somebody
  // creating their first account has nothing to authenticate with. It sits
  // beside /login rather than inside the console tree because it ends in a
  // session — the guard below is what they land behind once it succeeds.
  { path: '/register', element: <RegisterPage />, handle: { title: 'Create an account' } },

  // Credential handover, OUTSIDE the authenticated tree by necessity. Somebody
  // redeeming an invitation has never had a password and somebody who has
  // forgotten theirs cannot sign in to ask — neither can be behind RequireAuth.
  { path: '/forgot-password', element: <ForgotPasswordPage />, handle: { title: 'Reset your password' } },
  { path: '/redeem', element: <RedeemPage />, handle: { title: 'Set your password' } },

  {
    path: '/',
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <DashboardPage />, handle: { title: 'Overview' } },

      // People are addressed by external_id -- the identifier terminals hold
      // and sync against, and the one an operator already knows.
      { path: 'people', element: <PeopleListPage />, handle: { title: 'People' } },
      { path: 'people/:externalId', element: <PersonDetailPage />, handle: { title: 'Person' } },
      // Terminals are a PLATFORM resource. The serial is the path parameter
      // because it is what the API addresses a terminal by, and what is printed
      // on the hardware an operator is standing in front of.
      { path: 'terminals', element: <TerminalsListPage />, handle: { title: 'Terminals' } },
      { path: 'terminals/:serial', element: <TerminalDetailPage />, handle: { title: 'Terminal' } },
      // Sites are a PLATFORM resource: every deployment has locations,
      // whatever it uses the platform for. The lifecycle writes behind these
      // screens are ADMIN-gated in the UI and enforced by the API regardless.
      { path: 'sites', element: <SitesListPage />, handle: { title: 'Sites' } },
      { path: 'sites/:siteId', element: <SiteDetailPage />, handle: { title: 'Site' } },
      // ADMIN, matching the server's route group. RequireRole is a courtesy --
      // the API refuses every one of these regardless of what the router allows.
      {
        path: 'operators',
        handle: { title: 'Operators' },
        element: (
          <RequireRole minimum="ADMIN">
            <OperatorsListPage />
          </RequireRole>
        ),
      },
      {
        path: 'operators/:operatorId',
        handle: { title: 'Operator' },
        element: (
          <RequireRole minimum="ADMIN">
            <OperatorDetailPage />
          </RequireRole>
        ),
      },
      // The door log. VIEWER, unlike Activity below, which is ADMIN — an event
      // trail says what happened in the field, while an audit trail names which
      // operators changed what.
      { path: 'events', element: <EventsPage />, handle: { title: 'Events' } },

      // Who may go where, and when. MANAGER to change, VIEWER to read, and the
      // write gate lives on the controls rather than the route so a viewer can
      // still answer "why was she refused".
      { path: 'access/schedules', element: <SchedulesPage />, handle: { title: 'Schedules' } },

      // ADMIN, matching the server's route group: an audit trail names which
      // operators did what, which is administrative information rather than
      // something every viewer needs.
      {
        path: 'activity',
        handle: { title: 'Activity' },
        element: (
          <RequireRole minimum="ADMIN">
            <ActivityPage />
          </RequireRole>
        ),
      },

      // Your own account and your company, plus signposts to the other
      // configuration scopes. No role gate: it holds your own password.
      { path: 'settings', element: <SettingsPage />, handle: { title: 'Settings' } },

      // ADMIN to READ, OWNER to change -- the write gate lives on the controls
      // rather than the route, so an administrator can see what the company is
      // configured for without being able to decide it. The API is looser still
      // (any operator may read), so this is the stricter of the two.
      // ADMIN, matching the server's route group. These writes move the value
      // every "is this terminal outdated" report is measured against, which is
      // why they left the site-key tree.
      {
        path: 'settings/firmware',
        handle: { title: 'Firmware' },
        element: (
          <RequireRole minimum="ADMIN">
            <FirmwarePage />
          </RequireRole>
        ),
      },

      {
        path: 'settings/applications',
        handle: { title: 'Features' },
        element: (
          <RequireRole minimum="ADMIN">
            <ApplicationsPage />
          </RequireRole>
        ),
      },
      {
        path: 'settings/applications/:slug',
        handle: { title: 'Feature' },
        element: (
          <RequireRole minimum="ADMIN">
            <ApplicationDetailPage />
          </RequireRole>
        ),
      },

      // ADMIN, matching the server's route group for /console/api-credentials.
      // Same courtesy as the operators routes: the API refuses MANAGER and
      // below with 403 regardless; this only spares an administrator's
      // colleague a request that would fail.
      {
        path: 'settings/api-credentials',
        handle: { title: 'API access' },
        element: (
          <RequireRole minimum="ADMIN">
            <ApiCredentialsListPage />
          </RequireRole>
        ),
      },
      {
        path: 'settings/api-credentials/:credentialId',
        handle: { title: 'API credential' },
        element: (
          <RequireRole minimum="ADMIN">
            <ApiCredentialDetailPage />
          </RequireRole>
        ),
      },

      { path: 'forbidden', element: <Forbidden />, handle: { title: 'Not available' } },
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
    handle: { title: 'Platform sign in' },
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
      { index: true, element: <CompaniesPage />, handle: { title: 'Companies' } },
      { path: 'companies/:companyId', element: <CompanyDetailPage />, handle: { title: 'Company' } },
    ],
  },

  /*
    EVERY ADDRESS THAT MATCHES NOTHING, signed in or not.

    This used to be two routes: a catch-all inside the console tree, which a
    signed-out visitor never reached because RequireAuth sent them to sign in
    first, and a redirect to the root for anything outside it. So a mistyped or
    stale link said "sign in" to somebody without a session, as though the
    address existed. Now the session decides the FRAME and the answer is the
    same: the console's own page inside the shell for an operator, the public
    one for anybody else.
  */
  {
    element: <NotFoundFrame />,
    children: [{ path: '*', element: <NotFound />, handle: { title: 'Page not found' } }],
  },
]

/**
 * Every route sits under DocumentTitle, which names the browser tab from the
 * matched route's `handle.title`. Exported so a test can drive the real table
 * through a memory router; `router` below is the one the app mounts.
 */
export const routes: RouteObject[] = [{ element: <DocumentTitle />, children: appRoutes }]

export const router = createBrowserRouter(routes)

/**
 * The frame an unmatched address is shown in. A signed-out visitor gets the
 * public page; everybody else goes through RequireAuth exactly as a real
 * screen would -- including its loading, unreachable-API and forced password
 * change states -- and sees NotFound inside the shell.
 */
function NotFoundFrame() {
  const { status } = useSession()
  if (status === 'anonymous') return <PublicNotFound />
  return (
    <RequireAuth>
      <AppShell />
    </RequireAuth>
  )
}
