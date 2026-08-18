import { useState } from 'react'

import type { PendingTerminal } from '../../api/types'
import { Badge } from '../../components/Badge'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { useNotifications } from '../../components/Notifications'
import { Timestamp } from '../../components/Timestamp'
import { usePendingTerminals, useRejectTerminal } from '../../data/console'
import { ConfirmTerminalStep } from './AddTerminalDialog'

/**
 * Terminals that have announced themselves and are waiting for somebody.
 *
 * WHY THIS IS A SECTION ON THE FLEET PAGE and not a page of its own: it is empty
 * almost always, and when it is not, it is the most urgent thing on the screen —
 * a customer is standing next to a unit that is not working yet. A separate page
 * would put it behind a click nobody takes when nothing is waiting, and behind a
 * click somebody takes too late when something is.
 *
 * IT RENDERS NOTHING WHEN THERE IS NOTHING, deliberately. An always-visible
 * empty panel trains people to stop seeing it, and there is a permanent "Add a
 * terminal" button above that covers the "how do I start" case.
 *
 * MANAGER CAN SEE THIS; ADMIN CAN ACT ON IT. The person who unpacked the box is
 * often not an administrator, and a pending terminal nobody can see is a support
 * call — so the list is not gated on being able to approve. The buttons are.
 */
export function PendingTerminals({ canApprove }: { canApprove: boolean }) {
  const pending = usePendingTerminals()
  const [approving, setApproving] = useState<PendingTerminal | null>(null)
  const [rejecting, setRejecting] = useState<PendingTerminal | null>(null)

  const rows = pending.data?.pending ?? []
  if (rows.length === 0) return null

  return (
    <section className="panel pending-terminals" aria-labelledby="pending-terminals-title">
      <h2 className="panel__title" id="pending-terminals-title">
        Waiting to be set up
      </h2>
      <p className="card__detail">
        {canApprove
          ? 'These terminals have connected and are waiting to be approved.'
          : 'These terminals have connected and are waiting for an administrator to approve them.'}
      </p>

      <ul className="pending-terminals__list">
        {rows.map((row) => (
          <li key={row.id} className="pending-terminals__item">
            <div className="pending-terminals__identity">
              <code className="mono">{row.serial_number}</code>
              <PendingStateBadge row={row} />
            </div>

            <p className="card__detail">
              {describe(row)}
              {row.last_seen_at ? (
                <>
                  {' · last seen '}
                  <Timestamp value={row.last_seen_at} relative />
                </>
              ) : null}
            </p>

            {canApprove ? (
              <div className="pending-terminals__actions">
                {row.state === 'ADOPTED' ? (
                  <button
                    type="button"
                    className="button button--primary"
                    onClick={() => setApproving(row)}
                  >
                    Approve
                  </button>
                ) : null}
                <button
                  type="button"
                  className="button button--quiet"
                  onClick={() => setRejecting(row)}
                >
                  Remove
                </button>
              </div>
            ) : null}
          </li>
        ))}
      </ul>

      {approving ? (
        <Dialog
          open
          title="Confirm the terminal"
          description="Check this is the unit you mean, then choose where it is installed."
          size="wide"
          onClose={() => setApproving(null)}
        >
          <ConfirmTerminalStep
            pending={approving}
            onApproved={() => setApproving(null)}
            onCancel={() => setApproving(null)}
          />
        </Dialog>
      ) : null}

      {rejecting ? (
        <RejectDialog row={rejecting} onClose={() => setRejecting(null)} />
      ) : null}
    </section>
  )
}

/**
 * The three states an operator sees, and what each one means to them.
 *
 * APPROVED is the one worth a distinct label. It does NOT mean the terminal is
 * working — it means the decision is made and the unit has not come to collect
 * yet, which is the difference between "wait a moment" and "check its network".
 */
function PendingStateBadge({ row }: { row: PendingTerminal }) {
  if (row.state === 'EXPIRED') return <Badge tone="neutral">Expired</Badge>
  if (row.state === 'APPROVED') return <Badge tone="info">Approved — collecting</Badge>
  return <Badge tone="warning">Waiting for approval</Badge>
}

function describe(row: PendingTerminal): string {
  if (row.state === 'EXPIRED') {
    return 'This took too long and has timed out. The terminal will be showing a new code — add it again.'
  }
  if (row.state === 'APPROVED') {
    return `Approved for ${row.site_name ?? 'a site'}. It will finish setting itself up the next time it checks in.`
  }
  if (row.verdict === 'REFUSED_OTHER_COMPANY') {
    return 'Registered to another AccessLink account, so it cannot be set up here.'
  }
  if (row.verdict === 'REFUSED_DISABLED') {
    return 'This terminal is disabled. Re-enable it before setting it up again.'
  }
  if (row.verdict === 'RE_PROVISION') {
    return 'Already set up here. Approving gives it a new credential and the current one stops working.'
  }
  return `Added by ${row.adopted_by ?? 'an administrator'}.`
}

/**
 * Removing one from the list.
 *
 * "Remove" rather than "Reject": a customer who typed a code for the wrong unit
 * is not rejecting anything, they are undoing. It is also the recovery for an
 * approval that never completed, and the copy says so — that is the case
 * somebody will actually be in when they reach for this.
 */
function RejectDialog({ row, onClose }: { row: PendingTerminal; onClose: () => void }) {
  const reject = useRejectTerminal(row.id)
  const notifications = useNotifications()

  return (
    <ConfirmDialog
      open
      title={`Remove ${row.serial_number} from the list?`}
      confirmLabel="Remove"
      tone="danger"
      consequence={
        <>
          The terminal will not be set up, and no credential is issued to it. It keeps
          whatever it was doing before — this changes nothing on the hardware itself.
        </>
      }
      detail={
        <>
          If you want it later, it will show a new code on its screen and you can add
          it again.
        </>
      }
      onClose={onClose}
      onConfirm={async () => {
        await reject.mutateAsync({ reason: 'removed from the setup list' })
        notifications.success(`${row.serial_number} was removed from the setup list.`)
        onClose()
      }}
    />
  )
}
