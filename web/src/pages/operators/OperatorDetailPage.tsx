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
  InviteOperatorDialog,
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
  const [inviting, setInviting] = useState(false)
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
  // An account that has never signed in is either freshly invited or one whose
  // invitation never arrived. Either way an INVITATION is the right instrument,
  // not a reset — the two are audited differently and the server refuses the
  // wrong one.
  const neverSignedIn = !operator.last_login_at

  return (
    <div className="page">
      <PageHeader
        title={operator.full_name}
        breadcrumb={<Link to="/operators">Operators</Link>}
        lead={<span className="mono">{operator.email}</span>}
        /*
          THE HEADER CARRIES THE EVERYDAY ACTIONS AND NOTHING THAT STOPS AN
          ACCOUNT WORKING. It used to hold all five, and at 390px they wrapped
          into two rows with Deactivate and Remove alone on the second — Remove
          in the destructive fill, landing at the x-position Change role had
          occupied the row above. A thumb going for the leftmost button of a row
          it has already used once is how somebody removes a colleague's account
          while meaning to change their role.

          Deactivate and Remove now sit in their own block at the end of the
          page, stacked rather than wrapped. THE PERMISSION GATES ARE UNCHANGED
          and moved with them: `mayChangeRole` still governs deactivation and
          `mayRemove` still governs removal, both of which already exclude the
          signed-in operator's own account.
        */
        actions={
          mayManage ? (
            <>
              {mayChangeRole ? (
                <button type="button" className="button" onClick={() => setChangingRole(true)}>
                  Change role
                </button>
              ) : null}
              {/*
                SITE ACCESS IS NOT OFFERED ON YOUR OWN ACCOUNT, because it could
                never do anything there. This page is ADMIN-gated, so "your own
                account" is always an administrator or an owner, and both roles
                reach every site regardless of what is stored. The dialog used to
                open and say so — an actionable-looking control whose whole
                content was an explanation of why it was inert.
              */}
              {!self ? (
                <button type="button" className="button" onClick={() => setChangingSites(true)}>
                  Site access
                </button>
              ) : null}
              {/*
                INVITE AND RESET ARE DIFFERENT OPERATIONS and the console picks
                between them from the account's own state rather than offering
                both and letting the server refuse one. An invitation is only
                valid for an account that has never signed in; offering it for
                one that has would produce a 409 that means nothing to the
                operator who pressed it.

                NEITHER IS OFFERED ON YOUR OWN ACCOUNT. Changing your own
                password is a different act with a different screen — it does not
                go through an administrator handing a colleague a link — and the
                note below this header has always said so while the header
                offered the button anyway.
              */}
              {self ? null : neverSignedIn ? (
                <button type="button" className="button" onClick={() => setInviting(true)}>
                  Send invitation
                </button>
              ) : (
                <button type="button" className="button" onClick={() => setResettingPassword(true)}>
                  Reset password
                </button>
              )}
            </>
          ) : null
        }
      />

      {self ? (
        <InfoNote title="This is your own account">
          <p>
            You cannot change your own role, deactivate yourself or remove your own
            account — that is what stops the last administrator locking everybody out.
            Ask another administrator or owner if any of those need to change.
          </p>
          {/*
            A LINK, NOT A PLACE NAME. This sentence has always been here and the
            header used to contradict it with a "Reset password" button directly
            above — two instructions about the same task, forty pixels apart. The
            button is gone; the sentence now takes you there.
          */}
          <p>
            To change your own password, go to <Link to="/settings">Settings</Link>.
          </p>
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

      {neverSignedIn && mayManage ? (
        <InfoNote title="This account has never been used">
          {operator.full_name} has not signed in yet. If their invitation did not
          arrive, issue a new one — it supersedes any earlier link, and they set
          their own password from it.
        </InfoNote>
      ) : null}

      {/*
        THREE CARDS, DOWN FROM FOUR. Nothing was dropped; one card was folded
        into another.

        "LAST SIGNED IN" HAD A CARD OF ITS OWN holding a single value, next to a
        Status card holding a single badge — two of the four cards on the page
        spent on two words that describe the same thing: whether this account is
        in use. They are now one card, where "Active, never signed in" reads as
        one state of one account rather than as two unrelated facts a reader has
        to combine.

        The line is kept even on a never-used account, where the notice above
        also mentions it. The notice is the ACTIONABLE form — it exists to say
        that a fresh invitation is the remedy — and it is only rendered for
        somebody who can act on it. The card is the plain fact, and it is the
        only place the fact appears for a reader who cannot.

        STATUS STAYS. The deactivated banner is negative-only, so removing this
        would leave an active account with no positive statement of its state.
      */}
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
          <p className="card__detail">
            {operator.last_login_at ? (
              <>
                last signed in <Timestamp value={operator.last_login_at} relative />
              </>
            ) : (
              'never signed in'
            )}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Account created</h2>
          <p className="card__value">
            {/*
              NO TIME OF DAY. The instant is whatever moment the row was written
              and is read back in the viewer's zone, so a midnight-UTC creation
              renders as "1:00 AM" one zone east — a precise-looking hour that
              describes nothing anybody did. The full instant is still in the
              element's `datetime` and `title` for anyone correlating a log.
            */}
            <Timestamp value={operator.created_at} dateOnly />
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
            {/*
              "ALL SITES", THE SAME WORDS THE LIST COLUMN USES. This panel said
              "every site" while the column said "All sites", which is two names
              for one state across two screens a reader moves between.
            */}
            {roleLabel(operator.role)} reaches <strong>all sites</strong> in your
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
            Not restricted — this account reaches <strong>all sites</strong> in your
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

      {mayManage && (mayChangeRole || mayRemove) ? (
        <AccountAdministration
          operator={operator}
          mayChangeActivation={mayChangeRole}
          mayRemove={mayRemove}
          onChangeActivation={() => setChangingActive(true)}
          onRemove={() => setRemoving(true)}
        />
      ) : null}

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
      {inviting ? (
        <InviteOperatorDialog open operator={operator} onClose={() => setInviting(false)} />
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

/**
 * The two actions that stop an account working.
 *
 * GROUPED, LABELLED AND LAST, rather than wrapping out of the page header. Each
 * still opens its own differently-shaped dialog — that is where deactivation and
 * removal are kept from being mistaken for one another, and removal still
 * requires typing the account's email address — but a customer should not have
 * to read five button labels at the top of the page to find out which one is
 * safe.
 *
 * THE GATES ARE THE CALLER'S AND ARE PASSED THROUGH UNCHANGED: `mayChangeRole`
 * for deactivation and `mayRemove` for removal, both of which already exclude
 * the signed-in operator's own account and any account this caller may not
 * manage. This block renders only when at least one of them is true, so an
 * operator who may do neither sees no empty panel.
 *
 * On a phone the buttons stack full width rather than wrapping, which is the
 * whole point: nothing lands where something else was a moment ago.
 */
function AccountAdministration({
  operator,
  mayChangeActivation,
  mayRemove,
  onChangeActivation,
  onRemove,
}: {
  operator: { active: boolean }
  mayChangeActivation: boolean
  mayRemove: boolean
  onChangeActivation: () => void
  onRemove: () => void
}) {
  return (
    <section className="panel panel--danger" aria-labelledby="operator-admin-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="operator-admin-heading">
          Account administration
        </h2>
        <p className="field__hint">
          These stop this person reaching the console. Each one explains what it
          affects before it happens.
        </p>
      </div>

      <div className="danger-actions">
        {mayChangeActivation ? (
          <button type="button" className="button" onClick={onChangeActivation}>
            {operator.active ? 'Deactivate' : 'Reactivate'}
          </button>
        ) : null}
        {mayRemove ? (
          <button type="button" className="button button--danger" onClick={onRemove}>
            Remove
          </button>
        ) : null}
      </div>
    </section>
  )
}
