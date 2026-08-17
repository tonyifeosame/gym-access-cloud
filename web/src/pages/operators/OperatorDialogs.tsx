import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { CredentialToken, OperatorAccount, Role } from '../../api/types'
import { assignableRoles } from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { CheckboxGroup, FormError, SelectField, TextField } from '../../components/Form'
import { HandoverLinkPanel } from '../../components/HandoverLinkPanel'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import {
  useDeleteOperator,
  useInviteOperator,
  useResetOperatorPassword,
  useSetOperatorSites,
  useSites,
  useUpdateOperator,
} from '../../data/console'
import { useAuthenticatedSession } from '../../session/useSession'
import { ROLE_DESCRIPTIONS } from './OperatorFormDialog'

const MIN_PASSWORD_LENGTH = 12

/**
 * The four things that can be done to an existing operator.
 *
 * EVERY ONE OF THEM SIGNS THE OPERATOR OUT EVERYWHERE. Changing a role,
 * deactivating an account, resetting a password and removing an account all
 * revoke that operator's sessions server-side, in the same transaction as the
 * change. That is correct — someone whose privileges just changed should not
 * still be holding a session issued under the old ones — but it is invisible
 * from the outside, so each dialog says so. An administrator who does this to a
 * colleague mid-shift should know they have just logged them out.
 */

// ---------------------------------------------------------------------------
// Role
// ---------------------------------------------------------------------------

export function ChangeRoleDialog({
  open,
  operator,
  onClose,
}: {
  open: boolean
  operator: OperatorAccount
  onClose: () => void
}) {
  const session = useAuthenticatedSession()
  const update = useUpdateOperator(operator.id)
  const notifications = useNotifications()
  const [role, setRole] = useState<Role>(operator.role)

  const roles = assignableRoles(session)
  const changed = role !== operator.role

  // Grants are stored but ignored for ADMIN and OWNER, and are consulted again
  // if the account is later demoted. Promoting somebody who holds grants
  // therefore leaves a scope that reappears on demotion -- see MR-016.
  const promotingPastGrants =
    (role === 'ADMIN' || role === 'OWNER') && (operator.sites?.length ?? 0) > 0

  return (
    <Dialog
      open={open}
      title={`Change ${operator.full_name}'s role`}
      dismissible={!update.isPending}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={update.isPending}
          >
            Cancel
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={!changed || update.isPending}
            onClick={() => {
              void (async () => {
                try {
                  await update.mutateAsync({ role })
                  notifications.success(`${operator.full_name} is now ${roleLabel(role)}`)
                  onClose()
                } catch {
                  /* rendered below; the dialog stays open */
                }
              })()
            }}
          >
            {update.isPending ? 'Saving…' : 'Change role'}
          </button>
        </>
      }
    >
      <SelectField
        label="Role"
        value={role}
        onChange={(value) => setRole(value as Role)}
        disabled={update.isPending}
        options={roles.map((option) => ({
          value: option,
          label: roleLabel(option),
          description: ROLE_DESCRIPTIONS[option],
        }))}
      />

      {changed ? (
        <InfoNote tone="warning" title="This signs them out">
          Changing a role ends every session this operator currently holds. They
          will need to sign in again, and will do so with the new role.
        </InfoNote>
      ) : null}

      {promotingPastGrants ? (
        <InfoNote title="Their site restrictions stop applying">
          {roleLabel(role)} reaches every site in the company, so the{' '}
          {operator.sites?.length} site restriction
          {operator.sites?.length === 1 ? '' : 's'} on this account will be ignored.
          The restrictions are kept, and would apply again if the account were later
          moved back to a manager or viewer role.
        </InfoNote>
      ) : null}

      <FormError
        message={
          update.error
            ? update.error instanceof ApiError
              ? update.error.message
              : 'The role could not be changed.'
            : null
        }
        requestId={update.error instanceof ApiError ? update.error.requestId : null}
      />
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Site grants
// ---------------------------------------------------------------------------

export function SiteGrantsDialog({
  open,
  operator,
  onClose,
}: {
  open: boolean
  operator: OperatorAccount
  onClose: () => void
}) {
  const session = useAuthenticatedSession()
  const sites = useSites()
  const save = useSetOperatorSites(operator.id)
  const notifications = useNotifications()
  const [selected, setSelected] = useState<string[]>(
    (operator.sites ?? []).map((grant) => grant.site_id),
  )

  const roleIgnoresGrants = operator.role === 'ADMIN' || operator.role === 'OWNER'
  const unscoped = selected.length === 0

  return (
    <Dialog
      open={open}
      title={`Site access for ${operator.full_name}`}
      size="wide"
      dismissible={!save.isPending}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={save.isPending}
          >
            Cancel
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={save.isPending}
            onClick={() => {
              void (async () => {
                try {
                  await save.mutateAsync({ site_ids: selected })
                  notifications.success(`Site access updated for ${operator.full_name}`)
                  onClose()
                } catch {
                  /* rendered below */
                }
              })()
            }}
          >
            {save.isPending ? 'Saving…' : 'Save site access'}
          </button>
        </>
      }
    >
      {roleIgnoresGrants ? (
        <InfoNote title="Not used for this role">
          {roleLabel(operator.role)} reaches every site in the company regardless of
          what is selected here. Anything you save is kept but has no effect until
          the account is moved to a manager or viewer role.
        </InfoNote>
      ) : null}

      <CheckboxGroup
        legend="Sites"
        hint={
          unscoped
            ? 'Nothing selected means EVERY site in the company.'
            : `Narrowed to ${selected.length} site${selected.length === 1 ? '' : 's'}.`
        }
        options={(sites.data?.sites ?? []).map((site) => ({
          value: site.id,
          label: site.name,
          description: site.active ? undefined : 'Deactivated',
        }))}
        selected={selected}
        onChange={setSelected}
        disabled={save.isPending}
        empty="Your company has no sites yet."
      />

      {/*
        The single most misreadable state in the product. Empty means
        unrestricted, and an operator clearing the list to "remove their access"
        would achieve the exact opposite.
      */}
      {!roleIgnoresGrants && unscoped ? (
        <InfoNote tone="warning" title="This grants every site">
          An empty selection is not &ldquo;no access&rdquo;. It means the account is
          not scoped, and reaches every site in {session.company.name}. To restrict
          this operator, select the sites they should have.
        </InfoNote>
      ) : null}

      <FormError
        message={
          save.error
            ? save.error instanceof ApiError
              ? save.error.message
              : 'Site access could not be updated.'
            : null
        }
        requestId={save.error instanceof ApiError ? save.error.requestId : null}
      />
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Credential recovery: invitation and password reset
// ---------------------------------------------------------------------------

/**
 * Getting somebody back into an account they cannot reach.
 *
 * TWO PATHS, AND THE DEFAULT IS THE ONE WHERE THE ADMINISTRATOR NEVER LEARNS
 * THE PASSWORD. A reset link is minted, handed over once, and redeemed by its
 * owner; the administrator's involvement ends at delivery. This is SEC-10, and
 * before it existed the only answer to "they are locked out" was for an
 * administrator to type a new password and read it out — which leaves them
 * knowing a colleague's credential indefinitely, and is audited as exactly that.
 *
 * The second path is kept because a deployment with no way to send a link needs
 * it, and because standing next to somebody is a legitimate channel. It is not
 * hidden; it simply is not first, and it says what it costs.
 *
 * WHEN THE SESSIONS END DIFFERS BETWEEN THE TWO, and the dialog says which.
 * Setting a password directly signs them out at once. A link signs them out when
 * it is REDEEMED — until then their current password still works, which matters
 * to an administrator deciding whether a colleague can keep working this
 * afternoon.
 */
export function ResetPasswordDialog({
  open,
  operator,
  onClose,
}: {
  open: boolean
  operator: OperatorAccount
  onClose: () => void
}) {
  const update = useUpdateOperator(operator.id)
  const issueReset = useResetOperatorPassword(operator.id)
  const notifications = useNotifications()

  const [method, setMethod] = useState<'LINK' | 'PASSWORD'>('LINK')
  const [password, setPassword] = useState('')
  const [touched, setTouched] = useState(false)
  const [issued, setIssued] = useState<CredentialToken | null>(null)

  const busy = update.isPending || issueReset.isPending
  const tooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH
  const error =
    touched && password.length === 0
      ? 'Password is required.'
      : tooShort
        ? `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`
        : undefined

  const activeError = method === 'LINK' ? issueReset.error : update.error

  function dismiss() {
    // Resetting the mutation drops its `data`, which is the only other place the
    // token exists once this component's state is cleared.
    issueReset.reset()
    setIssued(null)
    setPassword('')
    onClose()
  }

  if (issued) {
    return (
      <Dialog
        open={open}
        title="Reset link issued"
        size="wide"
        // Escape and a backdrop click are the two ways a dialog gets closed by
        // accident, and here that loses the only copy of the link.
        dismissible={false}
        onClose={dismiss}
      >
        <HandoverLinkPanel token={issued} operatorName={operator.full_name} onDismiss={dismiss} />
      </Dialog>
    )
  }

  return (
    <Dialog
      open={open}
      title={`Reset ${operator.full_name}'s password`}
      dismissible={!busy}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={busy}
          >
            Cancel
          </button>
          {method === 'LINK' ? (
            <button
              type="button"
              className="button button--primary"
              disabled={busy}
              onClick={() => {
                void (async () => {
                  try {
                    const result = await issueReset.mutateAsync()
                    setIssued(result.reset)
                  } catch {
                    /* rendered below */
                  }
                })()
              }}
            >
              {issueReset.isPending ? 'Issuing…' : 'Issue a reset link'}
            </button>
          ) : (
            <button
              type="button"
              className="button button--primary"
              disabled={password.length < MIN_PASSWORD_LENGTH || busy}
              onClick={() => {
                void (async () => {
                  try {
                    await update.mutateAsync({ password })
                    notifications.success(
                      `Password reset for ${operator.full_name}. They have been signed out everywhere.`,
                    )
                    // Not kept in state a moment longer than the request needs it.
                    setPassword('')
                    onClose()
                  } catch {
                    /* rendered below */
                  }
                })()
              }}
            >
              {update.isPending ? 'Resetting…' : 'Set password'}
            </button>
          )}
        </>
      }
    >
      <SelectField
        label="How they get back in"
        value={method}
        onChange={(value) => setMethod(value as 'LINK' | 'PASSWORD')}
        disabled={busy}
        options={[
          {
            value: 'LINK',
            label: 'Issue a single-use reset link',
            description:
              'They choose their own password. You never learn it — you only pass the link on.',
          },
          {
            value: 'PASSWORD',
            label: 'Set a password for them',
            description:
              'You choose the password and tell them yourself. You will know their credential, and the audit trail records that you set it.',
          },
        ]}
      />

      {method === 'PASSWORD' ? (
        <TextField
          label="New password"
          type="password"
          required
          autoComplete="new-password"
          value={password}
          error={error}
          onChange={setPassword}
          onBlur={() => setTouched(true)}
          disabled={busy}
          hint={`At least ${MIN_PASSWORD_LENGTH} characters. You will need to give this to them yourself — AccessLink cannot show it again.`}
        />
      ) : (
        <InfoNote title="The link is shown once, here">
          AccessLink has no email. After you issue it, copy the link and send it over
          a channel you trust — it cannot be shown again.
        </InfoNote>
      )}

      <InfoNote tone="warning" title="This signs them out everywhere">
        {method === 'LINK'
          ? 'Their sessions end the moment they redeem the link, on every device. Until then their current password keeps working.'
          : 'Resetting a password ends every session this operator holds, on every device. They will not be able to work until you have given them the new password.'}
      </InfoNote>

      <FormError
        message={
          activeError
            ? activeError instanceof ApiError
              ? activeError.message
              : method === 'LINK'
                ? 'The reset link could not be issued.'
                : 'The password could not be reset.'
            : null
        }
        requestId={activeError instanceof ApiError ? activeError.requestId : null}
      />
    </Dialog>
  )
}

/**
 * Re-issuing an invitation for an account that has never been used.
 *
 * SEPARATE FROM A RESET, because the server keeps them separate and audits them
 * differently: an invitation is only valid for an account that has never signed
 * in, and re-inviting one that has would be a reset wearing an invitation's
 * name. The console offers this only when the account qualifies, so the
 * distinction is made here rather than discovered as a 409 after the fact.
 *
 * Issuing SUPERSEDES any outstanding link, which is stated because an
 * administrator re-sending an invitation usually assumes the first one still
 * works — and two live links to one account is the state this prevents.
 */
export function InviteOperatorDialog({
  open,
  operator,
  onClose,
}: {
  open: boolean
  operator: OperatorAccount
  onClose: () => void
}) {
  const invite = useInviteOperator(operator.id)
  const [issued, setIssued] = useState<CredentialToken | null>(null)

  function dismiss() {
    invite.reset()
    setIssued(null)
    onClose()
  }

  if (issued) {
    return (
      <Dialog
        open={open}
        title="Invitation issued"
        size="wide"
        dismissible={false}
        onClose={dismiss}
      >
        <HandoverLinkPanel token={issued} operatorName={operator.full_name} onDismiss={dismiss} />
      </Dialog>
    )
  }

  return (
    <Dialog
      open={open}
      title={`Invite ${operator.full_name}`}
      description="This account has never been signed in to. A fresh invitation lets them set their own password."
      dismissible={!invite.isPending}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={invite.isPending}
          >
            Cancel
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={invite.isPending}
            onClick={() => {
              void (async () => {
                try {
                  const result = await invite.mutateAsync()
                  setIssued(result.invitation)
                } catch {
                  /* rendered below */
                }
              })()
            }}
          >
            {invite.isPending ? 'Issuing…' : 'Issue an invitation'}
          </button>
        </>
      }
    >
      <InfoNote title="Any earlier invitation stops working">
        Issuing a new link invalidates the previous one, so two people cannot each
        be holding a live invitation to the same account.
      </InfoNote>

      <FormError
        message={
          invite.error
            ? invite.error instanceof ApiError
              ? invite.error.message
              : 'The invitation could not be issued.'
            : null
        }
        requestId={invite.error instanceof ApiError ? invite.error.requestId : null}
      />
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Activation and removal
// ---------------------------------------------------------------------------

export function OperatorActivationDialog({
  open,
  operator,
  onClose,
}: {
  open: boolean
  operator: OperatorAccount
  onClose: () => void
}) {
  const update = useUpdateOperator(operator.id)
  const notifications = useNotifications()
  const deactivating = operator.active

  return (
    <ConfirmDialog
      open={open}
      tone={deactivating ? 'danger' : 'default'}
      title={
        deactivating
          ? `Deactivate ${operator.full_name}?`
          : `Reactivate ${operator.full_name}?`
      }
      consequence={
        deactivating ? (
          <>
            They are signed out everywhere immediately and cannot sign in again
            until the account is reactivated.
          </>
        ) : (
          <>They will be able to sign in again with their existing password.</>
        )
      }
      detail={
        deactivating
          ? 'Reversible — the account and its site access are kept, and you can reactivate it from this same screen. To remove the account entirely, use Remove.'
          : undefined
      }
      confirmLabel={deactivating ? 'Deactivate account' : 'Reactivate account'}
      onConfirm={async () => {
        await update.mutateAsync({ active: !operator.active })
        notifications.success(
          deactivating
            ? `${operator.full_name} deactivated and signed out`
            : `${operator.full_name} reactivated`,
        )
      }}
      onClose={onClose}
    />
  )
}

export function RemoveOperatorDialog({
  open,
  operator,
  onClose,
  onRemoved,
}: {
  open: boolean
  operator: OperatorAccount
  onClose: () => void
  onRemoved: () => void
}) {
  const remove = useDeleteOperator()
  const notifications = useNotifications()

  return (
    <ConfirmDialog
      open={open}
      title={`Remove ${operator.full_name}?`}
      consequence={
        <>
          They are signed out everywhere and can no longer reach this console. Their
          email address becomes available for a new account.
        </>
      }
      detail={
        <>
          The account is kept for audit but disappears from this list, and cannot be
          restored from here. If they are only away temporarily,{' '}
          <strong>deactivate the account instead</strong> — that is reversible and
          keeps their site access.
        </>
      }
      confirmPhrase={operator.email}
      confirmLabel="Remove operator"
      onConfirm={async () => {
        await remove.mutateAsync(operator.id)
        notifications.success(`${operator.full_name} removed`)
        onRemoved()
      }}
      onClose={onClose}
    />
  )
}
