import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { ApiError } from '../api/client'
import { Badge } from '../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../components/states'
import { Timestamp } from '../components/Timestamp'
import {
  CompanyActivationDialog,
  EditCompanyDialog,
  FirstOperatorDialog,
} from './CompanyDialogs'
import { useCompany } from './data'

/**
 * One customer company, and the onboarding checklist for it.
 *
 * THE CHECKLIST IS THE POINT OF THIS PAGE. "Create a company" is one API call;
 * getting a customer to the state where they can actually use the product is
 * five or six, spread across two identities, and the failure mode is stopping
 * after the first one. A company with no operator looks fine in a list and is
 * completely unusable — nobody can sign in to it at all.
 *
 * So the steps are enumerated with their real state, and the ones this surface
 * CANNOT do are listed alongside the ones it can, rather than omitted. Sites,
 * terminals, people and applications are the customer's own work in their own
 * console: platform administration has no route to any of them, deliberately,
 * and an onboarding checklist that quietly stopped at the boundary would leave
 * whoever is following it believing the job was done.
 */
export function CompanyDetailPage() {
  const { companyId } = useParams<{ companyId: string }>()
  const query = useCompany(companyId)

  const [editing, setEditing] = useState(false)
  const [changingActive, setChangingActive] = useState(false)
  const [issuingOwner, setIssuingOwner] = useState(false)

  if (query.isPending) return <LoadingState label="Loading company…" />

  if (query.isError) {
    const error = query.error
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader
            title="Company not found"
            breadcrumb={<Link to="/platform">Companies</Link>}
          />
          <InfoNote title="Nothing here">
            No company with that id exists on this installation.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Company" breadcrumb={<Link to="/platform">Companies</Link>} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const company = query.data
  const hasOwner = company.operator_count > 0

  return (
    <div className="page">
      <PageHeader
        title={company.name}
        breadcrumb={<Link to="/platform">Companies</Link>}
        lead={<code className="mono">{company.slug}</code>}
        actions={
          <>
            <button type="button" className="button" onClick={() => setEditing(true)}>
              Edit details
            </button>
            <button
              type="button"
              className={company.active ? 'button button--danger' : 'button'}
              onClick={() => setChangingActive(true)}
            >
              {company.active ? 'Suspend' : 'Restore'}
            </button>
          </>
        }
      />

      {!company.active ? (
        <InfoNote tone="warning" title="This company is suspended">
          Every operator in {company.name} is signed out and cannot sign in. Their
          terminals are not authenticating. Nothing has been deleted — restoring
          the company brings all of it back exactly as it was.
        </InfoNote>
      ) : null}

      <section className="cards" aria-label="Summary">
        <article className="card">
          <h2 className="card__title">State</h2>
          <p className="card__value">
            {!company.active ? (
              <Badge tone="danger">Suspended</Badge>
            ) : hasOwner ? (
              <Badge tone="positive">Active</Badge>
            ) : (
              <Badge tone="warning">No operator yet</Badge>
            )}
          </p>
          <p className="card__detail">
            {hasOwner ? 'Somebody can sign in' : 'Nobody can sign in to this company'}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Operators</h2>
          <p className="card__value">{company.operator_count}</p>
          <p className="card__detail">accounts that can use their console</p>
        </article>

        <article className="card">
          <h2 className="card__title">Sites</h2>
          <p className="card__value">{company.site_count}</p>
          <p className="card__detail">{company.terminal_count} terminals</p>
        </article>

        <article className="card">
          <h2 className="card__title">People</h2>
          <p className="card__value">{company.person_count}</p>
          <p className="card__detail">on their roster</p>
        </article>
      </section>

      {/* --- onboarding ----------------------------------------------------- */}
      <section className="panel" aria-labelledby="onboarding-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="onboarding-heading">
            Onboarding
          </h2>
          <p className="field__hint">
            What a new customer needs before the product does anything for them.
          </p>
        </div>

        <ol className="checklist">
          <li className="checklist__item" data-done="true">
            <div className="checklist__text">
              <h3 className="checklist__title">Company created</h3>
              <p className="checklist__detail">
                Created <Timestamp value={company.created_at} />. Default timezone{' '}
                <code className="mono">{company.timezone}</code>.
              </p>
            </div>
            <Badge tone="positive">Done</Badge>
          </li>

          <li className="checklist__item" data-done={hasOwner}>
            <div className="checklist__text">
              <h3 className="checklist__title">First operator issued</h3>
              <p className="checklist__detail">
                {hasOwner
                  ? 'This company has an operator. Further accounts are created by them, from their own console — this surface cannot add another.'
                  : 'Nobody can sign in to this company yet. Issue an invitation for its owner; they set their own password from it, so you never learn their credential.'}
              </p>
            </div>
            {hasOwner ? (
              <Badge tone="positive">Done</Badge>
            ) : (
              <button
                type="button"
                className="button button--primary"
                onClick={() => setIssuingOwner(true)}
              >
                Issue an invitation
              </button>
            )}
          </li>

          {/*
            The rest is the CUSTOMER'S work, in their own console, and is listed
            so that whoever is following this checklist knows the job is not
            finished when the invitation is sent. Platform administration has no
            route to any of it -- by design, not by omission.
          */}
          <li className="checklist__item" data-done={company.site_count > 0}>
            <div className="checklist__text">
              <h3 className="checklist__title">Sites created</h3>
              <p className="checklist__detail">
                {company.site_count > 0
                  ? `${company.site_count} site${company.site_count === 1 ? '' : 's'}.`
                  : 'No sites yet.'}{' '}
                Their owner creates these in their own console. Each one mints a
                provisioning key, which is how their terminals are enrolled.
              </p>
            </div>
            <Badge>{company.site_count > 0 ? 'Their console' : 'Not started'}</Badge>
          </li>

          <li className="checklist__item" data-done={company.terminal_count > 0}>
            <div className="checklist__text">
              <h3 className="checklist__title">Terminals provisioned</h3>
              <p className="checklist__detail">
                {company.terminal_count > 0
                  ? `${company.terminal_count} terminal${company.terminal_count === 1 ? '' : 's'} registered.`
                  : 'No terminals yet.'}{' '}
                Hardware registers itself at the door using its site&apos;s
                provisioning key — neither console can do it on the device&apos;s
                behalf.
              </p>
            </div>
            <Badge>{company.terminal_count > 0 ? 'Their console' : 'Not started'}</Badge>
          </li>

          <li className="checklist__item" data-done={company.person_count > 0}>
            <div className="checklist__text">
              <h3 className="checklist__title">People and applications</h3>
              <p className="checklist__detail">
                {company.person_count > 0
                  ? `${company.person_count} people on their roster.`
                  : 'No people yet.'}{' '}
                Which capabilities the company uses is their owner&apos;s decision,
                made in their console. AccessLink has no opinion about what a
                deployment is for.
              </p>
            </div>
            <Badge>{company.person_count > 0 ? 'Their console' : 'Not started'}</Badge>
          </li>
        </ol>
      </section>

      {/* --- details -------------------------------------------------------- */}
      <section className="panel" aria-labelledby="company-details-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="company-details-heading">
            Details
          </h2>
        </div>

        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Slug</dt>
            <dd>
              <code className="mono">{company.slug}</code>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Contact</dt>
            <dd>{company.contact_email || <span className="muted">Not recorded</span>}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Default timezone</dt>
            <dd>
              <code className="mono">{company.timezone}</code>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Event retention</dt>
            {/* Null means keep for ever. Rendering it as a number nobody chose,
                or as 0, would read as a policy that had been set. */}
            <dd>
              {company.event_retention_days === null ? (
                <span className="muted">Indefinite</span>
              ) : (
                `${company.event_retention_days} days`
              )}
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Audit retention</dt>
            <dd>
              {company.audit_retention_days === null ? (
                <span className="muted">Indefinite</span>
              ) : (
                `${company.audit_retention_days} days`
              )}
            </dd>
          </div>
        </dl>
      </section>

      <InfoNote title="What is deliberately not on this page">
        There is no way from here to open this customer&apos;s people, credentials,
        events, terminals or site keys — no route exists, and none should. A
        support credential that could read every customer&apos;s roster would be the
        most valuable secret on this installation, and the safest way not to leak
        it is to be unable to load it.
      </InfoNote>

      {editing ? (
        <EditCompanyDialog open company={company} onClose={() => setEditing(false)} />
      ) : null}
      {changingActive ? (
        <CompanyActivationDialog
          open
          company={company}
          onClose={() => setChangingActive(false)}
        />
      ) : null}
      {issuingOwner ? (
        <FirstOperatorDialog open company={company} onClose={() => setIssuingOwner(false)} />
      ) : null}
    </div>
  )
}
