import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { APICredentialIssued, APICredentialScope } from '../../api/types'
import { Dialog } from '../../components/Dialog'
import { CheckboxGroup, FormActions, FormError, TextField } from '../../components/Form'
import { SecretPanel } from '../../components/SecretPanel'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useIssueAPICredential, useSites } from '../../data/console'
import { useAuthenticatedSession } from '../../session/useSession'
import { AVAILABLE_SCOPES, UNAVAILABLE_SCOPES } from './credentialVocabulary'

/** Mirrors models.MaxAPICredentialNameLength. The server is the authority; this spares a round trip. */
const MAX_NAME_LENGTH = 80

interface Values extends Record<string, unknown> {
  name: string
  scopes: APICredentialScope[]
  site_ids: string[]
  /** A plain day from the date control, or "" for the server's default lifetime. */
  expires_on: string
}

/**
 * Issuing an integration credential.
 *
 * THE DIALOG STAYS OPEN AFTER SUCCESS and turns into the secret panel. Closing
 * on success would discard the only copy of a key that cannot be read back, and
 * the administrator would have to issue a second credential to recover from a
 * successful action. The same shape as creating an operator by invitation.
 *
 * ONLY SCOPES WITH LIVE ENDPOINTS ARE OFFERED. The server will issue a key with
 * any scope in its registry, including five that no public route honours yet;
 * such a key authenticates and then gets 403 or 404 for everything. Offering
 * those would let an integrator be issued a credential that cannot do anything
 * and go looking for the fault in their own code. They are listed, greyed, so
 * the roadmap is visible and nobody asks whether the console is hiding them.
 *
 * AN EMPTY SITE SELECTION MEANS EVERY SITE, NOT NONE -- the platform's grant
 * rule, said in words beside the control, as the operator form does.
 *
 * WHAT THE SERVER STILL DECIDES, and this form only reports: the issuer bound
 * (a scope above the caller's role is 403 with the scope named), the cap of
 * twenty live credentials per company (409), and a name already in use (409).
 */
export function IssueCredentialDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const session = useAuthenticatedSession()
  const issue = useIssueAPICredential()
  const sites = useSites()
  const notifications = useNotifications()

  // The minted credential, held for the life of this panel and nowhere else.
  const [issued, setIssued] = useState<APICredentialIssued | null>(null)

  const form = useForm<Values>({
    initialValues: { name: '', scopes: [], site_ids: [], expires_on: '' },
    validate: (values) => ({
      name:
        validators.required(values.name, 'Name') ??
        validators.maxLength(values.name, MAX_NAME_LENGTH, 'Name'),
      scopes: values.scopes.length === 0 ? 'Choose at least one scope.' : undefined,
      expires_on: expiryError(values.expires_on),
    }),
    onSubmit: async (values) => {
      const response = await issue.mutateAsync({
        name: values.name.trim(),
        scopes: values.scopes,
        // Omitted, not empty: an empty list is a meaningful value to the API
        // ("not restricted"), which is also what omitting produces.
        site_ids: values.site_ids.length > 0 ? values.site_ids : undefined,
        // OMITTED for the default lifetime. The server tests for the KEY's
        // presence, so this must be absent rather than null or "".
        expires_at: values.expires_on ? endOfDayISO(values.expires_on) : undefined,
      })
      setIssued(response)
    },
  })

  function dismiss() {
    // Reset drops the mutation's `data`, which is the only other place the
    // secret still exists. Without this it would survive the panel for the life
    // of the hook.
    issue.reset()
    setIssued(null)
    notifications.success('Credential issued')
    onClose()
  }

  const unscoped = form.values.site_ids.length === 0
  const error = form.submitError
  const apiError = error instanceof ApiError ? error : null

  if (issued) {
    return (
      <Dialog
        open={open}
        title="Credential issued"
        // Not dismissible: Escape and backdrop clicks are the two ways a dialog
        // gets closed by accident, and here that loses the only copy of a key.
        dismissible={false}
        onClose={dismiss}
        size="wide"
      >
        <SecretPanel
          heading={`Secret for ${issued.name}`}
          warning={
            <>
              <strong>This secret is shown once and cannot be recovered.</strong> Store it
              in your integration&apos;s secret store now. Anyone holding it can read what
              this credential is scoped to. If it is lost, the only remedy is to rotate the
              credential, which issues a new secret and retires this one.
            </>
          }
          label="Secret"
          secret={issued.secret}
          prefix={issued.key_prefix}
          onDismiss={dismiss}
        />
      </Dialog>
    )
  }

  return (
    <Dialog
      open={open}
      title="Issue a credential"
      description="A key one of your own systems presents to the public API."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Name"
          required
          value={form.values.name}
          error={form.errors.name}
          onChange={(value) => form.setValue('name', value)}
          onBlur={() => form.touch('name')}
          disabled={form.submitting}
          hint="What this integration is, for the list and the audit trail — for example the system that will hold the key. Unique within your company."
          autoComplete="off"
        />

        <CheckboxGroup
          legend="What it may do"
          hint="Read-only scopes. A credential cannot issue or manage other credentials; that stays with administrators here."
          options={AVAILABLE_SCOPES.map((definition) => ({
            value: definition.scope,
            label: definition.label,
            description: definition.description,
          }))}
          selected={form.values.scopes}
          onChange={(selected) => form.setValue('scopes', selected as APICredentialScope[])}
          disabled={form.submitting}
        />
        {form.errors.scopes ? (
          <p className="field__error" role="alert">
            {form.errors.scopes}
          </p>
        ) : null}

        <InfoNote title="Not available yet" headingLevel={3}>
          <p>
            The public API does not have endpoints for these yet, so a credential cannot
            be issued with them from here:
          </p>
          <ul className="chip-list">
            {UNAVAILABLE_SCOPES.map((definition) => (
              <li key={definition.scope}>
                <span className="chip" title={definition.description}>
                  {definition.label}
                </span>
              </li>
            ))}
          </ul>
        </InfoNote>

        <CheckboxGroup
          legend="Sites it may read"
          hint={
            unscoped
              ? 'Nothing selected means EVERY site in the company. Select sites to narrow it to those.'
              : `Narrowed to ${form.values.site_ids.length} site${form.values.site_ids.length === 1 ? '' : 's'}. Members are company-wide and are not narrowed by this.`
          }
          options={(sites.data?.sites ?? []).map((site) => ({
            value: site.id,
            label: site.name,
            description: site.active ? undefined : 'Deactivated',
          }))}
          selected={form.values.site_ids}
          onChange={(selected) => form.setValue('site_ids', selected)}
          disabled={form.submitting}
          loading={sites.isPending}
          empty="Your company has no sites yet."
        />

        {unscoped ? (
          <InfoNote tone="warning" headingLevel={3} title="This credential will reach all sites">
            An empty selection is not &ldquo;no access&rdquo; — it means the credential is
            not restricted, which is every site in {session.company.name}. Choose sites
            above to limit it.
          </InfoNote>
        ) : null}

        <TextField
          label="Expires on"
          type="date"
          value={form.values.expires_on}
          error={form.errors.expires_on}
          onChange={(value) => form.setValue('expires_on', value)}
          onBlur={() => form.touch('expires_on')}
          disabled={form.submitting}
          hint="Optional. Leave empty for the default of one year. An expired credential stops working and cannot be extended — issue a new one."
        />

        <FormError message={issueErrorMessage(apiError, error)} requestId={apiError?.requestId ?? null} />

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
            {form.submitting ? 'Issuing…' : 'Issue credential'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

/**
 * The server's refusals, in the console's words where the raw message is not
 * enough on its own. Everything else is the server's own sentence, which is
 * already written for a person.
 */
function issueErrorMessage(apiError: ApiError | null, error: unknown): string | null {
  if (!apiError) return submitErrorMessage(error)
  if (apiError.code === 'API_CREDENTIAL_LIMIT_REACHED') {
    return 'Your company already has the maximum number of live credentials. Revoke one you no longer use, then try again.'
  }
  if (apiError.status === 403) {
    return `${apiError.message}. Ask an owner to issue this credential, or choose fewer scopes.`
  }
  return apiError.message
}

function expiryError(day: string): string | undefined {
  if (!day) return undefined
  const instant = Date.parse(`${day}T00:00:00Z`)
  if (Number.isNaN(instant)) return 'Enter a date.'
  if (instant <= Date.now()) return 'The expiry must be in the future.'
  return undefined
}

/**
 * A plain day becomes the END of that day, UTC. "Expires on the 30th" means it
 * still works on the 30th; a midnight instant would expire it as the day began.
 */
function endOfDayISO(day: string): string {
  return `${day}T23:59:59Z`
}
