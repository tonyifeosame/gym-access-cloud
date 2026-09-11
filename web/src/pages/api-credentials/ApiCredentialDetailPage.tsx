import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import type { APICredential, APICredentialUsageDay } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useAPICredential, useAPICredentialUsage } from '../../data/console'
import { RevokeCredentialDialog, RotateCredentialDialog } from './CredentialLifecycleDialogs'
import {
  CredentialStatusBadge,
  ScopeChips,
  isLive,
  statusExplanation,
  statusLabel,
} from './credentialVocabulary'

const USAGE_DAYS = 30

/**
 * One integration credential.
 *
 * EVERYTHING BUT THE SECRET. The server has no read path that returns one, and
 * this page says so on a retired credential rather than leaving an operator to
 * look for a "show key" control that does not exist: the remedy for a lost
 * secret is rotation (on a live credential) or a new credential (otherwise).
 *
 * THE LIFECYCLE CONTROLS ARE ONLY OFFERED WHERE THEY MEAN SOMETHING. Rotating
 * or revoking a credential that is already revoked or expired is a 409 from
 * the server; the buttons are absent and the status card explains why.
 */
export function ApiCredentialDetailPage() {
  const { credentialId } = useParams<{ credentialId: string }>()
  const query = useAPICredential(credentialId)
  const [rotating, setRotating] = useState(false)
  const [revoking, setRevoking] = useState(false)

  const breadcrumb = <Link to="/settings/api-credentials">API access</Link>

  if (query.isPending) return <LoadingState label="Loading credential…" />

  if (query.isError) {
    const error = query.error
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader title="Credential not found" breadcrumb={breadcrumb} />
          <InfoNote title="Nothing here">
            No integration credential with that id exists in your company.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Credential" breadcrumb={breadcrumb} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const credential = query.data
  const live = isLive(credential)

  return (
    <div className="page">
      <PageHeader
        title={credential.name}
        breadcrumb={breadcrumb}
        lead={<span className="mono">{credential.key_prefix}…</span>}
        actions={
          live ? (
            <button type="button" className="button" onClick={() => setRotating(true)}>
              Rotate
            </button>
          ) : null
        }
      />

      {!live ? (
        <InfoNote tone="warning" title={`This credential is ${statusLabel(credential.status).toLowerCase()}`}>
          <p>{statusExplanation(credential.status)} The public API refuses it.</p>
          <p>
            Its secret cannot be shown or recovered. If the integration still needs
            access, <Link to="/settings/api-credentials">issue a new credential</Link>.
          </p>
        </InfoNote>
      ) : null}

      <section className="cards" aria-label="Summary">
        <article className="card">
          <h2 className="card__title">Status</h2>
          <p className="card__value">
            <CredentialStatusBadge status={credential.status} />
          </p>
          <p className="card__detail">
            {credential.last_used_at ? (
              <>
                last used <Timestamp value={credential.last_used_at} relative />
                {credential.last_used_ip ? (
                  <>
                    {' '}
                    from <span className="mono">{credential.last_used_ip}</span>
                  </>
                ) : null}
              </>
            ) : (
              'never used'
            )}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Scopes</h2>
          <div className="card__value">
            <ScopeChips scopes={credential.scopes} />
          </div>
          <p className="card__detail">
            {credential.environment === 'live' ? 'Live environment' : 'Test environment'}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Issued</h2>
          <p className="card__value">
            <Timestamp value={credential.created_at} dateOnly />
          </p>
          <p className="card__detail">
            {credential.created_by_email ? (
              <>
                by <span className="mono">{credential.created_by_email}</span>
              </>
            ) : (
              'issuer not recorded'
            )}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Expires</h2>
          <p className="card__value">
            {credential.expires_at ? (
              <Timestamp value={credential.expires_at} dateOnly />
            ) : (
              <span className="muted">Never</span>
            )}
          </p>
          <p className="card__detail">
            An expired credential cannot be extended; issue a new one.
          </p>
        </article>
      </section>

      {credential.status === 'IN_GRACE' || credential.superseded_at ? (
        <InfoNote title="Rotated">
          <p>
            Rotated <Timestamp value={credential.superseded_at} relative />
            {credential.grace_expires_at ? (
              <>
                ; this key is accepted until{' '}
                <Timestamp value={credential.grace_expires_at} />
              </>
            ) : null}
            .
          </p>
          {credential.superseded_by ? (
            <p>
              Replaced by{' '}
              <Link to={`/settings/api-credentials/${credential.superseded_by}`}>
                the new credential
              </Link>
              .
            </p>
          ) : null}
        </InfoNote>
      ) : null}

      {credential.status === 'REVOKED' ? (
        <InfoNote title="Revocation">
          Revoked <Timestamp value={credential.revoked_at} relative />
          {credential.revoked_by_email ? (
            <>
              {' '}
              by <span className="mono">{credential.revoked_by_email}</span>
            </>
          ) : null}
          {credential.revoked_reason ? <> — {credential.revoked_reason}</> : null}.
        </InfoNote>
      ) : null}

      <SiteAccess credential={credential} />

      <UsagePanel credentialId={credential.id} />

      {live ? (
        <section className="panel panel--danger" aria-labelledby="credential-admin-heading">
          <div className="panel__header">
            <h2 className="panel__title" id="credential-admin-heading">
              Turn off
            </h2>
            <p className="field__hint">
              Revoking stops this key for good. To replace it with a new secret and keep
              the integration working, use Rotate above instead.
            </p>
          </div>
          <div className="danger-actions">
            <button type="button" className="button button--danger" onClick={() => setRevoking(true)}>
              Revoke
            </button>
          </div>
        </section>
      ) : null}

      {rotating ? (
        <RotateCredentialDialog open credential={credential} onClose={() => setRotating(false)} />
      ) : null}
      {revoking ? (
        <RevokeCredentialDialog
          open
          credential={credential}
          onClose={() => setRevoking(false)}
          onRevoked={() => setRevoking(false)}
        />
      ) : null}
    </div>
  )
}

function SiteAccess({ credential }: { credential: APICredential }) {
  const grants = credential.sites
  const unrestricted = credential.all_sites || grants.length === 0
  return (
    <section className="panel" aria-labelledby="credential-sites-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="credential-sites-heading">
          Site access
        </h2>
      </div>
      {unrestricted ? (
        <p>
          Not restricted — this credential reads <strong>all sites</strong> in your company.
          That is what an empty set of site restrictions means.
        </p>
      ) : (
        <>
          <p>
            Restricted to {grants.length} site{grants.length === 1 ? '' : 's'}:
          </p>
          <ul className="chip-list">
            {grants.map((grant) => (
              <li key={grant.site_id}>
                <Link to={`/sites/${grant.site_id}`} className="chip">
                  {grant.site_name}
                </Link>
              </li>
            ))}
          </ul>
          <p className="field__hint">
            Site restrictions bound what the credential can read about sites. Members are
            company-wide and are not narrowed by them.
          </p>
        </>
      )}
    </section>
  )
}

/**
 * Per-day counts from the credential's usage rollup.
 *
 * `requests` is what was served and `refusals` what the rate limiter turned
 * away; they are disjoint, and a key that is being refused constantly is as
 * useful a thing to see as one that is busy. A table, not a chart: thirty rows
 * of two integers is something a table already does well.
 */
function UsagePanel({ credentialId }: { credentialId: string }) {
  const usage = useAPICredentialUsage(credentialId, USAGE_DAYS)

  const columns: Column<APICredentialUsageDay>[] = [
    { id: 'day', header: 'Day (UTC)', primary: true, render: (row) => <span className="mono">{row.day}</span> },
    { id: 'class', header: 'Class', render: (row) => row.class },
    { id: 'requests', header: 'Served', align: 'end', render: (row) => row.requests.toLocaleString() },
    { id: 'refusals', header: 'Rate limited', align: 'end', render: (row) => row.refusals.toLocaleString() },
  ]

  return (
    <section className="panel" aria-labelledby="credential-usage-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="credential-usage-heading">
          Usage
        </h2>
        <p className="field__hint">
          The last {USAGE_DAYS} days. Counts are written a minute or so after the requests
          they describe.
        </p>
      </div>
      <DataTable<APICredentialUsageDay>
        caption={`Requests per day, last ${USAGE_DAYS} days`}
        columns={columns}
        rows={usage.data?.days}
        rowKey={(row) => `${row.day}:${row.class}`}
        isLoading={usage.isPending}
        isFetching={usage.isFetching}
        error={usage.isError ? usage.error : null}
        onRetry={() => void usage.refetch()}
        emptyTitle="No use recorded"
        emptyDescription={`Nothing has presented this credential in the last ${USAGE_DAYS} days.`}
      />
    </section>
  )
}
