import { useCallback, useMemo, useState } from 'react'

import { ApiError } from '../api/client'

/**
 * Form state, validation and submission.
 *
 * Hand-rolled rather than pulling in a form library. The console's forms are
 * small — a handful of fields, a submit, an error — and the parts a library
 * would bring that actually matter here are the ones written out below: when to
 * show an error, and what to do when the SERVER rejects something the client
 * thought was fine.
 *
 * TWO RULES ABOUT WHEN AN ERROR APPEARS, and both exist because the obvious
 * implementation is hostile:
 *
 *   - A field shows its error only once it has been TOUCHED or a submit has been
 *     attempted. Validating as someone types their first character means telling
 *     them their email is invalid before they have finished the local part.
 *   - A failed submit marks everything touched, so nothing stays hidden that is
 *     the reason the form will not go through.
 *
 * SERVER ERRORS ARE NOT CLIENT ERRORS. A 409 on a duplicate id and a 400 from
 * the validator are answers the client could not have known in advance; they are
 * kept separate from field validation, shown as a form-level message, and NEVER
 * parsed for meaning. The API's error strings are explicitly not stable, so
 * behaviour branches on status.
 */

export type FieldErrors<Values> = Partial<Record<keyof Values, string>>

export type Validator<Values> = (values: Values) => FieldErrors<Values>

/**
 * Drops keys whose value is undefined.
 *
 * A validator written the natural way —
 *
 *   validate: (values) => ({ email: validators.email(values.email) })
 *
 * returns `{ email: undefined }` when the field is FINE, and `Object.keys` on
 * that is length 1. Counting keys to decide whether a form is valid therefore
 * rejects every valid submission, silently and forever. Normalising here means a
 * validator can be written the obvious way and still be counted correctly.
 */
function compact<Values>(errors: FieldErrors<Values> | undefined): FieldErrors<Values> {
  if (!errors) return {}
  const result: FieldErrors<Values> = {}
  for (const key of Object.keys(errors) as (keyof Values)[]) {
    const message = errors[key]
    if (message) result[key] = message
  }
  return result
}

export interface UseFormOptions<Values> {
  initialValues: Values
  validate?: Validator<Values>
  onSubmit: (values: Values) => Promise<unknown> | unknown
}

export interface UseFormResult<Values> {
  values: Values
  /** Errors for fields the operator should currently be shown. */
  errors: FieldErrors<Values>
  /** True once a submit has been attempted. */
  submitAttempted: boolean
  submitting: boolean
  /** A rejection from the server, kept apart from field validation. */
  submitError: unknown
  /** True when the values differ from where they started. */
  dirty: boolean
  setValue: <Key extends keyof Values>(field: Key, value: Values[Key]) => void
  setValues: (values: Partial<Values>) => void
  touch: (field: keyof Values) => void
  handleSubmit: (event?: { preventDefault: () => void }) => Promise<void>
  reset: (values?: Values) => void
  /** Props for a field, wired to the state above. */
  fieldProps: <Key extends keyof Values & string>(
    field: Key,
  ) => {
    name: Key
    value: Values[Key]
    error: string | undefined
    onChange: (value: Values[Key]) => void
    onBlur: () => void
  }
}

export function useForm<Values extends Record<string, unknown>>({
  initialValues,
  validate,
  onSubmit,
}: UseFormOptions<Values>): UseFormResult<Values> {
  const [initial, setInitial] = useState(initialValues)
  const [values, setValuesState] = useState<Values>(initialValues)
  const [touched, setTouched] = useState<Partial<Record<keyof Values, boolean>>>({})
  const [submitAttempted, setSubmitAttempted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<unknown>(null)

  const allErrors = useMemo<FieldErrors<Values>>(
    () => compact(validate?.(values)),
    [validate, values],
  )

  // Only what the operator should see right now.
  const errors = useMemo(() => {
    const visible: FieldErrors<Values> = {}
    for (const key of Object.keys(allErrors) as (keyof Values)[]) {
      if (submitAttempted || touched[key]) {
        visible[key] = allErrors[key]
      }
    }
    return visible
  }, [allErrors, submitAttempted, touched])

  const setValue = useCallback(<Key extends keyof Values>(field: Key, value: Values[Key]) => {
    setValuesState((current) => ({ ...current, [field]: value }))
    // Editing a field clears the SERVER's complaint about the submission: it
    // described values that no longer exist, and leaving it up makes a
    // corrected form look like it is still failing.
    setSubmitError(null)
  }, [])

  const setValues = useCallback((patch: Partial<Values>) => {
    setValuesState((current) => ({ ...current, ...patch }))
    setSubmitError(null)
  }, [])

  const touch = useCallback((field: keyof Values) => {
    setTouched((current) => ({ ...current, [field]: true }))
  }, [])

  const reset = useCallback((next?: Values) => {
    const target = next ?? initial
    if (next) setInitial(next)
    setValuesState(target)
    setTouched({})
    setSubmitAttempted(false)
    setSubmitError(null)
    setSubmitting(false)
  }, [initial])

  const handleSubmit = useCallback(
    async (event?: { preventDefault: () => void }) => {
      event?.preventDefault()
      setSubmitAttempted(true)
      setSubmitError(null)

      const found = compact(validate?.(values))
      if (Object.keys(found).length > 0) {
        // Reveal every reason at once rather than one per attempt.
        setTouched(
          Object.fromEntries(Object.keys(values).map((key) => [key, true])) as Partial<
            Record<keyof Values, boolean>
          >,
        )
        return
      }

      setSubmitting(true)
      try {
        await onSubmit(values)
      } catch (error) {
        setSubmitError(error)
      } finally {
        setSubmitting(false)
      }
    },
    [onSubmit, validate, values],
  )

  const dirty = useMemo(
    () => (Object.keys(initial) as (keyof Values)[]).some((key) => values[key] !== initial[key]),
    [initial, values],
  )

  const fieldProps = useCallback(
    <Key extends keyof Values & string>(field: Key) => ({
      name: field,
      value: values[field],
      error: errors[field],
      onChange: (value: Values[Key]) => setValue(field, value),
      onBlur: () => touch(field),
    }),
    [errors, setValue, touch, values],
  )

  return {
    values,
    errors,
    submitAttempted,
    submitting,
    submitError,
    dirty,
    setValue,
    setValues,
    touch,
    handleSubmit,
    reset,
    fieldProps,
  }
}

/**
 * A submission failure as a sentence.
 *
 * The message is shown, never parsed. Where the status carries meaning the UI
 * can act on — a conflict, a permission failure — that is decided from the
 * status code, which IS stable.
 */
export function submitErrorMessage(error: unknown): string | null {
  if (!error) return null
  if (error instanceof ApiError) return error.message
  if (error instanceof Error) return error.message
  return 'The change could not be saved.'
}

/** Common validators, so the same rule is not spelled three ways. */
export const validators = {
  required(value: unknown, label: string): string | undefined {
    if (typeof value === 'string' ? value.trim() === '' : value === null || value === undefined) {
      return `${label} is required.`
    }
    return undefined
  },

  /**
   * Deliberately permissive. The server is the authority on what it will accept,
   * and a stricter client-side pattern only rejects addresses that would have
   * worked — a real and irritating failure mode.
   */
  email(value: string, label = 'Email'): string | undefined {
    if (value.trim() === '') return `${label} is required.`
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())) {
      return `${label} does not look like an email address.`
    }
    return undefined
  },

  minLength(value: string, length: number, label: string): string | undefined {
    if (value.length < length) return `${label} must be at least ${length} characters.`
    return undefined
  },

  maxLength(value: string, length: number, label: string): string | undefined {
    if (value.length > length) return `${label} must be ${length} characters or fewer.`
    return undefined
  },
}
