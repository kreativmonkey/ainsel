import { useMemo } from 'react'
import type { TokensSubjectRow } from '../../api/observability'
import { RegisterTable, type Column } from '../../primitives/RegisterTable'
import { NotReported } from '../../primitives/NotReported'
import { Tag } from '../../primitives/Tag'
import { abbreviateNumber, formatUSD } from '../../utils/format'

type TokensTableProps = { rows: TokensSubjectRow[] }

const columns: readonly Column<TokensSubjectRow>[] = [
  {
    key: 'repo',
    header: 'Repo / Event',
    // Records rows written before migration 0024 carry an empty subject; they
    // render as "—" rather than a blank cell (issue #411).
    cell: (r) => (
      <>
        <b>{r.repo || '—'}</b>
        {r.eventType ? (
          <>
            {' '}
            <Tag>{r.eventType}</Tag>
          </>
        ) : null}
      </>
    ),
  },
  { key: 'agent', header: 'Agent', width: 160, cell: (r) => r.agentName ?? r.agent },
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
  // excludes it) and is priced differently from input, so it gets its own
  // columns — without them the visible columns do not sum to the total
  // (issue #281). A row whose cache was never reported renders "not
  // reported", not 0: the summed zero there is a placeholder, not a
  // measurement (issue #75). A missing flag is a hub older than the flags —
  // unknown, so the legacy rendering stays.
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
]

export function TokensTable({ rows }: TokensTableProps) {
  const sorted = useMemo(
    () => [...rows].sort((a, b) => b.totalTokens - a.totalTokens),
    [rows],
  )
  return (
    <RegisterTable
      rows={sorted}
      columns={columns}
      rowKey={(r) => `${r.agent}|${r.repo ?? ''}|${r.eventType ?? ''}|${r.model ?? ''}`}
      emptyLabel="No token consumption recorded for this range."
    />
  )
}
