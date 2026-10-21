import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Observability } from './Observability'
import { renderWithProviders } from '../../test/renderWithProviders'

function timeseriesPayload(metric: string) {
  return {
    metric,
    range: '24h',
    step: '1h',
    points: Array.from({ length: 24 }, (_, i) => ({
      timestamp: new Date(Date.now() - (23 - i) * 3600_000).toISOString(),
      value: 10 + i,
    })),
  }
}

function defaultFetch(url: string): Response {
  if (url.includes('/observability/metrics/summary')) {
    return new Response(
      JSON.stringify({
        eventsConsumed: 1234,
        triggersMatched: 1100,
        eventsRouted: 1050,
        routingErrors: 3,
        updatedAt: '2026-05-21T00:00:00Z',
      }),
      { status: 200 },
    )
  }
  if (url.includes('/observability/metrics/tokens/summary')) {
    return new Response(
      JSON.stringify({
        totalTokens: 1_240_000,
        inputTokens: 820_000,
        outputTokens: 420_000,
        cacheReadTokens: 8_000_000,
        cacheWriteTokens: 1_000_000,
        costUsd: 12.34,
      }),
      { status: 200 },
    )
  }
  if (url.includes('/observability/metrics/tokens/by-subject')) {
    return new Response(
      JSON.stringify({
        range: '24h',
        // Records-first since issue #411: a hub with a database answers this
        // panel from its own conversation rows by default.
        source: 'postgres',
        rows: [
          {
            agent: 'doc-writer',
            agentName: 'Doc Writer',
            repo: 'k-weber/insel',
            eventType: 'pull_request.opened',
            model: 'gpt-4',
            inputTokens: 500_000,
            outputTokens: 300_000,
            cacheReadTokens: 6_000_000,
            cacheWriteTokens: 100_000,
            totalTokens: 6_900_000,
            costUsd: 4.5,
          },
          {
            agent: 'triage-bot',
            agentName: 'Triage Bot',
            repo: 'k-weber/insel',
            model: 'claude-3',
            inputTokens: 200_000,
            outputTokens: 100_000,
            totalTokens: 300_000,
          },
        ],
      }),
      { status: 200 },
    )
  }
  if (url.includes('/observability/metrics/timeseries')) {
    const m =
      new URL(url, 'http://x/').searchParams.get('metric') ?? 'events_routed'
    return new Response(JSON.stringify(timeseriesPayload(m)), { status: 200 })
  }
  if (url.includes('/observability/logs')) {
    const logs = [
      {
        timestamp: new Date(Date.now() - 30_000).toISOString(),
        message: 'request handled',
        agentName: 'Doc Writer',
        labels: { app: 'hub', agent: 'doc-writer', level: 'info' },
      },
    ]
    return new Response(
      JSON.stringify({ logs, total: logs.length, query: '' }),
      { status: 200 },
    )
  }
  return new Response('{}', { status: 200 })
}

describe('Observability', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve(defaultFetch(url))))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // SearchSpy records the current location.search so tests can assert that
  // LogStream filters persist in (and clean up after themselves in) the URL.
  function renderObservability(route: string) {
    const search = { current: '' }
    function SearchSpy() {
      search.current = useLocation().search
      return null
    }
    const utils = renderWithProviders(
      <>
        <Observability />
        <SearchSpy />
      </>,
      { route },
    )
    return { search, ...utils }
  }

  it('renders summary cards with token total', async () => {
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() => expect(screen.getByText('1.2K')).toBeInTheDocument())
    expect(screen.getByText('1.2M')).toBeInTheDocument() // token total
  })

  it('renders the cache hit rate and cost tiles from the token summary', async () => {
    // Cache hit rate = cacheRead / (input + cacheRead + cacheWrite) =
    // 8M / (820k + 8M + 1M) ≈ 81%. A drop means the prompt prefix is being
    // invalidated and re-billed at the full input rate (issue #281).
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() => expect(screen.getByText(/Cache Hit Rate/)).toBeInTheDocument())
    expect(screen.getByText('81%')).toBeInTheDocument()
    expect(screen.getByText('$12.34')).toBeInTheDocument()
  })

  it('says "not reported" instead of 0% / $0.00 when the hub reports nothing', async () => {
    // Issue #75: a window whose runtime never reported cache or cost must not
    // render a measured-looking 0% or $0.00 — the numerics there are
    // placeholders, and the tiles must say "not reported" instead.
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/observability/metrics/tokens/summary')) {
        return Promise.resolve(new Response(
          JSON.stringify({
            totalTokens: 1200,
            inputTokens: 1000,
            outputTokens: 200,
            cacheReadTokens: 0,
            cacheWriteTokens: 0,
            costUsd: 0,
            cacheReported: false,
            costReported: false,
          }),
          { status: 200 },
        ))
      }
      return Promise.resolve(defaultFetch(url))
    }))
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() => expect(screen.getByText(/Cache Hit Rate/)).toBeInTheDocument())
    expect(screen.getByText('not reported by the provider')).toBeInTheDocument()
    expect(screen.getByText('not reported — model unpriced')).toBeInTheDocument()
    // The placeholder zeros must not appear as measured figures.
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
    expect(screen.queryByText('$0.00')).not.toBeInTheDocument()
  })

  it('renders cache and cost columns in the tokens table', async () => {
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() =>
      expect(screen.getByRole('cell', { name: 'Doc Writer' })).toBeInTheDocument(),
    )
    // 6M cache reads and $4.50 for the doc-writer row — the visible columns
    // must sum to the displayed total.
    expect(screen.getByRole('cell', { name: '6M' })).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '$4.50' })).toBeInTheDocument()
  })

  it('renders the chart svg with 4 series paths', async () => {
    const { container } = renderWithProviders(<Observability />, {
      route: '/observability',
    })
    await waitFor(() => {
      const paths = container.querySelectorAll('svg path')
      expect(paths.length).toBe(4)
    })
  })

  it('renders the tokens table with rows sorted by total descending', async () => {
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() =>
      expect(screen.getByRole('cell', { name: 'Doc Writer' })).toBeInTheDocument(),
    )
    expect(screen.getByRole('cell', { name: 'Triage Bot' })).toBeInTheDocument()
  })

  it('changes URL when a new range is selected', async () => {
    let currentSearch = ''
    function Spy() {
      currentSearch = useLocation().search
      return null
    }
    function wrap(children: ReactNode) {
      return renderWithProviders(
        <Routes>
          <Route
            path="/observability"
            element={
              <>
                {children}
                <Spy />
              </>
            }
          />
        </Routes>,
        { route: '/observability' },
      )
    }
    wrap(<Observability />)
    await waitFor(() =>
      expect(screen.getByRole('cell', { name: 'Doc Writer' })).toBeInTheDocument(),
    )
    await userEvent.click(screen.getByRole('button', { name: '7d' }))
    await waitFor(() => expect(currentSearch).toContain('range=7d'))
  })

  it('renders range suffix on summary card labels', async () => {
    renderWithProviders(<Observability />, { route: '/observability?range=6h' })
    await waitFor(() => expect(screen.getByText('1.2K')).toBeInTheDocument())
    // All five cards should carry the range suffix.
    expect(screen.getByText(/Events Consumed · 6h/)).toBeInTheDocument()
    expect(screen.getByText(/Triggers Matched · 6h/)).toBeInTheDocument()
    expect(screen.getByText(/Events Routed · 6h/)).toBeInTheDocument()
    expect(screen.getByText(/Errors · 6h/)).toBeInTheDocument()
    expect(screen.getByText(/Tokens · 6h/)).toBeInTheDocument()
  })

  it('links summary cards to the detail views pre-filtered to the same range', async () => {
    // Regression guard for #423: the card hrefs must be built with the
    // obsLinks helpers so each detail view opens on the same range the
    // summary was rendered for, not on hardcoded paths.
    renderWithProviders(<Observability />, { route: '/observability?range=6h' })
    await waitFor(() => expect(screen.getByText('1.2K')).toBeInTheDocument())
    expect(
      screen.getByRole('link', { name: /Events Consumed · 6h/ }),
    ).toHaveAttribute('href', '/observability/events?range=6h')
    expect(
      screen.getByRole('link', { name: /Triggers Matched · 6h/ }),
    ).toHaveAttribute('href', '/observability/routing?range=6h')
    expect(
      screen.getByRole('link', { name: /Events Routed · 6h/ }),
    ).toHaveAttribute('href', '/observability/routing?range=6h')
    expect(
      screen.getByRole('link', { name: /Errors · 6h/ }),
    ).toHaveAttribute('href', '/observability/errors?range=6h')
    expect(
      screen.getByRole('link', { name: /Tokens · 6h/ }),
    ).toHaveAttribute('href', '/observability/tokens?range=6h')
    expect(
      screen.getByRole('link', { name: /Cost · 6h/ }),
    ).toHaveAttribute('href', '/observability/tokens?range=6h')
  })

  it('falls back to unavailable when summary returns 503', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url.includes('/observability/metrics/summary')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: 'no metrics backend: set observability.prometheus.url',
              }),
              { status: 503, headers: { 'Content-Type': 'application/json' } },
            ),
          )
        }
        return Promise.resolve(defaultFetch(url))
      }),
    )
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() =>
      expect(screen.getByText('No metrics source configured')).toBeInTheDocument(),
    )
    // The hub's reason is shown, not a claim about telemetry in general.
    expect(
      screen.getByText('no metrics backend: set observability.prometheus.url'),
    ).toBeInTheDocument()
  })

  it('labels the token table with the backend that answered', async () => {
    // The two backends differ in coverage — a scale-to-zero agent has rows
    // even when it was never scraped — so the table says which one produced it
    // (issue #411).
    renderWithProviders(<Observability />, { route: '/observability' })
    await waitFor(() =>
      expect(screen.getByRole('cell', { name: 'Doc Writer' })).toBeInTheDocument(),
    )
    expect(screen.getByText(/by agent · subject · hub records/)).toBeInTheDocument()
  })

  it('keeps the event cards when only the token queries return 503', async () => {
    // A hub with neither a database nor Prometheus: the event metrics come from
    // its own records and only the token panels have nothing to show.
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url.includes('/metrics/tokens/')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: 'no metrics source configured: set observability.prometheus.url or the hub database',
              }),
              { status: 503, headers: { 'Content-Type': 'application/json' } },
            ),
          )
        }
        return Promise.resolve(defaultFetch(url))
      }),
    )
    renderWithProviders(<Observability />, { route: '/observability' })

    // Event cards come from the hub's own records, so they render. The routed
    // and matched figures are both 1.1K here, so count tiles rather than match
    // one by name.
    await waitFor(() => expect(screen.getByText('1.2K')).toBeInTheDocument())
    expect(screen.getAllByText('1.1K').length).toBe(2)
    // The token tile has nothing to show. Since #411 the panel is records-backed
    // like the rest, so it names the general condition — not Prometheus alone —
    // and shows the hub's own reason.
    expect(screen.queryByText('1.2M')).not.toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByText('No metrics source configured')).toBeInTheDocument(),
    )
    expect(
      screen.getByText(/no metrics source configured: set observability\.prometheus\.url/),
    ).toBeInTheDocument()
    expect(screen.getByText(/Token usage needs a metrics source/)).toBeInTheDocument()
  })

  describe('log stream URL-persisted filters', () => {
    it('preselects the log level from the URL', async () => {
      renderObservability('/observability?level=error&range=6h')
      await waitFor(() =>
        expect(screen.getByLabelText(/filter logs by level/i)).toHaveValue('error'),
      )
      // The sample log line is info-level, so the client-side level filter
      // empties the list.
      await waitFor(() =>
        expect(screen.getByText('No log entries.')).toBeInTheDocument(),
      )
    })

    it('clearing the level removes it from the URL and keeps range', async () => {
      const { search } = renderObservability('/observability?level=info&range=6h')
      await waitFor(() =>
        expect(screen.getByLabelText(/filter logs by level/i)).toHaveValue('info'),
      )
      await waitFor(() =>
        expect(screen.getByText('request handled')).toBeInTheDocument(),
      )

      await userEvent.selectOptions(screen.getByLabelText(/filter logs by level/i), '')
      await waitFor(() => expect(search.current).not.toContain('level'))
      expect(search.current).toContain('range=6h')
    })

    it('sends the agent filter from the URL to the logs API', async () => {
      renderObservability('/observability?agent=doc-writer')
      await waitFor(() =>
        expect(screen.getByLabelText(/filter logs by agent/i)).toHaveValue('doc-writer'),
      )
      const calls = (fetch as ReturnType<typeof vi.fn>).mock.calls as [string][]
      const logsCall = calls.find(([u]) => u.includes('/observability/logs'))
      expect(
        new URL(logsCall![0], 'http://x/').searchParams.get('agent'),
      ).toBe('doc-writer')
    })

    it('falls back to "any level" for an invalid level in the URL', async () => {
      renderObservability('/observability?level=bogus')
      await waitFor(() =>
        expect(screen.getByLabelText(/filter logs by level/i)).toHaveValue(''),
      )
      // The info-level sample row stays visible.
      await waitFor(() =>
        expect(screen.getByText('request handled')).toBeInTheDocument(),
      )
    })
  })
})
