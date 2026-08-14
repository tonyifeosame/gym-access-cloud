import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import {
  canChangeOperatorRole,
  canDeleteOperator,
  canManageOperator,
  isSelf,
} from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { ActiveBadge, Badge } from '../../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useOperator } from '../../data/console'
import { useSession } from '../../session/useSession'
import {
  ChangeRoleDialog,
  OperatorActivationDialog,
  RemoveOperatorDialog,
  ResetPasswordDialog,
  SiteGrantsDialog,
} from './OperatorDialogs'
import { ROLE_DESCRIPTIONS } from './OperatorFormDialog'

/**
 * One operator account.
 *
 * WHAT THIS PAGE WILL NOT OFFER, and why each is right:
 *
 *   - Changing your OWN role, or deactivating or removing YOURSELF. The server
 *     refuses all three, and not as a courtesy: the sole OWNER of a company
 *     demoting themselves would leave nobody able to manage operators and no way
 *     back that does not involve the database.
 *   - Anything at all on an OWNER, unless the caller is one. Otherwise ADMIN
 *     would be a synonym for OWNER one request later.
 *
 * Both are enforced server-side; hiding the controls only saves the operator a
 * 403. The page explains the absence rather than leaving a gap, because a
 * missing button with no reason reads as a bug.
 */
export function OperatorDetailPage() {
  const { operatorId } = useParams<{ operatorId: string }>()
  const navigate = useNavigate()
  const { session } = useSession()
  const query = useOperator(operatorId)

  const [changingRole, setChangingRole] = useState(false)
  const [changingSites, setChangingSites] = useState(false)
  const [resettingPassword, setResettingPassword] = useState(false)
  const [changingActive, setChangingActive] = useState(false)
  const [removing, setRemoving] = useState(false)

  if (query.isPending) return <LoadingState label="Loading operator…" />

  if (query.isError) {
    const error = query.error
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader title="Operator not found" breadcrumb={<Link to="/operators">Operators</Link>} />
          <InfoNote title="Nothing here">
            No operator account with that id exists in your company. It may have been
            removed.
          </InfoNote>
        </div>
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="page">
          <PageHeader title="Operator" breadcrumb={<Link to="/operators">Operators</Link>} />
          <InfoNote title="Not available to you">
            Your role does not include managing this account.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Operator" breadcrumb={<Link to="/operators">Operators</Link>} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const operator = query.data
  const self = isSelf(session, operator)
  const mayManage = canManageOperator(session, operator)
  const mayChangeRole = canChangeOperatorRole(session, operator)
  const mayRemove = canDeleteOperator(session, operator)
  const roleIgnoresGrants = operator.role === 'ADMIN' || operator.role === 'OWNER'
  const grants = operator.sites ?? []

  return (
    <div className="page">
      <PageHeader
        title={operator.full_name}
        breadcrumb={<Link to="/operators">Operators</Link>}
        lead={<span className="mono">{operator.email}</span>}
        actions={
          mayManage ? (
            <>
              {mayChangeRole ? (
                <button type="button" className="button" onClick={() => setChangingRole(true)}>
                  Change role
                </button>
              ) : null}
              <button type="button" className="button" onClick={() => setChangingSites(true)}>
                Site access
              </button>
              <button type="button" className="button" onClick={() => setResettingPassword(true)}>
                Reset password
              </button>
              {mayChangeRole ? (
                <button type="button" className="button" onClick={() => setChangingActive(true)}>
                  {operator.active ? 'Deactivate' : 'Reactivate'}
                </button>
              ) : null}
              {mayRemove ? (
                <button
                  type="button"
                  className="button button--danger"
                  onClick={() => setRemoving(true)}
                >
                  Remove
                </button>
              ) : null}
            </>
          ) : null
        }
      />

      {self ? (
        <InfoNote title="This is your own account">
          You cannot change your own role, deactivate yourself or remove your own
          account — that is what stops the last administrator locking everybody out.
          Ask another administrator or owner if any of those need to change. Your own
          password is changed from Settings.
        </InfoNote>
      ) : null}

      {!mayManage ? (
        <InfoNote title="You cannot change this account">
          Only an owner can modify another owner&apos;s account.
        </InfoNote>
      ) : null}

      {!operator.active ? (
        <InfoNote tone="warning" title="This account is deactivated">
          They are signed out and cannot sign in. The account and its site access are
          kept.
        </InfoNote>
      ) : null}

      <section className="cards" aria-label="Summary">
        <article className="card">
          <h2 className="card__title">Role</h2>
          <p className="card__value">
            <Badge>{roleLabel(operator.role)}</Badge>
          </p>
          <p className="card__detail">{ROLE_DESCRIPTIONS[operator.role]}</p>
        </article>

        <article className="card">
          <h2 className="card__title">Status</h2>
          <p className="card__value">
            <ActiveBadge active={operator.active} />
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Last signed in</h2>
          <p className="card__value">
            {operator.last_login_at ? (
              <Timestamp value={operator.last_login_at} relative />
            ) : (
              <span className="muted">Never</span>
            )}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Account created</h2>
          <p className="card__value">
            <Timestamp value={operator.created_at} />
          </p>
        </article>
      </section>

      {/* --- site access ---------------------------------------------------- */}
      <section className="panel" aria-labelledby="operator-sites-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="operator-sites-heading">
            Site access
          </h2>
        </div>

        {roleIgnoresGrants ? (
          <p>
            {roleLabel(operator.role)} reaches <strong>every site</strong> in your
            company. Site restrictions do not apply to this role.
            {grants.length > 0 ? (
              <>
                {' '}
                {grants.length} restriction{grants.length === 1 ? '' : 's'} {grants.length === 1 ? 'is' : 'are'}{' '}
                stored on the account and would apply again if it were moved to a
                manager or viewer role.
              </>
            ) : null}
          </p>
        ) : grants.length === 0 ? (
          // Empty means unrestricted. Spelled out, because the opposite reading
          // is the natural one and is exactly backwards.
          <p>
            Not restricted — this account reaches <strong>every site</strong> in your
            company. That is what an empty set of site restrictions means.
          </p>
        ) : (
          <>
            <p>
              Restricted to {grants.length} site{grants.length === 1 ? '' : 's'}:
            </p>
            <ul className="chip-list">
              {grants.map((grant) => (
                <li key={grant.site_id}>
                  <Link to={`/sites/${grant.site_id}`} className="chip">
                    {grant.site_name}
                  </Link>
                </li>
              ))}
            </ul>
            <p className="field__hint">
              Site restrictions bound sites, terminals and site settings. People are
              company-wide and are not narrowed by them.
            </p>
          </>
        )}
      </section>

      {changingRole ? (
        <ChangeRoleDialog open operator={operator} onClose={() => setChangingRole(false)} />
      ) : null}
      {changingSites ? (
        <SiteGrantsDialog open operator={operator} onClose={() => setChangingSites(false)} />
      ) : null}
      {resettingPassword ? (
        <ResetPasswordDialog
          open
          operator={operator}
          onClose={() => setResettingPassword(false)}
        />
      ) : null}
      {changingActive ? (
        <OperatorActivationDialog
          open
          operator={operator}
          onClose={() => setChangingActive(false)}
        />
      ) : null}
      {removing ? (
        <RemoveOperatorDialog
          open
          operator={operator}
          onClose={() => setRemoving(false)}
          onRemoved={() => navigate('/operators', { replace: true })}
        />
      ) : null}
    </div>
  )
}
