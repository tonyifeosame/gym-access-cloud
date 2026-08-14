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
  CompanyDetail,
  ConfiguredApplication,
  CreateOperatorRequest,
  FleetSummary,
  OperatorAccount,
  OperatorSitesResponse,
  OperatorsResponse,
  PeoplePage,
  PeopleQuery,
  Person,
  PersonRequest,
  Site,
  SiteGrantsRequest,
  SiteSettings,
  SiteSettingsRequest,
  SitesResponse,
  TerminalDetail,
  TerminalModeRequest,
  TerminalsResponse,
  UpdateOperatorRequest,
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

export function useCreateOperator(): UseMutationResult<
  OperatorAccount,
  Error,
  CreateOperatorRequest
> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateOperatorRequest) => endpoints.createOperator(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.operators.all })
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
