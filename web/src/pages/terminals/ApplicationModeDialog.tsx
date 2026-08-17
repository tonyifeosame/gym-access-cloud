import { useState } from 'react'

import { ApiError } from '../../api/client'
import { MULTI_PURPOSE, type ApplicationCode, type TerminalDetail } from '../../api/types'
import { describeApplication } from '../../applications/registry'
import { Dialog } from '../../components/Dialog'
import { FormError, SelectField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { useUpdateTerminalMode } from '../../data/console'
import { useAuthenticatedSession } from '../../session/useSession'

/**
 * What a terminal is for.
 *
 * THE TWO CONCEPTS THIS DIALOG KEEPS APART:
 *
 *   application_mode          what this ONE TERMINAL is assigned to do.
 *   the company's applications which capabilities the COMPANY has enabled.
 *
 * A terminal may only be pointed at a capability its company has enabled, or at
 * MULTI_PURPOSE — the server refuses anything else with a 409, so the options
 * here are built from the session's enabled applications rather than from a
 * hard-coded list. A capability the platform gains later appears without a
 * frontend release; one the company has not enabled is never offered.
 *
 * MULTI_PURPOSE IS NOT A CAPABILITY. It is a terminal mode meaning "serve
 * whatever this company has enabled", and the API rejects it as a company
 * application. It is offered here and only here.
 *
 * NOTHING ABOUT THIS ASSUMES A BUSINESS PURPOSE. A terminal at a door, a
 * turnstile, a reception desk or a time clock is the same object with a
 * different assignment.
 */
export function ApplicationModeDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const session = useAuthenticatedSession()
  const update = useUpdateTerminalMode(terminal.serial_number)
  const notifications = useNotifications()
  const [mode, setMode] = useState<string>(terminal.application_mode)

  const enabled = session.applications.map((application) => application.code)

  const options = [
    {
      value: MULTI_PURPOSE,
      label: 'Multi-purpose',
      description:
        'Serves every capability this company has enabled. The default, and what a terminal keeps unless it is given a narrower job.',
    },
    ...enabled.map((code: ApplicationCode) => {
      const definition = describeApplication(code)
      return {
        value: code,
        label: definition.label,
        description: definition.description,
      }
    }),
  ]

  // The terminal may currently be pointed at a capability the company has since
  // disabled. The server RETAINS that assignment rather than rewriting it, so
  // the select has to be able to show it or the dialog would silently propose
  // changing something the operator never asked to change.
  const currentIsOrphaned =
    terminal.application_mode !== MULTI_PURPOSE && !enabled.includes(terminal.application_mode)

  if (currentIsOrphaned) {
    options.push({
      value: terminal.application_mode,
      label: `${describeApplication(terminal.application_mode).label} (not enabled)`,
      description:
        'This terminal is still assigned to a capability your company has switched off. It resolves to nothing until the capability is re-enabled.',
    })
  }

  const changed = mode !== terminal.application_mode

  async function save() {
    try {
      await update.mutateAsync({ application_mode: mode as ApplicationCode })
      notifications.success(`${terminal.serial_number} updated`)
      onClose()
    } catch {
      // Held in the mutation and rendered below; the dialog stays open so the
      // operator can see what happened and retry or cancel.
    }
  }

  const conflict = update.error instanceof ApiError && update.error.status === 409

  return (
    <Dialog
      open={open}
      title={`What is ${terminal.serial_number} for?`}
      description="This changes what this one terminal does. It does not change which capabilities your company has enabled."
      dismissible={!update.isPending}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={update.isPending}
          >
            Cancel
          </button>
          <button
            type="button"
            className="button button--primary"
            onClick={() => void save()}
            disabled={!changed || update.isPending}
          >
            {update.isPending ? 'Saving…' : 'Save'}
          </button>
        </>
      }
    >
      {enabled.length === 0 ? (
        <p className="field__hint">
          Your company has no applications enabled, so this terminal can only be
          multi-purpose. An owner can enable capabilities from Applications.
        </p>
      ) : null}

      <SelectField
        label="Application mode"
        value={mode}
        options={options}
        onChange={setMode}
        hint="Only capabilities your company has enabled can be assigned."
      />

      <FormError
        message={
          conflict
            ? 'That capability is not enabled for your company. An owner can enable it from Applications.'
            : update.error
              ? update.error instanceof ApiError
                ? update.error.message
                : 'The terminal could not be updated.'
              : null
        }
        requestId={update.error instanceof ApiError ? update.error.requestId : null}
      />
    </Dialog>
  )
}
