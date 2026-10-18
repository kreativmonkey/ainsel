import assert from 'node:assert/strict';
import test from 'node:test';
import { buildPrompt, runOneShot, runtimeEnvironment } from './runner.mjs';

const env = {
  AINSEL_TASK_ID: '42', AGENT_NAME: 'reviewer', HUB_URL: 'http://hub',
  HUB_INTERNAL_VALIDATE_SECRET: 'secret', AINSEL_RUNTIME: 'codex',
  OLLAMA_CLOUD_MODEL: 'gpt-5.4',
};
const task = { id: 42, agent_name: 'reviewer', invocation_id: 'inv-1', status: 'pending',
  headers: { type: 'pull_request.opened' }, payload: { repository: { full_name: 'owner/repo' }, pull_request: { number: 3 } } };

function harness(taskResponse = { status: 200, ok: true, json: async () => task }, runTask = async () => ({ text: 'Review done' })) {
  const calls = [];
  const fetcher = async (url, opts) => {
    calls.push({ url, opts });
    return calls.length === 1 ? taskResponse : { ok: true, status: 202 };
  };
  return { calls, fetcher, implementations: { codex: { runTask } } };
}

test('successful task posts conversation and ACKs after CLI response', async () => {
  const h = harness();
  assert.equal(await runOneShot({ env, ...h }), 'completed');
  assert.match(h.calls[1].url, /task-messages$/);
  assert.match(h.calls[2].url, /tasks\/42\/ack$/);
  assert.equal(h.calls[0].opts.headers['X-Internal-Token'], 'secret');
  assert.equal(JSON.parse(h.calls[1].opts.body).repo, 'owner/repo');
});

test('CLI failure NACKs; retry backoff skips execution', async () => {
  const h = harness(undefined, async () => { throw new Error('login required'); });
  assert.equal(await runOneShot({ env, ...h }), 'failed');
  assert.match(h.calls[1].url, /tasks\/42\/nack$/);
  const backoff = harness({ status: 409, ok: false });
  assert.equal(await runOneShot({ env, ...backoff }), 'skipped');
  assert.equal(backoff.calls.length, 1);
});

test('aborted task is left pending and identity mismatch cannot run', async () => {
  const controller = new AbortController();
  const h = harness(undefined, async () => { controller.abort(); throw new Error('terminated'); });
  await assert.rejects(runOneShot({ env, signal: controller.signal, ...h }), /terminated/);
  assert.equal(h.calls.length, 1);
  const wrong = harness({ status: 200, ok: true, json: async () => ({ ...task, agent_name: 'other' }) });
  await assert.rejects(runOneShot({ env, ...wrong }), /identity mismatch/);
  assert.equal(wrong.calls.length, 1);
});

test('failed ACK is not turned into a NACK after successful execution', async () => {
  const h = harness();
  const original = h.fetcher;
  h.fetcher = async (url, opts) => {
    const response = await original(url, opts);
    return url.endsWith('/ack') ? { ok: false, status: 503 } : response;
  };
  await assert.rejects(runOneShot({ env, ...h }), /ack failed/);
  assert.equal(h.calls.length, 3);
});

test('prompt bounds untrusted payload', () => {
  const prompt = buildPrompt({ ...task, payload: { text: 'x'.repeat(40000) } }, 'Review carefully');
  assert.ok(prompt.length < 33000);
  assert.match(prompt, /untrusted data/);
});

test('native CLI does not inherit hub credentials or API billing overrides', () => {
  const clean = runtimeEnvironment({ ...env, ANTHROPIC_API_KEY: 'api-key', GITHUB_TOKEN: 'forge-token' });
  assert.equal(clean.HUB_INTERNAL_VALIDATE_SECRET, undefined);
  assert.equal(clean.ANTHROPIC_API_KEY, undefined);
  assert.equal(clean.GITHUB_TOKEN, 'forge-token');
});
