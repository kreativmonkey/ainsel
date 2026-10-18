import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from './client'
import type { Paginated } from './types'

export type AgentLLM = {
  model: string
  provider?: string
  maxTurns?: number
  temperature?: number
  /** Whether pi sends image input; advertised in pi's models.json `input` array. */
  vision?: boolean
  /** Context window (tokens) declared in pi's models.json; should match the
   *  limit the actual LLM endpoint enforces, or compaction fires too late. */
  contextWindow?: number
}
export type AgentRuntime = {
  engine: 'pi' | 'codex' | 'claude'
  subscription?: { claimName: string; login: boolean }
}
export type AgentPersona = { id: string }
export type AgentImageRef = { name: string; displayName?: string }
/** Agent-scoped skill selection: present = explicit override (empty items
 *  = no skills), absent = inherit the runtime image's enabledSkills. */
export type AgentSkills = { items: string[] }
/** One agent-scoped MCP server definition, resolved from the hub's MCP
 *  registry at write time (a snapshot — registry edits don't rewrite
 *  running agents). */
export type AgentMCPServer = {
  name: string
  url: string
  tokenFromEnv?: string
}
/** Agent-scoped MCP selection: present = explicit override (empty servers
 *  = no connections), absent = inherit the image's mcpServers. */
export type AgentMCP = { servers: AgentMCPServer[] }
/** One of an agent's own environment variables, layered on top of the runtime
 *  image's env. The hub masks secret values, so a secret entry always reads
 *  back with an empty value. */
export type AgentEnvVar = { name: string; value: string; secret?: boolean }
export type AgentOllamaCloud = { apiKey?: string }
export type AgentOpenCode = { apiKey?: string }
export type AgentAlibabaCloud = { apiKey?: string }
export type AgentCustomProvider = { url?: string; apiKey?: string }

export type AgentSummary = {
  id: string
  name: string
  description?: string
  llm?: { model: string; provider?: string }
  imageRef?: AgentImageRef
  persona?: AgentPersona
  replicas?: number
  status?: {
    ready: boolean
    /** Tasks running right now — the active per-task Jobs the operator keeps. */
    desired?: number
    /** Operator's one-word explanation, e.g. 'JobPerTask'. */
    reason?: string
    message?: string
  }
  /** RFC3339 timestamp of the last hub-mediated write (falls back to
   *  creation time for agents that predate the annotation). */
  updatedAt?: string
  /** True while task delivery is withheld: routed tasks pile up as a
   *  backlog and drain on resume. Ingestion and interactive chat keep
   *  working while paused. */
  paused: boolean
  /** When an expiring pause ends (RFC3339). Absent for a running agent and
   *  for an indefinite pause, which must be resumed explicitly. */
  pausedUntil?: string
}

/**
 * True when the operator reports no task in flight: the agent is idle, which is
 * the normal resting state of this model — work arrives as one Job per task and
 * a jobless agent has nothing to run, not a broken one. Only the operator's
 * run account says this; the readiness count alone never did.
 */
export function isIdle(status?: AgentSummary['status']): boolean {
  return status != null && (status.desired ?? 0) === 0
}

export type AgentResponse = AgentSummary & {
  runtime?: AgentRuntime
  llm?: AgentLLM
  persona?: AgentPersona
  enabledTools?: string[]
  skills?: AgentSkills
  mcp?: AgentMCP
  /** This agent's env overrides; absent = it runs on the image env alone. */
  env?: AgentEnvVar[]
  ollamaCloud?: AgentOllamaCloud
  openCode?: AgentOpenCode
  alibabaCloud?: AgentAlibabaCloud
  customProvider?: AgentCustomProvider
}

export type AgentRequest = Omit<
  AgentResponse,
  'id' | 'status' | 'mcp' | 'env' | 'paused' | 'pausedUntil'
> & {
  groupId?: string
  /** Write-side MCP selection: registry names, resolved by the hub to
   *  full definitions when writing the agent. */
  mcp?: { servers: string[] }
  /** Write-side env overrides: replaces this agent's list. Present-but-empty
   *  clears the overrides so the agent runs on the image env again; a secret
   *  entry submitted with an empty value keeps its stored value. */
  env?: AgentEnvVar[]
}

export type ListAgentsParams = {
  page?: number
  pageSize?: number
}

export function listAgents(params: ListAgentsParams = {}) {
  return request<Paginated<AgentSummary>>('/agents', { query: params })
}

export function getAgent(id: string) {
  return request<AgentResponse>(`/agents/${encodeURIComponent(id)}`)
}

export function createAgent(body: AgentRequest) {
  return request<AgentResponse>('/agents', { method: 'POST', body })
}

export function updateAgent(id: string, body: AgentRequest) {
  return request<AgentResponse>(`/agents/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body,
  })
}

export function deleteAgent(id: string) {
  return request<void>(`/agents/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/** Pause options accepted by POST /agents/{id}/pause. Neither field = an
 *  indefinite pause that must be lifted explicitly. */
export type PauseAgentRequest = {
  /** Go duration string from now, e.g. '30m', '8h'. Mutually exclusive with until. */
  duration?: string
  /** RFC3339 timestamp when the pause ends. Mutually exclusive with duration. */
  until?: string
}

/** Withhold task delivery: queued tasks wait as a backlog and drain on resume. */
export function pauseAgent(id: string, body: PauseAgentRequest = {}) {
  return request<AgentResponse>(`/agents/${encodeURIComponent(id)}/pause`, {
    method: 'POST',
    body,
  })
}

/** Lift the pause; the backlog drains in id order. */
export function resumeAgent(id: string) {
  return request<AgentResponse>(`/agents/${encodeURIComponent(id)}/pause`, {
    method: 'DELETE',
  })
}

export function useAgents(params: ListAgentsParams = {}) {
  return useQuery({
    queryKey: ['agents', params],
    queryFn: () => listAgents(params),
    placeholderData: keepPreviousData,
  })
}

export function useAgent(id: string | undefined) {
  return useQuery({
    queryKey: ['agents', 'detail', id],
    queryFn: () => getAgent(id!),
    enabled: id !== undefined,
  })
}

export function useCreateAgent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: createAgent,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agents'] }),
  })
}

export function useUpdateAgent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: AgentRequest }) => updateAgent(id, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agents'] }),
  })
}

export function useDeleteAgent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: deleteAgent,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agents'] }),
  })
}

export function usePauseAgent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body?: PauseAgentRequest }) =>
      pauseAgent(id, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agents'] }),
  })
}

export function useResumeAgent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: resumeAgent,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agents'] }),
  })
}
