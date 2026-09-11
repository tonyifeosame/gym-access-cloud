import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { APICredential, APICredentialIssued } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, RadioGroup, TextField } from '../../components/Form'
import { SecretPanel } from '../../components/SecretPanel'
import { useNotifications } from '../../components/Notifications'
import { useRevokeAPICredential, useRotateAPICredential } from '../../data/console'

/**
 * The two things that change whether a key works.
 *
 * ROTATION IS THE REMEDY FOR A LOST OR LEAKED SECRET, and its one decision is
 * how long the old key keeps working. The server's own default is three days,
 * which is right when an integrator needs time to deploy the new key and wrong
 * when the reason for rotating is that somebody else has the old one. THE
 * CONSOLE DEFAULTS TO "IMMEDIATELY" -- the safe reading -- and offers the
 * grace explicitly, because a key rotated from a screen is more often a key
 * being replaced under pressure than one being renewed on a schedule.
 *
 * REVOCATION IS FINAL. The server keeps the row for the audit trail and the
 * secret is gone from everywhere the moment it lands; nothing here suggests
 * otherwise.
 */

interface GraceOption {
  value: string
  label: string
  description: string
  seconds: number
}

const IMMEDIATELY: GraceOption = {
  value: '0',
  label: 'Immediately',
  description:
    'The old secret stops working the moment you confirm. Right for a leaked or lost key.',
  seconds: 0,
}

const GRACE_OPTIONS: GraceOption[] = [
  IMMEDIATELY,
  {
    value: '86400',
    label: '24 hours',
    description: 'The old secret keeps working for a day, so the integration can be updated first.',
    seconds: 24 * 60 * 60,
  },
  {
    value: '259200',
    label: '72 hours',
    description: 'Three days of overlap — the platform default for a planned rotation.',
    seconds: 72 * 60 * 60,
  },
]

export function RotateCredentialDialog({
  open,
  credential,
  onClose,
}: {
  open: boolean
  credential: APICredential
  onClose: () => void
}) {
  const rotate = useRotateAPICredential()
  const notifications = useNotifications()
  const [grace, setGrace] = useState<string>('0')
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<unknown>(null)
  // The new credential, held for the life of this panel and nowhere else.
  const [issued, setIssued] = useState<APICredentialIssued | null>(null)

  const chosen = GRACE_OPTIONS.find((option) => option.value === grace) ?? IMMEDIATELY

  async function submit(event: { preventDefault: () => void }) {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const response = await rotate.mutateAsync({
        credentialId: credential.id,
        body: { grace_seconds: chosen.seconds, reason: reason.trim() || undefined },
      })
      setIssued(response)
    } catch (caught) {
      setError(caught)
    } finally {
      setSubmitting(false)
    }
  }

  function dismiss() {
    // Reset drops the mutation's `data`, the only other copy of the secret.
    rotate.reset()
    setIssued(null)
    notifications.success(`${credential.name} rotated`)
    onClose()
  }

  if (issued) {
    return (
      <Dialog
        open={open}
        title="Credential rotated"
        dismissible={false}
        onClose={dismiss}
        size="wide"
      >
        <SecretPanel
          heading={`New secret for ${issued.name}`}
          warning={
            <>
              <strong>This secret is shown once and cannot be recovered.</strong> Update the
              integration that holds the old key with this one now.
            </>
          }
          label="New secret"
          secret={issued.secret}
          prefix={issued.key_prefix}
          extra={
            <p className="credential__impact">
              {chosen.seconds === 0 ? (
                <>
                  The previous secret (<code>{credential.key_prefix}…</code>) has{' '}
                  <strong>already stopped working</strong>. Anything still presenting it
                  is being refused.
                </>
              ) : (
                <>
                  The previous secret (<code>{credential.key_prefix}…</code>) keeps working
                  for <strong>{chosen.label.toLowerCase()}</strong>, then is refused.
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
    <Dialog
      open={open}
      title={`Rotate ${credential.name}?`}
      description="Issues a new secret for this credential. Its scopes and site access are kept."
      dismissible={!submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void submit(event)} noValidate>
        <RadioGroup
          legend="Old secret keeps working for"
          name="grace"
          options={GRACE_OPTIONS.map((option) => ({
            value: option.value,
            label: option.label,
            description: option.description,
          }))}
          value={grace}
          onChange={setGrace}
          disabled={submitting}
        />

        <TextField
          label="Reason"
          value={reason}
          onChange={setReason}
          disabled={submitting}
          hint="Optional. Recorded in the audit trail."
          autoComplete="off"
        />

        <FormError
          message={error instanceof ApiError ? error.message : error ? String(error) : null}
          requestId={error instanceof ApiError ? error.requestId : null}
        />

        <FormActions>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={submitting}
          >
            Cancel
          </button>
          <button type="submit" className="button button--primary" disabled={submitting}>
            {submitting ? 'Rotating…' : 'Rotate credential'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

export function RevokeCredentialDialog({
  open,
  credential,
  onClose,
  onRevoked,
}: {
  open: boolean
  credential: APICredential
  onClose: () => void
  onRevoked?: () => void
}) {
  const revoke = useRevokeAPICredential()
  const notifications = useNotifications()
  const [reason, setReason] = useState('')

  return (
    <ConfirmDialog
      open={open}
      title={`Revoke ${credential.name}?`}
      consequence={
        <>
          The public API refuses this key from the next request onwards. Anything
          presenting <code>{credential.key_prefix}…</code> stops working.
        </>
      }
      detail={
        <>
          This cannot be undone and the secret cannot be shown again. If the integration
          should keep working with a fresh key, <strong>rotate it instead</strong>. The
          credential stays in the list, marked revoked, for the audit trail.
        </>
      }
      confirmPhrase={credential.name}
      confirmLabel="Revoke credential"
      onConfirm={async () => {
        await revoke.mutateAsync({
          credentialId: credential.id,
          body: { reason: reason.trim() || undefined },
        })
        notifications.success(`${credential.name} revoked`)
        onRevoked?.()
      }}
      onClose={onClose}
    >
      <TextField
        label="Reason"
        value={reason}
        onChange={setReason}
        hint="Optional. Recorded on the credential and in the audit trail."
        autoComplete="off"
      />
    </ConfirmDialog>
  )
}
