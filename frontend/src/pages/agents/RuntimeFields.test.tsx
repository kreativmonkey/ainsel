import { useState } from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { AgentRuntime } from '../../api/agents'
import { RuntimeFields } from './RuntimeFields'
import { agentDefaults, agentSchema, buildAgentRequest } from './agentFormModel'

function Harness() {
  const [runtime, setRuntime] = useState<AgentRuntime>({ engine: 'pi' })
  return <><RuntimeFields value={runtime} onChange={setRuntime} /><output>{JSON.stringify(runtime)}</output></>
}

describe('subscription runtime', () => {
  it('selects the native engine, credential claim and login mode', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.selectOptions(screen.getByLabelText('Agent runtime'), 'codex')
    await user.type(screen.getByLabelText('Credential PVC'), 'private-home')
    await user.click(screen.getByRole('checkbox'))
    expect(screen.getByRole('status')).toHaveTextContent('"login":true')
    expect(screen.getByRole('status')).toHaveTextContent('"claimName":"private-home"')
    await user.selectOptions(screen.getByLabelText('Agent runtime'), 'pi')
    expect(screen.queryByLabelText('Credential PVC')).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('{"engine":"pi"}')
  })

  it('validates the claim and carries the runtime in create and edit requests', () => {
    const input = { ...agentDefaults, name: 'Agent', imageRef: { name: 'image' },
      llm: { model: 'gpt-5' }, persona: { id: 'persona' },
      runtime: { engine: 'codex', subscription: { claimName: 'private-home', login: true } } }
    const values = agentSchema.parse(input)
    expect(buildAgentRequest(values, { isEdit: false }).runtime).toEqual(input.runtime)
    expect(buildAgentRequest(values, { isEdit: true }).runtime).toEqual(input.runtime)
    expect(agentSchema.safeParse({ ...input, runtime: { engine: 'codex' } }).success).toBe(false)
  })
  it('does not submit a stale API key or Pi settings for either native engine', () => {
    for (const engine of ['codex', 'claude'] as const) {
      const values = agentSchema.parse({ ...agentDefaults, name: 'Agent', imageRef: { name: 'image' },
        llm: { model: 'model', provider: 'custom', vision: true, contextWindow: 100000, temperature: 1 },
        providerApiKey: 'stale-key', customProviderUrl: '', persona: { id: 'persona' },
        runtime: { engine, subscription: { claimName: 'private-home', login: false } } })
      const body = buildAgentRequest(values, { isEdit: false })
      expect(body.llm).toEqual({ model: 'model', provider: engine })
      expect(JSON.stringify(body)).not.toContain('stale-key')
      expect(body.customProvider).toBeUndefined()
    }
  })

})
