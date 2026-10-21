import { useMemo } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ServiceUnavailableError } from '../../../api/client'
import {
  metricsSourceLabel,
  unavailableDetail,
  useTokensBySubject,
  useTokensLedger,
} from '../../../api/observability'
import type { TokensSubjectRow, Range } from '../../../api/observability'
import { Titleblock } from '../../../layout/Titleblock'
import { Panel } from '../../../primitives/Panel'
import { RegisterTable, type Column } from '../../../primitives/RegisterTable'
import { NotReported } from '../../../primitives/NotReported'
import { Tag } from '../../../primitives/Tag'
import { SectionStatus } from '../../../primitives/SectionStatus'
import { RangeSelector } from '../RangeSelector'
import { abbreviateNumber, formatUSD } from '../../../utils/format'
import { eventsLink } from '../../../utils/obsLinks'

const VALID_RANGES = ['1h', '6h', '24h', '7d'] as const
const isRange = (s: string | null): s is Range =>
  s !== null && (VALID_RANGES as readonly string[]).includes(s)

export function TokensDetail() {
  const [params, setParams] = useSearchParams()
  const range: Range = isRange(params.get('range')) ? (params.get('range') as Range) : '24h'

  const setRange = (next: Range) =>
    setParams((prev) => { const p = new URLSearchParams(prev); p.set('range', next); return p }, { replace: true })

  const allTime = useTokensLedger()
  const bySubject = useTokensBySubject(range)

  const rows: TokensSubjectRow[] = useMemo(
    () => [...(bySubject.data?.rows ?? [])].sort((a, b) => b.totalTokens - a.totalTokens),
    [bySubject.data],
  )

  const totals = useMemo(() => {
    const input = rows.reduce((s, r) => s + r.inputTokens, 0)
    const output = rows.reduce((s, r) => s + r.outputTokens, 0)
    const cacheRead = rows.reduce((s, r) => s + (r.cacheReadTokens ?? 0), 0)
    const cacheWrite = rows.reduce((s, r) => s + (r.cacheWriteTokens ?? 0), 0)
    const costUsd = rows.reduce((s, r) => s + (r.costUsd ?? 0), 0)
    // Window-level presence for the cost tile (issue #75): reported when any
    // row reports; when no row carries the flag the hub predates it, so the
    // state is unknown and the legacy rendering stays.
    const anyCostFlag = rows.some((r) => r.costReported !== undefined)
    const costReported = rows.some((r) => r.costReported === true)
    return { input, output, cacheRead, cacheWrite, costUsd, costNotReported: anyCostFlag && !costReported }
  }, [rows])

  const columns: readonly Column<TokensSubjectRow>[] = [
    {
      key: 'repo',
      header: 'Repo / Event',
      cell: (r) => (
        <>
          <b>{r.repo || '—'}</b>
          {r.eventType ? <> <Tag>{r.eventType}</Tag></> : null}
        </>
      ),
    },
    { key: 'agent', header: 'Agent', width: 160, cell: (r) => r.agent },
    {
      key: 'model',
      header: 'Model',
      width: 160,
      cell: (r) => <span className="num">{r.model ?? '—'}</span>,
    },
    {
      key: 'input',
      header: 'Input',
      width: 90,
      align: 'right',
      cell: (r) => <span className="num">{abbreviateNumber(r.inputTokens)}</span>,
    },
    {
      key: 'output',
      header: 'Output',
      width: 90,
      align: 'right',
      cell: (r) => <span className="num">{abbreviateNumber(r.outputTokens)}</span>,
    },
    // Cache traffic is the bulk of an agent loop's prompt (pi's usage.input
    // excludes it) and is priced differently from input. Without these
    // columns the visible ones do not sum to the displayed Total. A row whose
    // cache was never reported renders "not reported", not 0 (issue #75).
    {
      key: 'cacheRead',
      header: 'Cache R',
      width: 90,
      align: 'right',
      cell: (r) =>
        r.cacheReported === false ? (
          <NotReported />
        ) : (
          <span className="num">{abbreviateNumber(r.cacheReadTokens ?? 0)}</span>
        ),
    },
    {
      key: 'cacheWrite',
      header: 'Cache W',
      width: 90,
      align: 'right',
      cell: (r) =>
        r.cacheReported === false ? (
          <NotReported />
        ) : (
          <span className="num">{abbreviateNumber(r.cacheWriteTokens ?? 0)}</span>
        ),
    },
    {
      key: 'ratio',
      header: 'I/O',
      width: 70,
      align: 'right',
      cell: (r) =>
        r.outputTokens > 0 ? (
          <span className="num">{(r.inputTokens / r.outputTokens).toFixed(1)}×</span>
        ) : (
          <span className="label">—</span>
        ),
    },
    {
      key: 'total',
      header: 'Total',
      width: 90,
      align: 'right',
      cell: (r) => <span className="num">{abbreviateNumber(r.totalTokens)}</span>,
    },
    {
      key: 'cost',
      header: 'Cost',
      width: 90,
      align: 'right',
      // An unpriced model reports nothing — "not reported", not $0.00 (issue #75).
      cell: (r) =>
        r.costReported === false ? (
          <NotReported />
        ) : (
          <span className="num">{formatUSD(r.costUsd ?? 0)}</span>
        ),
    },
    {
      key: 'events',
      header: '',
      width: 120,
      align: 'right',
      cell: (r) => (
        <Link
          to={eventsLink({ agent: r.agent, range })}
          className="label"
          style={{ color: 'var(--ink-3)', textDecoration: 'none' }}
          onClick={(e) => e.stopPropagation()}
        >
          view events →
        </Link>
      ),
    },
  ]

  return (
    <>
      <Titleblock
        crumbs={
          <>
            Operations / <Link to="/observability">Observability</Link> / <b>Tokens</b>
          </>
        }
        title={<>Token <em>Detail</em></>}
        actions={<RangeSelector value={range} onChange={setRange} />}
      />
      <div style={{ padding: '28px 32px', display: 'grid', gap: 24 }}>
        <div className="kpi-row" style={{ gridTemplateColumns: 'repeat(4, 1fr)' }}>
          <div className="kpi">
            <div className="hd">
              <span className="label">Total Tokens (all time)</span>
            </div>
            <div className="figure num">
              {allTime.data?.total ? abbreviateNumber(allTime.data.total.totalTokens) : '—'}
            </div>
          </div>
          <div className="kpi">
            <div className="hd">
              <span className="label">Input · {range}</span>
            </div>
            <div className="figure num">{abbreviateNumber(totals.input)}</div>
          </div>
          <div className="kpi">
            <div className="hd">
              <span className="label">Output · {range}</span>
            </div>
            <div className="figure num">{abbreviateNumber(totals.output)}</div>
          </div>
          <div className="kpi">
            <div className="hd">
              <span className="label">Cost · {range}</span>
            </div>
            <div className="figure num">
              {totals.costNotReported ? <NotReported /> : formatUSD(totals.costUsd)}
            </div>
          </div>
        </div>

        <Panel
          title={`By Agent · ${range}`}
          right={
            <span className="label">{metricsSourceLabel(bySubject.data?.source) ?? ''}</span>
          }
          className="cropped"
        >
          {bySubject.isLoading ? (
            <SectionStatus state="loading" />
          ) : bySubject.error ? (
            <SectionStatus
              state={bySubject.error instanceof ServiceUnavailableError ? 'unavailable' : 'error'}
              title={
                bySubject.error instanceof ServiceUnavailableError
                  ? 'No metrics source configured'
                  : undefined
              }
              detail={unavailableDetail(bySubject.error)}
              onRetry={() => bySubject.refetch()}
            />
          ) : (
            <RegisterTable
              rows={rows}
              columns={columns}
              rowKey={(r) => `${r.agent}|${r.repo ?? ''}|${r.eventType ?? ''}|${r.model ?? ''}`}
              emptyLabel="No token consumption recorded for this range."
            />
          )}
        </Panel>
      </div>
    </>
  )
}
