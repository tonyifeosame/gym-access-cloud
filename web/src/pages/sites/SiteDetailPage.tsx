import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { can } from '../../auth/permissions'
import { ActiveBadge } from '../../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useSite } from '../../data/console'
import { useSession } from '../../session/useSession'
import { SiteFormDialog } from './SiteFormDialog'
import {
  RetireSiteDialog,
  RotateSiteKeyDialog,
  SiteActivationDialog,
} from './SiteLifecycleDialogs'
import { SiteSettingsPanel } from './SiteSettingsPanel'

/**
 * One site: what it is, what state it is in, and what can be done to it.
 *
 * THE LIFECYCLE ACTIONS ARE ORDERED AND SEPARATED BY CONSEQUENCE — edit, then
 * rotate, then deactivate, then retire — with the irreversible one last and
 * visually apart. A row of equal-looking buttons is how somebody retires a live
 * location while meaning to rename it.
 */
export function SiteDetailPage() {
  const { siteId } = useParams<{ siteId: string }>()
  const navigate = useNavigate()
  const { session } = useSession()
  const query = useSite(siteId)

  const [editing, setEditing] = useState(false)
  const [rotating, setRotating] = useState(false)
  const [changingActivation, setChangingActivation] = useState(false)
  const [retiring, setRetiring] = useState(false)

  const mayManage = can(session, 'manageSites')

  if (query.isPending) return <LoadingState label="Loading site…" />

  if (query.isError) {
    const error = query.error
    // 404 covers both "no such site" and "another company's site" — the API
    // does not distinguish, deliberately, so neither does this.
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader
            title="Site not found"
            breadcrumb={<Link to="/sites">Sites</Link>}
          />
          <InfoNote title="Nothing here">
            This site does not exist, or it is not part of your company. It may also
            have been retired.
          </InfoNote>
        </div>
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="page">
          <PageHeader title="Site" breadcrumb={<Link to="/sites">Sites</Link>} />
          <InfoNote title="Not one of your sites">
            This site belongs to your company, but your account is not scoped to it.
            An administrator can grant you access.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Site" breadcrumb={<Link to="/sites">Sites</Link>} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const site = query.data

  return (
    <div className="page">
      <PageHeader
        title={site.name}
        breadcrumb={<Link to="/sites">Sites</Link>}
        lead={site.address || undefined}
        actions={
          mayManage ? (
            <>
              <button type="button" className="button" onClick={() => setEditing(true)}>
                Edit
              </button>
              <button type="button" className="button" onClick={() => setRotating(true)}>
                Rotate key
              </button>
              <button
                type="button"
                className="button"
                onClick={() => setChangingActivation(true)}
              >
                {site.active ? 'Deactivate' : 'Reactivate'}
              </button>
              {/* Apart from the rest, and the only one styled as destructive. */}
              <button
                type="button"
                className="button button--danger"
                onClick={() => setRetiring(true)}
              >
                Retire
              </button>
            </>
          ) : null
        }
      />

      {!site.active ? (
        <InfoNote tone="warning" title="This site is deactivated">
          Its provisioning key and all {site.terminal_count} terminal
          {site.terminal_count === 1 ? '' : 's'} are refused while it stays this way.
          Nothing has been deleted — reactivating restores service immediately.
        </InfoNote>
      ) : null}

      <section className="cards">
        <article className="card">
          <h2 className="card__title">Status</h2>
          <p className="card__value">
            <ActiveBadge active={site.active} />
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Terminals</h2>
          <p className="card__value">{site.terminal_count}</p>
          <p className="card__detail">installed at this site</p>
        </article>

        <article className="card">
          <h2 className="card__title">Time zone</h2>
          <p className="card__value">
            <code className="mono">{site.timezone}</code>
          </p>
          <p className="card__detail">where the hardware stands</p>
        </article>

        <article className="card">
          <h2 className="card__title">Provisioning key</h2>
          <p className="card__value">
            {site.api_key_prefix ? (
              <code className="mono">{site.api_key_prefix}…</code>
            ) : (
              <span className="muted">Not shown</span>
            )}
          </p>
          <p className="card__detail">
            {/*
              The full key is deliberately unreachable. It is stored as a hash
              and returned only when created or rotated; there is no endpoint
              that could show it here, which is the intended design rather than
              a missing feature.
            */}
            Shown once when issued. Rotate to replace it.
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Added</h2>
          <p className="card__value">
            <Timestamp value={site.created_at} />
          </p>
        </article>
      </section>

      <SiteSettingsPanel siteId={site.id} siteName={site.name} />

      {editing ? (
        <SiteFormDialog open site={site} onClose={() => setEditing(false)} />
      ) : null}
      {rotating ? (
        <RotateSiteKeyDialog open site={site} onClose={() => setRotating(false)} />
      ) : null}
      {changingActivation ? (
        <SiteActivationDialog
          open
          site={site}
          onClose={() => setChangingActivation(false)}
        />
      ) : null}
      {retiring ? (
        <RetireSiteDialog
          open
          site={site}
          onClose={() => setRetiring(false)}
          // The site this page is about no longer exists; staying here would
          // show a 404 where a detail view used to be.
          onRetired={() => navigate('/sites', { replace: true })}
        />
      ) : null}
    </div>
  )
}
