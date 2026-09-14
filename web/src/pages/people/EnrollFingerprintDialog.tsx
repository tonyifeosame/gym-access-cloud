import type { Person } from '../../api/types'
import { Dialog } from '../../components/Dialog'
import {
  describeWorkflowLead,
  EnrollmentWorkflowActions,
  EnrollmentWorkflowBody,
  useEnrollmentWorkflow,
} from './EnrollmentWorkflow'

/**
 * Enrolling a fingerprint from the person's own page.
 *
 * THE SECOND DOOR INTO THE WORKFLOW. The first is the enrolment step of "Add a
 * person" (PersonFormDialog), for the person who is standing at a terminal the
 * moment they are created. This one is for everybody else: added before that
 * step existed, added and skipped, or being re-enrolled. It renders exactly
 * what the first does -- see EnrollmentWorkflow for the states and the rules.
 */
export function EnrollFingerprintDialog({
  open,
  person,
  onClose,
}: {
  open: boolean
  person: Person
  onClose: () => void
}) {
  const workflow = useEnrollmentWorkflow(person, { enabled: open })

  return (
    <Dialog
      open={open}
      size="wide"
      title={`Enrol a fingerprint for ${person.full_name || person.external_id}`}
      description={describeWorkflowLead(workflow)}
      onClose={onClose}
      dismissible={!workflow.starting && !workflow.cancelling}
      footer={<EnrollmentWorkflowActions workflow={workflow} onClose={onClose} />}
    >
      <EnrollmentWorkflowBody workflow={workflow} />
    </Dialog>
  )
}
