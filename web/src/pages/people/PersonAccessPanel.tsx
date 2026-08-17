import { useState } from 'react'
import { Link } from 'react-router-dom'

import { ApiError } from '../../api/client'
import type { Permission, PermissionEffect, PermissionScope } from '../../api/types'
import { can } from '../../auth/permissions'
import { Badge } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, SelectField, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import {
  useGrantPermission,
  usePersonPermissions,
  useRevokePermission,
  useSchedules,
  useSites,
  useTerminals,
} from '../../data/console'
import { useSession } from '../../session/useSession'
import {
  describeScope,
  describeWindow,
  EFFECT_LABELS,
  SCOPE_DESCRIPTIONS,
  SCOPE_LABELS,
  STANDING_LABELS,
  standingOf,
} from '../access/accessVocabulary'

/**
 * Who this person may reach, and when.
 *
 * THE ONE SENTENCE THIS PANEL EXISTS TO MAKE UNMISTAKABLE:
 *
 *   Absence of permission is not permission.
 *
 * A person with no rules reaches nothing. That is the opposite of what the
 * platform did before the engine existed — nothing read the permissions table,
 * so every active person with a credential opened every terminal in their
 * company, permanently — and it is the assumption an operator carries in from
 * every other product like this one. Getting it backwards ADMITS people rather
 * than locking them out, which is the failure nobody reports.
 *
 * So the empty state says it in words rather than showing an empty table, and a
 * person who genuinely has no access is described as having none rather than as
 * having nothing configured.
 *
 * WHAT THIS PANEL DOES NOT DO: decide anything. Whether a rule permits this
 * minute depends on a schedule in a timezone, evaluated by the engine. The
 * console shows what the rules ARE and offers the terminal page's preview for
 * what they WOULD DECIDE — a second implementation in the browser would
 * eventually disagree with the door, and the door is the one that matters.
 */
export function PersonAccessPanel({ externalId }: { externalId: string }) {
  const { session } = useSession()
  const permissions = usePersonPermissions(externalId)
  const [granting, setGranting] = useState(false)
  const [revoking, setRevoking] = useState<Permission | null>(null)

  const mayManage = can(session, 'manageAccess')

  return (
    <section className="panel" aria-labelledby="person-access-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="person-access-heading">
          Access
        </h2>
        <p className="field__hint">
          Where this person may go, and when. A rule with no schedule applies at
          any time; a <strong>deny</strong> beats every allow.
        </p>
      </div>

      {permissions.isPending ? <LoadingState label="Loading access rules…" /> : null}

      {permissions.isError ? (
        <ErrorState error={permissions.error} onRetry={() => void permissions.refetch()} />
      ) : null}

      {permissions.data ? (
        permissions.data.permissions.length === 0 ? (
          /*
            NOT an empty table. "No rules" and "reaches nothing" are the same
            fact, and only one of them is obvious.
          */
          <InfoNote tone="warning" title="This person cannot get in anywhere">
            No rule grants them access. On this platform, having no rules means
            reaching <strong>nothing</strong> — it is not an unconfigured state
            that defaults to open.
            {mayManage ? ' Grant access below to change that.' : null}
          </InfoNote>
        ) : (
          <ul className="rule-list">
            {permissions.data.permissions.map((permission) => {
              const standing = standingOf(permission)
              return (
                <li
                  key={permission.id}
                  className="rule"
                  data-effect={permission.effect}
                  data-standing={standing}
                >
                  <div className="rule__main">
                    <h3 className="rule__title">
                      <Badge tone={permission.effect === 'DENY' ? 'danger' : 'positive'}>
                        {EFFECT_LABELS[permission.effect]}
                      </Badge>{' '}
                      {describeScope(permission)}
                      {/*
                        A rule that is expired, not yet valid or switched off is
                        marked. All three look identical in a list showing only
                        dates, and an operator asking "why can she not get in" is
                        looking at exactly this.
                      */}
                      {standing !== 'IN_FORCE' ? (
                        <Badge tone="warning">{STANDING_LABELS[standing]}</Badge>
                      ) : null}
                    </h3>

                    <p className="rule__detail">
                      <span className="audit__role">{SCOPE_LABELS[permission.scope_type]}</span>
                      {permission.application ? (
                        <> · only for {permission.application}</>
                      ) : (
                        <> · any capability that terminal serves</>
                      )}
                    </p>

                    <p className="rule__detail">
                      {permission.schedule_name ? (
                        <>
                          Schedule: <strong>{permission.schedule_name}</strong>
                        </>
                      ) : (
                        <span className="muted">No schedule — applies at any time of day</span>
                      )}
                    </p>

                    {permission.starts_at || permission.ends_at ? (
                      <p className="rule__detail">
                        Valid{' '}
                        {permission.starts_at ? (
                          <>
                            from <Timestamp value={permission.starts_at} />
                          </>
                        ) : (
                          'from any time'
                        )}{' '}
                        {permission.ends_at ? (
                          <>
                            until <Timestamp value={permission.ends_at} />
                          </>
                        ) : (
                          'with no end date'
                        )}
                      </p>
                    ) : null}
                  </div>

                  {mayManage ? (
                    /*
                      "Remove rule", NOT "Remove". The page header carries a
                      Remove that deletes the PERSON from the platform and tells
                      every terminal to forget them. Two controls with the same
                      label doing things that different is how somebody deletes
                      a colleague meaning to withdraw one door — and it also
                      leaves a screen reader announcing several identical
                      buttons with no way to tell them apart.
                    */
                    <button
                      type="button"
                      className="button button--quiet"
                      onClick={() => setRevoking(permission)}
                    >
                      Remove rule
                    </button>
                  ) : null}
                </li>
              )
            })}
          </ul>
        )
      ) : null}

      {mayManage ? (
        <FormActions>
          <button
            type="button"
            className="button button--primary"
            onClick={() => setGranting(true)}
          >
            Grant access
          </button>
        </FormActions>
      ) : (
        <p className="field__hint">
          Changing who may go where is a manager action. You can read the rules.
        </p>
      )}

      {granting ? (
        <GrantAccessDialog
          open
          externalId={externalId}
          onClose={() => setGranting(false)}
        />
      ) : null}

      {revoking ? (
        <RevokeAccessDialog
          open
          externalId={externalId}
          permission={revoking}
          onClose={() => setRevoking(null)}
        />
      ) : null}
    </section>
  )
}

// ---------------------------------------------------------------------------
// Grant
// ---------------------------------------------------------------------------

interface GrantValues extends Record<string, unknown> {
  scope_type: PermissionScope
  site_id: string
  device_serial: string
  effect: PermissionEffect
  schedule_id: string
  application: string
  starts_at: string
  ends_at: string
}

/**
 * Granting — or denying — at one scope.
 *
 * THE SCOPE CHOICE IS THE CONSEQUENTIAL ONE and its description says what it
 * actually reaches. "Everywhere" includes terminals that do not exist yet:
 * somebody granting company-wide access today is granting it to whatever is
 * installed next year, and that is worth knowing at the point of choosing rather
 * than discovering afterwards.
 *
 * DENY IS OFFERED HERE rather than hidden, because exclusion cannot be expressed
 * with allow-only rules without enumerating everybody who is not excluded and
 * re-enumerating them whenever anyone joins. It is the second option and it says
 * that it wins over every allow.
 */
function GrantAccessDialog({
  open,
  externalId,
  onClose,
}: {
  open: boolean
  externalId: string
  onClose: () => void
}) {
  const grant = useGrantPermission(externalId)
  const sites = useSites()
  const terminals = useTerminals()
  const schedules = useSchedules()
  const { session } = useSession()
  const notifications = useNotifications()

  const form = useForm<GrantValues>({
    initialValues: {
      scope_type: 'SITE',
      site_id: '',
      device_serial: '',
      effect: 'ALLOW',
      schedule_id: '',
      application: '',
      starts_at: '',
      ends_at: '',
    },
    validate: (values) => ({
      site_id:
        values.scope_type === 'SITE'
          ? validators.required(values.site_id, 'A site')
          : undefined,
      device_serial:
        values.scope_type === 'TERMINAL'
          ? validators.required(values.device_serial, 'A terminal')
          : undefined,
      ends_at:
        values.starts_at && values.ends_at && values.ends_at <= values.starts_at
          ? 'The end date must be after the start date.'
          : undefined,
    }),
    onSubmit: async (values) => {
      await grant.mutateAsync({
        scope_type: values.scope_type,
        // Sent only for the scope that takes them. The server refuses a COMPANY
        // scope that names a site, and rightly — a rule that says two things
        // about where it applies is a rule nobody can read.
        site_id: values.scope_type === 'SITE' ? values.site_id : undefined,
        device_serial: values.scope_type === 'TERMINAL' ? values.device_serial : undefined,
        effect: values.effect,
        schedule_id: values.schedule_id || undefined,
        application: values.application || undefined,
        starts_at: values.starts_at ? new Date(`${values.starts_at}T00:00:00`).toISOString() : undefined,
        // The END of the chosen day. A bare date would mean midnight at its
        // start, which ends the rule a day early — and the person is refused on
        // the last day they were meant to have.
        ends_at: values.ends_at ? new Date(`${values.ends_at}T23:59:59.999`).toISOString() : undefined,
      })
      notifications.success(
        values.effect === 'DENY' ? 'Denial added' : 'Access granted',
      )
      onClose()
    },
  })

  const scope = form.values.scope_type
  const error = form.submitError

  return (
    <Dialog
      open={open}
      title="Grant access"
      description="One rule, at one scope. Add more rules for more places."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <SelectField
          label="Where"
          required
          value={scope}
          onChange={(value) => form.setValue('scope_type', value as PermissionScope)}
          disabled={form.submitting}
          options={(['TERMINAL', 'SITE', 'COMPANY'] as PermissionScope[]).map((value) => ({
            value,
            label: SCOPE_LABELS[value],
            description: SCOPE_DESCRIPTIONS[value],
          }))}
        />

        {scope === 'SITE' ? (
          <SelectField
            label="Site"
            required
            placeholder="Choose a site…"
            value={form.values.site_id}
            error={form.errors.site_id}
            onChange={(value) => form.setValue('site_id', value)}
            onBlur={() => form.touch('site_id')}
            disabled={form.submitting}
            options={(sites.data?.sites ?? []).map((site) => ({
              value: site.id,
              label: site.name,
              description: site.active ? undefined : 'Deactivated — no terminal there is working',
            }))}
          />
        ) : null}

        {scope === 'TERMINAL' ? (
          <SelectField
            label="Terminal"
            required
            placeholder="Choose a terminal…"
            value={form.values.device_serial}
            error={form.errors.device_serial}
            onChange={(value) => form.setValue('device_serial', value)}
            onBlur={() => form.touch('device_serial')}
            disabled={form.submitting}
            options={(terminals.data?.terminals ?? []).map((terminal) => ({
              value: terminal.serial_number,
              label: `${terminal.device_name || terminal.serial_number} — ${terminal.site_name}`,
            }))}
          />
        ) : null}

        <SelectField
          label="Effect"
          required
          value={form.values.effect}
          onChange={(value) => form.setValue('effect', value as PermissionEffect)}
          disabled={form.submitting}
          options={[
            { value: 'ALLOW', label: 'Allow', description: 'Let them in here.' },
            {
              value: 'DENY',
              label: 'Deny',
              description:
                'Keep them out here. A deny beats EVERY allow at every scope, including a company-wide grant.',
            },
          ]}
        />

        <SelectField
          label="When"
          placeholder="Any time"
          value={form.values.schedule_id}
          onChange={(value) => form.setValue('schedule_id', value)}
          disabled={form.submitting}
          hint={
            <>
              Leave blank for any time of day. Schedules are shared and reusable —
              manage them under <Link to="/access/schedules">Schedules</Link>.
            </>
          }
          options={(schedules.data?.schedules ?? []).map((schedule) => ({
            value: schedule.id,
            label: schedule.name,
            description: schedule.windows.map(describeWindow).join(' · '),
          }))}
        />

        <SelectField
          label="Only for one capability"
          placeholder="Any capability the terminal serves"
          value={form.values.application}
          onChange={(value) => form.setValue('application', value)}
          disabled={form.submitting}
          hint="Blank means this rule applies to whatever the terminal is doing, which is what most rules want."
          options={(session?.applications ?? []).map((application) => ({
            value: application.code,
            label: application.code,
          }))}
        />

        <div className="filter-grid">
          <TextField
            label="Valid from"
            type="date"
            value={form.values.starts_at}
            onChange={(value) => form.setValue('starts_at', value)}
            disabled={form.submitting}
            hint="Optional. Blank means immediately."
          />
          <TextField
            label="Valid until"
            type="date"
            value={form.values.ends_at}
            error={form.errors.ends_at}
            onChange={(value) => form.setValue('ends_at', value)}
            onBlur={() => form.touch('ends_at')}
            disabled={form.submitting}
            hint="Optional, and INCLUSIVE — the rule lasts to the end of this day."
          />
        </div>

        {scope === 'COMPANY' ? (
          <InfoNote tone="warning" title="This covers terminals that do not exist yet">
            A company-wide rule applies to every terminal you have and every one
            installed later. That is often what somebody wants for staff, and
            rarely what they want for a visitor.
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
          <button type="submit" className="button button--primary" disabled={form.submitting}>
            {form.submitting
              ? 'Saving…'
              : form.values.effect === 'DENY'
                ? 'Add denial'
                : 'Grant access'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

function RevokeAccessDialog({
  open,
  externalId,
  permission,
  onClose,
}: {
  open: boolean
  externalId: string
  permission: Permission
  onClose: () => void
}) {
  const revoke = useRevokePermission(externalId)
  const notifications = useNotifications()

  const denial = permission.effect === 'DENY'

  return (
    <ConfirmDialog
      open={open}
      title={denial ? 'Remove this denial?' : 'Remove this access?'}
      consequence={
        denial ? (
          <>
            The rule keeping this person out of{' '}
            <strong>{describeScope(permission)}</strong> is removed. If another
            rule allows them there, they will be admitted from the next sync.
          </>
        ) : (
          <>
            This person loses access to <strong>{describeScope(permission)}</strong>.
            If no other rule covers that place, they will be refused.
          </>
        )
      }
      detail={
        <>
          Terminals learn about this on their next sync rather than instantly, and
          a terminal that is offline keeps its cached answer until it reconnects —
          bounded by its site&apos;s offline policy.
        </>
      }
      confirmLabel={denial ? 'Remove denial' : 'Remove access'}
      onConfirm={async () => {
        await revoke.mutateAsync(permission.id)
        notifications.success(denial ? 'Denial removed' : 'Access removed')
      }}
      onClose={onClose}
    />
  )
}
