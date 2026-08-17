import { ApiError } from '../../api/client'
import type { Person } from '../../api/types'
import { CheckboxField, FormActions, FormError, TextField } from '../../components/Form'
import { Dialog } from '../../components/Dialog'
import { useNotifications } from '../../components/Notifications'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCreatePerson, useUpdatePerson } from '../../data/console'

/**
 * Creating and editing a person.
 *
 * A PERSON IS DOMAIN-NEUTRAL. Depending on what a company has enabled, the same
 * record is an employee, a student, a contractor, a visitor, an attendee or a
 * customer. Nothing in this form names one of those, and the copy is written so
 * that a school, a factory and a conference all read it as describing what they
 * are doing.
 *
 * THERE IS NO CREDENTIAL FIELD IN EITHER DIRECTION, and there must never be one.
 * Enrolment happens at a terminal, with the person present; the console does not
 * write credentials and the API does not accept them here. `biometric_enrolled`
 * is a read-only fact reported back.
 *
 * `external_id` IS IMMUTABLE AFTER CREATION. It is the identifier terminals hold
 * and sync against, so the API addresses a person by it and offers no way to
 * change it. The field is therefore disabled when editing rather than absent —
 * an operator needs to see the id they are editing.
 */

interface Values extends Record<string, unknown> {
  external_id: string
  full_name: string
  category: string
  active: boolean
}

export function PersonFormDialog({
  open,
  person,
  onClose,
  onCreated,
}: {
  open: boolean
  /** Absent to create; present to edit. */
  person?: Person
  onClose: () => void
  onCreated?: (person: Person) => void
}) {
  const editing = Boolean(person)
  const notifications = useNotifications()
  const create = useCreatePerson()
  const update = useUpdatePerson(person?.external_id ?? '')

  const form = useForm<Values>({
    initialValues: {
      external_id: person?.external_id ?? '',
      full_name: person?.full_name ?? '',
      category: person?.category ?? '',
      active: person?.active ?? true,
    },
    validate: (values) => ({
      external_id: editing
        ? undefined
        : validators.required(values.external_id, 'Identifier') ??
          validators.maxLength(values.external_id, 50, 'Identifier'),
      full_name:
        validators.required(values.full_name, 'Full name') ??
        validators.maxLength(values.full_name, 100, 'Full name'),
    }),
    onSubmit: async (values) => {
      if (person) {
        await update.mutateAsync({
          full_name: values.full_name.trim(),
          category: values.category.trim() || undefined,
          active: values.active,
        })
        notifications.success(`${values.full_name.trim()} updated`)
        onClose()
        return
      }

      const created = await create.mutateAsync({
        external_id: values.external_id.trim(),
        full_name: values.full_name.trim(),
        category: values.category.trim() || undefined,
        active: values.active,
      })
      notifications.success(`${created.full_name} added`)
      onCreated?.(created)
      onClose()
    },
  })

  const error = form.submitError
  const conflict = error instanceof ApiError && error.status === 409

  return (
    <Dialog
      open={open}
      title={editing ? `Edit ${person?.full_name}` : 'Add a person'}
      description={
        editing
          ? undefined
          : 'Someone this platform should recognise. What that means depends on what your company uses AccessLink for.'
      }
      dismissible={!form.submitting}
      onClose={onClose}
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Identifier"
          required={!editing}
          value={form.values.external_id}
          error={form.errors.external_id}
          onChange={(value) => form.setValue('external_id', value)}
          onBlur={() => form.touch('external_id')}
          // Immutable after creation: terminals hold this and sync against it,
          // so the API addresses a person by it and cannot change it.
          disabled={editing || form.submitting}
          mono
          hint={
            editing
              ? 'Cannot be changed — terminals identify this person by it.'
              : 'The badge, staff, student or reference number your organisation already uses. Unique within your company.'
          }
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
          label="Person type"
          value={form.values.category}
          onChange={(value) => form.setValue('category', value)}
          onBlur={() => form.touch('category')}
          disabled={form.submitting}
          // FREE TEXT, DELIBERATELY. The platform has no opinion about how a
          // company classifies people: staff and contractor for one customer,
          // student and visitor for another, nothing at all for a third. A fixed
          // list here would be the product deciding what business you are in.
          hint="Optional, and free text — whatever classification your organisation uses. Leave blank if you do not need one."
        />

        <CheckboxField
          label="Active"
          checked={form.values.active}
          onChange={(checked) => form.setValue('active', checked)}
          disabled={form.submitting}
          hint="An inactive person is kept on file, and terminals are told to stop admitting them."
        />

        <FormError
          message={
            conflict
              ? 'Someone with that identifier already exists in your company.'
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
            {form.submitting ? 'Saving…' : editing ? 'Save changes' : 'Add person'}
          </button>
        </FormActions>

        {!editing ? (
          <p className="field__hint">
            Adding someone sends them to every terminal in your company. A biometric
            credential is enrolled separately, at a terminal.
          </p>
        ) : null}
      </form>
    </Dialog>
  )
}
