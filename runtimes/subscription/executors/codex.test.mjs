import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import test from 'node:test';
import { authStatus, runTask, startLogin } from './codex.mjs';

// Exercise real process pipes, exit statuses, and cancellation without a login
// or network request. Only the executable transport is replaced.
function transport(script, inspect = () => {}) {
  return (command, args, options) => {
    assert.equal(command, 'codex');
    assert.equal(options.shell, undefined);
    inspect(args, options);
    return spawn(process.execPath, ['-e', script], options);
  };
}

const taskOptions = { prompt: 'Review this PR', cwd: process.cwd() };

test('login status reports only authentication, never native credential output', async () => {
  for (const [code, authenticated] of [[0, true], [1, false]]) {
    const result = await authStatus({
      spawn: transport(`console.error('Logged in using API key sk-private'); process.exit(${code})`, (args) => {
        assert.deepEqual(args, ['login', 'status']);
      }),
    });
    assert.deepEqual(result, { authenticated });
  }
  await assert.rejects(authStatus({ spawn: transport('process.exit(2)') }), /login status failed/);
});

test('device login emits only official verification URL and one-time code', async () => {
  const output = [];
  const result = await startLogin({
    onOutput: (event) => output.push(event),
    spawn: transport(`
      console.error('Warning: access_token=private');
      console.error('https://evil.example/codex/device');
      console.error('https://auth.openai.com/codex/device?access_token=private');
      console.error('  https://auth.openai.com/codex/device');
      console.error('\\x1b[1mABCD-12345\\x1b[0m');
      console.log('https://auth.openai.com/codex/device');
      console.log('Login complete: refresh_token=private');
    `, (args) => assert.deepEqual(args, ['login', '--device-auth'])),
  });
  assert.deepEqual(result, { authenticated: true });
  assert.deepEqual(output, [
    { type: 'verification_url', url: 'https://auth.openai.com/codex/device' },
    { type: 'user_code', code: 'ABCD-12345' },
  ]);
  await assert.rejects(startLogin({ spawn: transport("console.error('private-token'); process.exit(1)") }), {
    message: 'Codex device login failed',
  });
});

test('task uses explicit cwd, stdin prompt, native ephemeral mode, and read-only sandbox', async () => {
  const events = [];
  const prompt = '--dangerously-bypass-approvals-and-sandbox\n$(touch /tmp/not-executed)';
  const result = await runTask({
    ...taskOptions,
    prompt,
    model: 'selected-model',
    onEvent: (event) => events.push(event),
    spawn: transport(`
      let input = '';
      process.stdin.setEncoding('utf8');
      process.stdin.on('data', chunk => input += chunk);
      process.stdin.on('end', () => {
        if (input !== ${JSON.stringify(prompt)}) process.exit(2);
        console.error('diagnostic with private credentials');
        const lines = [
          {type:'thread.started',thread_id:'thread-1'},
          {type:'item.completed',item:{type:'agent_message',text:'Ergebnis: sicher ✓'}},
          {type:'turn.completed',usage:{input_tokens:10,cached_input_tokens:3,output_tokens:2}},
          {type:'item.completed',item:{type:'agent_message',text:'Second message'}},
          {type:'turn.completed',usage:{input_tokens:5,output_tokens:1}},
        ];
        for (const event of lines) process.stdout.write(JSON.stringify(event) + '\\r\\n');
      });
    `, (args, options) => {
      assert.equal(options.cwd, taskOptions.cwd);
      assert.deepEqual(args, [
        'exec', '--json', '--ephemeral', '--sandbox', 'read-only', '--cd', taskOptions.cwd,
        '--model', 'selected-model', '-',
      ]);
      assert.equal(args.includes(prompt), false);
    }),
  });
  assert.deepEqual(result, {
    text: 'Ergebnis: sicher ✓\n\nSecond message',
    usage: { input_tokens: 15, cached_input_tokens: 3, output_tokens: 3 },
  });
  assert.equal(events.length, 5);
});

test('task redacts environment credentials in nested events and result text', async () => {
  const events = [];
  const result = await runTask({
    ...taskOptions,
    env: { CODEX_ACCESS_TOKEN: 'private-token', API_KEY: 'private-key' },
    onEvent: (event) => events.push(event),
    spawn: transport(`
      console.log(JSON.stringify({type:'item.completed',item:{type:'agent_message',text:'private-token / private-key'}}));
      console.log(JSON.stringify({type:'item.completed',item:{type:'mcp_tool_call',result:{content:['private-token']}}}));
      console.log(JSON.stringify({type:'turn.completed',usage:{input_tokens:1}}));
    `),
  });
  assert.equal(result.text, '[REDACTED] / [REDACTED]');
  assert.equal(JSON.stringify(events).includes('private-token'), false);
  assert.equal(JSON.stringify(events).includes('private-key'), false);
});

test('task rejects unsuccessful, failed, malformed, and incomplete streams without exposing diagnostics', async () => {
  for (const script of [
    "console.error('private-token'); process.exit(1)",
    "console.log(JSON.stringify({type:'turn.failed',error:{message:'private-token'}}))",
    "console.log(JSON.stringify({type:'error',message:'private-token'})); console.log(JSON.stringify({type:'turn.completed'}))",
    "console.log('private-token, not JSON')",
    "console.log(JSON.stringify({type:'thread.started'}))",
    "console.log('null')",
  ]) {
    await assert.rejects(runTask({ ...taskOptions, spawn: transport(script) }), (error) => {
      assert.equal(error.message.includes('private-token'), false);
      return /Codex/.test(error.message);
    });
  }
});

test('task handles subprocess launch errors without leaking command context', async () => {
  await assert.rejects(runTask({
    ...taskOptions,
    spawn: (_command, _args, options) => spawn('/nonexistent/ainsel-codex', [], options),
  }), { message: 'Unable to start Codex CLI' });
});

test('abort terminates a running native child and rejects', async () => {
  const controller = new AbortController();
  let child;
  const running = runTask({
    ...taskOptions,
    signal: controller.signal,
    spawn: (...args) => {
      child = transport("console.log(JSON.stringify({type:'thread.started'})); setInterval(() => {}, 1000)")(...args);
      return child;
    },
    onEvent: () => controller.abort(),
  });
  await assert.rejects(running, /interrupted/);
  assert.equal(child.killed, true);
});

test('reject invalid input before invoking native CLI', async () => {
  const neverSpawn = () => assert.fail('invalid input spawned Codex');
  for (const overrides of [{ prompt: '' }, { cwd: 'relative' }, { sandbox: 'unsafe' }, { model: 5 }]) {
    await assert.rejects(runTask({ ...taskOptions, ...overrides, spawn: neverSpawn }), TypeError);
  }
});
