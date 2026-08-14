import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { Person } from '../../api/types'
import { can } from '../../auth/permissions'
import { ActiveBadge, BiometricBadge } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { Pagination, SearchInput } from '../../components/Pagination'
import { PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { usePeople } from '../../data/console'
import { useSession } from '../../session/useSession'
import { PersonFormDialog } from './PersonFormDialog'

/** Matches the API's own default, so the first page asks for what it would get. */
const PAGE_SIZE = 50

/**
 * Everyone this platform knows about.
 *
 * SEARCH AND PAGING ARE BOTH SERVER-SIDE, and that is not interchangeable with
 * how the terminal list works. This endpoint returns ONE PAGE — filtering it in
 * the browser would search fifty people and call it searching the roster, which
 * is silently wrong the moment a company has more than fits on a page and
 * silently right in every test with three fixtures. The terminal list can filter
 * client-side precisely because it is not paginated; the difference is written
 * at both call sites so neither drifts.
 *
 * PEOPLE ARE COMPANY-WIDE, NOT SITE-SCOPED. The schema has no site relationship
 * for a person, so site grants cannot narrow this list and the console does not
 * pretend otherwise — a site-scoped operator sees every person in the company,
 * and is told so rather than left to assume the list is filtered.
 */
export function PeopleListPage() {
  const navigate = useNavigate()
  const { session } = useSession()
  const [search, setSearch] = useState('')
  const [offset, setOffset] = useState(0)
  const [adding, setAdding] = useState(false)

  const query = usePeople({ search, limit: PAGE_SIZE, offset })
  const mayManage = can(session, 'managePeople')

  // A new search invalidates the page position: results for "ada" have no
  // meaningful page 3 carried over from the previous query.
  function changeSearch(value: string) {
    setSearch(value)
    setOffset(0)
  }

  const page = query.data
  const searching = search.trim() !== ''

  const columns: Column<Person>[] = [
    {
      id: 'name',
      header: 'Name',
      primary: true,
      render: (person) => (
        <Link
          to={`/people/${encodeURIComponent(person.external_id)}`}
          className="table__link"
        >
          {person.full_name || <span className="muted">Unnamed</span>}
        </Link>
      ),
    },
    {
      id: 'external_id',
      header: 'Identifier',
      render: (person) => <code className="mono">{person.external_id}</code>,
    },
    {
      id: 'type',
      header: 'Person type',
      secondary: true,
      render: (person) => person.category || <span className="muted">—</span>,
    },
    {
      id: 'status',
      header: 'Status',
      render: (person) => <ActiveBadge active={person.active} />,
    },
    {
      id: 'credential',
      header: 'Credential',
      // A BOOLEAN AND NOTHING MORE. No template, no locator, no sensor detail.
      render: (person) => <BiometricBadge enrolled={person.biometric_enrolled} />,
    },
    {
      id: 'updated',
      header: 'Updated',
      secondary: true,
      render: (person) => <Timestamp value={person.updated_at} relative />,
    },
  ]

  return (
    <div className="page">
      <PageHeader
        title="People"
        lead="Everyone this platform recognises."
        actions={
          mayManage ? (
            <button type="button" className="button button--primary" onClick={() => setAdding(true)}>
              Add a person
            </button>
          ) : null
        }
      />

      <div className="toolbar">
        <SearchInput
          label="Search people"
          placeholder="Name or identifier"
          value={search}
          onChange={changeSearch}
          busy={query.isFetching && !query.isPending}
        />
        <RefreshingIndicator active={query.isFetching && !query.isPending} />
      </div>

      <DataTable<Person>
        caption="People"
        columns={columns}
        rows={page?.people}
        rowKey={(person) => person.external_id}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        error={query.isError ? query.error : null}
        onRetry={() => void query.refetch()}
        onRowClick={(person) => navigate(`/people/${encodeURIComponent(person.external_id)}`)}
        // "Nothing matched" and "nobody yet" are different facts, and telling a
        // company with a full roster that it has nobody is the worse mistake.
        emptyTitle={searching ? 'Nobody matches that search' : 'No people yet'}
        emptyDescription={
          searching
            ? 'Search matches a name or an identifier, anywhere in the value.'
            : mayManage
              ? 'Add the people this platform should recognise. What that means depends on what your company uses AccessLink for — employees, students, contractors, visitors.'
              : 'Nobody has been added to your company yet.'
        }
        emptyAction={
          searching ? (
            <button type="button" className="button" onClick={() => changeSearch('')}>
              Clear search
            </button>
          ) : mayManage ? (
            <button type="button" className="button button--primary" onClick={() => setAdding(true)}>
              Add a person
            </button>
          ) : null
        }
      />

      {page ? (
        <Pagination
          count={page.count}
          total={page.total}
          offset={page.offset}
          limit={page.limit}
          hasMore={page.has_more}
          onOffsetChange={setOffset}
          noun="people"
          busy={query.isFetching}
        />
      ) : null}

      {/*
        Stated rather than left to be inferred. A site-scoped operator who sees
        every person could reasonably assume the list is narrowed like their
        sites and terminals are; it is not, because the data model has no
        person-to-site relationship to narrow by.
      */}
      {session && !session.all_sites ? (
        <p className="field__hint">
          People are company-wide, so this list is not narrowed to your sites.
        </p>
      ) : null}

      {adding ? <PersonFormDialog open onClose={() => setAdding(false)} /> : null}
    </div>
  )
}
