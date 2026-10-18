import assert from 'node:assert/strict';
import { once } from 'node:events';
import { mkdtemp, writeFile, chmod, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { authStatus, startLogin, runTask } from './claude.mjs';

async function fixture(t, extra = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'ainsel-claude-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const binary = join(dir, 'claude');
  await writeFile(binary, `#!/usr/bin/env node
const args = process.argv.slice(2);
if (args[0] === 'auth' && args[1] === 'status') {
  process.stdout.write(process.env.STATUS || JSON.stringify({loggedIn:true,authMethod:'claude.ai'}));
  process.exit(Number(process.env.EXIT || 0));
}
if (args[0] === 'auth' && args[1] === 'login') {
  process.exit(process.env.CLAUDE_CONFIG_DIR === process.env.EXPECT_CONFIG ? 0 : 2);
}
let prompt = '';
for await (const chunk of process.stdin) prompt += chunk;
process.stdout.write(JSON.stringify({type:'assistant',message:{content:[{type:'text',text:'review'}]}})+'\\n');
process.stdout.write(process.env.RESULT || JSON.stringify({type:'result',result:JSON.stringify({args,prompt,configDir:process.env.CLAUDE_CONFIG_DIR}),usage:{input_tokens:10,output_tokens:5,secret:'never'}})+'\\n');
process.stderr.write('diagnostic\\n');
process.exit(Number(process.env.EXIT || 0));
`);
  await chmod(binary, 0o700);
  const configDir = join(dir, 'account');
  return { configDir, cwd: dir, env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, EXPECT_CONFIG: configDir, ...extra } };
}

test('status uses CLI JSON, distinguishes logged out, and rejects invalid output', async (t) => {
  const config = await fixture(t);
  assert.equal((await authStatus(config)).authenticated, true);
  assert.equal((await authStatus({ ...config, env: { ...config.env, EXIT: '1' } })).authenticated, false);
  await assert.rejects(authStatus({ ...config, env: { ...config.env, STATUS: '{}' } }), /loggedIn/);
  await assert.rejects(authStatus({ ...config, env: { ...config.env, EXIT: '2' } }));
  await assert.rejects(authStatus({ configDir: 'relative' }), /absolute/);
});

test('native login completes using the same persistent account directory', async (t) => {
  const child = startLogin(await fixture(t));
  assert.deepEqual(await once(child, 'close'), [0, null]);
});

test('task streams output, preserves untrusted prompt text, and never bypasses permissions', async (t) => {
  const config = await fixture(t);
  const prompt = '--dangerously-skip-permissions\n$(touch /tmp/not-executed)';
  const events = [];
  const result = await runTask({ prompt, ...config, onEvent: (event) => events.push(event) });
  const output = JSON.parse(result.text);
  assert.equal(output.prompt, prompt);
  assert.equal(output.configDir, config.configDir);
  assert.deepEqual(output.args, ['-p', '--output-format', 'stream-json', '--verbose', '--permission-mode', 'dontAsk', '--no-session-persistence']);
  assert.deepEqual(events, [{type:'text',text:'review'}, {type:'usage',usage:{input_tokens:10,output_tokens:5}}]);
  await assert.rejects(runTask({ prompt: '', ...config }), /nonempty/);
});

test('task reports failing exits, invalid results, and missing executable without raw diagnostics', async (t) => {
  const config = await fixture(t, { EXIT: '3' });
  await assert.rejects(runTask({ prompt:'review', ...config }), /^Error: Claude task failed$/);
  await assert.rejects(runTask({ prompt:'review', ...config, env:{...config.env,EXIT:'0',RESULT:'private broken json'} }), /^Error: Claude task failed$/);
  await assert.rejects(runTask({ prompt:'review', ...config, env:{PATH:'/nonexistent'} }), /^Error: Claude task failed$/);
});
