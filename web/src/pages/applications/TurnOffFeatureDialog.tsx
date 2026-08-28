import { ConfirmDialog } from '../../components/ConfirmDialog'

/**
 * Confirming that a feature is switched off.
 *
 * ONE COMPONENT BECAUSE THERE ARE TWO WAYS TO DO IT, and until now only one of
 * them asked. The detail page opened a confirmation naming the consequence; the
 * list — which is where the toggles are, and therefore the path almost everyone
 * takes — fired the change immediately. Same destructive action, two routes, one
 * guarded.
 *
 * Turning a feature off is not a display change: a terminal assigned to it
 * resolves to nothing until the feature comes back or the terminal is
 * reassigned. That is worth a question wherever it is asked from, and the answer
 * has to read the same both times — which is why the copy lives here rather than
 * being written out at each call site and drifting.
 *
 * NO TYPED PHRASE, DELIBERATELY. It is reversible: switching the feature back on
 * restores every assignment, because assignments are kept rather than rewritten.
 * Reserving the typed confirmation for the genuinely irreversible is what stops
 * operators typing phrases without reading them.
 *
 * TURNING A FEATURE ON IS NOT CONFIRMED, and should not be. It takes nothing
 * away, breaks no terminal, and asking about it would train somebody to dismiss
 * the question that matters.
 */
export function TurnOffFeatureDialog({
  open,
  label,
  onConfirm,
  onClose,
}: {
  open: boolean
  /** The feature's display name, as the customer sees it elsewhere. */
  label: string
  onConfirm: () => Promise<unknown> | unknown
  onClose: () => void
}) {
  return (
    <ConfirmDialog
      open={open}
      title={`Turn off ${label}?`}
      consequence={
        <>
          It stops being available across your company, and any terminal assigned to
          it will <strong>stop doing anything</strong> until it is turned back on or
          the terminal is assigned to something else.
        </>
      }
      detail={
        <>
          Terminal assignments are <strong>kept, not cleared</strong> — a terminal
          pointed at this feature stays pointed at it and starts working again if you
          turn it back on. Anything stored under Advanced settings is kept too.
        </>
      }
      confirmLabel="Turn off feature"
      onConfirm={onConfirm}
      onClose={onClose}
    />
  )
}
