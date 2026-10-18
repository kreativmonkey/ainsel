import type { AgentRuntime } from '../../api/agents'
import { Field } from '../../primitives/Field'
import { Input } from '../../primitives/Input'
import { Select } from '../../primitives/Select'

export function RuntimeFields({ value, onChange, error }: {
  value: AgentRuntime
  onChange: (value: AgentRuntime) => void
  error?: string
}) {
  return (
    <div style={{ display: 'grid', gap: 16 }}>
      <Field label="Agent runtime" htmlFor="runtime-engine">
        <Select id="runtime-engine" value={value.engine} onChange={(engine) => onChange({
          engine: engine as AgentRuntime['engine'],
          ...(value.subscription || engine !== 'pi' ? { subscription: value.subscription ?? { claimName: '', login: false } } : {}),
        })} options={[
          { value: 'pi', label: 'Pi (API or ChatGPT subscription)' },
          { value: 'codex', label: 'Codex (ChatGPT subscription)' },
          { value: 'claude', label: 'Claude Code (Claude subscription)' },
        ]} />
      </Field>
      {value.engine === 'pi' ? <label className="label">
        <input type="checkbox" checked={!!value.subscription}
          onChange={(e) => onChange({ engine: 'pi', ...(e.target.checked ? { subscription: { claimName: '', login: false } } : {}) })} />
        {' '}ChatGPT subscription
      </label> : null}
      {value.engine !== 'pi' || value.subscription ? <>
        <Field label="Credential PVC" htmlFor="runtime-claim" error={error}>
          <Input id="runtime-claim" value={value.subscription?.claimName ?? ''}
            onChange={(e) => onChange({ ...value, subscription: { login: value.subscription?.login ?? false, claimName: e.target.value } })} />
        </Field>
        <label className="label">
          <input type="checkbox" checked={value.subscription?.login ?? false}
            onChange={(e) => onChange({ ...value, subscription: { claimName: value.subscription?.claimName ?? '', login: e.target.checked } })} />
          {' '}Login mode (pause tasks for subscription login)
        </label>
        <p className="label" style={{ margin: 0, color: 'var(--ink-3)' }}>
          {value.subscription?.login ? 'After saving, tasks pause for subscription sign-in. ' : 'After saving, tasks use the saved subscription credentials. '}
          Use a dedicated existing ReadWriteOncePod PVC in the agent namespace.
          {value.engine === 'pi' ? 'Enable login mode and save to sign in with Pi in the agent’s login pod.' : 'Enable login mode and save to sign in in the agent’s login pod.'}
          Disable login mode and save to resume tasks. Credentials stay on the PVC. {value.engine === 'pi' ? 'Pi uses the OpenAI Codex provider for the ChatGPT subscription.' : 'The selected image must include the selected runtime.'}
        </p>
      </> : null}
    </div>
  )
}
