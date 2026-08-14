import { useId, type ReactNode } from 'react'

/**
 * Form controls.
 *
 * Every one wires the same four things, which is the entire reason they exist as
 * components: a label bound to its control by id, a hint bound by
 * aria-describedby, an error bound the same way and marked aria-invalid, and a
 * required marker that is announced rather than merely drawn as an asterisk.
 *
 * Getting that wrong is the single most common accessibility failure in an admin
 * console, and it is invisible unless somebody tests with a screen reader. Doing
 * it once here means no screen can get it wrong.
 *
 * The error is rendered in a live region so that a validation message appearing
 * after a blur is ANNOUNCED, not silently painted.
 */

interface FieldShellProps {
  label: string
  hint?: ReactNode
  error?: string
  required?: boolean
  children: (ids: { id: string; describedBy: string | undefined }) => ReactNode
}

function FieldShell({ label, hint, error, required, children }: FieldShellProps) {
  const id = useId()
  const hintId = `${id}-hint`
  const errorId = `${id}-error`

  const describedBy =
    [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(' ') || undefined

  return (
    <div className={`field${error ? ' field--invalid' : ''}`}>
      <label className="field__label" htmlFor={id}>
        {label}
        {required ? (
          <span className="field__required">
            {' '}
            <span aria-hidden="true">*</span>
            <span className="visually-hidden">(required)</span>
          </span>
        ) : null}
      </label>

      {hint ? (
        <p className="field__hint" id={hintId}>
          {hint}
        </p>
      ) : null}

      {children({ id, describedBy })}

      {/*
        Always present so the region exists before it has anything to say: a
        live region inserted at the same moment as its text is not reliably
        announced.

        POLITE, not role="alert". A validation message is not an emergency — it
        should be read when the reader reaches a pause, not cut across whatever
        they are hearing. role="alert" is reserved for the form-level failure in
        FormError, which IS interrupting news, and keeping the distinction means
        "the alert" on a form unambiguously means the submission failed.
      */}
      <p className="field__error" id={errorId} aria-live="polite">
        {error ?? ''}
      </p>
    </div>
  )
}

export interface TextFieldProps {
  label: string
  value: string
  onChange: (value: string) => void
  onBlur?: () => void
  error?: string
  hint?: ReactNode
  required?: boolean
  type?: 'text' | 'email' | 'password' | 'number'
  placeholder?: string
  autoComplete?: string
  disabled?: boolean
  /** Renders a multi-line control instead of an input. */
  multiline?: boolean
  rows?: number
  /** Monospace, for identifiers and JSON. */
  mono?: boolean
}

export function TextField({
  label,
  value,
  onChange,
  onBlur,
  error,
  hint,
  required,
  type = 'text',
  placeholder,
  autoComplete,
  disabled,
  multiline,
  rows = 8,
  mono,
}: TextFieldProps) {
  return (
    <FieldShell label={label} hint={hint} error={error} required={required}>
      {({ id, describedBy }) =>
        multiline ? (
          <textarea
            id={id}
            className={`field__input${mono ? ' field__input--mono' : ''}`}
            value={value}
            rows={rows}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
            aria-describedby={describedBy}
            aria-invalid={error ? true : undefined}
            aria-required={required || undefined}
            placeholder={placeholder}
            disabled={disabled}
            spellCheck={mono ? false : undefined}
          />
        ) : (
          <input
            id={id}
            className={`field__input${mono ? ' field__input--mono' : ''}`}
            type={type}
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
            aria-describedby={describedBy}
            aria-invalid={error ? true : undefined}
            aria-required={required || undefined}
            placeholder={placeholder}
            autoComplete={autoComplete}
            disabled={disabled}
            spellCheck={mono ? false : undefined}
          />
        )
      }
    </FieldShell>
  )
}

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
  /** Shown under the label where a choice needs explaining. */
  description?: string
}

export interface SelectFieldProps {
  label: string
  value: string
  options: SelectOption[]
  onChange: (value: string) => void
  onBlur?: () => void
  error?: string
  hint?: ReactNode
  required?: boolean
  disabled?: boolean
  /** Leading option for "no choice made". Omit to require one of the options. */
  placeholder?: string
}

export function SelectField({
  label,
  value,
  options,
  onChange,
  onBlur,
  error,
  hint,
  required,
  disabled,
  placeholder,
}: SelectFieldProps) {
  const selected = options.find((option) => option.value === value)

  return (
    <FieldShell label={label} hint={hint} error={error} required={required}>
      {({ id, describedBy }) => (
        <>
          <select
            id={id}
            className="field__input field__select"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
            aria-describedby={describedBy}
            aria-invalid={error ? true : undefined}
            aria-required={required || undefined}
            disabled={disabled}
          >
            {placeholder ? <option value="">{placeholder}</option> : null}
            {options.map((option) => (
              <option key={option.value} value={option.value} disabled={option.disabled}>
                {option.label}
              </option>
            ))}
          </select>
          {selected?.description ? (
            <p className="field__hint field__hint--selection">{selected.description}</p>
          ) : null}
        </>
      )}
    </FieldShell>
  )
}

export interface CheckboxFieldProps {
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
  hint?: ReactNode
  disabled?: boolean
  error?: string
}

export function CheckboxField({
  label,
  checked,
  onChange,
  hint,
  disabled,
  error,
}: CheckboxFieldProps) {
  const id = useId()
  const hintId = `${id}-hint`

  return (
    <div className="field field--checkbox">
      <div className="checkbox">
        <input
          id={id}
          type="checkbox"
          className="checkbox__input"
          checked={checked}
          onChange={(event) => onChange(event.target.checked)}
          aria-describedby={hint ? hintId : undefined}
          disabled={disabled}
        />
        <label className="checkbox__label" htmlFor={id}>
          {label}
        </label>
      </div>
      {hint ? (
        <p className="field__hint" id={hintId}>
          {hint}
        </p>
      ) : null}
      {error ? (
        <p className="field__error" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  )
}

/**
 * A group of checkboxes as one labelled control, for multi-select — site grants,
 * most obviously.
 *
 * A fieldset with a legend rather than a `<label>` and a pile of inputs: a label
 * may only name one control, so grouping this way is what makes the set announce
 * as a set rather than as unrelated checkboxes.
 */
export interface CheckboxGroupProps {
  legend: string
  hint?: ReactNode
  options: { value: string; label: string; description?: string }[]
  selected: string[]
  onChange: (selected: string[]) => void
  disabled?: boolean
  /** Shown when there is nothing to choose from. */
  empty?: ReactNode
}

export function CheckboxGroup({
  legend,
  hint,
  options,
  selected,
  onChange,
  disabled,
  empty,
}: CheckboxGroupProps) {
  const hintId = useId()

  function toggle(value: string, checked: boolean) {
    onChange(checked ? [...selected, value] : selected.filter((entry) => entry !== value))
  }

  return (
    <fieldset className="fieldset" aria-describedby={hint ? hintId : undefined}>
      <legend className="fieldset__legend">{legend}</legend>
      {hint ? (
        <p className="field__hint" id={hintId}>
          {hint}
        </p>
      ) : null}

      {options.length === 0 ? (
        <p className="field__hint">{empty ?? 'Nothing to choose from.'}</p>
      ) : (
        <div className="fieldset__options">
          {options.map((option) => (
            <div className="checkbox" key={option.value}>
              <input
                id={`${hintId}-${option.value}`}
                type="checkbox"
                className="checkbox__input"
                checked={selected.includes(option.value)}
                onChange={(event) => toggle(option.value, event.target.checked)}
                disabled={disabled}
              />
              <label className="checkbox__label" htmlFor={`${hintId}-${option.value}`}>
                {option.label}
                {option.description ? (
                  <span className="checkbox__description">{option.description}</span>
                ) : null}
              </label>
            </div>
          ))}
        </div>
      )}
    </fieldset>
  )
}

/**
 * A form-level failure, distinct from any field's.
 *
 * This is where a server rejection lands. Rendered as an alert so it is
 * announced, and carrying the request id when there is one — that id is in the
 * server log against the same failure, which is what turns "it didn't work" into
 * something findable.
 */
export function FormError({ message, requestId }: { message: string | null; requestId?: string | null }) {
  if (!message) return null
  return (
    <div className="form__error" role="alert">
      <p>{message}</p>
      {requestId ? (
        <p className="state__meta">
          Reference <code>{requestId}</code>
        </p>
      ) : null}
    </div>
  )
}

export function FormActions({ children }: { children: ReactNode }) {
  return <div className="form__actions">{children}</div>
}
