import { render, screen } from '@testing-library/react'
import { NotReported } from './NotReported'

describe('NotReported', () => {
  it('renders the default marker', () => {
    render(<NotReported />)
    expect(screen.getByText('not reported')).toBeInTheDocument()
  })

  it('renders a custom label in the label class', () => {
    render(<NotReported label="not reported — model unpriced" />)
    const el = screen.getByText('not reported — model unpriced')
    expect(el).toHaveClass('label')
  })
})
