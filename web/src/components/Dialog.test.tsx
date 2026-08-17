import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { ConfirmDialog } from './ConfirmDialog'
import { Dialog } from './Dialog'

/**
 * Dialog accessibility.
 *
 * These are the assertions that make a modal usable without a mouse, and every
 * one of them is a thing that silently regresses: focus that never enters,
 * focus that escapes to the page behind, focus that is dropped on close. None
 * shows up in a screenshot, so they are pinned here.
 */

function Harness({ dismissible = true }: { dismissible?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button type="button" onClick={() => setOpen(true)}>
        Open dialog
      </button>
      <button type="button">Behind the dialog</button>
      <Dialog
        open={open}
        title="Edit terminal"
        description="Change what this terminal is for."
        dismissible={dismissible}
        onClose={() => setOpen(false)}
        footer={
          <>
            <button type="button" onClick={() => setOpen(false)}>
              Cancel
            </button>
            <button type="button">Save</button>
          </>
        }
      >
        <input aria-label="Terminal name" />
      </Dialog>
    </div>
  )
}

describe('Dialog', () => {
  it('announces itself as a modal with an accessible name and description', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Open dialog' }))

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAccessibleName('Edit terminal')
    expect(dialog).toHaveAccessibleDescription('Change what this terminal is for.')
  })

  it('moves focus into the dialog when it opens', async () => {
    // A modal that leaves focus behind it does not exist for a keyboard user.
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Open dialog' }))

    await waitFor(() => {
      expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
    })
  })

  it('traps Tab inside the dialog, cycling at both ends', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Open dialog' }))

    const dialog = screen.getByRole('dialog')
    const save = screen.getByRole('button', { name: 'Save' })

    // Walk forward past the last control; focus must come back to the first.
    for (let step = 0; step < 8; step += 1) {
      await user.tab()
      expect(dialog.contains(document.activeElement)).toBe(true)
    }

    // And backwards from the first.
    save.focus()
    await user.tab()
    expect(dialog.contains(document.activeElement)).toBe(true)
    await user.tab({ shift: true })
    expect(dialog.contains(document.activeElement)).toBe(true)
  })

  it('returns focus to whatever opened it', async () => {
    // Losing it drops a keyboard user at the top of the document.
    const user = userEvent.setup()
    render(<Harness />)
    const opener = screen.getByRole('button', { name: 'Open dialog' })

    await user.click(opener)
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    await waitFor(() => expect(opener).toHaveFocus())
  })

  it('closes on Escape and on a backdrop click', async () => {
    const user = userEvent.setup()

    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Open dialog' }))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: 'Open dialog' }))
    await user.click(document.querySelector('.dialog-layer') as HTMLElement)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('does not close on a click that merely ENDED on the backdrop', async () => {
    // Releasing a text selection that began inside the panel must not discard
    // whatever was being typed.
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Open dialog' }))

    await user.click(screen.getByRole('dialog'))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('refuses to dismiss while an action is in flight', async () => {
    const user = userEvent.setup()
    render(<Harness dismissible={false} />)
    await user.click(screen.getByRole('button', { name: 'Open dialog' }))

    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // And offers no close affordance that would contradict that.
    expect(screen.queryByRole('button', { name: 'Close dialog' })).not.toBeInTheDocument()
  })

  it('holds the page still behind it', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    await user.click(screen.getByRole('button', { name: 'Open dialog' }))
    expect(document.body.style.overflow).toBe('hidden')

    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.body.style.overflow).not.toBe('hidden'))
  })
})

describe('ConfirmDialog', () => {
  function confirmProps(overrides: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
    return {
      open: true,
      title: 'Remove this person?',
      consequence:
        'Every terminal in the company will be told to forget them and their credential.',
      onConfirm: vi.fn(),
      onClose: vi.fn(),
      ...overrides,
    }
  }

  it('states the consequence, not just the question', async () => {
    // "Are you sure?" tells an operator nothing they did not already know.
    render(<ConfirmDialog {...confirmProps()} />)
    expect(
      screen.getByText(/Every terminal in the company will be told to forget them/),
    ).toBeInTheDocument()
  })

  it('runs the action and closes on success', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn().mockResolvedValue(undefined)
    const onClose = vi.fn()
    render(<ConfirmDialog {...confirmProps({ onConfirm, onClose })} />)

    await user.click(screen.getByRole('button', { name: 'Confirm' }))

    await waitFor(() => expect(onConfirm).toHaveBeenCalledOnce())
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
  })

  it('STAYS OPEN and reports the failure when the action rejects', async () => {
    // Closing here would leave the operator believing a destructive action
    // succeeded, which is the worst available outcome.
    const user = userEvent.setup()
    const onConfirm = vi.fn().mockRejectedValue(new Error('Terminal is unreachable'))
    const onClose = vi.fn()
    render(<ConfirmDialog {...confirmProps({ onConfirm, onClose })} />)

    await user.click(screen.getByRole('button', { name: 'Confirm' }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Terminal is unreachable'))
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('requires the exact phrase when one is demanded', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn().mockResolvedValue(undefined)
    render(<ConfirmDialog {...confirmProps({ onConfirm, confirmPhrase: 'AT-0001' })} />)

    const confirm = screen.getByRole('button', { name: 'Confirm' })
    expect(confirm).toBeDisabled()

    await user.type(screen.getByRole('textbox'), 'AT-0002')
    expect(confirm).toBeDisabled()

    await user.clear(screen.getByRole('textbox'))
    await user.type(screen.getByRole('textbox'), 'AT-0001')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    await waitFor(() => expect(onConfirm).toHaveBeenCalledOnce())
  })
})
