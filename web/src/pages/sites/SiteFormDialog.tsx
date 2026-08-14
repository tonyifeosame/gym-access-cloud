import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { CreateSiteResponse, Site } from '../../api/types'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCreateSite, useUpdateSite } from '../../data/console'
import { CredentialPanel } from './CredentialPanel'

/**
 * Creating and editing a site.
 *
 * ONE COMPONENT FOR BOTH, because the fields are identical and the difference
 * is entirely in what happens afterwards: creating produces a provisioning key
 * that must be shown once, editing produces nothing to show. Two components
 * would be two copies of the same validation.
 *
 * A site here is DOMAIN-NEUTRAL. It is a location with terminals at it -- an
 * office, a school, a warehouse, a depot, a venue. Nothing in this form assumes
 * what the company uses the platform for, and the placeholder text is chosen so
 * as not to imply one.
 */

interface Values extends Record<string, unknown> {
  name: string
  address: string
  timezone: string
}

function validate(values: Values) {
  return {
    name:
      validators.required(values.name, 'Site name') ??
      validators.maxLength(values.name, 100, 'Site name'),
  }
}

export function SiteFormDialog({
  open,
  site,
  onClose,
}: {
  open: boolean
  /** Absent to create; present to edit. */
  site?: Site
  onClose: () => void
}) {
  const editing = Boolean(site)
  const notifications = useNotifications()
  const create = useCreateSite()
  const update = useUpdateSite(site?.id ?? '')

  // The credential lives HERE and nowhere else, for the life of this panel.
  // Never persisted, never cached, never in a URL.
  const [created, setCreated] = useState<CreateSiteResponse | null>(null)

  const form = useForm<Values>({
    initialValues: {
      name: site?.name ?? '',
      address: site?.address ?? '',
      timezone: site?.timezone ?? '',
    },
    validate,
    onSubmit: async (values) => {
      if (site) {
        await update.mutateAsync({
          name: values.name.trim(),
          address: values.address.trim(),
          timezone: values.timezone.trim() || undefined,
        })
        notifications.success(`${values.name.trim()} updated`)
        onClose()
        return
      }

      const result = await create.mutateAsync({
        name: values.name.trim(),
        address: values.address.trim() || undefined,
        timezone: values.timezone.trim() || undefined,
      })
      // Do NOT close: the key is on screen and closing would discard it.
      setCreated(result)
    },
  })

  function dismissCredential() {
    // Drop the credential from this component AND from the mutation's own
    // `data`, which would otherwise keep it alive for as long as the hook is
    // mounted. Both, or "gone" is only half true.
    setCreated(null)
    create.reset()
    form.reset({ name: '', address: '', timezone: '' })
    onClose()
  }

  const submitError = form.submitError
  const conflict = submitError instanceof ApiError && submitError.status === 409

  return (
    <Dialog
      open={open}
      title={
        created
          ? 'Site created'
          : editing
            ? `Edit ${site?.name}`
            : 'Add a site'
      }
      // While a key is on screen the dialog cannot be dismissed by Escape or a
      // backdrop click -- both are ways it gets closed by accident, and here
      // that loses a credential that cannot be recovered.
      dismissible={!created && !form.submitting}
      onClose={created ? dismissCredential : onClose}
      size={created ? 'wide' : 'default'}
      description={
        created
          ? undefined
          : editing
            ? 'A site is a location with terminals at it.'
            : 'A site is a location with terminals at it — an office, a depot, a campus, a venue.'
      }
    >
      {created ? (
        <CredentialPanel
          credential={created.credential}
          siteName={created.site.name}
          context="created"
          onDismiss={dismissCredential}
        />
      ) : (
        <form
          className="form"
          onSubmit={(event) => void form.handleSubmit(event)}
          noValidate
        >
          <TextField
            label="Site name"
            required
            value={form.values.name}
            error={form.errors.name}
            onChange={(value) => form.setValue('name', value)}
            onBlur={() => form.touch('name')}
            hint="Unique within your company. A retired site's name can be reused."
            disabled={form.submitting}
          />

          <TextField
            label="Address"
            value={form.values.address}
            onChange={(value) => form.setValue('address', value)}
            onBlur={() => form.touch('address')}
            hint="Optional. Where somebody would go to find the hardware."
            disabled={form.submitting}
          />

          <TextField
            label="Time zone"
            value={form.values.timezone}
            onChange={(value) => form.setValue('timezone', value)}
            onBlur={() => form.touch('timezone')}
            placeholder="UTC"
            hint="IANA zone where the hardware stands, e.g. Europe/Lisbon. Defaults to UTC. This does not change how times are shown to you — those follow your own browser."
            disabled={form.submitting}
          />

          <FormError
            message={
              conflict
                ? 'A site with that name already exists in your company.'
                : submitErrorMessage(submitError)
            }
            requestId={submitError instanceof ApiError ? submitError.requestId : null}
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
                ? editing
                  ? 'Saving…'
                  : 'Creating…'
                : editing
                  ? 'Save changes'
                  : 'Create site'}
            </button>
          </FormActions>

          {!editing ? (
            <p className="field__hint">
              Creating a site generates its provisioning key. You will see it once.
            </p>
          ) : null}
        </form>
      )}
    </Dialog>
  )
}
