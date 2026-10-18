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
          ...(engine === 'pi' ? {} : { subscription: value.subscription ?? { claimName: '', login: false } }),
        })} options={[
          { value: 'pi', label: 'Pi (API provider)' },
          { value: 'codex', label: 'Codex (ChatGPT subscription)' },
          { value: 'claude', label: 'Claude Code (Claude subscription)' },
        ]} />
      </Field>
      {value.engine !== 'pi' ? <>
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
          Use a dedicated existing ReadWriteOncePod PVC in the agent namespace.
          Enable login mode, save, then run the native CLI login in the agent's login pod.
          Disable login mode and save to resume tasks. Credentials stay on the PVC;
          sign-in to this console continues through Authentik. The selected image must include this CLI.
        </p>
      </> : null}
    </div>
  )
}
