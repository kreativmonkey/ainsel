import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

// Set AINSEL_PI_CLI to the deployed Pi 1.3.0 CLI to test the actual RPC stack.
// The extension's historic peer lock is older, so do not silently test that build.
const cli = process.env.AINSEL_PI_CLI;
test('Pi RPC advertises the scoped reader to the model and executes a commit-bound source read', { skip: !cli && 'Set AINSEL_PI_CLI to a Pi 1.3.0 CLI', timeout: 30000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), 'ainsel-review-rpc-'));
  const repository = join(root, 'repository');
  const requests = [];
  const server = createServer(async (req, res) => {
    let data = '';
    for await (const chunk of req) data += chunk;
    const body = JSON.parse(data);
    requests.push(body);
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    const delta = requests.length === 1
      ? { role: 'assistant', tool_calls: [{ index: 0, id: 'read-source', type: 'function', function: { name: 'ainsel_review_repository', arguments: JSON.stringify({ operation: 'read', path: 'source.txt' }) } }] }
      : { role: 'assistant', content: 'Review completed using verified source.' };
    res.end(`data: ${JSON.stringify({ id: 'fixture', object: 'chat.completion.chunk', created: 1, model: 'fixture', choices: [{ index: 0, delta, finish_reason: null }] })}\n\ndata: ${JSON.stringify({ id: 'fixture', object: 'chat.completion.chunk', created: 1, model: 'fixture', choices: [{ index: 0, delta: {}, finish_reason: requests.length === 1 ? 'tool_calls' : 'stop' }] })}\n\ndata: [DONE]\n\n`);
  });
  let child;
  try {
    await mkdir(repository);
    const git = async (...args) => (await promisify(execFile)('git', args, { cwd: repository })).stdout.trim();
    await git('init', '--template=');
    await writeFile(join(repository, 'source.txt'), 'immutable-review-source');
    await git('add', '.');
    await git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'source');
    const sha = await git('rev-parse', 'HEAD');
    await writeFile(join(repository, 'source.txt'), 'working-tree-must-not-be-read');
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const moduleUrl = pathToFileURL(fileURLToPath(new URL('./review.mjs', import.meta.url))).href;
    const fixture = join(root, 'fixture.mjs');
    await writeFile(fixture, `import { createRepositoryReader, registerReviewTool } from ${JSON.stringify(moduleUrl)};
export default async function(pi) {
  const read = await createRepositoryReader(${JSON.stringify(repository)}, '${sha}', '${sha}');
  registerReviewTool(pi, () => read);
  pi.registerTool({ name: 'mcp__hostile__read_auth', label: 'Forbidden', description: 'Must not be visible', parameters: { type: 'object', properties: {} }, async execute() { throw new Error('Forbidden tool executed'); } });
  pi.registerProvider('review-fixture', { baseUrl: 'http://127.0.0.1:${server.address().port}/v1', apiKey: 'fixture-not-a-secret', api: 'openai-completions', models: [{ id: 'fixture', name: 'Fixture', reasoning: false, input: ['text'], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 64000, maxTokens: 1000 }] });
}`);
    const entrypoint = await import('node:fs/promises').then(fs => fs.readFile(new URL('../../entrypoint.sh', import.meta.url), 'utf8'));
    const modeFlags = entrypoint.match(/set -- "\$@" (.*--no-prompt-templates)/)?.[1].split(' ');
    assert.ok(modeFlags, 'review CLI flags must be found in the actual entrypoint');
    child = spawn(process.execPath, [resolve(cli), '--mode', 'rpc', '--no-session', ...modeFlags, '--extension', fixture, '--provider', 'review-fixture', '--model', 'fixture'], {
      cwd: root, env: { PATH: process.env.PATH, HOME: root, PI_CODING_AGENT_DIR: join(root, 'agent') }, stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = ''; let stderr = '';
    child.stderr.on('data', chunk => stderr += chunk);
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`Pi RPC did not settle: ${stderr}\n${stdout}`)), 15000);
      child.stdout.on('data', chunk => {
        stdout += chunk;
        if (stdout.includes('"type":"agent_settled"')) { clearTimeout(timer); resolve(); }
      });
      child.on('error', reject);
      child.on('exit', code => reject(new Error(`Pi exited ${code}: ${stderr}\n${stdout}`)));
      child.stdin.write(JSON.stringify({ type: 'prompt', message: 'Inspect source.txt and review it.' }) + '\n');
    });
    assert.equal(requests.length, 2, stderr + stdout);
    for (const body of requests) assert.deepEqual((body.tools ?? []).map(t => t.function.name), ['ainsel_review_repository']);
    const results = requests[1].messages.filter(m => m.role === 'tool');
    assert.ok(results.some(m => JSON.stringify(m.content).includes('immutable-review-source')));
    assert.ok(results.every(m => !JSON.stringify(m.content).includes('working-tree-must-not-be-read')));
  } finally {
    if (child) { child.kill('SIGKILL'); await new Promise(resolve => child.once('close', resolve)); }
    await new Promise(resolve => server.close(resolve));
    await rm(root, { recursive: true, force: true });
  }
});
