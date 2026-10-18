import { z } from 'zod'
import type {
  AgentAlibabaCloud,
  AgentCustomProvider,
  AgentOllamaCloud,
  AgentOpenCode,
  AgentRequest,
} from '../../api/agents'

export const LLM_PROVIDERS = [
  { value: '', label: 'None' },
  { value: 'ollama-cloud', label: 'Ollama Cloud' },
  { value: 'opencode', label: 'OpenCode' },
  { value: 'alibaba-cloud', label: 'Alibaba Token Plan' },
  { value: 'custom', label: 'Custom' },
] as const

/**
 * Human-readable label for a stored LLM provider id. Derived from the same
 * LLM_PROVIDERS options the forms offer, so display can never drift from the
 * form vocabulary — including the empty id, which the forms call 'None'.
 * Unknown ids fall through to the raw value so a provider added later still
 * renders honestly until this list learns it.
 */
export function llmProviderLabel(provider: string | undefined): string {
  if (!provider) return 'None'
  return LLM_PROVIDERS.find((p) => p.value === provider)?.label ?? provider
}

export const API_KEY_LABELS: Record<string, string> = {
  'ollama-cloud': 'Ollama Cloud API Key',
  opencode: 'OpenCode API Key',
  'alibaba-cloud': 'Alibaba Token Plan API Key',
  custom: 'API Key',
}

/**
 * Validation for both agent write forms: the creation wizard and the edit
 * form. Kept in one module so the two cannot drift.
 */
export const agentSchema = z
  .object({
    name: z.string().min(1, 'Name is required'),
    description: z.string().optional(),
    runtime: z.object({
      engine: z.enum(['pi', 'codex', 'claude']),
      subscription: z.object({ claimName: z.string(), login: z.boolean() }).optional(),
    }).optional(),
    imageRef: z.object({ name: z.string().min(1, 'Image is required') }),
    llm: z.object({
      model: z.string().min(1, 'Model is required'),
      provider: z.string().optional(),
      maxTurns: z.coerce.number().int().min(1).optional(),
      temperature: z.coerce.number().min(0).max(2).optional(),
      // Empty input means "use the operator default (131072)": the hub treats an
      // absent field on update as "leave unchanged", so blank keeps whatever
      // is stored instead of wiping it to a bogus 0.
      contextWindow: z.preprocess(
        (v) => (v === '' || v === undefined || v === null ? undefined : v),
        z.coerce.number().int().min(1024).optional(),
      ),
      // Always a concrete boolean in the payload: the hub treats a missing
      // `vision` on update as "leave unchanged", so the form must send false
      // explicitly to turn image input back off.
      vision: z.boolean().optional(),
    }),
    providerApiKey: z.string().optional(),
    customProviderUrl: z.string().optional(),
    persona: z.object({ id: z.string().min(1, 'Persona is required') }),
    // Concurrency cap: the operator keeps at most this many per-task Jobs
    // running. 0 pauses the agent. Always sent as a concrete number, like
    // `vision`, because the hub reads an absent value on update as
    // "leave unchanged".
    replicas: z.coerce.number().int().min(0).optional(),
    groupId: z.string().optional(),
  })
  .superRefine((data, ctx) => {
    if (data.runtime?.engine !== undefined && data.runtime.engine !== 'pi' && !data.runtime.subscription?.claimName.trim()) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['runtime', 'subscription', 'claimName'], message: 'Credential PVC is required' })
    }
    if ((!data.runtime || data.runtime.engine === 'pi') && data.llm.provider === 'custom' && !data.customProviderUrl?.trim()) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['customProviderUrl'],
        message: 'URL is required for custom providers',
      })
    }
  })

// zod 4 tracks input/output types separately (coercions & defaults make
// inputs wider); useForm needs both so the resolver types line up.
export type AgentFormInput = z.input<typeof agentSchema>
export type AgentFormValues = z.infer<typeof agentSchema>

export const agentDefaults: AgentFormInput = {
  name: '',
  description: '',
  runtime: { engine: 'pi' },
  imageRef: { name: '' },
  llm: {
    model: '',
    provider: 'ollama-cloud',
    maxTurns: 100,
    temperature: 1,
    vision: false,
    contextWindow: undefined,
  },
  customProviderUrl: '',
  providerApiKey: '',
  persona: { id: '' },
  replicas: 1,
  groupId: '',
}

/**
 * Builds the write-side agent body from validated form values.
 *
 * A provider credential block is attached only when a key was entered, so an
 * edit that leaves the key blank keeps the stored one. The group is only sent
 * on create: an agent's group is fixed for its lifetime.
 *
 * `replicas` is the concurrency cap on the agent's per-task Jobs — always sent
 * as a number, never omitted, so an edit that lowers or raises it lands.
 */
export function buildAgentRequest(
  values: AgentFormValues,
  opts: { isEdit: boolean },
): AgentRequest {
  const nativeEngine = values.runtime?.engine !== 'pi' ? values.runtime?.engine : undefined
  const provider = nativeEngine ?? values.llm.provider ?? ''
  const apiKey = values.providerApiKey ?? ''
  const key = apiKey ? { apiKey } : undefined

  const ollamaCloud: AgentOllamaCloud | undefined =
    provider === 'ollama-cloud' ? key : undefined
  const openCode: AgentOpenCode | undefined = provider === 'opencode' ? key : undefined
  const alibabaCloud: AgentAlibabaCloud | undefined =
    provider === 'alibaba-cloud' ? key : undefined
  const customProvider: AgentCustomProvider | undefined =
    provider === 'custom'
      ? { url: values.customProviderUrl ?? '', ...(key ?? {}) }
      : undefined

  return {
    name: values.name,
    description: values.description || undefined,
    imageRef: values.imageRef,
    runtime: values.runtime,
    llm: nativeEngine
      ? { model: values.llm.model, provider: nativeEngine, maxTurns: values.llm.maxTurns }
      : values.llm,
    ollamaCloud,
    openCode,
    alibabaCloud,
    customProvider,
    persona: { id: values.persona.id },
    replicas: values.replicas,
    groupId: opts.isEdit ? undefined : values.groupId,
  }
}
