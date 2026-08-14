import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { CheckboxField, CheckboxGroup, FormError, SelectField, TextField } from './Form'
import { submitErrorMessage, useForm, validators } from './useForm'

/**
 * Form primitives.
 *
 * The assertions are mostly about WIRING — that a label names its control, that
 * an error is announced and marks the field invalid, that a required field says
 * so to a screen reader rather than only drawing an asterisk. Getting this wrong
 * is the most common accessibility failure in an admin console and is invisible
 * without testing for it.
 */

describe('TextField', () => {
  it('binds its label, hint and error to the control', () => {
    render(
      <TextField
        label="Full name"
        value="Ada"
        onChange={() => {}}
        hint="As it should appear on reports."
        error="Full name is required."
        required
      />,
    )

    const input = screen.getByLabelText(/Full name/)
    expect(input).toHaveValue('Ada')
    expect(input).toHaveAccessibleDescription(/As it should appear on reports\./)
    expect(input).toHaveAccessibleDescription(/Full name is required\./)
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAttribute('aria-required', 'true')
  })

  it('announces the error politely rather than only painting it', () => {
    // Polite, not assertive: a validation message should be read at a pause
    // rather than cutting across whatever the reader is hearing. role="alert"
    // is reserved for a form-level submission failure.
    const { container } = render(
      <TextField label="Email" value="" onChange={() => {}} error="Email is required." />,
    )

    const region = container.querySelector('[aria-live="polite"]')
    expect(region).toHaveTextContent('Email is required.')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('marks a required field for screen readers, not only with an asterisk', () => {
    render(<TextField label="Serial" value="" onChange={() => {}} required />)
    expect(screen.getByText('(required)')).toBeInTheDocument()
  })

  it('renders a multi-line control when asked', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<TextField label="Settings" value="{}" onChange={onChange} multiline mono />)

    const textarea = screen.getByLabelText('Settings')
    expect(textarea.tagName).toBe('TEXTAREA')
    await user.type(textarea, 'x')
    expect(onChange).toHaveBeenCalled()
  })
})

describe('SelectField', () => {
  it('explains the selected option where a choice needs explaining', async () => {
    const user = userEvent.setup()

    function Harness() {
      const [value, setValue] = useState('MULTI_PURPOSE')
      return (
        <SelectField
          label="Application mode"
          value={value}
          onChange={setValue}
          options={[
            {
              value: 'MULTI_PURPOSE',
              label: 'Multi-purpose',
              description: 'Serves whatever this company has enabled.',
            },
            {
              value: 'ATTENDANCE',
              label: 'Attendance',
              description: 'Records presence against a schedule.',
            },
          ]}
        />
      )
    }

    render(<Harness />)
    expect(screen.getByText('Serves whatever this company has enabled.')).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('Application mode'), 'ATTENDANCE')
    expect(screen.getByText('Records presence against a schedule.')).toBeInTheDocument()
  })
})

describe('CheckboxField and CheckboxGroup', () => {
  it('toggles a single checkbox by its label', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<CheckboxField label="Active" checked={false} onChange={onChange} />)

    await user.click(screen.getByLabelText('Active'))
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('groups multi-select options under one legend', async () => {
    // A label may only name one control, so a fieldset is what makes site
    // grants announce as a set rather than as unrelated checkboxes.
    const user = userEvent.setup()
    const onChange = vi.fn()

    render(
      <CheckboxGroup
        legend="Sites this operator may act on"
        hint="Selecting none means every site in the company."
        options={[
          { value: 'site-a', label: 'Lagos Depot' },
          { value: 'site-b', label: 'Abuja Depot' },
        ]}
        selected={['site-a']}
        onChange={onChange}
      />,
    )

    const group = screen.getByRole('group', { name: 'Sites this operator may act on' })
    expect(group).toHaveAccessibleDescription(/Selecting none means every site/)
    expect(screen.getByLabelText('Lagos Depot')).toBeChecked()

    await user.click(screen.getByLabelText('Abuja Depot'))
    expect(onChange).toHaveBeenCalledWith(['site-a', 'site-b'])
  })

  it('says so when there is nothing to choose from', () => {
    render(
      <CheckboxGroup
        legend="Sites"
        options={[]}
        selected={[]}
        onChange={() => {}}
        empty="This company has no sites yet."
      />,
    )
    expect(screen.getByText('This company has no sites yet.')).toBeInTheDocument()
  })
})

describe('FormError', () => {
  it('renders nothing when there is no failure', () => {
    const { container } = render(<FormError message={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('announces the failure and shows the request id', () => {
    render(<FormError message="That email address is already in use" requestId="req-7" />)
    expect(screen.getByRole('alert')).toHaveTextContent('That email address is already in use')
    expect(screen.getByText('req-7')).toBeInTheDocument()
  })
})

describe('useForm', () => {
  interface Values extends Record<string, unknown> {
    email: string
    name: string
  }

  function Harness({ onSubmit }: { onSubmit: (values: Values) => Promise<unknown> | unknown }) {
    const form = useForm<Values>({
      initialValues: { email: '', name: '' },
      validate: (values) => ({
        email: validators.email(values.email),
        name: validators.required(values.name, 'Name'),
      }),
      onSubmit,
    })

    return (
      <form onSubmit={(event) => void form.handleSubmit(event)}>
        <TextField label="Email" {...textProps(form.fieldProps('email'))} />
        <TextField label="Name" {...textProps(form.fieldProps('name'))} />
        <FormError message={submitErrorMessage(form.submitError)} />
        <button type="submit" disabled={form.submitting}>
          Save
        </button>
        <span data-testid="dirty">{String(form.dirty)}</span>
      </form>
    )
  }

  /** Narrows fieldProps to what a TextField takes, for a string-valued field. */
  function textProps(props: {
    value: string
    error: string | undefined
    onChange: (value: string) => void
    onBlur: () => void
  }) {
    return props
  }

  it('does not complain before a field has been touched', async () => {
    const user = userEvent.setup()
    render(<Harness onSubmit={vi.fn()} />)

    // Telling somebody their email is invalid before they have finished typing
    // the local part is hostile.
    await user.type(screen.getByLabelText('Email'), 'a')
    expect(screen.queryByText(/does not look like an email/)).not.toBeInTheDocument()
  })

  it('complains once the field is blurred', async () => {
    const user = userEvent.setup()
    render(<Harness onSubmit={vi.fn()} />)

    await user.type(screen.getByLabelText('Email'), 'not-an-email')
    await user.tab()

    expect(await screen.findByText(/does not look like an email/)).toBeInTheDocument()
  })

  it('reveals EVERY reason on a failed submit, not one at a time', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    render(<Harness onSubmit={onSubmit} />)

    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText(/Email is required/)).toBeInTheDocument()
    expect(screen.getByText(/Name is required/)).toBeInTheDocument()
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('submits when valid', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    render(<Harness onSubmit={onSubmit} />)

    await user.type(screen.getByLabelText('Email'), 'ops@example.com')
    await user.type(screen.getByLabelText('Name'), 'Ada')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({ email: 'ops@example.com', name: 'Ada' }),
    )
  })

  it('keeps a SERVER rejection apart from field validation, and clears it on edit', async () => {
    // A 409 on a duplicate id is an answer the client could not have known in
    // advance. It belongs at form level, and it must not survive a correction.
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockRejectedValue(new Error('That email address is already in use'))
    render(<Harness onSubmit={onSubmit} />)

    await user.type(screen.getByLabelText('Email'), 'taken@example.com')
    await user.type(screen.getByLabelText('Name'), 'Ada')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'That email address is already in use',
    )

    await user.type(screen.getByLabelText('Email'), 'x')
    await waitFor(() =>
      expect(screen.queryByText('That email address is already in use')).not.toBeInTheDocument(),
    )
  })

  it('tracks whether anything has changed', async () => {
    const user = userEvent.setup()
    render(<Harness onSubmit={vi.fn()} />)

    expect(screen.getByTestId('dirty')).toHaveTextContent('false')
    await user.type(screen.getByLabelText('Name'), 'A')
    expect(screen.getByTestId('dirty')).toHaveTextContent('true')
  })
})

describe('validators', () => {
  it('treats whitespace as absent', () => {
    expect(validators.required('   ', 'Name')).toMatch(/required/)
    expect(validators.required('Ada', 'Name')).toBeUndefined()
  })

  it('is permissive about email, because the server is the authority', () => {
    // A stricter pattern only rejects addresses that would have worked.
    expect(validators.email('ops+tag@sub.example.co.uk')).toBeUndefined()
    expect(validators.email('nope')).toMatch(/does not look like/)
  })

  it('bounds length in both directions', () => {
    expect(validators.minLength('abc', 8, 'Password')).toMatch(/at least 8/)
    expect(validators.maxLength('abcdef', 3, 'Code')).toMatch(/3 characters or fewer/)
  })
})
