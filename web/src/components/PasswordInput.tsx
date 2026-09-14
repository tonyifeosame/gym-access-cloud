import { useId, useState, type ComponentPropsWithoutRef } from 'react'

/**
 * A password field with a show/hide control.
 *
 * ONE COMPONENT FOR EVERY PASSWORD ON EVERY SCREEN. Sign in, signup, the
 * reset link, the forced change, an operator's settings page and the platform
 * login all type passwords, and a toggle drawn slightly differently on each of
 * them would be six toggles to keep consistent. TextField routes its
 * `type="password"` here too, so a form built from the field primitives gets
 * the same control without asking.
 *
 * WHAT THE TOGGLE IS, ACCESSIBLY:
 *
 *   - a real <button type="button">, so it cannot submit the form it sits in
 *     and it is reachable by keyboard like any other control;
 *   - aria-pressed reports its state, so a screen reader hears "Show
 *     password, toggle button, pressed" rather than an unlabelled icon;
 *   - aria-controls ties it to the input it reveals;
 *   - tabIndex -1 is DELIBERATELY NOT set. It is a control, and hiding a
 *     control from the keyboard to save a tab stop is the wrong trade on a
 *     form somebody may be filling in with a screen reader.
 *
 * WHAT IT DOES NOT CHANGE. Revealing the value changes the input's type and
 * nothing else: autocomplete, name, validity and the form's submission are
 * exactly what they were. The value is never copied anywhere.
 */
export interface PasswordInputProps
  extends Omit<ComponentPropsWithoutRef<'input'>, 'type' | 'className'> {
  /** Class for the input itself; the wrapper carries its own. */
  inputClassName?: string
}

export function PasswordInput({ inputClassName, id, disabled, ...rest }: PasswordInputProps) {
  const [revealed, setRevealed] = useState(false)
  const generatedId = useId()
  const inputId = id ?? generatedId

  return (
    <div className="password-control">
      <input
        {...rest}
        id={inputId}
        type={revealed ? 'text' : 'password'}
        className={`field__input password-control__input${inputClassName ? ` ${inputClassName}` : ''}`}
        disabled={disabled}
        // Spell-check and capitalisation prompts on a revealed password would
        // put it into a suggestion list somewhere; keep them off in both modes.
        spellCheck={false}
        autoCapitalize="off"
      />
      <button
        type="button"
        className="password-control__toggle"
        aria-pressed={revealed}
        aria-controls={inputId}
        aria-label={revealed ? 'Hide password' : 'Show password'}
        title={revealed ? 'Hide password' : 'Show password'}
        disabled={disabled}
        onClick={() => setRevealed((value) => !value)}
      >
        {revealed ? <EyeOffIcon /> : <EyeIcon />}
      </button>
    </div>
  )
}

/*
  The icons are inline so nothing is fetched and the CSP stays at 'self'.
  aria-hidden: the button's label is the accessible name; the picture is
  decoration for sighted users.
*/
function EyeIcon() {
  return (
    <svg
      aria-hidden="true"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  )
}

function EyeOffIcon() {
  return (
    <svg
      aria-hidden="true"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M17.94 17.94A10.94 10.94 0 0 1 12 19c-6.5 0-10-7-10-7a19.8 19.8 0 0 1 5.06-5.94" />
      <path d="M9.9 4.24A10.4 10.4 0 0 1 12 5c6.5 0 10 7 10 7a19.7 19.7 0 0 1-3.22 4.19" />
      <path d="M14.12 14.12A3 3 0 1 1 9.88 9.88" />
      <line x1="2" y1="2" x2="22" y2="22" />
    </svg>
  )
}
