import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { CredentialToken, Role } from '../../api/types'
import { invitationOf, operatorOf } from '../../api/types'
import { assignableRoles } from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { Dialog } from '../../components/Dialog'
import {
  CheckboxGroup,
  FormActions,
  FormError,
  SelectField,
  TextField,
} from '../../components/Form'
import { HandoverLinkPanel } from '../../components/HandoverLinkPanel'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCreateOperator, useSites } from '../../data/console'
import { useAuthenticatedSession } from '../../session/useSession'

/** Mirrors models.MinPasswordLength. The server is the authority; this spares a round trip. */
const MIN_PASSWORD_LENGTH = 12

/**
 * Creating an operator account.
 *
 * THE DEFAULT IS AN INVITATION, AND THAT IS THE POINT OF THIS SCREEN. The
 * account is created with a credential nobody holds, and the response carries a
 * single-use link. The administrator creating the account therefore never learns
 * how to sign in as it.
 *
 * The alternative — typing a password and reading it out — is still offered,
 * because a deployment with no way to send a link needs it. It is the second
 * option, it says what it costs, and choosing it is a deliberate act rather than
 * the only path. Before the credential handover existed it WAS the only path,
 * and the realistic consequence was not hypothetical: initial passwords travelled
 * by chat in plain text, usually stayed unchanged, and the administrator knew
 * them indefinitely with nothing recording that they did.
 *
 * TWO OTHER THINGS ARE EASY TO GET DANGEROUSLY WRONG HERE:
 *
 * THE ROLE LIST IS NARROWED TO WHAT THE CALLER MAY ASSIGN. An ADMIN cannot
 * create an OWNER, or ADMIN would be a synonym for OWNER one request later. The
 * server enforces it; offering the option anyway would produce a 403 after the
 * form was filled in.
 *
 * AN EMPTY SITE SELECTION MEANS EVERY SITE, NOT NONE. That is the platform's
 * grant rule — absence is the default rather than a denial — and it is the one
 * place in this product whose two readings are exact opposites, where the
 * dangerous one looks like the safe one. The form says which it is, in words, as
 * the selection changes.
 */

type Handover = 'INVITE' | 'PASSWORD'

interface Values extends Record<string, unknown> {
  email: string
  full_name: string
  password: string
  role: Role
  site_ids: string[]
}

export function OperatorFormDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const session = useAuthenticatedSession()
  const create = useCreateOperator()
  const sites = useSites()
  const notifications = useNotifications()

  const [handover, setHandover] = useState<Handover>('INVITE')
  // The minted link, held for the life of this panel and nowhere else.
  const [issued, setIssued] = useState<{ token: CredentialToken; name: string } | null>(null)

  const roles = assignableRoles(session)

  const form = useForm<Values>({
    initialValues: {
      email: '',
      full_name: '',
      password: '',
      role: 'VIEWER',
      site_ids: [],
    },
    validate: (values) => ({
      email: validators.email(values.email),
      full_name: validators.required(values.full_name, 'Full name'),
      // Only validated on the path that collects one. Requiring a password the
      // form is not asking for would make the invitation path unsubmittable.
      password:
        handover === 'PASSWORD'
          ? (validators.required(values.password, 'Password') ??
            validators.minLength(values.password, MIN_PASSWORD_LENGTH, 'Password'))
          : undefined,
    }),
    onSubmit: async (values) => {
      const response = await create.mutateAsync({
        email: values.email.trim(),
        full_name: values.full_name.trim(),
        // Omitted, not empty: an absent password is what asks the server for an
        // invitation, and sending "" would mean the same thing by accident
        // rather than on purpose.
        password: handover === 'PASSWORD' ? values.password : undefined,
        role: values.role,
        // Likewise omitted rather than sent empty: an empty list is a meaningful
        // value to the API ("not scoped"), which is also what omitting produces.
        site_ids: values.site_ids.length > 0 ? values.site_ids : undefined,
      })

      const created = operatorOf(response)
      const invitation = invitationOf(response)

      if (invitation) {
        // The dialog STAYS OPEN and switches to the link. Closing here would
        // discard a credential that cannot be read back, and the operator would
        // have to issue a second invitation to recover from a successful action.
        setIssued({ token: invitation, name: created.full_name })
        return
      }

      notifications.success(`${created.full_name} can now sign in`)
      onClose()
    },
  })

  function dismiss() {
    // Reset drops the mutation's `data`, which is the only place the token still
    // exists. Without this it would survive the panel for the life of the hook.
    create.reset()
    setIssued(null)
    onClose()
  }

  // Grants are only consulted for MANAGER and VIEWER; ADMIN and OWNER reach
  // every site regardless. Saying so beats letting somebody carefully pick three
  // sites for an ADMIN and believe it did something.
  const roleIgnoresGrants = form.values.role === 'ADMIN' || form.values.role === 'OWNER'
  const unscoped = form.values.site_ids.length === 0

  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

  if (issued) {
    return (
      <Dialog
        open={open}
        title="Operator created"
        // Not dismissible: Escape and backdrop clicks are the two ways a dialog
        // gets closed by accident, and here that loses the only copy of a link.
        dismissible={false}
        onClose={dismiss}
        size="wide"
      >
        <HandoverLinkPanel
          token={issued.token}
          operatorName={issued.name}
          onDismiss={dismiss}
        />
      </Dialog>
    )
  }

  return (
    <Dialog
      open={open}
      title="Add an operator"
      description="Someone who can sign in to this console. This is not a person the terminals recognise — that is People."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Email"
          type="email"
          required
          autoComplete="off"
          value={form.values.email}
          error={form.errors.email}
          onChange={(value) => form.setValue('email', value)}
          onBlur={() => form.touch('email')}
          disabled={form.submitting}
          hint="What they sign in with. Unique within your company."
        />

        <TextField
          label="Full name"
          required
          value={form.values.full_name}
          error={form.errors.full_name}
          onChange={(value) => form.setValue('full_name', value)}
          onBlur={() => form.touch('full_name')}
          disabled={form.submitting}
        />

        <SelectField
          label="How they get their password"
          required
          value={handover}
          onChange={(value) => setHandover(value as Handover)}
          disabled={form.submitting}
          options={[
            {
              value: 'INVITE',
              label: 'Send them an invitation link',
              description:
                'The account is created with no usable password and you get a single-use link to send them. You never learn their password.',
            },
            {
              value: 'PASSWORD',
              label: 'Set a password myself',
              description:
                'You choose the password and hand it over yourself. You will know their credential, and they are asked to change it at first sign-in.',
            },
          ]}
        />

        {handover === 'PASSWORD' ? (
          <TextField
            label="Initial password"
            type="password"
            required
            autoComplete="new-password"
            value={form.values.password}
            error={form.errors.password}
            onChange={(value) => form.setValue('password', value)}
            onBlur={() => form.touch('password')}
            disabled={form.submitting}
            hint={`At least ${MIN_PASSWORD_LENGTH} characters. You will need to give this to them yourself — AccessLink does not send it, and cannot show it to you again.`}
          />
        ) : (
          <InfoNote title="You will be given a link to send">
            AccessLink does not have email. The link appears once, here, after the
            account is created — copy it then and send it over a channel you trust.
          </InfoNote>
        )}

        <SelectField
          label="Role"
          required
          value={form.values.role}
          onChange={(value) => form.setValue('role', value as Role)}
          disabled={form.submitting}
          options={roles.map((role) => ({
            value: role,
            label: roleLabel(role),
            description: ROLE_DESCRIPTIONS[role],
          }))}
        />

        <CheckboxGroup
          legend="Sites this operator may act on"
          hint={
            roleIgnoresGrants
              ? 'Not used for this role — an administrator or owner reaches every site in the company.'
              : unscoped
                ? 'Nothing selected means EVERY site in the company. Select sites to narrow them to those.'
                : `Narrowed to ${form.values.site_ids.length} site${form.values.site_ids.length === 1 ? '' : 's'}.`
          }
          options={(sites.data?.sites ?? []).map((site) => ({
            value: site.id,
            label: site.name,
            description: site.active ? undefined : 'Deactivated',
          }))}
          selected={form.values.site_ids}
          onChange={(selected) => form.setValue('site_ids', selected)}
          disabled={form.submitting || roleIgnoresGrants}
          empty="Your company has no sites yet."
        />

        {!roleIgnoresGrants && unscoped ? (
          <InfoNote tone="warning" title="This operator will reach every site">
            An empty selection is not &ldquo;no access&rdquo; — it means the account is
            not scoped, which is every site in {session.company.name}. Choose sites
            above to limit them.
          </InfoNote>
        ) : null}

        <FormError
          message={
            conflict
              ? 'That email address is already in use in your company.'
              : submitErrorMessage(error)
          }
          requestId={error instanceof ApiError ? error.requestId : null}
        />

        <FormActions>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={form.submitting}
          >
            Cancel
          </button>
          <button type="submit" className="button button--primary" disabled={form.submitting}>
            {form.submitting
              ? 'Creating…'
              : handover === 'INVITE'
                ? 'Create and invite'
                : 'Create operator'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

/**
 * What each role may do, in the console's own terms.
 *
 * Kept here rather than in the roles module because these are descriptions of
 * product capability, and they have to stay in step with the gates in
 * auth/permissions.ts and with the route groups on the server.
 */
export const ROLE_DESCRIPTIONS: Record<Role, string> = {
  VIEWER: 'Read everything they are scoped to. Changes nothing.',
  MANAGER: 'Day-to-day work: people, terminal configuration, site settings.',
  ADMIN: 'Everything a manager can do, plus sites and operator accounts. Reaches every site.',
  OWNER: 'Everything, including which applications the company has enabled.',
}
