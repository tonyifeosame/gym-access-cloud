import { Component, type ErrorInfo, type ReactNode } from 'react'

/**
 * The last line of defence against a blank page.
 *
 * WHAT THIS IS FOR, AND WHAT IT IS NOT. It does not catch failed requests —
 * those are data, every query already reports them, and DataTable refuses to
 * render an empty table over an error. It catches the other kind: a rendering
 * exception, a field the API stopped sending that something dereferenced, a
 * type assumption that turned out to be wrong at exactly one customer. React
 * unmounts the whole tree when one of those escapes, and the operator is left
 * looking at white.
 *
 * A WHITE PAGE IS THE WORST AVAILABLE FAILURE for this product. The console is
 * used standing in front of a door, often to deal with something that has
 * already gone wrong. Somebody who needs to revoke a stolen terminal and gets a
 * blank screen has no way to tell whether the platform is down, their session
 * ended, or they should try another browser — and no reference to quote to
 * anybody who could tell them.
 *
 * SO THE RECOVERY IS SPECIFIC. It offers the two things that actually help — try
 * this screen again without losing the session, or go somewhere known to work —
 * and it shows the error text, because the operator reporting it is the only
 * channel this platform has. There is no client-side error reporting service,
 * and adding one would send a customer's screen contents to a third party.
 *
 * IT IS A CLASS COMPONENT because React provides no hook equivalent of
 * componentDidCatch. That is the only reason.
 */

interface Props {
  children: ReactNode
  /** Names what failed, for a boundary around part of a page. */
  label?: string
  /** Resets when this changes — the route path, so navigating clears it. */
  resetKey?: string
}

interface State {
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  override state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  override componentDidUpdate(previous: Props): void {
    // Navigating away from a broken screen must clear the boundary, or the
    // operator is stuck on the error for the rest of the session.
    if (this.state.error && previous.resetKey !== this.props.resetKey) {
      this.setState({ error: null })
    }
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    // The console, deliberately, and nothing else. There is no reporting
    // endpoint on this platform, and wiring one to a third party would send a
    // customer's screen contents somewhere they did not agree to.
    //
    // The message and stack only. NOT the component props, which is where a
    // person's name, an email address or a site key would be — a "helpful"
    // dump of props here would be the console logging exactly what the rest of
    // this codebase is careful never to log.
    console.error('AccessLink console: unhandled rendering error', error.message, info.componentStack)
  }

  override render(): ReactNode {
    const { error } = this.state
    if (!error) return this.props.children

    return (
      <div className="page">
        <div className="state state--error" role="alert">
          <h1>{this.props.label ? `${this.props.label} could not be displayed` : 'This screen could not be displayed'}</h1>

          <p>
            Something went wrong drawing this page. Your session is still active
            and nothing you did has been lost — this is a fault in the console
            itself, not in your account or your data.
          </p>

          {/*
            Shown, not hidden behind a details toggle. The operator reporting
            this is the only channel that exists, and a message they cannot see
            is one they cannot quote.
          */}
          <p className="state__meta">
            <code>{error.message}</code>
          </p>

          <div className="form__actions">
            <button
              type="button"
              className="button button--primary"
              onClick={() => this.setState({ error: null })}
            >
              Try this screen again
            </button>
            {/*
              A full reload rather than a router navigation: whatever state
              produced the exception is still in memory, and going "home" inside
              the same tree can walk straight back into it.
            */}
            <button
              type="button"
              className="button"
              onClick={() => {
                window.location.href = '/'
              }}
            >
              Reload the console
            </button>
          </div>
        </div>
      </div>
    )
  }
}
