/**
 * NotReported renders the "not reported" marker for a figure the upstream
 * never measured (issue #75). The token/cost surfaces must never show an
 * absent measurement as a real 0 / 0% / $0.00 — a silent provider reads as
 * "caching is broken" or "these runs were free" unless it says so itself.
 *
 * Rendered in the `label` class so it reads as a qualifier, not a value.
 */
type NotReportedProps = {
  label?: string
}

export function NotReported({ label = 'not reported' }: NotReportedProps) {
  return <span className="label">{label}</span>
}
