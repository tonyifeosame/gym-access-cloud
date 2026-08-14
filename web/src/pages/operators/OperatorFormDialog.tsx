import { ApiError } from '../../api/client'
import type { Role } from '../../api/types'
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
 * TWO THINGS HERE ARE EASY TO GET DANGEROUSLY WRONG, and both are handled
 * explicitly rather than left to the operator to infer.
 *
 * THE ROLE LIST IS NARROWED TO WHAT THE CALLER MAY ASSIGN. An ADMIN cannot
 * create an OWNER, because otherwise ADMIN would be a synonym for OWNER one
 * request later. The server enforces it; offering the option anyway would just
 * produce a 403 after the password had been typed.
 *
 * AN EMPTY SITE SELECTION MEANS EVERY SITE, NOT NONE. That is the platform's
 * grant rule — absence is the default rather than a denial — and it is the one
 * piece of this product whose two readings are exact opposites, where the
 * dangerous one looks like the safe one. The form states which it is, in words,
 * as the selection changes.
 *
 * THE PASSWORD IS SET DIRECTLY AND MUST BE COMMUNICATED OUT OF BAND. There is no
 * invite flow in the platform (MR-015), so whoever creates the account has to
 * hand the password over by some other means. The form says so rather than
 * leaving somebody to email it in plain text without thinking.
 */

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
      password:
        validators.required(values.password, 'Password') ??
        validators.minLength(values.password, MIN_PASSWORD_LENGTH, 'Password'),
    }),
    onSubmit: async (values) => {
      const created = await create.mutateAsync({
        email: values.email.trim(),
        full_name: values.full_name.trim(),
        password: values.password,
        role: values.role,
        // Omitted rather than sent empty: an empty list is a meaningful value
        // to the API ("not scoped"), and it is also what omitting it produces,
        // so this keeps the request honest about intent.
        site_ids: values.site_ids.length > 0 ? values.site_ids : undefined,
      })
      notifications.success(`${created.full_name} can now sign in`)
      onClose()
    },
  })

  // Grants are only consulted for MANAGER and VIEWER; ADMIN and OWNER reach
  // every site regardless. Saying so beats letting somebody carefully pick
  // three sites for an ADMIN and believe it did something.
  const roleIgnoresGrants = form.values.role === 'ADMIN' || form.values.role === 'OWNER'
  const unscoped = form.values.site_ids.length === 0

  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

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
          hint={`At least ${MIN_PASSWORD_LENGTH} characters. You will need to give this to them yourself — AccessLink does not send invitations, and cannot show you this password again.`}
        />

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
            {form.submitting ? 'Creating…' : 'Create operator'}
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
