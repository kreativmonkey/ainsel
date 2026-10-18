import { execFile, spawn } from 'node:child_process';
import { isAbsolute } from 'node:path';
import { createInterface } from 'node:readline';
import { promisify } from 'node:util';

const execute = promisify(execFile);

/**
 * Official Claude Code CLI adapter. Install the unmodified `claude` executable
 * on PATH. Every operation requires {configDir}: an absolute, writable directory
 * on persistent storage belonging to this account. Only Claude manages its
 * credentials; this module never opens them or implements OAuth.
 *
 * Shared options: {configDir, cwd?, env?, signal?}. env defaults to process.env.
 * Login returns a ChildProcess with inherited terminal I/O: attach the user
 * directly to that terminal for Anthropic's browser/code flow. Do not capture
 * or persist its terminal input. Tasks return a Promise<{text, usage}> and emit normalized text/usage events.
 * Raw CLI stderr never leaves this adapter. No shell runs.
 */
function options({ configDir, cwd, env = process.env, signal } = {}) {
  if (typeof configDir !== 'string' || !isAbsolute(configDir)) {
    throw new TypeError('configDir must be an absolute path');
  }
  return { cwd, env: { ...env, CLAUDE_CONFIG_DIR: configDir }, signal };
}

/** Return {authenticated}; a missing CLI or invalid output rejects. */
export async function authStatus(config) {
  const processOptions = options(config);
  let stdout;
  try {
    ({ stdout } = await execute('claude', ['auth', 'status'], {
      ...processOptions, timeout: 30_000, maxBuffer: 1024 * 1024,
    }));
  } catch (error) {
    if (error.code === 1) return { authenticated: false };
    throw new Error('Claude authentication status check failed');
  }
  let details;
  try { details = JSON.parse(stdout); } catch {
    throw new Error('Claude authentication status returned invalid JSON');
  }
  if (typeof details?.loggedIn !== 'boolean') {
    throw new Error('Claude auth status did not return a loggedIn boolean');
  }
  return { authenticated: details.loggedIn };
}

/** Start native login; user completes the official CLI/browser interaction. */
export function startLogin(config) {
  return spawn('claude', ['auth', 'login'], { ...options(config), stdio: 'inherit' });
}

/**
 * Run a task with native credentials. The caller supplies configDir (or sets
 * CLAUDE_CONFIG_DIR in env), prompt, cwd, optional model, signal and onEvent.
 * onEvent receives only {type:'text',text} or {type:'usage',usage}.
 * dontAsk denies unapproved tools instead of waiting for unattended approval.
 * Task text goes over stdin; the caller must remove API-key/cloud env overrides
 * when subscription billing is intended. spawn may be injected for transport tests.
 */
export async function runTask({ prompt, model, onEvent = () => {}, spawn: spawnCLI = spawn, ...config }) {
  if (typeof config.cwd !== 'string' || !isAbsolute(config.cwd)) {
    throw new TypeError('cwd must be an absolute path');
  }
  if (typeof prompt !== 'string' || prompt.trim() === '') {
    throw new TypeError('prompt must be a nonempty string');
  }
  if (model !== undefined && (typeof model !== 'string' || !model.trim())) {
    throw new TypeError('model must be a nonempty string');
  }
  const args = [
    '-p', '--output-format', 'stream-json', '--verbose',
    '--permission-mode', 'dontAsk', '--no-session-persistence',
  ];
  if (model) args.push('--model', model);
  const child = spawnCLI('claude', args, {
    ...options({ ...config, configDir: config.configDir ?? config.env?.CLAUDE_CONFIG_DIR }),
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  // Consume diagnostics without exposing paths, prompts or credentials in errors.
  child.stderr.resume();
  const completion = new Promise((resolve, reject) => {
    child.once('error', () => reject(new Error('Claude task process failed')));
    child.once('close', (code) => code === 0 ? resolve() : reject(new Error('Claude task exited unsuccessfully')));
  });
  // Attach immediately: an early spawn failure must not become unhandled.
  completion.catch(() => {});
  child.stdin.on('error', () => {});
  child.stdin.end(prompt);
  let result;
  try {
    for await (const line of createInterface({ input: child.stdout, crlfDelay: Infinity })) {
      if (!line.trim()) continue;
      const event = JSON.parse(line);
      if (event.type === 'assistant') {
        for (const block of event.message?.content ?? []) {
          if (block.type === 'text' && typeof block.text === 'string') {
            onEvent({ type: 'text', text: block.text });
          }
        }
      }
      if (event.type === 'result') result = event;
    }
    await completion;
    if (!result || result.is_error || typeof result.result !== 'string') {
      throw new Error('Claude task did not produce a successful result');
    }
    // Allowlist numeric usage values; never forward arbitrary CLI result fields.
    const usage = {};
    for (const key of ['input_tokens', 'output_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens']) {
      const value = result.usage?.[key];
      if (typeof value === 'number' && Number.isFinite(value) && value >= 0) usage[key] = value;
    }
    onEvent({ type: 'usage', usage });
    return { text: result.result, usage };
  } catch {
    child.kill();
    throw new Error('Claude task failed');
  }
}
