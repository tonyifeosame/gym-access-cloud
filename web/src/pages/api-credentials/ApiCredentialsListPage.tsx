import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import type { APICredential } from '../../api/types'
import { DataTable, type Column } from '../../components/DataTable'
import { PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useAPICredentials } from '../../data/console'
import { CredentialStatusBadge, ScopeChips, isLive } from './credentialVocabulary'
import { IssueCredentialDialog } from './IssueCredentialDialog'

/**
 * The company's integration credentials -- the keys its own systems present to
 * the public API.
 *
 * NOT OPERATORS, AND NOT SITE KEYS. An operator signs in here; a site key
 * registers terminals; an integration credential reads the roster from
 * outside. The page says so in its lead because the three are easy to conflate
 * and the consequences of confusing them are not symmetrical.
 *
 * REVOKED AND EXPIRED ROWS ARE HIDDEN BY DEFAULT and a toggle shows them. The
 * server returns them on purpose -- "what integrations has this company ever
 * had" is the review question -- but the everyday question is "which keys are
 * live", and a list where half the rows are dead answers that badly.
 *
 * THIS PAGE NEVER RENDERS A SECRET. The list carries key prefixes only; the
 * secret exists in the issue dialog for as long as its panel is open.
 */
export function ApiCredentialsListPage() {
  const navigate = useNavigate()
  const query = useAPICredentials()
  const [issuing, setIssuing] = useState(false)
  const [showRetired, setShowRetired] = useState(false)

  const all = query.data?.credentials ?? []
  const retired = all.filter((credential) => !isLive(credential)).length
  const rows = showRetired ? all : all.filter(isLive)

  const columns: Column<APICredential>[] = [
    {
      id: 'name',
      header: 'Name',
      primary: true,
      render: (credential) => (
        <Link to={`/settings/api-credentials/${credential.id}`} className="table__link">
          {credential.name}
        </Link>
      ),
    },
    {
      id: 'prefix',
      header: 'Key',
      render: (credential) => <span className="mono">{credential.key_prefix}…</span>,
    },
    {
      id: 'status',
      header: 'Status',
      render: (credential) => <CredentialStatusBadge status={credential.status} />,
    },
    {
      id: 'scopes',
      header: 'Scopes',
      render: (credential) => <ScopeChips scopes={credential.scopes} />,
    },
    {
      id: 'sites',
      header: 'Site access',
      // The same rule and the same words as the operators list: an EMPTY grant
      // set is every site, never none, and the server's all_sites flag is what
      // says so rather than the length of the list.
      render: (credential) => {
        if (credential.all_sites || credential.sites.length === 0) {
          return (
            <span>
              All sites <span className="muted">(not restricted)</span>
            </span>
          )
        }
        const count = credential.sites.length
        return (
          <span>
            {count} site{count === 1 ? '' : 's'}
          </span>
        )
      },
    },
    {
      id: 'last_used',
      header: 'Last used',
      secondary: true,
      render: (credential) =>
        credential.last_used_at ? (
          <Timestamp value={credential.last_used_at} relative />
        ) : (
          <span className="muted">Never</span>
        ),
    },
    {
      id: 'created_by',
      header: 'Issued by',
      secondary: true,
      render: (credential) =>
        credential.created_by_email ? (
          <span className="mono">{credential.created_by_email}</span>
        ) : (
          <span className="muted">—</span>
        ),
    },
  ]

  return (
    <div className="page">
      <PageHeader
        title="API access"
        lead="Keys your own systems present to the AccessLink public API. Not operator accounts, and not site provisioning keys."
        actions={
          <button type="button" className="button button--primary" onClick={() => setIssuing(true)}>
            Issue credential
          </button>
        }
      />

      <div className="toolbar">
        <label className="checkbox toolbar__toggle">
          <input
            type="checkbox"
            className="checkbox__input"
            checked={showRetired}
            onChange={(event) => setShowRetired(event.target.checked)}
          />
          <span className="checkbox__label">
            Show revoked and expired
            {retired > 0 ? ` (${retired})` : ''}
          </span>
        </label>
        <RefreshingIndicator active={query.isFetching && !query.isPending} />
      </div>

      <DataTable<APICredential>
        caption="Integration credentials"
        columns={columns}
        rows={query.isPending ? undefined : rows}
        rowKey={(credential) => credential.id}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        error={query.isError ? query.error : null}
        onRetry={() => void query.refetch()}
        onRowClick={(credential) => navigate(`/settings/api-credentials/${credential.id}`)}
        emptyTitle={
          all.length > 0 && !showRetired ? 'No live credentials' : 'No integration credentials'
        }
        emptyDescription={
          all.length > 0 && !showRetired
            ? 'Every credential this company has issued is revoked or expired. Show them above, or issue a new one.'
            : 'Nothing outside AccessLink can read this company’s data yet. Issue a credential to let your own systems in.'
        }
      />

      <p className="field__hint">
        A credential’s secret is shown once, when it is issued or rotated, and cannot
        be shown again. Operators who sign in here are managed under{' '}
        <Link to="/operators">Operators</Link>; the keys terminals register with
        belong to each <Link to="/sites">site</Link>.
      </p>

      {issuing ? <IssueCredentialDialog open onClose={() => setIssuing(false)} /> : null}
    </div>
  )
}
