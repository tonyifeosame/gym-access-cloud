import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { Schedule, ScheduleWindow } from '../../api/types'
import { EVERY_DAY } from '../../api/types'
import { can } from '../../auth/permissions'
import { Badge } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { CheckboxGroup, FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCreateSchedule, useDeleteSchedule, useSchedules, useUpdateSchedule } from '../../data/console'
import { useSession } from '../../session/useSession'
import { DAYS, crossesMidnight, describeDays, describeWindow, maskOf, shortTime } from './accessVocabulary'

/**
 * Named, reusable time windows.
 *
 * A schedule is not access — it is a shape that access rules refer to. That
 * indirection is the point: "Office hours" is defined once and every rule using
 * it moves together, which is what makes a rota change one edit rather than
 * fifty.
 *
 * It is also what makes editing one dangerous in a way editing a rule is not.
 * Widening a window widens every rule that uses it, at once, everywhere — so the
 * count of dependent rules is shown before an edit and deletion is refused
 * outright while any rule still points at it.
 */
export function SchedulesPage() {
  const { session } = useSession()
  const schedules = useSchedules()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Schedule | null>(null)
  const [deleting, setDeleting] = useState<Schedule | null>(null)

  const mayManage = can(session, 'manageAccess')

  return (
    <div className="page">
      <PageHeader
        title="Schedules"
        lead="When an access rule applies. Defined once, used by any number of rules."
        actions={
          mayManage ? (
            <button
              type="button"
              className="button button--primary"
              onClick={() => setCreating(true)}
            >
              New schedule
            </button>
          ) : null
        }
      />

      <InfoNote title="A schedule on its own admits nobody">
        Schedules do not grant access. They narrow a rule that already does — a
        person with no rules reaches nothing, whatever schedules exist. Grant
        access on a person&apos;s record, then choose a schedule there.
      </InfoNote>

      {schedules.isPending ? <LoadingState label="Loading schedules…" /> : null}
      {schedules.isError ? (
        <ErrorState error={schedules.error} onRetry={() => void schedules.refetch()} />
      ) : null}

      {schedules.data ? (
        schedules.data.schedules.length === 0 ? (
          <InfoNote title="No schedules yet">
            Every access rule currently applies at any time of day. Create a
            schedule if some of them should only apply during particular hours.
          </InfoNote>
        ) : (
          <ul className="rule-list">
            {schedules.data.schedules.map((schedule) => (
              <li key={schedule.id} className="rule">
                <div className="rule__main">
                  <h2 className="rule__title">
                    {schedule.name}
                    {!schedule.active ? <Badge tone="warning">Inactive</Badge> : null}
                    {/*
                      What this schedule COSTS to change, stated before anybody
                      opens the editor. Widening a window widens every rule
                      using it, everywhere, at once.
                    */}
                    <Badge>
                      {schedule.permission_count} rule
                      {schedule.permission_count === 1 ? '' : 's'}
                    </Badge>
                  </h2>

                  {schedule.description ? (
                    <p className="rule__detail">{schedule.description}</p>
                  ) : null}

                  <ul className="window-list">
                    {schedule.windows.map((window, index) => (
                      <li key={index} className="window">
                        {describeWindow(window)}
                      </li>
                    ))}
                  </ul>

                  <p className="rule__detail">
                    {schedule.timezone ? (
                      <>
                        Times are in <code className="mono">{schedule.timezone}</code>
                      </>
                    ) : (
                      // The default, and the one that is right for a company
                      // whose sites are in different zones.
                      <span className="muted">
                        Times are in each terminal&apos;s own site timezone
                      </span>
                    )}
                  </p>
                </div>

                {mayManage ? (
                  <div className="rule__actions">
                    <button
                      type="button"
                      className="button"
                      onClick={() => setEditing(schedule)}
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      className="button button--quiet"
                      onClick={() => setDeleting(schedule)}
                    >
                      Delete
                    </button>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        )
      ) : null}

      {creating ? <ScheduleDialog open onClose={() => setCreating(false)} /> : null}
      {editing ? (
        <ScheduleDialog open schedule={editing} onClose={() => setEditing(null)} />
      ) : null}
      {deleting ? (
        <DeleteScheduleDialog open schedule={deleting} onClose={() => setDeleting(null)} />
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Create / edit
// ---------------------------------------------------------------------------

interface ScheduleValues extends Record<string, unknown> {
  name: string
  description: string
  timezone: string
}

/**
 * Creating or editing a schedule.
 *
 * WINDOWS ARE EDITED AS A LIST, not as one row, because the shapes that matter
 * need several: weekdays 08:00–18:00 plus Saturday 09:00–13:00 is two windows
 * and one idea.
 *
 * A WINDOW ENDING BEFORE IT STARTS IS VALID AND MEANS "CROSSES MIDNIGHT". The
 * form says so where it happens rather than refusing it: a 22:00–06:00 night
 * shift is one window whose day mask names the day it STARTS on, and rejecting
 * it would make night shifts inexpressible while splitting it in two would make
 * Sunday night's shift look like Monday's.
 */
function ScheduleDialog({
  open,
  schedule,
  onClose,
}: {
  open: boolean
  schedule?: Schedule
  onClose: () => void
}) {
  const create = useCreateSchedule()
  const update = useUpdateSchedule(schedule?.id ?? '')
  const notifications = useNotifications()

  const [windows, setWindows] = useState<ScheduleWindow[]>(
    schedule?.windows.length
      ? schedule.windows.map((window) => ({
          ...window,
          start_time: shortTime(window.start_time),
          end_time: shortTime(window.end_time),
        }))
      : [{ days_of_week: EVERY_DAY, start_time: '09:00', end_time: '17:00' }],
  )
  const [windowError, setWindowError] = useState<string | null>(null)

  const editing = Boolean(schedule)
  const mutation = editing ? update : create

  const form = useForm<ScheduleValues>({
    initialValues: {
      name: schedule?.name ?? '',
      description: schedule?.description ?? '',
      timezone: schedule?.timezone ?? '',
    },
    validate: (values) => ({ name: validators.required(values.name, 'A name') }),
    onSubmit: async (values) => {
      // Checked here rather than by the server alone, because the server's
      // message names a constraint and this names the row.
      const empty = windows.findIndex((window) => window.days_of_week === 0)
      if (empty !== -1) {
        setWindowError(`Window ${empty + 1} has no days selected, so it would never apply.`)
        return
      }
      if (windows.length === 0) {
        setWindowError('A schedule needs at least one window.')
        return
      }
      setWindowError(null)

      const body = {
        name: values.name.trim(),
        description: values.description.trim(),
        timezone: values.timezone.trim(),
        windows,
      }

      if (editing) {
        await update.mutateAsync(body)
        notifications.success(`${body.name} updated`)
      } else {
        await create.mutateAsync(body)
        notifications.success(`${body.name} created`)
      }
      onClose()
    },
  })

  function updateWindow(index: number, patch: Partial<ScheduleWindow>) {
    setWindows((current) =>
      current.map((window, position) => (position === index ? { ...window, ...patch } : window)),
    )
    setWindowError(null)
  }

  const dependents = schedule?.permission_count ?? 0
  const error = form.submitError

  return (
    <Dialog
      open={open}
      title={editing ? `Edit ${schedule?.name}` : 'New schedule'}
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
          hint="What an operator will choose it by. “Office hours”, “Night shift”, “Contractor window”."
        />

        <TextField
          label="Description"
          value={form.values.description}
          onChange={(value) => form.setValue('description', value)}
          disabled={form.submitting}
          hint="Optional."
        />

        <TextField
          label="Timezone"
          value={form.values.timezone}
          onChange={(value) => form.setValue('timezone', value)}
          disabled={form.submitting}
          placeholder="Africa/Lagos"
          hint="Leave blank to use each terminal's own site timezone, which is what a company with sites in several zones wants. Set one only for a single shift pattern that must be the same everywhere."
        />

        <fieldset className="fieldset">
          <legend className="fieldset__legend">Windows</legend>

          {windows.map((window, index) => (
            <div key={index} className="window-editor">
              <CheckboxGroup
                legend={`Days for window ${index + 1}`}
                options={DAYS.map((day) => ({ value: String(day.bit), label: day.label }))}
                selected={DAYS.filter((day) => (window.days_of_week & day.bit) !== 0).map((day) =>
                  String(day.bit),
                )}
                onChange={(selected) =>
                  updateWindow(index, { days_of_week: maskOf(selected.map(Number)) })
                }
                disabled={form.submitting}
              />

              <div className="filter-grid">
                <TextField
                  label={`Start (window ${index + 1})`}
                  value={window.start_time}
                  onChange={(value) => updateWindow(index, { start_time: value })}
                  disabled={form.submitting}
                  placeholder="09:00"
                />
                <TextField
                  label={`End (window ${index + 1})`}
                  value={window.end_time}
                  onChange={(value) => updateWindow(index, { end_time: value })}
                  disabled={form.submitting}
                  placeholder="17:00"
                />
              </div>

              <p className="field__hint">
                {describeDays(window.days_of_week)}, {shortTime(window.start_time)}–
                {shortTime(window.end_time)}.
                {crossesMidnight(window) ? (
                  <>
                    {' '}
                    <strong>This window crosses midnight</strong> and runs into the
                    following day. The days above are the days it STARTS on.
                  </>
                ) : null}
              </p>

              {windows.length > 1 ? (
                <button
                  type="button"
                  className="button button--quiet button--small"
                  onClick={() => setWindows((current) => current.filter((_, p) => p !== index))}
                  disabled={form.submitting}
                >
                  Remove window {index + 1}
                </button>
              ) : null}
            </div>
          ))}

          <button
            type="button"
            className="button"
            disabled={form.submitting}
            onClick={() =>
              setWindows((current) => [
                ...current,
                { days_of_week: EVERY_DAY, start_time: '09:00', end_time: '17:00' },
              ])
            }
          >
            Add another window
          </button>

          {windowError ? (
            <p className="field__error" role="alert">
              {windowError}
            </p>
          ) : null}
        </fieldset>

        {editing && dependents > 0 ? (
          <InfoNote tone="warning" title="This changes every rule that uses it">
            {dependents} access rule{dependents === 1 ? '' : 's'} refer
            {dependents === 1 ? 's' : ''} to this schedule. Widening a window
            widens all of them at once, everywhere.
          </InfoNote>
        ) : null}

        <FormError
          message={submitErrorMessage(error)}
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
          <button type="submit" className="button button--primary" disabled={mutation.isPending}>
            {form.submitting ? 'Saving…' : editing ? 'Save schedule' : 'Create schedule'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

/**
 * Deleting a schedule.
 *
 * REFUSED BY THE SERVER while any rule references it, and that refusal is
 * correct rather than an inconvenience: the foreign key would set the column to
 * NULL, which silently WIDENS every rule that used it — a permission restricted
 * to office hours becomes one with no time restriction at all. There is no
 * cascade that could be safe here.
 *
 * The console does not pre-empt it with a disabled button, because the count can
 * be stale. It shows the count, lets the attempt happen, and turns the 409 into
 * an instruction rather than a failure.
 */
function DeleteScheduleDialog({
  open,
  schedule,
  onClose,
}: {
  open: boolean
  schedule: Schedule
  onClose: () => void
}) {
  const remove = useDeleteSchedule()
  const notifications = useNotifications()
  const inUse = schedule.permission_count > 0

  return (
    <ConfirmDialog
      open={open}
      title={`Delete ${schedule.name}?`}
      consequence={
        inUse ? (
          <>
            {schedule.permission_count} access rule
            {schedule.permission_count === 1 ? '' : 's'} still use this schedule,
            so this will be <strong>refused</strong>.
          </>
        ) : (
          <>No access rule uses this schedule, so nothing loses its time restriction.</>
        )
      }
      detail={
        inUse ? (
          <>
            Deleting it cannot simply remove the reference: a rule restricted to
            office hours would silently become one with{' '}
            <strong>no time restriction at all</strong>. Change or remove those
            rules first, or point them at another schedule.
          </>
        ) : undefined
      }
      confirmLabel="Delete schedule"
      onConfirm={async () => {
        await remove.mutateAsync(schedule.id)
        notifications.success(`${schedule.name} deleted`)
      }}
      onClose={onClose}
    />
  )
}
