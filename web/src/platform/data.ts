import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query'

import * as platform from './api'
import type {
  CreateCompanyRequest,
  FirstOperatorRequest,
  FirstOperatorResponse,
  PlatformCompaniesResponse,
  PlatformCompany,
  UpdateCompanyRequest,
} from './types'

/**
 * The platform surface's data layer.
 *
 * ITS OWN KEY ROOT, `platform`, sitting outside the `console` root entirely.
 * That is not tidiness: `invalidateConsoleData` and the session-ended handler
 * both drop everything under `console`, and platform data must survive a tenant
 * operator signing out in another tab — they are different identities and one
 * ending says nothing about the other.
 */

const ROOT = 'platform' as const

export const platformKeys = {
  all: [ROOT] as const,
  session: [ROOT, 'session'] as const,
  companies: {
    all: [ROOT, 'companies'] as const,
    list: () => [ROOT, 'companies', 'list'] as const,
    detail: (companyId: string) => [ROOT, 'companies', 'detail', companyId] as const,
  },
} as const

export function useCompanies(): UseQueryResult<PlatformCompaniesResponse> {
  return useQuery({
    queryKey: platformKeys.companies.list(),
    queryFn: () => platform.fetchCompanies(),
    staleTime: 30_000,
  })
}

export function useCompany(companyId: string | undefined): UseQueryResult<PlatformCompany> {
  return useQuery({
    queryKey: platformKeys.companies.detail(companyId ?? ''),
    queryFn: () => platform.fetchCompany(companyId as string),
    enabled: Boolean(companyId),
  })
}

export function useCreateCompany(): UseMutationResult<
  PlatformCompany,
  Error,
  CreateCompanyRequest
> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateCompanyRequest) => platform.createCompany(body),
    onSuccess: (company) => {
      queryClient.setQueryData(platformKeys.companies.detail(company.id), company)
      void queryClient.invalidateQueries({ queryKey: platformKeys.companies.all })
    },
  })
}

export function useUpdateCompany(
  companyId: string,
): UseMutationResult<PlatformCompany, Error, UpdateCompanyRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: UpdateCompanyRequest) => platform.updateCompany(companyId, body),
    onSuccess: (company) => {
      queryClient.setQueryData(platformKeys.companies.detail(companyId), company)
      void queryClient.invalidateQueries({ queryKey: platformKeys.companies.all })
    },
  })
}

/**
 * Mints a company's first owner.
 *
 * INVALIDATES THE COMPANY, because `operator_count` is what the console reads to
 * decide whether onboarding is finished — and whether to offer this action at
 * all. Leaving it stale would show a completed company as still needing an
 * owner, and offer a button the server would refuse with 409.
 *
 * As everywhere else, the invitation in the response is for one panel: nothing
 * writes it into the query cache, and the caller resets the mutation on dismiss.
 */
export function useCreateFirstOperator(
  companyId: string,
): UseMutationResult<FirstOperatorResponse, Error, FirstOperatorRequest> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: FirstOperatorRequest) => platform.createFirstOperator(companyId, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: platformKeys.companies.all })
    },
  })
}
