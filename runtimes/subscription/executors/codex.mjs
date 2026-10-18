/**
 * Native Codex CLI adapter; credentials and refresh belong entirely to Codex.
 * Supply env (including CODEX_HOME) to select the platform-managed auth directory.
 * authStatus(options) returns { authenticated }; startLogin(options) emits only
 * {type:'verification_url',url} / {type:'user_code',code} through onOutput and
 * resolves { authenticated: true }. Neither exposes CLI diagnostic output.
 * runTask({prompt,cwd,model?,sandbox?,env?,signal?,onEvent?}) returns {text,usage}.
 * cwd must be absolute. The default sandbox is read-only. onEvent receives each
 * execution JSONL event; text joins completed agent messages, usage is the sum of
 * turn.completed counters. A missing completion, failed turn, malformed stream,
 * interrupted process, or unsuccessful CLI exit rejects with a generic error.
 * Callbacks are synchronous. Optional spawn is a transport seam for tests.
 */
import { spawn as spawnProcess } from 'node:child_process';
import { isAbsolute } from 'node:path';
import { createInterface } from 'node:readline';

const sandboxes = new Set(['read-only', 'workspace-write', 'danger-full-access']);

function launch(args, options, consume, input = '') {
  const { cwd, env = process.env, signal, spawn = spawnProcess } = options;
  if (cwd !== undefined && (typeof cwd !== 'string' || !isAbsolute(cwd))) {
    throw new TypeError('Codex cwd must be an absolute path');
  }
  return new Promise((resolve, reject) => {
    let child;
    try {
      child = spawn('codex', args, { cwd, env, signal, stdio: ['pipe', 'pipe', 'pipe'] });
    } catch {
      reject(new Error('Unable to start Codex CLI'));
      return;
    }
    let failure;
    const readers = [];
    const fail = (message) => {
      failure ??= new Error(message);
      child.kill('SIGTERM');
    };
    for (const [name, stream] of [['stdout', child.stdout], ['stderr', child.stderr]]) {
      const reader = createInterface({ input: stream, crlfDelay: Infinity });
      readers.push(reader);
      reader.on('line', (line) => {
        if (failure) return;
        try {
          consume(name, line);
        } catch {
          fail('Invalid Codex CLI output');
        }
      });
      stream.on('error', () => fail('Unable to read Codex CLI output'));
    }
    child.once('error', (error) => {
      failure ??= new Error(error.name === 'AbortError' ? 'Codex CLI interrupted' : 'Unable to start Codex CLI');
    });
    // A child can exit before reading stdin; never surface its command or env.
    child.stdin.on('error', () => fail('Unable to send Codex task'));
    child.once('close', (code, terminationSignal) => {
      for (const reader of readers) reader.close();
      if (failure) reject(failure);
      else if (terminationSignal) reject(new Error('Codex CLI interrupted'));
      else resolve(code);
    });
    child.stdin.end(input);
  });
}

export async function authStatus(options = {}) {
  const code = await launch(['login', 'status'], options, () => {});
  if (code !== 0 && code !== 1) throw new Error('Codex login status failed');
  return { authenticated: code === 0 };
}

export async function startLogin(options = {}) {
  const seen = new Set();
  const code = await launch(['login', '--device-auth'], options, (_stream, rawLine) => {
    const line = rawLine.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').trim();
    let event;
    if (/^https:\/\/auth\.openai\.com\/codex\/device\/?$/.test(line)) {
      event = { type: 'verification_url', url: line };
    } else if (/^[A-Z0-9]{4}-[A-Z0-9]{4,8}$/.test(line)) {
      event = { type: 'user_code', code: line };
    }
    if (event && !seen.has(line)) {
      seen.add(line);
      options.onOutput?.(event);
    }
  });
  if (code !== 0) throw new Error('Codex device login failed');
  return { authenticated: true };
}

export async function runTask(options) {
  const { prompt, cwd, model, sandbox = 'read-only', env = process.env, onEvent } = options;
  if (typeof prompt !== 'string' || !prompt.trim()) throw new TypeError('Codex prompt is required');
  if (typeof cwd !== 'string' || !isAbsolute(cwd)) throw new TypeError('Codex cwd must be an absolute path');
  if (!sandboxes.has(sandbox)) throw new TypeError('Invalid Codex sandbox');
  if (model !== undefined && (typeof model !== 'string' || !model.trim())) throw new TypeError('Invalid Codex model');
  const args = ['exec', '--json', '--ephemeral', '--sandbox', sandbox, '--cd', cwd];
  if (model) args.push('--model', model);
  args.push('-');

  // These values can occur in command results as well as assistant messages.
  // Credentials inside Codex's private auth files are never read by this adapter.
  const secrets = Object.entries(env)
    .filter(([name, value]) => /token|secret|password|api_?key|credential/i.test(name) && typeof value === 'string' && value.length > 0)
    .map(([, value]) => value)
    .sort((a, b) => b.length - a.length);
  const redact = (value) => {
    if (typeof value === 'string') {
      for (const secret of secrets) value = value.split(secret).join('[REDACTED]');
      return value;
    }
    if (Array.isArray(value)) return value.map(redact);
    if (value && typeof value === 'object') {
      return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, redact(item)]));
    }
    return value;
  };
  const messages = [];
  const usage = {};
  let completed = false;
  let failed = false;
  const code = await launch(args, options, (stream, line) => {
    if (stream !== 'stdout' || !line.trim()) return;
    const event = redact(JSON.parse(line));
    if (!event || typeof event !== 'object' || typeof event.type !== 'string') {
      throw new Error('Invalid event');
    }
    if (event.type === 'item.completed' && event.item?.type === 'agent_message' && typeof event.item.text === 'string') {
      messages.push(event.item.text);
    }
    if (event.type === 'turn.completed') {
      completed = true;
      for (const [key, value] of Object.entries(event.usage ?? {})) {
        if (typeof value === 'number' && Number.isFinite(value) && value >= 0) {
          usage[key] = (usage[key] ?? 0) + value;
        }
      }
    }
    if (event.type === 'turn.failed' || event.type === 'error') failed = true;
    onEvent?.(event);
  }, prompt);
  if (code !== 0 || failed || !completed) throw new Error('Codex task failed');
  return { text: messages.join('\n\n'), usage };
}
