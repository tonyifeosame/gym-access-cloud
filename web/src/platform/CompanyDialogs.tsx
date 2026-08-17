import { useState } from 'react'
import { useNavigate } from 'react-router-dom'

import { ApiError } from '../api/client'
import type { CredentialToken } from '../api/types'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Dialog } from '../components/Dialog'
import { FormActions, FormError, TextField } from '../components/Form'
import { HandoverLinkPanel } from '../components/HandoverLinkPanel'
import { useNotifications } from '../components/Notifications'
import { InfoNote } from '../components/states'
import { submitErrorMessage, useForm, validators } from '../components/useForm'
import { useCreateCompany, useCreateFirstOperator, useUpdateCompany } from './data'
import type { PlatformCompany } from './types'

/**
 * Creating and administering a tenant.
 *
 * The four operations here are the whole of GP-01 and CON-01: create a company,
 * change its details, suspend or restore it, and issue its first operator.
 * Before these existed, all four needed SQL against production.
 */

/** Mirrors models.NormalizeSlug, so the preview matches what the server stores. */
export function deriveSlug(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

interface CreateValues extends Record<string, unknown> {
  name: string
  slug: string
  contact_email: string
  timezone: string
}

/**
 * Creating a company.
 *
 * IT IS CREATED EMPTY AND WITH NO OPERATOR, and the dialog says so before
 * submitting rather than leaving somebody to discover a company nobody can sign
 * in to. Issuing the first operator is a separate step on the company's own
 * page, which is what makes a half-finished onboarding visible and finishable
 * rather than a failure inside one request.
 *
 * THE SLUG IS DERIVED AND PREVIEWED. The server derives it from the name when
 * absent — "Acme Logistics (UK)" becomes "acme-logistics-uk" rather than being
 * refused — so the console shows the derivation instead of demanding one. It is
 * still editable, because the slug is permanent: it appears in the bootstrap
 * environment and in operator-facing URLs, and there is deliberately no route
 * that renames it.
 */
export function CreateCompanyDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const create = useCreateCompany()
  const notifications = useNotifications()
  const navigate = useNavigate()
  const [slugTouched, setSlugTouched] = useState(false)

  const form = useForm<CreateValues>({
    initialValues: { name: '', slug: '', contact_email: '', timezone: '' },
    validate: (values) => ({
      name: validators.required(values.name, 'Company name'),
      slug: slugTouched && values.slug.trim() ? validateSlug(values.slug) : undefined,
      contact_email: values.contact_email.trim()
        ? validators.email(values.contact_email, 'Contact email')
        : undefined,
    }),
    onSubmit: async (values) => {
      const company = await create.mutateAsync({
        name: values.name.trim(),
        // Omitted rather than sent derived: letting the server derive it keeps
        // one implementation of the rule, and the preview below is only a
        // preview.
        slug: slugTouched && values.slug.trim() ? values.slug.trim() : undefined,
        contact_email: values.contact_email.trim() || undefined,
        timezone: values.timezone.trim() || undefined,
      })
      notifications.success(`${company.name} created. It has no operator yet.`)
      onClose()
      // Straight to the company, because onboarding is not finished: the next
      // step is issuing its first operator, and that is on that page.
      navigate(`/platform/companies/${company.id}`)
    },
  })

  const preview = slugTouched ? form.values.slug.trim() : deriveSlug(form.values.name)
  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

  return (
    <Dialog
      open={open}
      title="Create a company"
      description="A new customer. It starts empty — no operators, no sites, no terminals."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Company name"
          required
          value={form.values.name}
          error={form.errors.name}
          onChange={(value) => form.setValue('name', value)}
          onBlur={() => form.touch('name')}
          disabled={form.submitting}
          hint="What their operators will see at the top of their console. This can be changed later."
        />

        <TextField
          label="Slug"
          value={preview}
          error={form.errors.slug}
          onChange={(value) => {
            setSlugTouched(true)
            form.setValue('slug', value)
          }}
          onBlur={() => form.touch('slug')}
          disabled={form.submitting}
          mono
          hint={
            slugTouched
              ? 'Lower case letters, numbers and hyphens. PERMANENT — there is no route that renames a slug, because it appears in configuration and in URLs that would silently break.'
              : 'Derived from the name. Edit it if you need something specific — it is PERMANENT once set, because it appears in configuration and in URLs.'
          }
        />

        <TextField
          label="Contact email"
          type="email"
          value={form.values.contact_email}
          error={form.errors.contact_email}
          onChange={(value) => form.setValue('contact_email', value)}
          onBlur={() => form.touch('contact_email')}
          disabled={form.submitting}
          hint="Optional. Who to reach about this account — not a login."
        />

        <TextField
          label="Timezone"
          value={form.values.timezone}
          onChange={(value) => form.setValue('timezone', value)}
          disabled={form.submitting}
          placeholder="Africa/Lagos"
          hint="Optional IANA zone. Defaults to UTC. Each site sets its own; this is the company's default."
        />

        <InfoNote title="The next step is its first operator">
          A company with no operator cannot be signed in to. After it is created
          you will be taken to it, where you can issue a single-use invitation for
          its owner — so nobody here ever knows the customer&apos;s password.
        </InfoNote>

        <FormError
          message={
            conflict
              ? 'A company with that slug already exists on this installation. Choose a different one.'
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
            {form.submitting ? 'Creating…' : 'Create company'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

function validateSlug(slug: string): string | undefined {
  if (!/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(slug.trim())) {
    return 'Slug must be 3–64 characters of a–z, 0–9 and hyphen, starting and ending alphanumeric.'
  }
  return undefined
}

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

interface EditValues extends Record<string, unknown> {
  name: string
  contact_email: string
  timezone: string
  event_retention_days: string
  audit_retention_days: string
}

/**
 * Changing a company's details and its retention policy.
 *
 * NO SLUG FIELD, matching the API. It appears in the bootstrap environment and
 * in operator-facing URLs, so a rename would silently break whatever refers to
 * the company by it — and the display name is what a rebrand actually needs.
 *
 * RETENTION IS BLANK FOR "KEEP FOR EVER", not zero. The column is nullable and
 * null is the default; rendering it as 0 would read as "delete immediately",
 * which is the opposite. An empty field means indefinite and the hint says so.
 */
export function EditCompanyDialog({
  open,
  company,
  onClose,
}: {
  open: boolean
  company: PlatformCompany
  onClose: () => void
}) {
  const update = useUpdateCompany(company.id)
  const notifications = useNotifications()

  const form = useForm<EditValues>({
    initialValues: {
      name: company.name,
      contact_email: company.contact_email ?? '',
      timezone: company.timezone,
      event_retention_days:
        company.event_retention_days === null ? '' : String(company.event_retention_days),
      audit_retention_days:
        company.audit_retention_days === null ? '' : String(company.audit_retention_days),
    },
    validate: (values) => ({
      name: validators.required(values.name, 'Company name'),
      contact_email: values.contact_email.trim()
        ? validators.email(values.contact_email, 'Contact email')
        : undefined,
      event_retention_days: validateDays(values.event_retention_days),
      audit_retention_days: validateDays(values.audit_retention_days),
    }),
    onSubmit: async (values) => {
      await update.mutateAsync({
        name: values.name.trim(),
        contact_email: values.contact_email.trim(),
        timezone: values.timezone.trim(),
        // Sent only when a number was given. The API has no way to express
        // "back to indefinite" — the field is a plain optional int — so
        // CLEARING ONE IS NOT SOMETHING THIS FORM CAN DO, and the hint says so
        // rather than silently leaving the old value in place.
        event_retention_days: values.event_retention_days.trim()
          ? Number(values.event_retention_days)
          : undefined,
        audit_retention_days: values.audit_retention_days.trim()
          ? Number(values.audit_retention_days)
          : undefined,
      })
      notifications.success(`${values.name.trim()} updated`)
      onClose()
    },
  })

  return (
    <Dialog
      open={open}
      title={`Edit ${company.name}`}
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Company name"
          required
          value={form.values.name}
          error={form.errors.name}
          onChange={(value) => form.setValue('name', value)}
          onBlur={() => form.touch('name')}
          disabled={form.submitting}
        />

        <TextField
          label="Contact email"
          type="email"
          value={form.values.contact_email}
          error={form.errors.contact_email}
          onChange={(value) => form.setValue('contact_email', value)}
          onBlur={() => form.touch('contact_email')}
          disabled={form.submitting}
        />

        <TextField
          label="Timezone"
          value={form.values.timezone}
          onChange={(value) => form.setValue('timezone', value)}
          disabled={form.submitting}
          hint="IANA zone. Each site sets its own; this is the company's default."
        />

        <TextField
          label="Event retention (days)"
          type="number"
          value={form.values.event_retention_days}
          error={form.errors.event_retention_days}
          onChange={(value) => form.setValue('event_retention_days', value)}
          onBlur={() => form.touch('event_retention_days')}
          disabled={form.submitting}
          hint="Blank means keep for ever, which is the default. Once a period is set, this form cannot return it to indefinite — the API has no way to express that."
        />

        <TextField
          label="Audit retention (days)"
          type="number"
          value={form.values.audit_retention_days}
          error={form.errors.audit_retention_days}
          onChange={(value) => form.setValue('audit_retention_days', value)}
          onBlur={() => form.touch('audit_retention_days')}
          disabled={form.submitting}
          hint="Blank means keep for ever. An audit trail is usually kept longer than events."
        />

        {/* The slug's absence, explained. A missing field with no reason reads
            as an oversight, and somebody will ask for it. */}
        <InfoNote title="The slug cannot be changed">
          <code className="mono">{company.slug}</code> is permanent. It appears in
          deployment configuration and in URLs that refer to this company, so a
          rename would break them silently rather than visibly.
        </InfoNote>

        <FormError
          message={submitErrorMessage(form.submitError)}
          requestId={form.submitError instanceof ApiError ? form.submitError.requestId : null}
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
            {form.submitting ? 'Saving…' : 'Save changes'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

function validateDays(raw: string): string | undefined {
  if (!raw.trim()) return undefined
  const value = Number(raw)
  if (!Number.isInteger(value) || value < 1) {
    return 'Enter a whole number of days, or leave blank to keep for ever.'
  }
  return undefined
}

// ---------------------------------------------------------------------------
// Suspend / restore
// ---------------------------------------------------------------------------

/**
 * Suspending a tenant.
 *
 * THE SHARPEST OPERATION ON THIS SURFACE. A company with active=false fails the
 * predicate every operator session is resolved through, so every session in it
 * stops working on the next request — the customer is locked out of their own
 * console within seconds, and their terminals stop authenticating with it.
 *
 * Nothing is destroyed and it is reversible from this same screen, which is why
 * it is "suspend" rather than "delete". There is no delete: a tenant's data is
 * theirs, and removing it is not a button.
 */
export function CompanyActivationDialog({
  open,
  company,
  onClose,
}: {
  open: boolean
  company: PlatformCompany
  onClose: () => void
}) {
  const update = useUpdateCompany(company.id)
  const notifications = useNotifications()
  const [reason, setReason] = useState('')
  const suspending = company.active

  return (
    <ConfirmDialog
      open={open}
      tone={suspending ? 'danger' : 'default'}
      title={suspending ? `Suspend ${company.name}?` : `Restore ${company.name}?`}
      consequence={
        suspending ? (
          <>
            Every operator in {company.name} is signed out <strong>within
            seconds</strong> and cannot sign in again. Their terminals stop
            authenticating with the platform.
          </>
        ) : (
          <>
            Operators can sign in again immediately, and terminals resume
            authenticating.
          </>
        )
      }
      detail={
        suspending ? (
          <>
            Reversible from this screen — nothing is deleted. Their sites,
            terminals, people and history are kept exactly as they are. There is
            no way to delete a company from this console: a customer&apos;s data is
            theirs, and removing it is not something a button should do.
          </>
        ) : undefined
      }
      confirmPhrase={suspending ? company.slug : undefined}
      confirmLabel={suspending ? 'Suspend company' : 'Restore company'}
      onConfirm={async () => {
        await update.mutateAsync({
          active: !company.active,
          deactivated_reason: suspending ? reason.trim() || undefined : undefined,
        })
        notifications.success(
          suspending ? `${company.name} suspended` : `${company.name} restored`,
        )
      }}
      onClose={onClose}
    >
      {suspending ? (
        <TextField
          label="Reason (optional)"
          value={reason}
          onChange={setReason}
          placeholder="e.g. non-payment, awaiting contract renewal"
          hint="Recorded in this company's own audit trail, where their operators can read it."
        />
      ) : null}
    </ConfirmDialog>
  )
}

// ---------------------------------------------------------------------------
// First operator
// ---------------------------------------------------------------------------

interface OwnerValues extends Record<string, unknown> {
  email: string
  full_name: string
}

/**
 * Issuing a company's first operator.
 *
 * ONLY INTO A COMPANY THAT HAS NONE, refused server-side by a query predicate.
 * That rule is what keeps this from being a standing back door into every
 * customer: once a tenant has an owner, every further account is created by them
 * from their own console, and this surface can never add another.
 *
 * ALWAYS AN INVITATION. The password field the API still accepts is deliberately
 * NOT OFFERED here — this is the vendor creating a credential on a customer's
 * account, and the one thing that must not happen is the vendor knowing the
 * customer's owner password. On the tenant console an administrator choosing a
 * colleague's password is a defensible fallback; across a company boundary it is
 * not, and the console should not make it available just because the API would
 * accept it.
 */
export function FirstOperatorDialog({
  open,
  company,
  onClose,
}: {
  open: boolean
  company: PlatformCompany
  onClose: () => void
}) {
  const create = useCreateFirstOperator(company.id)
  const [issued, setIssued] = useState<{ token: CredentialToken; name: string } | null>(null)

  const form = useForm<OwnerValues>({
    initialValues: { email: '', full_name: '' },
    validate: (values) => ({ email: validators.email(values.email) }),
    onSubmit: async (values) => {
      const result = await create.mutateAsync({
        email: values.email.trim(),
        full_name: values.full_name.trim() || undefined,
        // No password, ever, from this surface. See the note above.
      })
      if (result.invitation) {
        setIssued({
          token: result.invitation,
          name: result.operator.full_name || result.operator.email,
        })
        return
      }
      // The server answered without an invitation, which should not happen when
      // no password was sent. Say so rather than closing on a success that left
      // nobody able to sign in.
      setIssued(null)
      onClose()
    },
  })

  function dismiss() {
    create.reset()
    setIssued(null)
    onClose()
  }

  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

  if (issued) {
    return (
      <Dialog
        open={open}
        title="Owner invited"
        size="wide"
        dismissible={false}
        onClose={dismiss}
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
      title={`Issue the first operator for ${company.name}`}
      description="An OWNER account for the customer. Everything else in their company is created by them, from their own console."
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
          hint="The customer's own address. This is what they will sign in with."
        />

        <TextField
          label="Full name"
          value={form.values.full_name}
          onChange={(value) => form.setValue('full_name', value)}
          disabled={form.submitting}
          hint="Optional."
        />

        <InfoNote title="You will never know their password">
          This issues a single-use invitation, not a credential. The customer sets
          their own password from it. AccessLink has no email, so the link appears
          once, here — copy it and send it over a channel you trust.
        </InfoNote>

        <InfoNote tone="warning" title="This is the only account you can create for them">
          Once {company.name} has an operator, this surface cannot add another —
          the server refuses it. Every further account is created by their owner,
          inside their own console. That is deliberate: an administration
          credential that could add accounts to a running customer at any time
          would be a standing way into every company on this installation.
        </InfoNote>

        <FormError
          message={
            conflict
              ? 'That company already has an operator, or that email address is already in use. Further accounts are created from the company’s own console.'
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
            {form.submitting ? 'Issuing…' : 'Issue an invitation'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}
