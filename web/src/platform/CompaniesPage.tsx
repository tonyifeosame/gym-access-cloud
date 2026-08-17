import { useState } from 'react'
import { Link } from 'react-router-dom'

import { Badge } from '../components/Badge'
import { DataTable, type Column } from '../components/DataTable'
import { InfoNote, PageHeader } from '../components/states'
import { Timestamp } from '../components/Timestamp'
import { CreateCompanyDialog } from './CompanyDialogs'
import { useCompanies } from './data'
import type { PlatformCompany } from './types'

/**
 * Every customer on this installation.
 *
 * GP-01: nothing created a company. `companies` had exactly one writer — the
 * INSERT in a migration that gave pre-existing rows a tenant to belong to — so
 * onboarding a second customer meant somebody with SQL against production, and
 * there was no way to rename one, put one on hold, or set its retention policy
 * either.
 *
 * WHAT THIS TABLE SHOWS IS CARDINALITY AND NOTHING ELSE. Sites, terminals,
 * people, operators — enough to answer "is this tenant healthy and how big is
 * it", which is the whole of what onboarding and support have a claim to. There
 * is no column here that could carry a person, a credential, an event or a site
 * key, because there is no platform route that loads one.
 *
 * THE ONBOARDING STATE IS THE MOST USEFUL COLUMN. A company with no operator is
 * not a working tenant: nobody can sign in to it, and it is the state a
 * half-finished onboarding leaves behind. Surfacing it here is what turns that
 * from a thing somebody has to remember into a thing they can see.
 */
export function CompaniesPage() {
  const companies = useCompanies()
  const [creating, setCreating] = useState(false)

  const columns: Column<PlatformCompany>[] = [
    {
      id: 'name',
      header: 'Company',
      primary: true,
      render: (company) => (
        <Link to={`/platform/companies/${company.id}`} className="table__link">
          {company.name}
        </Link>
      ),
    },
    {
      id: 'slug',
      header: 'Slug',
      secondary: true,
      render: (company) => <code className="mono">{company.slug}</code>,
    },
    {
      id: 'state',
      header: 'State',
      render: (company) =>
        !company.active ? (
          <Badge tone="danger">Suspended</Badge>
        ) : company.operator_count === 0 ? (
          // Not an error — a company created moments ago is legitimately here —
          // but it is unfinished, and nobody can sign in to it.
          <Badge tone="warning">No operator yet</Badge>
        ) : (
          <Badge tone="positive">Active</Badge>
        ),
    },
    {
      id: 'operators',
      header: 'Operators',
      align: 'end',
      render: (company) => company.operator_count,
    },
    {
      id: 'sites',
      header: 'Sites',
      align: 'end',
      secondary: true,
      render: (company) => company.site_count,
    },
    {
      id: 'terminals',
      header: 'Terminals',
      align: 'end',
      secondary: true,
      render: (company) => company.terminal_count,
    },
    {
      id: 'people',
      header: 'People',
      align: 'end',
      secondary: true,
      render: (company) => company.person_count,
    },
    {
      id: 'created',
      header: 'Created',
      align: 'end',
      secondary: true,
      render: (company) => <Timestamp value={company.created_at} />,
    },
  ]

  const unfinished = (companies.data?.companies ?? []).filter(
    (company) => company.operator_count === 0,
  )

  return (
    <div className="page">
      <PageHeader
        title="Companies"
        lead="Every customer on this installation."
        actions={
          <button
            type="button"
            className="button button--primary"
            onClick={() => setCreating(true)}
          >
            Create a company
          </button>
        }
      />

      {unfinished.length > 0 ? (
        <InfoNote tone="warning" title="Onboarding not finished">
          {unfinished.length} {unfinished.length === 1 ? 'company has' : 'companies have'} no
          operator account. Nobody can sign in to{' '}
          {unfinished.length === 1 ? 'it' : 'them'} until one is created:{' '}
          {unfinished.map((company, index) => (
            <span key={company.id}>
              {index > 0 ? ', ' : ''}
              <Link to={`/platform/companies/${company.id}`}>{company.name}</Link>
            </span>
          ))}
          .
        </InfoNote>
      ) : null}

      <DataTable
        caption="Companies"
        columns={columns}
        rows={companies.data?.companies}
        rowKey={(company) => company.id}
        isLoading={companies.isPending}
        isFetching={companies.isFetching}
        error={companies.error}
        onRetry={() => void companies.refetch()}
        emptyTitle="No companies yet"
        emptyDescription="Create the first customer company to begin. It starts empty — you then issue its first operator, and they set up their own sites, terminals and people."
        emptyAction={
          <button
            type="button"
            className="button button--primary"
            onClick={() => setCreating(true)}
          >
            Create a company
          </button>
        }
      />

      <InfoNote title="What this surface can and cannot see">
        These counts are the whole of what platform administration can read about
        a customer. It cannot open their people, their credentials, their events,
        their terminals or their site keys — there is no route that would serve
        one. Support questions about what is inside a tenant have to be answered
        by somebody who works there.
      </InfoNote>

      {creating ? <CreateCompanyDialog open onClose={() => setCreating(false)} /> : null}
    </div>
  )
}
