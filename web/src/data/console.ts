import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query'

import * as endpoints from '../api/endpoints'
import type {
  ApplicationCode,
  ApplicationRequest,
  ApplicationsResponse,
  AuditPage,
  AuditQuery,
  CompanyDetail,
  ConfiguredApplication,
  CreateFirmwareRequest,
  CreateOperatorRequest,
  CreateOperatorResponse,
  CreateSiteRequest,
  CreateSiteResponse,
  FirmwareResponse,
  FirmwareVersion,
  FleetSummary,
  InvitationResponse,
  OperatorAccount,
  OperatorSitesResponse,
  OperatorsResponse,
  PeoplePage,
  PeopleQuery,
  Person,
  PersonRequest,
  ResetResponse,
  RetireSiteResponse,
  RotateSiteKeyResponse,
  Site,
  SiteGrantsRequest,
  SiteSettings,
  SiteSettingsRequest,
  SitesResponse,
  TerminalDetail,
  TerminalLifecycleResponse,
  TerminalModeRequest,
  TerminalMoveRequest,
  TerminalResyncResponse,
  TerminalRetireRequest,
  TerminalRetiredResponse,
  TerminalRevokeRequest,
  TerminalStateRequest,
  TerminalsResponse,
  UpdateOperatorRequest,
  UpdateSiteRequest,
} from '../api/types'
import { keys } from './keys'

/**
 * The console's data layer.
 *
 * Every screen reads and writes through these hooks. Nothing calls `endpoints`
 * directly and nothing spells a query key by hand, because the two things that
 * go wrong otherwise are both silent: a screen that keeps showing a row it just
 * deleted, and two components fetching the same resource under different keys.
 *
 * WHAT EACH MUTATION INVALIDATES IS PART OF THE CONTRACT, not an afterthought.
 * A write is not finished when the request returns — it is finished when
 * everything on screen that could contradict it has been told. Where a write has
 * effects beyond its own resource (a person's deletion reaching terminals, a
 * settings change reaching hardware) that is written down at the hook.
 *
 * NOTE ON `useQuery` GENERICS. Each hook names its own return type rather than
 * leaning on inference, so a change to an API contract surfaces here as a type
 * error instead of as `unknown` spreading quietly through a screen.
 */

// ---------------------------------------------------------------------------
// Company
// ---------------------------------------------------------------------------

export function useCompany(): UseQueryResult<CompanyDetail> {
  return useQuery({
    queryKey: keys.company.detail(),
    queryFn: () => endpoints.fetchCompany(),
    // The tenant's own name and slug change about never.
    staleTime: 5 * 60_000,
  })
}

// ---------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------

export function useSites(): UseQueryResult<SitesResponse> {
  return useQuery({
    queryKey: keys.sites.list(),
    queryFn: () => endpoints.fetchSites(),
    staleTime: 60_000,
  })
}

export function useSite(siteId: string | undefined): UseQueryResult<Site> {
  return useQuery({
    queryKey: keys.sites.detail(siteId ?? ''),
    queryFn: () => endpoints.fetchSite(siteId as string),
    enabled: Boolean(siteId),
  })
}

export function useSiteSettings(siteId: string | undefined): UseQueryResult<SiteSettings> {
  return useQuery({
    queryKey: keys.sites.settings(siteId ?? ''),
    queryFn: () => endpoints.fetchSiteSettings(siteId as string),
    enabled: Boolean(siteId),
    // Settings are edited deliberately and rarely; a stale copy here would be
    // written straight back over a colleague's change, since the write is a
    // full replacement.
    staleTime: 0,
  })
}

/**
 * Creates a site.
 *
 * THE RESPONSE CARRIES A PROVISIONING KEY, and this hook is careful about where
 * that goes. React Query keeps every mutation's `data` for the life of the
 * hook, so the caller is expected to read the credential once, hand it to the
 * panel that displays it, and `reset()` the mutation when that panel closes.
 * Nothing here writes the credential into the QUERY cache, which is the part
 * that would otherwise outlive the screen and be readable from anywhere.
 *
 * The site list is invalidated; the new site belongs on it.
 */
export function useCreateSite(): UseMutationResult<CreateSiteResponse, Error, CreateSiteRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateSiteRequest) => endpoints.createSite(body),
    onSuccess: (result) => {
      // The SITE, deliberately -- not the response. Writing the whole thing
      // would put the key in the query cache, where it would survive the panel
      // that showed it and be readable by any component with the key factory.
      queryClient.setQueryData(keys.sites.detail(result.site.id), result.site)
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
    },
  })
}

/**
 * Updates a site's metadata, including reversible deactivation.
 *
 * Terminals are invalidated as well: deactivating a site stops every terminal
 * there authenticating, so a fleet view showing them as they were is stale in
 * the way that matters.
 */
export function useUpdateSite(
  siteId: string,
): UseMutationResult<Site, Error, UpdateSiteRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: UpdateSiteRequest) => endpoints.updateSite(siteId, body),
    onSuccess: (site) => {
      queryClient.setQueryData(keys.sites.detail(siteId), site)
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
    },
  })
}

/**
 * Retires a site and every terminal at it.
 *
 * The site's own cache entries are REMOVED rather than invalidated -- refetching
 * either would only produce a 404. Terminals go too, because the ones at this
 * site have just been soft-deleted server-side and any list still holding them
 * is showing hardware that no longer exists.
 *
 * The SESSION is invalidated last and it is the one that is easy to miss: site
 * grants are carried on the session, and an operator scoped to the site just
 * retired is now scoped to something that is gone. Leaving that stale gives the
 * site switcher an option the API will refuse.
 */
export function useRetireSite(): UseMutationResult<RetireSiteResponse, Error, string> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (siteId: string) => endpoints.retireSite(siteId),
    onSuccess: (_result, siteId) => {
      queryClient.removeQueries({ queryKey: keys.sites.detail(siteId) })
      queryClient.removeQueries({ queryKey: keys.sites.settings(siteId) })
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
      void queryClient.invalidateQueries({ queryKey: SESSION_KEY })
    },
  })
}

/**
 * Rotates a site's provisioning key.
 *
 * NOTHING CACHED CHANGES, which is why this invalidates so little: the key is
 * not part of any read, and the site's metadata is untouched. The site list is
 * refreshed only so that a displayed key PREFIX, where the API provides one,
 * does not go stale.
 *
 * As with creation, the credential in `data` is for one panel and the caller
 * resets the mutation when that panel closes.
 */
export function useRotateSiteKey(
  siteId: string,
): UseMutationResult<RotateSiteKeyResponse, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => endpoints.rotateSiteKey(siteId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
    },
  })
}

/**
 * Replaces a site's settings.
 *
 * TWO THINGS THE CALLER MUST SURFACE. The body is a FULL REPLACEMENT, so an
 * editor has to send every key it means to keep. And the server queues a
 * SETTINGS job for every terminal at the site, so this reconfigures hardware
 * rather than merely updating a record.
 *
 * Terminals are invalidated as well as the settings themselves, because those
 * jobs change what the fleet is about to do.
 */
export function useUpdateSiteSettings(
  siteId: string,
): UseMutationResult<SiteSettings, Error, SiteSettingsRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (settings: SiteSettingsRequest) => endpoints.updateSiteSettings(siteId, settings),
    onSuccess: (updated) => {
      queryClient.setQueryData(keys.sites.settings(siteId), updated)
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
    },
  })
}

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

export function useTerminals(
  options: { outdated?: boolean } = {},
): UseQueryResult<TerminalsResponse> {
  return useQuery({
    queryKey: keys.terminals.list(options),
    queryFn: () => endpoints.fetchTerminals(options),
    // Terminal state is live-ish: a device goes offline without telling the
    // browser, so a short staleness keeps a fleet view honest without polling.
    staleTime: 15_000,
  })
}

export function useTerminalSummary(): UseQueryResult<FleetSummary> {
  return useQuery({
    queryKey: keys.terminals.summary(),
    queryFn: () => endpoints.fetchTerminalSummary(),
    staleTime: 15_000,
  })
}

export function useTerminal(serial: string | undefined): UseQueryResult<TerminalDetail> {
  return useQuery({
    queryKey: keys.terminals.detail(serial ?? ''),
    queryFn: () => endpoints.fetchTerminal(serial as string),
    enabled: Boolean(serial),
    staleTime: 15_000,
  })
}

/**
 * Points a terminal at a capability.
 *
 * The response is the whole detail object, so it is written straight into the
 * cache rather than triggering a refetch of something we were just handed. The
 * list and the summary are still invalidated: both display the mode, and a
 * detail view is usually opened from a list that is still mounted behind it.
 */
export function useUpdateTerminalMode(
  serial: string,
): UseMutationResult<TerminalDetail, Error, TerminalModeRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: TerminalModeRequest) => endpoints.updateTerminalMode(serial, body),
    onSuccess: (detail) => {
      queryClient.setQueryData(keys.terminals.detail(serial), detail)
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
    },
  })
}

// ---------------------------------------------------------------------------
// Terminal lifecycle
// ---------------------------------------------------------------------------

/**
 * What every lifecycle mutation invalidates, in one place.
 *
 * ALL THREE OF THESE MATTER and getting one wrong is a screen that contradicts
 * the action just taken:
 *
 *   detail    the row itself, written from the response where the server
 *             returned one and refetched where it did not;
 *   list      the fleet view a detail page is usually opened from, still mounted
 *             behind it and still showing the old status;
 *   summary   the counts above that list, which are computed from the statuses
 *             this operation just changed.
 */
function settleTerminal(
  queryClient: QueryClient,
  serial: string,
  detail: TerminalDetail | undefined,
): void {
  if (detail) {
    queryClient.setQueryData(keys.terminals.detail(serial), detail)
  } else {
    void queryClient.invalidateQueries({ queryKey: keys.terminals.detail(serial) })
  }
  void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
}

/**
 * Disables or re-enables a terminal.
 *
 * REVERSIBLE and does not touch the credential. The audit trail is invalidated
 * too — every lifecycle operation writes a record, and an activity view open
 * beside this is stale the moment it succeeds.
 */
export function useSetTerminalState(
  serial: string,
): UseMutationResult<TerminalLifecycleResponse, Error, TerminalStateRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: TerminalStateRequest) => endpoints.setTerminalState(serial, body),
    onSuccess: (result) => {
      settleTerminal(queryClient, serial, result.terminal)
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Revokes a terminal's device credential.
 *
 * THE SITE IS INVALIDATED AS WELL, which is the one that is easy to miss: a
 * site's `terminal_count` counts live terminals, and revoking changes what that
 * number describes.
 */
export function useRevokeTerminalCredential(
  serial: string,
): UseMutationResult<TerminalLifecycleResponse, Error, TerminalRevokeRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: TerminalRevokeRequest) =>
      endpoints.revokeTerminalCredential(serial, body),
    onSuccess: (result) => {
      settleTerminal(queryClient, serial, result.terminal)
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Retires a terminal.
 *
 * The terminal's own cache entry is REMOVED rather than invalidated — refetching
 * it would only produce a 404, and a detail page that refetches into an error is
 * a worse answer than one whose caller navigates away.
 */
export function useRetireTerminal(
  serial: string,
): UseMutationResult<TerminalRetiredResponse, Error, TerminalRetireRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: TerminalRetireRequest) => endpoints.retireTerminal(serial, body),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: keys.terminals.detail(serial) })
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Moves a terminal to another site.
 *
 * SITES ARE INVALIDATED because two of their terminal counts have just changed,
 * and the SESSION is not — grants are on sites, and moving a terminal between
 * two sites does not change which sites an operator reaches.
 */
export function useMoveTerminal(
  serial: string,
): UseMutationResult<TerminalLifecycleResponse, Error, TerminalMoveRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: TerminalMoveRequest) => endpoints.moveTerminal(serial, body),
    onSuccess: (result) => {
      settleTerminal(queryClient, serial, result.terminal)
      void queryClient.invalidateQueries({ queryKey: keys.sites.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Forces a full resync.
 *
 * Changes no state an operator reasons about afterwards — it replaces a queue —
 * so the terminal row is invalidated rather than replaced, and nothing else is
 * touched.
 */
export function useResyncTerminal(
  serial: string,
): UseMutationResult<TerminalResyncResponse, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => endpoints.resyncTerminal(serial),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.terminals.detail(serial) })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

/**
 * One page of people.
 *
 * `placeholderData` keeps the previous page on screen while the next one loads,
 * so paging and typing in the search box do not blank the table on every
 * keystroke. The alternative reads as a flicker and costs the operator their
 * place in the list.
 */
export function usePeople(query: PeopleQuery = {}): UseQueryResult<PeoplePage> {
  return useQuery({
    queryKey: keys.people.list(query),
    queryFn: () => endpoints.fetchPeople(query),
    placeholderData: (previous) => previous,
  })
}

export function usePerson(externalId: string | undefined): UseQueryResult<Person> {
  return useQuery({
    queryKey: keys.people.detail(externalId ?? ''),
    queryFn: () => endpoints.fetchPerson(externalId as string),
    enabled: Boolean(externalId),
  })
}

export function useCreatePerson(): UseMutationResult<Person, Error, PersonRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: PersonRequest) => endpoints.createPerson(body),
    onSuccess: (person) => {
      queryClient.setQueryData(keys.people.detail(person.external_id), person)
      // Every page and search is now potentially wrong -- the new person may
      // belong on any of them, and every total is off by one.
      void queryClient.invalidateQueries({ queryKey: keys.people.all })
    },
  })
}

export function useUpdatePerson(
  externalId: string,
): UseMutationResult<Person, Error, PersonRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: PersonRequest) => endpoints.updatePerson(externalId, body),
    onSuccess: (person) => {
      queryClient.setQueryData(keys.people.detail(externalId), person)
      void queryClient.invalidateQueries({ queryKey: keys.people.all })
    },
  })
}

/**
 * Soft-deletes a person, and tells the whole fleet to forget them.
 *
 * THIS IS NOT A LOCAL DELETION. The server enqueues a DELETE sync job to every
 * terminal in the company; that job is the only way an offline terminal ever
 * learns to remove a credential it already holds. A caller must say so in the
 * confirmation rather than presenting this as removing a row from a table.
 *
 * The person's own cache entry is dropped rather than invalidated: refetching it
 * would only produce a 404.
 */
export function useDeletePerson(): UseMutationResult<void, Error, string> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (externalId: string) => endpoints.deletePerson(externalId),
    onSuccess: (_result, externalId) => {
      queryClient.removeQueries({ queryKey: keys.people.detail(externalId) })
      void queryClient.invalidateQueries({ queryKey: keys.people.all })
    },
  })
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

export function useOperators(): UseQueryResult<OperatorsResponse> {
  return useQuery({
    queryKey: keys.operators.list(),
    queryFn: () => endpoints.fetchOperators(),
  })
}

export function useOperator(operatorId: string | undefined): UseQueryResult<OperatorAccount> {
  return useQuery({
    queryKey: keys.operators.detail(operatorId ?? ''),
    queryFn: () => endpoints.fetchOperator(operatorId as string),
    enabled: Boolean(operatorId),
  })
}

export function useOperatorSites(
  operatorId: string | undefined,
): UseQueryResult<OperatorSitesResponse> {
  return useQuery({
    queryKey: keys.operators.sites(operatorId ?? ''),
    queryFn: () => endpoints.fetchOperatorSites(operatorId as string),
    enabled: Boolean(operatorId),
  })
}

/**
 * Creates an operator, by invitation unless the caller supplies a password.
 *
 * THE RESPONSE MAY CARRY A CREDENTIAL, and this hook treats it exactly as site
 * creation treats a provisioning key: nothing is written into the QUERY cache,
 * because that would outlive the panel that displayed it and be readable from
 * anywhere with the key factory. The invitation lives in the mutation's own
 * `data` for the life of one panel, and the caller resets the mutation when that
 * panel closes.
 */
export function useCreateOperator(): UseMutationResult<
  CreateOperatorResponse,
  Error,
  CreateOperatorRequest
> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateOperatorRequest) => endpoints.createOperator(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.operators.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Issues a fresh invitation.
 *
 * The operator row is invalidated because the server flags the account
 * `must_change_password` on the way through, and refused with 409 for an account
 * that has already signed in — which the caller surfaces as an instruction to
 * use a reset instead, rather than as a generic failure.
 *
 * As with creation, the token is for one panel and is never cached.
 */
export function useInviteOperator(
  operatorId: string,
): UseMutationResult<InvitationResponse, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => endpoints.inviteOperator(operatorId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.operators.detail(operatorId) })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/** Mints a reset link. Same caching contract as the invitation above. */
export function useResetOperatorPassword(
  operatorId: string,
): UseMutationResult<ResetResponse, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => endpoints.resetOperatorPassword(operatorId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.operators.detail(operatorId) })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Changes an operator's role, active state or password.
 *
 * Invalidates the SESSION as well when the target is the signed-in operator.
 * That case is mostly refused by the server -- nobody may change their own role
 * or disable themselves -- but a password reset is allowed and revokes every
 * other session, so the caller's own view of itself should not be trusted after
 * one.
 */
export function useUpdateOperator(
  operatorId: string,
): UseMutationResult<OperatorAccount, Error, UpdateOperatorRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: UpdateOperatorRequest) => endpoints.updateOperator(operatorId, body),
    onSuccess: (operator) => {
      queryClient.setQueryData(keys.operators.detail(operatorId), operator)
      void queryClient.invalidateQueries({ queryKey: keys.operators.all })
    },
  })
}

/**
 * Replaces an operator's site grants.
 *
 * An EMPTY list means "not scoped", which is every site in the company — not
 * none. Any UI in front of this must make that unmistakable, because the two
 * readings are opposite and the destructive one looks like the safe one.
 */
export function useSetOperatorSites(
  operatorId: string,
): UseMutationResult<OperatorSitesResponse, Error, SiteGrantsRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: SiteGrantsRequest) => endpoints.setOperatorSites(operatorId, body),
    onSuccess: (result) => {
      queryClient.setQueryData(keys.operators.sites(operatorId), result)
      void queryClient.invalidateQueries({ queryKey: keys.operators.all })
    },
  })
}

export function useDeleteOperator(): UseMutationResult<void, Error, string> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (operatorId: string) => endpoints.deleteOperator(operatorId),
    onSuccess: (_result, operatorId) => {
      queryClient.removeQueries({ queryKey: keys.operators.detail(operatorId) })
      queryClient.removeQueries({ queryKey: keys.operators.sites(operatorId) })
      void queryClient.invalidateQueries({ queryKey: keys.operators.all })
    },
  })
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

export function useApplications(): UseQueryResult<ApplicationsResponse> {
  return useQuery({
    queryKey: keys.applications.list(),
    queryFn: () => endpoints.fetchApplications(),
    staleTime: 60_000,
  })
}

/**
 * Enables, disables or configures a capability. OWNER only, enforced server-side.
 *
 * INVALIDATES THE SESSION, which is the part that is easy to miss. Navigation is
 * derived from `session.applications`, so enabling a capability that does not
 * refresh the session leaves the operator looking at a console that has changed
 * everywhere except its menu. Terminals are invalidated too: a terminal's
 * `effective_applications` is computed against what the company has enabled, so
 * disabling one silently empties it on every terminal pointed at it.
 */
export function useUpdateApplication(): UseMutationResult<
  ConfiguredApplication,
  Error,
  { code: ApplicationCode; body: ApplicationRequest }
> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ code, body }: { code: ApplicationCode; body: ApplicationRequest }) =>
      endpoints.updateApplication(code, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.applications.all })
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
      void queryClient.invalidateQueries({ queryKey: SESSION_KEY })
    },
  })
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

/**
 * One page of the audit trail.
 *
 * `placeholderData` keeps the previous page on screen while the next loads, for
 * the same reason people does: changing a filter should not blank the table
 * under the operator's cursor.
 *
 * NOT POLLED. An audit trail is read deliberately, and a view that refetched on
 * a timer would move rows under somebody reading them.
 */
export function useAuditEvents(query: AuditQuery = {}): UseQueryResult<AuditPage> {
  return useQuery({
    queryKey: keys.audit.list(query),
    queryFn: () => endpoints.fetchAuditEvents(query),
    placeholderData: (previous) => previous,
  })
}

// ---------------------------------------------------------------------------
// Firmware catalogue
// ---------------------------------------------------------------------------

export function useFirmware(): UseQueryResult<FirmwareResponse> {
  return useQuery({
    queryKey: keys.firmware.list(),
    queryFn: () => endpoints.fetchFirmware(),
    staleTime: 60_000,
  })
}

export function useCreateFirmware(): UseMutationResult<
  FirmwareVersion,
  Error,
  CreateFirmwareRequest
> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateFirmwareRequest) => endpoints.createFirmware(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.firmware.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * Marks a build as the deployment target.
 *
 * INVALIDATES TERMINALS, which is the whole point: `firmware_outdated` on every
 * terminal is computed against this value, so moving it changes what the fleet
 * view says about hardware nobody has touched.
 */
export function useSetCurrentFirmware(): UseMutationResult<FirmwareVersion, Error, number> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => endpoints.setCurrentFirmware(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.firmware.all })
      void queryClient.invalidateQueries({ queryKey: keys.terminals.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
  })
}

/**
 * The session's own query key.
 *
 * Duplicated from SessionProvider rather than imported to keep this module free
 * of a dependency on React context; the constant is asserted equal in the tests
 * so the two cannot drift.
 */
const SESSION_KEY = ['session'] as const

/**
 * Drops every tenant-scoped query while leaving the session alone.
 *
 * For a site switch or a company-level change, where everything fetched was
 * scoped to something that no longer applies.
 */
export function invalidateConsoleData(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({ queryKey: keys.all })
}
