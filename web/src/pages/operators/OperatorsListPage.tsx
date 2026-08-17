import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { OperatorAccount } from '../../api/types'
import { isSelf } from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { ActiveBadge, Badge } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useOperators } from '../../data/console'
import { useSession } from '../../session/useSession'
import { OperatorFormDialog } from './OperatorFormDialog'

/**
 * Who can sign in to this console.
 *
 * NOT THE SAME THING AS PEOPLE, and the distinction is worth making on the page
 * itself: an operator is somebody who administers the platform, a person is
 * somebody the terminals recognise. They are different tables, different
 * credentials and, usually, different humans.
 *
 * THE SITE ACCESS COLUMN IS THE ONE TO READ CAREFULLY. "All sites" is the
 * default and covers two distinct cases — a role that is never scoped, and an
 * account that simply holds no restrictions — so the column distinguishes them
 * rather than showing the same words for both.
 */
export function OperatorsListPage() {
  const navigate = useNavigate()
  const { session } = useSession()
  const query = useOperators()
  const [adding, setAdding] = useState(false)

  const columns: Column<OperatorAccount>[] = [
    {
      id: 'name',
      header: 'Name',
      primary: true,
      render: (operator) => (
        <span className="operator-name">
          <Link to={`/operators/${operator.id}`} className="table__link">
            {operator.full_name}
          </Link>
          {isSelf(session, operator) ? <Badge tone="info">You</Badge> : null}
        </span>
      ),
    },
    {
      id: 'email',
      header: 'Email',
      render: (operator) => <span className="mono">{operator.email}</span>,
    },
    {
      id: 'role',
      header: 'Role',
      render: (operator) => <Badge>{roleLabel(operator.role)}</Badge>,
    },
    {
      id: 'sites',
      header: 'Site access',
      render: (operator) => {
        if (operator.role === 'ADMIN' || operator.role === 'OWNER') {
          return <span className="muted">All sites (by role)</span>
        }
        const count = operator.sites?.length ?? 0
        // Empty grants means unrestricted, not "no access". The two readings are
        // opposites, so the column never renders a bare "0".
        if (count === 0) return <span>All sites</span>
        return (
          <span>
            {count} site{count === 1 ? '' : 's'}
          </span>
        )
      },
    },
    {
      id: 'status',
      header: 'Status',
      render: (operator) => <ActiveBadge active={operator.active} />,
    },
    {
      id: 'last_login',
      header: 'Last signed in',
      secondary: true,
      render: (operator) =>
        operator.last_login_at ? (
          <Timestamp value={operator.last_login_at} relative />
        ) : (
          <span className="muted">Never</span>
        ),
    },
  ]

  return (
    <div className="page">
      <PageHeader
        title="Operators"
        lead="Who can sign in to this console, and what they may do."
        actions={
          <button type="button" className="button button--primary" onClick={() => setAdding(true)}>
            Add an operator
          </button>
        }
      />

      <DataTable<OperatorAccount>
        caption="Operators"
        columns={columns}
        rows={query.data?.operators}
        rowKey={(operator) => operator.id}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        error={query.isError ? query.error : null}
        onRetry={() => void query.refetch()}
        onRowClick={(operator) => navigate(`/operators/${operator.id}`)}
        // Not really reachable -- an operator is reading this page, so there is
        // at least one. Present for completeness rather than as a real state.
        emptyTitle="No operators"
        emptyDescription="Nobody can sign in to this console yet."
      />

      <p className="field__hint">
        Operators administer AccessLink. The people your terminals recognise are
        managed under <Link to="/people">People</Link>.
      </p>

      {adding ? <OperatorFormDialog open onClose={() => setAdding(false)} /> : null}
    </div>
  )
}
