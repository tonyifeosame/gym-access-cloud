import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { OperatorAccount, Role } from '../../api/types'
import { assignableRoles } from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { CheckboxGroup, FormError, SelectField, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { useDeleteOperator, useSetOperatorSites, useSites, useUpdateOperator } from '../../data/console'
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
// Password reset
// ---------------------------------------------------------------------------

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
  const notifications = useNotifications()
  const [password, setPassword] = useState('')
  const [touched, setTouched] = useState(false)

  const tooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH
  const error = touched && password.length === 0 ? 'Password is required.' : tooShort ? `Password must be at least ${MIN_PASSWORD_LENGTH} characters.` : undefined

  return (
    <Dialog
      open={open}
      title={`Reset ${operator.full_name}'s password`}
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
            disabled={password.length < MIN_PASSWORD_LENGTH || update.isPending}
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
            {update.isPending ? 'Resetting…' : 'Reset password'}
          </button>
        </>
      }
    >
      <TextField
        label="New password"
        type="password"
        required
        autoComplete="new-password"
        value={password}
        error={error}
        onChange={setPassword}
        onBlur={() => setTouched(true)}
        disabled={update.isPending}
        hint={`At least ${MIN_PASSWORD_LENGTH} characters. You will need to give this to them yourself — AccessLink cannot show it again.`}
      />

      <InfoNote tone="warning" title="This signs them out everywhere">
        Resetting a password ends every session this operator holds, on every
        device. They will not be able to work until you have given them the new
        password.
      </InfoNote>

      <FormError
        message={
          update.error
            ? update.error instanceof ApiError
              ? update.error.message
              : 'The password could not be reset.'
            : null
        }
        requestId={update.error instanceof ApiError ? update.error.requestId : null}
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
