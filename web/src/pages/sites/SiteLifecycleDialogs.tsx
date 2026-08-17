import { useRef, useState } from 'react'

import type { RotateSiteKeyResponse, Site } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { useNotifications } from '../../components/Notifications'
import { useRetireSite, useRotateSiteKey, useUpdateSite } from '../../data/console'
import { CredentialPanel } from './CredentialPanel'

/**
 * The three destructive-ish site operations, each deliberately shaped
 * differently so they cannot be mistaken for one another.
 *
 * DEACTIVATION AND RETIREMENT ARE NOT VARIANTS OF THE SAME THING and the UI
 * must not let them look like it:
 *
 *   Deactivate  reversible. Stops the site key and every terminal there
 *               authenticating, destroys nothing, and the same dialog offers to
 *               undo it. This is "closed for a month".
 *
 *   Retire      permanent through the API, and it takes the terminals with it.
 *               Requires typing the site's name, and states the terminal count
 *               before it happens. This is "this location is gone".
 *
 * If those two ever collapse into one control with a checkbox, somebody will
 * decommission a live site by reflex.
 */

// ---------------------------------------------------------------------------
// Deactivate / reactivate
// ---------------------------------------------------------------------------

export function SiteActivationDialog({
  open,
  site,
  onClose,
}: {
  open: boolean
  site: Site
  onClose: () => void
}) {
  const update = useUpdateSite(site.id)
  const notifications = useNotifications()
  const deactivating = site.active

  return (
    <ConfirmDialog
      open={open}
      tone={deactivating ? 'danger' : 'default'}
      title={deactivating ? `Deactivate ${site.name}?` : `Reactivate ${site.name}?`}
      consequence={
        deactivating ? (
          <>
            The site&apos;s provisioning key and{' '}
            <strong>
              all {site.terminal_count} terminal{site.terminal_count === 1 ? '' : 's'}
            </strong>{' '}
            at this site stop working immediately. Doors will not open by
            credential while it is deactivated.
          </>
        ) : (
          <>
            The site&apos;s provisioning key and its terminals start working again
            immediately.
          </>
        )
      }
      detail={
        deactivating
          ? 'This is reversible — nothing is deleted, and you can reactivate the site from this same screen. To remove a location permanently, use Retire instead.'
          : undefined
      }
      confirmLabel={deactivating ? 'Deactivate site' : 'Reactivate site'}
      onConfirm={async () => {
        await update.mutateAsync({ active: !site.active })
        notifications.success(
          deactivating ? `${site.name} deactivated` : `${site.name} reactivated`,
        )
      }}
      onClose={onClose}
    />
  )
}

// ---------------------------------------------------------------------------
// Retire
// ---------------------------------------------------------------------------

export function RetireSiteDialog({
  open,
  site,
  onClose,
  onRetired,
}: {
  open: boolean
  site: Site
  onClose: () => void
  onRetired: () => void
}) {
  const retire = useRetireSite()
  const notifications = useNotifications()
  const terminals = site.terminal_count

  return (
    <ConfirmDialog
      open={open}
      title={`Retire ${site.name}?`}
      // THE TERMINAL COUNT IS STATED BEFORE IT HAPPENS, not reported after.
      consequence={
        <>
          This retires the site and{' '}
          <strong>
            {terminals} terminal{terminals === 1 ? '' : 's'}
          </strong>{' '}
          installed at it. Every one of them stops opening doors immediately, and
          this cannot be undone from the console.
        </>
      }
      detail={
        <>
          Terminals at a retired site stop authenticating on their own device
          credentials as well as on the site key, so re-adding the site later
          means re-provisioning the hardware. If you only need to suspend this
          location, <strong>deactivate it instead</strong> — that is reversible.
        </>
      }
      // Typing the name is reserved for exactly this: the one console action
      // that cannot be undone and that stops physical hardware working.
      confirmPhrase={site.name}
      confirmLabel="Retire site"
      onConfirm={async () => {
        const result = await retire.mutateAsync(site.id)
        notifications.success(
          result.terminals_retired === 0
            ? `${site.name} retired`
            : `${site.name} retired, along with ${result.terminals_retired} terminal${
                result.terminals_retired === 1 ? '' : 's'
              }`,
        )
        onRetired()
      }}
      onClose={onClose}
    />
  )
}

// ---------------------------------------------------------------------------
// Rotate the provisioning key
// ---------------------------------------------------------------------------

/**
 * Rotation is two steps in one dialog: confirm, then read the new key once.
 *
 * They cannot be separate dialogs. The key exists only in the response to the
 * confirmation, so closing between the two would destroy it.
 */
export function RotateSiteKeyDialog({
  open,
  site,
  onClose,
}: {
  open: boolean
  site: Site
  onClose: () => void
}) {
  const rotate = useRotateSiteKey(site.id)
  const notifications = useNotifications()
  const [result, setResult] = useState<RotateSiteKeyResponse | null>(null)

  // A ref ALONGSIDE the state, and it is load-bearing. ConfirmDialog calls
  // onClose as soon as onConfirm resolves -- correct for every other
  // confirmation, and fatal here, because closing would discard the key that
  // confirmation just produced. React has not flushed setResult by the time
  // that runs, so the close handler cannot read the state; the ref is what lets
  // it know a credential is waiting.
  const pendingCredential = useRef(false)

  function closeConfirmStep() {
    // Reached on cancel AND immediately after a successful rotation. Only the
    // first should reach the parent.
    if (!pendingCredential.current) onClose()
  }

  function dismiss() {
    // Clear both copies of the credential: this component's, and the one the
    // mutation retains in `data`.
    pendingCredential.current = false
    setResult(null)
    rotate.reset()
    onClose()
  }

  if (result) {
    return (
      <Dialog
        open={open}
        title="New provisioning key"
        size="wide"
        dismissible={false}
        onClose={dismiss}
      >
        <CredentialPanel
          credential={result.credential}
          siteName={site.name}
          context="rotated"
          extra={
            <p className="credential__impact">
              The previous key stopped working the moment this was issued.{' '}
              {result.legacy_terminals === 0 ? (
                <>
                  No terminal at this site depends on the site key — every one of
                  them holds its own device credential, so none was affected.
                </>
              ) : (
                <>
                  <strong>
                    {result.legacy_terminals} terminal
                    {result.legacy_terminals === 1 ? '' : 's'}
                  </strong>{' '}
                  at this site {result.legacy_terminals === 1 ? 'has' : 'have'} never
                  been issued a device credential and {result.legacy_terminals === 1 ? 'was' : 'were'}{' '}
                  using the site key. {result.legacy_terminals === 1 ? 'It needs' : 'They need'}{' '}
                  re-provisioning with the new key.
                </>
              )}
            </p>
          }
          onDismiss={dismiss}
        />
      </Dialog>
    )
  }

  return (
    <ConfirmDialog
      open={open}
      title={`Rotate the key for ${site.name}?`}
      consequence={
        <>
          The current provisioning key stops working <strong>immediately</strong>.
          There is no overlap period.
        </>
      }
      detail={
        <>
          Any tooling holding the old key must be updated, and so must any terminal
          that authenticates with the site key rather than its own device
          credential. The new key is shown once, on the next screen.
        </>
      }
      confirmLabel="Rotate key"
      onConfirm={async () => {
        const rotated = await rotate.mutateAsync()
        pendingCredential.current = true
        setResult(rotated)
        notifications.success(`New provisioning key issued for ${site.name}`)
      }}
      onClose={closeConfirmStep}
    />
  )
}
