import { useState } from 'react'
import { useForm, type FieldErrors } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { RuntimeFields } from './RuntimeFields'
import { useCreateAgent } from '../../api/agents'
import { useAgentImage, useAgentImages } from '../../api/agentImages'
import { usePersonas } from '../../api/personas'
import { ApiError } from '../../api/client'
import { Button } from '../../primitives/Button'
import { Check } from '../../primitives/Check'
import { Field } from '../../primitives/Field'
import { Input } from '../../primitives/Input'
import { Select } from '../../primitives/Select'
import { Textarea } from '../../primitives/Textarea'
import { Titleblock } from '../../layout/Titleblock'
import { GroupField } from '../../components/GroupField'
import {
  API_KEY_LABELS,
  LLM_PROVIDERS,
  agentDefaults,
  agentSchema,
  buildAgentRequest,
  type AgentFormInput,
  type AgentFormValues,
} from './agentFormModel'

/**
 * The creation steps. Each step validates only its own fields, so a user can
 * move through the wizard without tripping over errors for steps they have not
 * reached yet.
 */
const STEPS = [
  { id: 'identity', idx: '§01', title: 'Identity', fields: ['name', 'description', 'groupId'] },
  { id: 'runtime', idx: '§02', title: 'Runtime', fields: ['imageRef.name', 'runtime.engine', 'runtime.subscription.claimName'] },
  {
    id: 'model',
    idx: '§03',
    title: 'Model',
    fields: [
      'llm.model',
      'llm.provider',
      'llm.maxTurns',
      'llm.temperature',
      'llm.contextWindow',
      'llm.vision',
      'customProviderUrl',
      'providerApiKey',
    ],
  },
  { id: 'persona', idx: '§04', title: 'Persona', fields: ['persona.id'] },
  { id: 'review', idx: '§05', title: 'Review', fields: ['replicas'] },
] as const

type StepId = (typeof STEPS)[number]['id']

const STEP_IDS: readonly StepId[] = STEPS.map((s) => s.id)

function stepIndex(id: string | null): number {
  const found = STEP_IDS.indexOf(id as StepId)
  return found === -1 ? 0 : found
}

/** True when a dotted field path (e.g. `llm.model`) carries an error. */
function hasError(errs: FieldErrors, path: string): boolean {
  const node = path
    .split('.')
    .reduce<unknown>(
      (acc, key) =>
        acc && typeof acc === 'object'
          ? (acc as Record<string, unknown>)[key]
          : undefined,
      errs,
    )
  return node !== undefined
}

/** The first step that still has an invalid field, or -1. */
function firstInvalidStep(errs: FieldErrors): number {
  return STEPS.findIndex((s) => s.fields.some((f) => hasError(errs, f)))
}

/**
 * The new-agent wizard: identity, runtime image, model, persona, review.
 *
 * Creating an agent used to be one long form mixing five concerns; the wizard
 * keeps the same fields and validation but shows one decision at a time and
 * ends on a review of what will be created. Editing stays on the single-page
 * form, where jumping straight to a field is what users want.
 */
export function AgentWizard() {
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const [submitError, setSubmitError] = useState<string | null>(null)

  const current = stepIndex(params.get('step'))
  const step = STEPS[current]
  const isLast = current === STEPS.length - 1

  const images = useAgentImages({ pageSize: 200 })
  const personas = usePersonas({ pageSize: 200 })
  const create = useCreateAgent()

  const {
    register,
    handleSubmit,
    setValue,
    watch,
    trigger,
    setError,
    clearErrors,
    formState: { errors, isSubmitting },
  } = useForm<AgentFormInput, unknown, AgentFormValues>({
    resolver: zodResolver(agentSchema),
    defaultValues: agentDefaults,
    mode: 'onBlur',
  })

  const selectedImage = watch('imageRef.name')
  const image = useAgentImage(selectedImage || undefined)
  const provider = watch('llm.provider')
  const nativeRuntime = (watch('runtime.engine') ?? 'pi') !== 'pi'

  const goTo = (index: number) => {
    const id = STEP_IDS[index]
    setParams(id === 'identity' ? {} : { step: id }, { replace: true })
    setSubmitError(null)
  }

  /** Validates every step up to and including `index`. */
  const validateThrough = async (index: number) => {
    const fields = STEPS.slice(0, index + 1).flatMap((s) => [...s.fields])
    const ok = await trigger(fields as Parameters<typeof trigger>[0])
    // The group has no zod rule (it only applies on create), so enforce it
    // here — reported alongside the schema errors rather than instead of
    // them, so one pass shows everything the step is missing.
    let groupOk = true
    if (!watch('groupId')) {
      setError('groupId', { message: 'Group is required' })
      groupOk = false
    }
    return ok && groupOk
  }

  const onNext = async () => {
    if (!(await validateThrough(current))) return
    goTo(current + 1)
  }

  const onStepClick = async (index: number) => {
    if (index <= current) {
      goTo(index)
      return
    }
    // Jumping forward validates everything in between, so the review can
    // never be reached with a missing required field.
    if (await validateThrough(index - 1)) goTo(index)
  }

  const onSubmit = handleSubmit(
    async (values) => {
      setSubmitError(null)
      if (!values.groupId) {
        setError('groupId', { message: 'Group is required' })
        goTo(0)
        return
      }
      try {
        const saved = await create.mutateAsync(
          buildAgentRequest(values, { isEdit: false }),
        )
        navigate(`/agents/${encodeURIComponent(saved.id)}`, { replace: true })
      } catch (err) {
        setSubmitError(
          err instanceof ApiError ? err.message : 'Create failed. Please try again.',
        )
      }
    },
    (errs) => {
      // Submitting validates every step at once, including ones the user may
      // have deep-linked past; without this the messages would render on
      // steps that are not on screen.
      const invalid = firstInvalidStep(errs)
      if (invalid >= 0) goTo(invalid)
      else if (!watch('groupId')) {
        setError('groupId', { message: 'Group is required' })
        goTo(0)
      }
    },
  )

  return (
    <>
      <Titleblock
        crumbs={
          <>
            Fleet / <Link to="/agents">Agents</Link> / <b>New</b>
          </>
        }
        title={
          <>
            New <em>Agent</em>
          </>
        }
        actions={<Button onClick={() => navigate('/agents')}>Cancel</Button>}
      />

      <div style={{ padding: '28px 32px', maxWidth: 920 }}>
        <ol
          aria-label="Creation steps"
          style={{
            display: 'flex',
            flexWrap: 'wrap',
            gap: 8,
            listStyle: 'none',
            margin: '0 0 24px',
            padding: 0,
          }}
        >
          {STEPS.map((s, i) => {
            const active = i === current
            return (
              <li key={s.id}>
                <button
                  type="button"
                  onClick={() => void onStepClick(i)}
                  aria-current={active ? 'step' : undefined}
                  style={{
                    display: 'flex',
                    alignItems: 'baseline',
                    gap: 6,
                    padding: '6px 10px',
                    border: `1.5px solid ${active ? 'var(--ink)' : 'var(--rule)'}`,
                    background: active ? 'var(--ink)' : 'transparent',
                    color: active ? 'var(--paper)' : 'var(--ink-3)',
                    cursor: 'pointer',
                    fontFamily: 'var(--mono)',
                    fontSize: 11,
                    letterSpacing: '0.08em',
                    textTransform: 'uppercase',
                  }}
                >
                  <span>{s.idx}</span>
                  <span>{s.title}</span>
                </button>
              </li>
            )
          })}
        </ol>

        <form id="agent-wizard" onSubmit={onSubmit} noValidate>
          {submitError ? (
            <div
              role="alert"
              style={{
                padding: '10px 12px',
                border: '1.5px solid var(--signal)',
                background: 'var(--signal-haze)',
                color: 'var(--signal)',
                fontFamily: 'var(--mono)',
                fontSize: 11,
                marginBottom: 20,
              }}
            >
              {submitError}
            </div>
          ) : null}

          <section
            className="form-section"
            aria-label={`Step ${current + 1} of ${STEPS.length}: ${step.title}`}
          >
            <header>
              <span className="idx">{step.idx}</span>
              <h3>{step.title}</h3>
            </header>

            {step.id === 'identity' ? (
              <div className="form-grid">
                <Field label="Name" htmlFor="name" error={errors.name?.message}>
                  <Input id="name" autoFocus {...register('name')} />
                </Field>
                <Field label="Description" htmlFor="description">
                  <Textarea id="description" rows={2} {...register('description')} />
                </Field>
                <GroupField
                  value={watch('groupId') ?? ''}
                  onChange={(v) => {
                    setValue('groupId', v, { shouldDirty: true })
                    // groupId is not a registered field, so its manual error
                    // would otherwise outlive the fix.
                    if (v) clearErrors('groupId')
                  }}
                  error={errors.groupId?.message}
                />
              </div>
            ) : null}

            {step.id === 'runtime' ? (
              <>
                <div className="form-grid">
                  <Field
                    label="Runtime image"
                    htmlFor="imageRef.name"
                    error={errors.imageRef?.name?.message}
                  >
                    <Select
                      id="imageRef.name"
                      value={selectedImage ?? ''}
                      onChange={(v) =>
                        setValue('imageRef.name', v, {
                          shouldValidate: true,
                          shouldDirty: true,
                        })
                      }
                      options={(images.data?.items ?? []).map((i) => ({
                        value: i.id,
                        label: i.displayName ?? i.id,
                      }))}
                      emptyLabel="Select an image…"
                    />
                    <input type="hidden" {...register('imageRef.name')} />
                  </Field>
                  <RuntimeFields
                    value={watch('runtime') ?? { engine: 'pi' }}
                    onChange={(value) => {
                      setValue('runtime', value, { shouldDirty: true })
                      if (value.engine === 'pi') {
                        if (value.subscription) {
                          setValue('llm.provider', 'openai-codex', { shouldDirty: true })
                        } else if (provider === 'codex' || provider === 'claude' || provider === 'openai-codex') {
                          setValue('llm.provider', '', { shouldDirty: true })
                        }
                      }
                    }}
                    error={errors.runtime?.subscription?.claimName?.message}
                  />
                </div>

                {selectedImage && image.data ? (
                  <div
                    style={{
                      marginTop: 16,
                      padding: '12px 14px',
                      border: '1.5px solid var(--rule)',
                      display: 'grid',
                      gap: 6,
                    }}
                  >
                    <span className="label" style={{ color: 'var(--ink-3)' }}>
                      This image brings
                    </span>
                    <span style={{ fontFamily: 'var(--mono)', fontSize: 12 }}>
                      {(image.data.tools ?? []).length} tools ·{' '}
                      {(image.data.enabledSkills ?? []).length} skills ·{' '}
                      {(image.data.mcpServers ?? []).length} MCP servers ·{' '}
                      {(image.data.env ?? []).length} env vars
                    </span>
                    <span className="label" style={{ color: 'var(--ink-3)' }}>
                      Tools, skills, MCP servers and environment can be
                      overridden per agent after creation, on the agent's own
                      tabs.
                    </span>
                  </div>
                ) : null}

                {(images.data?.items ?? []).length === 0 ? (
                  <p className="label" style={{ marginTop: 12, color: 'var(--ink-3)' }}>
                    No images yet? <Link to="/agent-images/new">Create one</Link>.
                  </p>
                ) : null}
              </>
            ) : null}

            {step.id === 'model' ? (
              <>
                <div className="form-grid cols-3">
                  <Field label="Model" htmlFor="llm.model" error={errors.llm?.model?.message}>
                    <Input id="llm.model" {...register('llm.model')} />
                  </Field>
                  <Field label="Max Turns" htmlFor="llm.maxTurns">
                    <Input id="llm.maxTurns" type="number" min={1} {...register('llm.maxTurns')} />
                  </Field>
                  {!nativeRuntime ? <>
<Field label="Temperature" htmlFor="llm.temperature">
                    <Input
                      id="llm.temperature"
                      type="number"
                      step={0.1}
                      min={0}
                      max={2}
                      {...register('llm.temperature')}
                    />
                  </Field>
                  <Field
                    label="Context Window"
                    htmlFor="llm.contextWindow"
                    error={errors.llm?.contextWindow?.message}
                    hint="Tokens declared in pi's models.json. Defaults to 131072 (128k) — must match what the LLM endpoint actually enforces, or context compaction fires too late; raise it only if the endpoint accepts more."
                  >
                    <Input
                      id="llm.contextWindow"
                      type="number"
                      min={1024}
                      placeholder="131072 (128k default)"
                      {...register('llm.contextWindow')}
                    />
                  </Field>
</> : null}
                </div>
                {!nativeRuntime ? <>
<div className="form-grid" style={{ marginTop: 16 }}>
                  <Field label="Provider" htmlFor="llm.provider">
                    <Select
                      id="llm.provider"
                      value={provider ?? ''}
                      onChange={(v) => {
                        setValue('llm.provider', v, { shouldDirty: true })
                        if (v === 'openai-codex') {
                          setValue('runtime', { engine: 'pi', subscription: watch('runtime.subscription') ?? { claimName: '', login: false } }, { shouldDirty: true })
                        } else if (watch('runtime.subscription')) {
                          setValue('runtime', { engine: 'pi' }, { shouldDirty: true })
                        }
                      }}
                      options={LLM_PROVIDERS}
                    />
                    <input type="hidden" {...register('llm.provider')} />
                  </Field>
                  {provider === 'openai-codex' ? <p className="label">
                    Configure the credential PVC and login mode in Runtime. Pi signs in with your ChatGPT subscription.
                  </p> : null}
                  {provider && provider !== 'openai-codex' ? (
                    <>
                      {provider === 'custom' ? (
                        <Field
                          label="Provider Base URL"
                          htmlFor="customProviderUrl"
                          error={errors.customProviderUrl?.message}
                        >
                          <Input
                            id="customProviderUrl"
                            {...register('customProviderUrl')}
                            placeholder="https://api.example.com/v1"
                          />
                        </Field>
                      ) : null}
                      <Field
                        label={API_KEY_LABELS[provider] ?? 'API Key'}
                        htmlFor="providerApiKey"
                      >
                        <Input
                          id="providerApiKey"
                          type="password"
                          autoComplete="off"
                          {...register('providerApiKey')}
                        />
                      </Field>
                    </>
                  ) : null}
                </div>
                <div className="form-grid" style={{ marginTop: 16 }}>
                  <Field
                    label="Image Input"
                    htmlFor="llm.vision"
                    hint="pi attaches screenshots and image files only while this is on. The endpoint is never probed: images sent to a model that cannot see them fail the request, and while this is off a model that can see them receives none. Takes effect when the agent's pod next starts."
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, paddingTop: 6 }}>
                      <Check
                        checked={watch('llm.vision') ?? false}
                        onChange={(v) => setValue('llm.vision', v, { shouldDirty: true })}
                        aria-label="Image Input"
                      />
                      <span
                        style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--ink-3)' }}
                      >
                        {watch('llm.vision') ? 'Images are sent' : 'Text only'}
                      </span>
                    </div>
                  </Field>
                </div>
                </> : null}
              </>
            ) : null}

            {step.id === 'persona' ? (
              <Field label="Persona" htmlFor="persona.id" error={errors.persona?.id?.message}>
                <Select
                  id="persona.id"
                  value={watch('persona.id') ?? ''}
                  onChange={(v) =>
                    setValue('persona.id', v, { shouldValidate: true, shouldDirty: true })
                  }
                  options={(personas.data?.items ?? []).map((p) => ({
                    value: p.id,
                    label: p.name,
                  }))}
                  emptyLabel="Select a persona…"
                />
                <input type="hidden" {...register('persona.id')} />
                <p className="label" style={{ marginTop: 8, color: 'var(--ink-3)' }}>
                  The persona shapes how the agent behaves. You can fork an
                  agent-owned copy later on the agent's Persona tab. No persona
                  yet? <Link to="/personas/new">Create one</Link>.
                </p>
              </Field>
            ) : null}

            {step.id === 'review' ? (
              <div style={{ display: 'grid', gap: 16 }}>
                <dl
                  style={{
                    display: 'grid',
                    gridTemplateColumns: '160px 1fr',
                    gap: '8px 16px',
                    margin: 0,
                    fontFamily: 'var(--mono)',
                    fontSize: 12,
                  }}
                >
                  <dt style={{ color: 'var(--ink-3)' }}>Name</dt>
                  <dd style={{ margin: 0 }}>{watch('name') || '—'}</dd>
                  <dt style={{ color: 'var(--ink-3)' }}>Group</dt>
                  <dd style={{ margin: 0 }}>{watch('groupId') || '—'}</dd>
                  <dt style={{ color: 'var(--ink-3)' }}>Runtime image</dt>
                  <dd style={{ margin: 0 }}>
                    {selectedImage
                      ? (images.data?.items.find((i) => i.id === selectedImage)?.displayName ??
                        selectedImage)
                      : '—'}
                  </dd>
                  <dt style={{ color: 'var(--ink-3)' }}>Model</dt>
                  <dd style={{ margin: 0 }}>
                    {watch('llm.model') || '—'}
                    {nativeRuntime ? ` · ${watch('runtime.engine')}` : provider ? ` · ${provider}` : ''}
                    {!nativeRuntime && watch('llm.vision') ? ' · images on' : ''}
                  </dd>
                  <dt style={{ color: 'var(--ink-3)' }}>Persona</dt>
                  <dd style={{ margin: 0 }}>
                    {personas.data?.items.find((p) => p.id === watch('persona.id'))?.name ??
                      watch('persona.id') ??
                      '—'}
                  </dd>
                </dl>
                <div className="form-grid" style={{ maxWidth: 240 }}>
                  <Field
                    label="Max concurrent tasks"
                    htmlFor="replicas"
                    hint="Up to this many tasks run at once, each in its own container. 0 pauses the agent."
                  >
                    <Input id="replicas" type="number" min={0} {...register('replicas')} />
                  </Field>
                </div>
              </div>
            ) : null}
          </section>

          <div style={{ display: 'flex', gap: 8, marginTop: 24 }}>
            {current > 0 ? <Button type="button" onClick={() => goTo(current - 1)}>Back</Button> : null}
            {isLast ? (
              <Button type="submit" variant="primary" disabled={isSubmitting}>
                {isSubmitting ? 'Creating…' : 'Create agent'}
              </Button>
            ) : (
              <Button type="button" variant="primary" onClick={() => void onNext()}>
                Next
              </Button>
            )}
          </div>
        </form>
      </div>
    </>
  )
}
