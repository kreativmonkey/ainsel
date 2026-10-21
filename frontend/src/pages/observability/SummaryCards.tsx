import { NavLink } from 'react-router-dom'
import type {
  ObservabilitySummary,
  Range,
  TokensSummary,
} from '../../api/observability'
import { Sparkbar } from '../../primitives/Sparkbar'
import { errorsLink, eventsLink, routingLink, tokensLink } from '../../utils/obsLinks'
import { abbreviateNumber, formatPercent, formatUSD } from '../../utils/format'

/** Prompt-cache hit rate: cache reads over every prompt token the model saw.
 *  Pi's usage.input excludes cache traffic, so the denominator is
 *  input + cacheRead + cacheWrite. A sudden drop means something is
 *  invalidating the prompt prefix — the same work re-billed at the full
 *  input rate (issue #281). Undefined when no prompt tokens were seen, and
 *  when the hub says cache was never reported in the window (issue #75) —
 *  a silent upstream is not a measured 0% and must not render as one. */
function cacheHitRate(tokens?: TokensSummary): number | undefined {
  if (!tokens) return undefined
  if (tokens.cacheReported === false) return undefined
  const input = tokens.inputTokens ?? 0
  const cacheRead = tokens.cacheReadTokens ?? 0
  const cacheWrite = tokens.cacheWriteTokens ?? 0
  const promptTotal = input + cacheRead + cacheWrite
  return promptTotal > 0 ? cacheRead / promptTotal : undefined
}

type Card = {
  key: string
  label: string
  code: string
  value: number | undefined
  formatter: (n: number) => string
  sub: string
  alert?: boolean
  to?: string
  spark?: number[]
}

type SummaryCardsProps = {
  summary?: ObservabilitySummary
  tokens?: TokensSummary
  range?: Range
}

export function SummaryCards({ summary, tokens, range }: SummaryCardsProps) {
  const rangeSuffix = range ? ` · ${range}` : ''
  // Presence flags (issue #75). A missing flag is a hub older than the
  // flags: unknown, so keep the legacy rendering rather than claiming
  // "not reported". A false flag means the window never carried the figure:
  // the numerics are placeholders, so the tile shows '—' with a "not
  // reported" sub-line instead of a measured 0% / $0.00.
  const cacheNotReported = tokens?.cacheReported === false
  const costNotReported = tokens?.costReported === false
  const cards: Card[] = [
    {
      key: 'consumed',
      label: `Events Consumed${rangeSuffix}`,
      code: 'EV·01',
      value: summary?.eventsConsumed,
      formatter: (n) => abbreviateNumber(n),
      sub: '',
      to: eventsLink({ range }),
      spark: [4, 5, 6, 7, 8, 9],
    },
    {
      key: 'matched',
      label: `Triggers Matched${rangeSuffix}`,
      code: 'EV·02',
      value: summary?.triggersMatched,
      formatter: (n) => abbreviateNumber(n),
      sub: '',
      to: routingLink({ range }),
      spark: [3, 4, 5, 5, 6, 7],
    },
    {
      key: 'routed',
      label: `Events Routed${rangeSuffix}`,
      code: 'EV·03',
      value: summary?.eventsRouted,
      formatter: (n) => abbreviateNumber(n),
      sub: '',
      to: routingLink({ range }),
      spark: [3, 4, 4, 5, 6, 7],
    },
    {
      key: 'errors',
      label: `Errors${rangeSuffix}`,
      code: 'ER·04',
      value: summary?.routingErrors,
      formatter: (n) => abbreviateNumber(n),
      sub: '',
      to: errorsLink({ range }),
      alert: (summary?.routingErrors ?? 0) > 0,
      spark: [0, 1, 0, 2, 1, summary?.routingErrors ?? 0],
    },
    {
      key: 'tokens',
      label: `Tokens${rangeSuffix}`,
      code: 'TK·05',
      value: tokens?.totalTokens,
      formatter: (n) => abbreviateNumber(n),
      sub: '',
      to: tokensLink({ range }),
      spark: [3, 4, 5, 4, 6, 7],
    },
    {
      key: 'cache-hit',
      label: `Cache Hit Rate${rangeSuffix}`,
      code: 'TK·06',
      value: cacheHitRate(tokens),
      formatter: (n) => formatPercent(n, 0),
      sub: cacheNotReported
        ? 'not reported by the provider'
        : 'a drop re-bills the prompt prefix',
    },
    {
      key: 'cost',
      label: `Cost${rangeSuffix}`,
      code: 'TK·07',
      value: costNotReported ? undefined : tokens?.costUsd,
      formatter: (n) => formatUSD(n),
      sub: costNotReported
        ? 'not reported — model unpriced'
        : 'needs spec.llm.cost pricing',
      to: tokensLink({ range }),
    },
  ]

  return (
    <div className="kpi-row" style={{ gridTemplateColumns: 'repeat(7, 1fr)' }}>
      {cards.map((c) => {
        const inner = (
          <>
            <div className="hd">
              <span className="label">{c.label}</span>
              <span className="code">{c.code}</span>
            </div>
            <div className="figure num">
              {c.value !== undefined ? c.formatter(c.value) : '—'}
            </div>
            {c.spark ? <Sparkbar values={c.spark} alert={c.alert} /> : null}
            <div className="sub">
              <span>{c.sub}</span>
            </div>
          </>
        )
        const className = c.alert ? 'kpi alert' : 'kpi'
        return c.to ? (
          <NavLink key={c.key} to={c.to} className={className}>
            {inner}
          </NavLink>
        ) : (
          <div key={c.key} className={className} style={{ cursor: 'default' }}>
            {inner}
          </div>
        )
      })}
    </div>
  )
}
