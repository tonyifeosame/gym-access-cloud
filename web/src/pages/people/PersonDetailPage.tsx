import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { can } from '../../auth/permissions'
import { BiometricBadge } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useDeletePerson, useEnrollment, usePerson, useUpdatePerson } from '../../data/console'
import { useSession } from '../../session/useSession'
import { describeEnrollment } from './enrollment'
import { EnrollFingerprintDialog } from './EnrollFingerprintDialog'
import { describeCategory } from './categories'
import { PERSON_ID_LABEL } from './personVocabulary'
import { PersonAccessPanel } from './PersonAccessPanel'
import { PersonFormDialog } from './PersonFormDialog'

/**
 * One person.
 *
 * THE CREDENTIAL SECTION IS THE PART TO BE CAREFUL WITH. What the API exposes is
 * a single boolean — whether a biometric credential exists — and that is
 * deliberately the whole of it. No template, no locator, no sensor model, no
 * vendor. The console must stay usable if the platform later supports different
 * hardware, or a credential that is not a fingerprint at all, so nothing here
 * names one.
 *
 * THE CONSOLE NOW STARTS AN ENROLMENT, AND STILL DOES NOT PERFORM ONE. Those
 * are different things and the distinction is the whole design. Capturing a
 * fingerprint happens at a terminal with the person physically present; what
 * this page does is ask ONE named terminal to do it, and then report what that
 * terminal said. No biometric data reaches this screen in either direction.
 *
 * Before this, there was no operator route at all: binding a finger meant a
 * technician at the terminal's serial console, which is a bench tool and not a
 * workflow for the person running a gym.
 */
export function PersonDetailPage() {
  const { externalId } = useParams<{ externalId: string }>()
  const navigate = useNavigate()
  const { session } = useSession()
  const notifications = useNotifications()

  const query = usePerson(externalId)
  const update = useUpdatePerson(externalId ?? '')
  const remove = useDeletePerson()

  const [editing, setEditing] = useState(false)
  const [changingActive, setChangingActive] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [enrolling, setEnrolling] = useState(false)

  const mayManage = can(session, 'managePeople')

  // The enrolment is read on the page as well as in the dialog, so the
  // credential panel can say WHICH terminal captured the fingerprint and when —
  // and so a live enrolment is visible without opening anything. The hook stops
  // polling by itself once the enrolment reaches a terminal state.
  const enrollmentQuery = useEnrollment(externalId)
  const latest = enrollmentQuery.data?.enrollment ?? null
  const described = latest ? describeEnrollment(latest) : null

  if (query.isPending) return <LoadingState label="Loading person…" />

  if (query.isError) {
    const error = query.error
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader title="Person not found" breadcrumb={<Link to="/people">People</Link>} />
          <InfoNote title="Nothing here">
            No person with that {PERSON_ID_LABEL} exists in your company. They may have
            been removed.
          </InfoNote>
        </div>
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="page">
          <PageHeader title="Person" breadcrumb={<Link to="/people">People</Link>} />
          <InfoNote title="Not available to you">
            Your role does not include viewing this record.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Person" breadcrumb={<Link to="/people">People</Link>} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const person = query.data

  return (
    <div className="page">
      <PageHeader
        title={person.full_name || person.external_id}
        breadcrumb={<Link to="/people">People</Link>}
        lead={<code className="mono">{person.external_id}</code>}
        actions={
          mayManage ? (
            <>
              <button type="button" className="button" onClick={() => setEditing(true)}>
                Edit
              </button>
              <button
                type="button"
                className="button"
                onClick={() => setChangingActive(true)}
              >
                {person.active ? 'Deactivate' : 'Activate'}
              </button>
              <button
                type="button"
                className="button button--danger"
                onClick={() => setDeleting(true)}
              >
                Remove
              </button>
            </>
          ) : null
        }
      />

      {!person.active ? (
        <InfoNote tone="warning" title="This person is inactive">
          Terminals have been told to stop admitting them. The record is kept, and
          reactivating restores access on the next sync.
        </InfoNote>
      ) : null}

      {/*
        NO "STATUS" CARD. An inactive person already gets the banner above,
        which says what inactive MEANS rather than just naming it, and an active
        one needs no card to say nothing is wrong. The card restated the banner
        a centimetre below it and, on a phone, spent a whole card on one word.
        The badge still appears against this person in the People list.
      */}
      <section className="cards" aria-label="Summary">
        <article className="card">
          <h2 className="card__title">Person type</h2>
          <p className="card__value">
            {describeCategory(person.category) || <span className="muted">Not set</span>}
          </p>
          <p className="card__detail">as your organisation classifies them</p>
        </article>

        <article className="card">
          <h2 className="card__title">Added</h2>
          <p className="card__value">
            <Timestamp value={person.created_at} />
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Last updated</h2>
          <p className="card__value">
            <Timestamp value={person.updated_at} relative />
          </p>
        </article>
      </section>

      {/*
        ACCESS BEFORE CREDENTIAL, deliberately. "Where may this person go" is the
        question somebody opens this page to answer, and the credential is the
        mechanism by which they prove who they are once they are permitted. A
        person with a credential and no rules reaches nothing, and putting the
        credential first would suggest the opposite.
      */}
      <PersonAccessPanel externalId={person.external_id} />

      {/* --- credential ----------------------------------------------------- */}
      <section className="panel" aria-labelledby="person-credential-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="person-credential-heading">
            Biometric credential
          </h2>
          {mayManage ? (
            <button type="button" className="button" onClick={() => setEnrolling(true)}>
              {person.biometric_enrolled ? 'Re-enrol fingerprint' : 'Enrol fingerprint'}
            </button>
          ) : null}
        </div>

        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Credential status</dt>
            <dd>
              {/*
                The entire biometric surface: a boolean. Whatever the hardware
                stores stays on the platform's side of the boundary, so this
                screen keeps working if the sensor, the vendor or the credential
                type ever changes.

                READ FROM THE ENROLMENT WHEN THERE IS ONE. The enrolment response
                carries the credential state precisely so a screen watching a
                capture never has to reconcile two sources: the poll that learns
                a terminal has bound a finger is the same response this reads, so
                the badge cannot say "Not enrolled" for a refresh interval while
                the panel above it says the enrolment succeeded.
              */}
              <BiometricBadge
                enrolled={
                  enrollmentQuery.data?.biometric_enrolled ?? person.biometric_enrolled
                }
              />
            </dd>
          </div>

          {/*
            WHICH TERMINAL, AND WHEN. The answer to the support call this whole
            feature exists to prevent: a person who is enrolled, is active, and
            does nothing at one particular door. A fingerprint lives on the
            sensor that captured it, so knowing which one that was is the
            difference between "re-enrol them here" and an afternoon of guessing.
          */}
          {latest && latest.status === 'COMPLETED' ? (
            <div className="detail-list__row">
              <dt>Enrolled at</dt>
              <dd>
                {latest.terminal_name || latest.terminal_serial}
                {latest.site_name ? <> · {latest.site_name}</> : null}
                {latest.completed_at ? (
                  <>
                    {' '}
                    <Timestamp value={latest.completed_at} relative />
                  </>
                ) : null}
              </dd>
            </div>
          ) : null}

          {latest && described && described.live ? (
            <div className="detail-list__row">
              <dt>Enrolment in progress</dt>
              <dd>
                {described.headline}.{' '}
                <button
                  type="button"
                  className="button button--small"
                  onClick={() => setEnrolling(true)}
                >
                  Open
                </button>
              </dd>
            </div>
          ) : null}

          {latest && described && !described.live && latest.status !== 'COMPLETED' ? (
            <div className="detail-list__row">
              <dt>Last enrolment attempt</dt>
              <dd>
                {described.label}
                {latest.terminal_serial ? <> at {latest.terminal_name || latest.terminal_serial}</> : null}
                {latest.error_message ? <> — {latest.error_message}</> : null}
              </dd>
            </div>
          ) : null}
        </dl>

        {/*
          WHAT THIS NOTE NO LONGER SAYS. It used to end "...there is currently no
          operator API to start, review or clear an enrolment", which is a
          statement about our API surface on a screen a customer administers
          their members from — and it had also gone stale, since the platform
          now has a read endpoint for exactly that.

          THE SECURITY SENTENCE STAYS, deliberately. That the console holds no
          copy of the biometric data is a property of the product worth telling
          a customer, and it is the reason this page can only show a yes or no.
        */}
        <InfoNote title="Enrolment happens at a terminal">
          The person has to be standing at the terminal you choose — that is where
          their fingerprint is captured and where it stays. AccessLink holds no copy
          of the biometric data, so a fingerprint enrolled at one terminal works at
          that terminal.
        </InfoNote>
      </section>

      {editing ? (
        <PersonFormDialog open person={person} onClose={() => setEditing(false)} />
      ) : null}

      {enrolling ? (
        <EnrollFingerprintDialog open person={person} onClose={() => setEnrolling(false)} />
      ) : null}

      {changingActive ? (
        <ConfirmDialog
          open
          tone={person.active ? 'danger' : 'default'}
          title={
            person.active
              ? `Deactivate ${person.full_name || person.external_id}?`
              : `Activate ${person.full_name || person.external_id}?`
          }
          consequence={
            person.active ? (
              <>
                Every terminal in your company will be told to stop admitting them.
                The record and any enrolled credential are kept.
              </>
            ) : (
              <>
                Every terminal in your company will be told to admit them again.
              </>
            )
          }
          detail={
            person.active
              ? 'Reversible — you can activate them again from this same screen. Terminals apply the change on their next sync, so it is not instant on hardware that is currently offline.'
              : undefined
          }
          confirmLabel={person.active ? 'Deactivate' : 'Activate'}
          onConfirm={async () => {
            await update.mutateAsync({
              full_name: person.full_name,
              category: person.category,
              active: !person.active,
            })
            notifications.success(
              person.active
                ? `${person.full_name || person.external_id} deactivated`
                : `${person.full_name || person.external_id} activated`,
            )
          }}
          onClose={() => setChangingActive(false)}
        />
      ) : null}

      {deleting ? (
        <ConfirmDialog
          open
          title={`Remove ${person.full_name || person.external_id}?`}
          // THE SYNC CONSEQUENCE IS THE POINT. Removing a person is not tidying
          // a table: it enqueues a DELETE to every terminal in the company, and
          // that job is the only mechanism by which an offline terminal ever
          // learns to forget a credential it already holds.
          consequence={
            <>
              Every terminal in your company will be told to <strong>forget this
              person and their credential</strong>. Terminals that are offline apply
              it when they next reconnect.
            </>
          }
          detail={
            <>
              The record is kept for audit and their {PERSON_ID_LABEL}{' '}
              <code>{person.external_id}</code> becomes available again, but they
              disappear from the console and cannot be restored from here. If you
              only need to stop admitting them,{' '}
              <strong>deactivate them instead</strong> — that is reversible and keeps
              their enrolled credential.
            </>
          }
          confirmPhrase={person.external_id}
          confirmLabel="Remove person"
          onConfirm={async () => {
            await remove.mutateAsync(person.external_id)
            notifications.success(
              `${person.full_name || person.external_id} removed. Terminals will forget them on their next sync.`,
            )
            navigate('/people', { replace: true })
          }}
          onClose={() => setDeleting(false)}
        />
      ) : null}
    </div>
  )
}
