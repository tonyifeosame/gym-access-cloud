import { useState } from 'react'
import { Link } from 'react-router-dom'

import { ApiError } from '../../api/client'
import * as endpoints from '../../api/endpoints'
import { can } from '../../auth/permissions'
import { roleLabel } from '../../auth/roles'
import { Badge } from '../../components/Badge'
import { FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useCompany } from '../../data/console'
import { formatDuration } from '../../format/datetime'
import { useAuthenticatedSession, useSession } from '../../session/useSession'

const MIN_PASSWORD_LENGTH = 12

/**
 * Settings.
 *
 * FIVE SCOPES EXIST AND THEY ARE NOT INTERCHANGEABLE. Mixing them is how an
 * operator changes something company-wide believing it applied to one site:
 *
 *   Account      this operator: their password, their session.       HERE
 *   Company      the tenant: name, slug, contact.                    HERE (read only)
 *   Site         one location's device configuration.                Sites → a site
 *   Application  one capability's configuration.                     Applications
 *   Terminal     one device's assignment.                            Terminals → a terminal
 *
 * This page owns the first two and POINTS AT the rest rather than duplicating
 * them. A second place to edit site settings would be a second place for them to
 * disagree.
 */
export function SettingsPage() {
  const session = useAuthenticatedSession()
  const { session: maybeSession } = useSession()
  const company = useCompany()

  return (
    <div className="page">
      <PageHeader
        title="Settings"
        lead="Your account, your company, and where everything else is configured."
      />

      {/* --- account -------------------------------------------------------- */}
      <section className="panel" aria-labelledby="settings-account-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="settings-account-heading">
            Your account
          </h2>
          <p className="field__hint">Applies to you only, on every device you sign in from.</p>
        </div>

        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Name</dt>
            <dd>{session.operator.full_name}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Email</dt>
            <dd className="mono">{session.operator.email}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Role</dt>
            <dd>
              <Badge>{roleLabel(session.role)}</Badge>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Site access</dt>
            <dd>
              {session.all_sites
                ? 'Every site in this company'
                : session.sites.length === 0
                  ? 'No sites'
                  : session.sites.map((grant) => grant.site_name).join(', ')}
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>This session expires</dt>
            <dd>
              <Timestamp value={session.session_expires_at} />{' '}
              <span className="muted">
                (in {formatDuration(session.session_expires_in_seconds)})
              </span>
            </dd>
          </div>
        </dl>

        {/*
          REPHRASED FROM A PROHIBITION TO A ROUTE.

          This said "Your name, email and role are set by an administrator" and
          then "You cannot change them yourself" — and it was one of two notices
          on this page whose headline was a refusal. Between them they made the
          settings screen read as a list of things the customer is not allowed to
          do.

          The RULE IS UNCHANGED and so is the reason for it, which is worth
          keeping: an account that could raise its own role would be no boundary
          at all. What changed is that the sentence now starts with who to ask.
        */}
        <p className="field__hint">
          To change your name, email or role, ask an administrator or owner in your
          company. An account cannot raise its own permissions — that is what makes
          the role mean anything.
        </p>
      </section>

      <ChangePasswordSection />

      {/* --- company -------------------------------------------------------- */}
      <section className="panel" aria-labelledby="settings-company-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="settings-company-heading">
            Company
          </h2>
          <p className="field__hint">Applies to everyone in {session.company.name}.</p>
        </div>

        {company.isPending ? (
          <LoadingState label="Loading company…" />
        ) : company.isError ? (
          <ErrorState error={company.error} onRetry={() => void company.refetch()} />
        ) : (
          <dl className="detail-list">
            <div className="detail-list__row">
              <dt>Name</dt>
              <dd>{company.data.name}</dd>
            </div>
            <div className="detail-list__row">
              {/*
                RENAMED AND EXPLAINED. "Identifier" with a monospaced string
                under it told a customer nothing about what it was for, so it
                read as something they were supposed to understand and did not.
                It is the short name support asks for, which is the only thing
                anybody outside this codebase uses it for.
              */}
              <dt>Reference</dt>
              <dd>
                <span className="mono">{company.data.slug}</span>{' '}
                <span className="muted">— quote this if you contact support</span>
              </dd>
            </div>
            {company.data.contact_email ? (
              <div className="detail-list__row">
                <dt>Contact</dt>
                <dd className="mono">{company.data.contact_email}</dd>
              </div>
            ) : null}
            <div className="detail-list__row">
              <dt>Status</dt>
              <dd>
                <Badge tone={company.data.active ? 'positive' : 'danger'}>
                  {company.data.active ? 'Active' : 'Inactive'}
                </Badge>
              </dd>
            </div>
            <div className="detail-list__row">
              <dt>Created</dt>
              <dd>
                {/*
                  NO TIME OF DAY. The value is whatever moment the row was
                  written, read back in the viewer's zone — so a midnight-UTC
                  creation rendered as "1:00 AM" one zone east, a precise-looking
                  hour describing nothing anybody did. The full instant stays in
                  the element's `datetime` and `title`.
                */}
                <Timestamp value={company.data.created_at} dateOnly />
              </dd>
            </div>
          </dl>
        )}

        {/*
          THE SUBSTANCE IS UNCHANGED AND MUST BE: nobody can edit these, whatever
          their role, because AccessLink offers no way to. Saying which kind of
          limit it is stops an owner hunting for a colleague with a higher role
          who also cannot do it.

          WHAT CHANGED IS THE VOCABULARY. "AccessLink has no operator API for
          changing a company's name" describes our system to somebody who does
          not have one, and "API" is the word that gives it away. A customer does
          not need to know what we did not build — only that the route is
          support, and that a bigger role would not help.
        */}
        <p className="field__hint">
          These cannot be changed from the console by anyone, whatever their role.
          Contact support to have your company&apos;s name or contact address updated.
        </p>
      </section>

      {/* --- everything else ------------------------------------------------ */}
      {/*
        SHORTER, AND EACH LINK NOW SAYS WHERE IT GOES.

        "Site settings" and "Terminal settings" both led to a LIST — /sites and
        /terminals — so the label promised a settings screen and delivered an
        index. The blurbs disclosed the extra step in their last sentence;
        the labels now do it in the first word.

        The blurbs are also shorter. Every destination here is already a
        top-level navigation item, so this section's job is to answer "which of
        those holds the thing I am looking for", not to describe each screen
        again. One clause each is enough to choose by.
      */}
      <section className="panel" aria-labelledby="settings-elsewhere-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="settings-elsewhere-heading">
            Configured elsewhere
          </h2>
          <p className="field__hint">
            Each of these belongs to the thing it configures, so it is edited there.
          </p>
        </div>

        <ul className="settings-links">
          <li>
            <Link to="/sites">Manage site settings</Link>
            <span>
              Relay hold time, sync interval, and what a location&apos;s terminals do
              during a network outage. Open a site to change them.
            </span>
          </li>
          <li>
            <Link to="/terminals">Manage terminal settings</Link>
            <span>
              Which feature a terminal runs. Open a terminal to change it.
            </span>
          </li>
          {can(maybeSession, 'configureApplications') || can(maybeSession, 'manageOperators') ? (
            <li>
              {/*
                GATED ON THE ROLES THAT ACTUALLY GOVERN THE DESTINATION. This was
                `manageOperators` alone, which is ADMIN and happened to match the
                page's ADMIN route gate -- correct by coincidence rather than by
                meaning. Reading is ADMIN and writing is OWNER, so both are named.
              */}
              <Link to="/settings/applications">Manage features</Link>
              <span>Which features your company has turned on.</span>
            </li>
          ) : null}
          {can(maybeSession, 'manageOperators') ? (
            <li>
              <Link to="/operators">Manage operators</Link>
              <span>Who can sign in, and what each of them may do.</span>
            </li>
          ) : null}
          {can(maybeSession, 'manageAPICredentials') ? (
            <li>
              <Link to="/settings/api-credentials">Manage API access</Link>
              <span>
                Keys that let your own systems read your roster and sites through the
                public API.
              </span>
            </li>
          ) : null}
        </ul>
      </section>
    </div>
  )
}

/**
 * Changing your own password.
 *
 * SELF-SERVICE, AND IT KEEPS THE SESSION YOU ARE USING. The server revokes every
 * OTHER session and spares this one, so an operator changing their password is
 * not immediately logged out of the screen they did it from — while anybody
 * holding a session on another device is. That is the useful behaviour and it is
 * worth stating, because the opposite would be the natural assumption.
 */
function ChangePasswordSection() {
  const notifications = useNotifications()
  const [done, setDone] = useState(false)

  interface Values extends Record<string, unknown> {
    current: string
    next: string
    confirm: string
  }

  const form = useForm<Values>({
    initialValues: { current: '', next: '', confirm: '' },
    validate: (values) => ({
      current: validators.required(values.current, 'Current password'),
      next:
        validators.required(values.next, 'New password') ??
        validators.minLength(values.next, MIN_PASSWORD_LENGTH, 'New password'),
      confirm:
        values.confirm !== values.next ? 'The two new passwords do not match.' : undefined,
    }),
    onSubmit: async (values) => {
      await endpoints.changePassword(values.current, values.next)
      notifications.success('Your password has been changed.')
      setDone(true)
      // Cleared immediately: there is no reason for any of the three to stay in
      // component state once the request has succeeded.
      form.reset({ current: '', next: '', confirm: '' })
    },
  })

  const error = form.submitError
  // 401 here means the CURRENT password was wrong, not that the session ended.
  const wrongPassword = error instanceof ApiError && error.status === 401
  const rateLimited = error instanceof ApiError && error.status === 429

  return (
    <section className="panel" aria-labelledby="settings-password-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="settings-password-heading">
          Change your password
        </h2>
      </div>

      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Current password"
          type="password"
          required
          autoComplete="current-password"
          value={form.values.current}
          error={form.errors.current}
          onChange={(value) => form.setValue('current', value)}
          onBlur={() => form.touch('current')}
          disabled={form.submitting}
        />
        <TextField
          label="New password"
          type="password"
          required
          autoComplete="new-password"
          value={form.values.next}
          error={form.errors.next}
          onChange={(value) => form.setValue('next', value)}
          onBlur={() => form.touch('next')}
          disabled={form.submitting}
          hint={`At least ${MIN_PASSWORD_LENGTH} characters.`}
        />
        <TextField
          label="Confirm new password"
          type="password"
          required
          autoComplete="new-password"
          value={form.values.confirm}
          error={form.errors.confirm}
          onChange={(value) => form.setValue('confirm', value)}
          onBlur={() => form.touch('confirm')}
          disabled={form.submitting}
        />

        <FormError
          message={
            wrongPassword
              ? 'That is not your current password.'
              : rateLimited
                ? 'Too many attempts. Wait a moment and try again.'
                : submitErrorMessage(error)
          }
          requestId={error instanceof ApiError ? error.requestId : null}
        />

        <FormActions>
          <button type="submit" className="button button--primary" disabled={form.submitting}>
            {form.submitting ? 'Changing…' : 'Change password'}
          </button>
        </FormActions>

        <p className="field__hint">
          You stay signed in here. Any other device where you are signed in is signed
          out.
        </p>

        {done ? (
          <p className="field__hint" role="status">
            Password changed.
          </p>
        ) : null}
      </form>
    </section>
  )
}
