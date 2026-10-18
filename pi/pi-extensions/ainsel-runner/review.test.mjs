import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, symlink, rm, readFile, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createRepositoryReader, pullRequestContext, preparePullRequestWorkspace, registerReviewTool, REVIEW_TOOL } from './review.mjs';

const execute = promisify(execFile);
test('immutable repository reader supports full source and rejects credential/symlink escape', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'ainsel-review-'));
  const git = async (...args) => (await execute('git', args, { cwd: directory })).stdout.trim();
  try {
    await git('init', '--template=');
    await git('config', 'user.email', 'test@example.invalid');
    await git('config', 'user.name', 'Test');
    await writeFile(join(directory, 'source.txt'), 'initial\n');
    await git('add', '.'); await git('commit', '-m', 'base');
    const base = await git('rev-parse', 'HEAD');
    await writeFile(join(directory, 'source.txt'), 'initial\nreview-me\n');
    await symlink('/home/agent/.pi/agent/auth.json', join(directory, 'credential-link'));
    await git('add', '.'); await git('commit', '-m', 'head');
    const head = await git('rev-parse', 'HEAD');
    const read = await createRepositoryReader(directory, head, base);
    await writeFile(join(directory, 'source.txt'), 'modified after checkout');
    assert.deepEqual(JSON.parse(await read({ operation: 'list' })).files, ['source.txt']);
    assert.match(await read({ operation: 'read', path: 'source.txt' }), /review-me/);
    assert.match(await read({ operation: 'search', query: 'review-me' }), /source.txt/);
    assert.match(await read({ operation: 'diff' }), /\+review-me/);
    for (const path of ['credential-link', '../auth.json', '/home/agent/.pi/agent/auth.json', '.git/config']) {
      await assert.rejects(read({ operation: 'read', path }), /regular tracked/);
    }
    await assert.rejects(read({ operation: 'read', path: 'source.txt', offset: -1 }), /offset/);
    await assert.rejects(read({ operation: 'search', query: '' }), /literal/);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

test('review tool disables built-in and MCP execution and returns only scoped data', async () => {
  const handlers = new Map(); let tool; let active;
  registerReviewTool({ registerTool: t => tool = t, on: (name, fn) => handlers.set(name, fn), setActiveTools: names => active = names }, () => async () => 'verified source');
  handlers.get('before_agent_start')();
  assert.deepEqual(active, [REVIEW_TOOL]);
  for (const toolName of ['bash', 'read', 'write', 'mcp__forge__exec', 'mcp__http__fetch']) assert.equal(handlers.get('tool_call')({ toolName }).block, true);
  assert.equal(handlers.get('tool_call')({ toolName: REVIEW_TOOL }), undefined);
  assert.equal((await tool.execute('id', { operation: 'list' })).content[0].text, 'verified source');
});

test('PR context uses canonical unauthenticated API, bounds data and pins revisions', async () => {
  const revision = { base: { sha: 'a'.repeat(40) }, head: { sha: 'b'.repeat(40) } };
  const task = { headers: { type: 'pull_request' }, payload: { repository: { full_name: 'owner/repo' }, pull_request: { number: 1, ...revision, patch_url: 'https://evil.invalid' } } };
  let calls = 0;
  const fetcher = async (url, options) => {
    calls++;
    assert.equal(url, 'https://api.github.com/repos/owner/repo/pulls/1');
    assert.equal(options.redirect, 'error');
    assert.equal(options.headers.Authorization, undefined);
    return new Response(options.headers.Accept.endsWith('+json') ? JSON.stringify(revision) : 'diff --git a/a b/a');
  };
  assert.match(await pullRequestContext(task, { repository: 'owner/repo', fetcher }), /untrusted-pr-patch/);
  assert.equal(calls, 3);
  await assert.rejects(pullRequestContext(task, { repository: 'other/repo', fetcher }), /not allowed/);
  await assert.rejects(pullRequestContext(task, { repository: 'owner/repo', fetcher: async () => new Response('x'.repeat(512 * 1024 + 1)) }), /512 KiB/);
  await assert.rejects(pullRequestContext(task, { repository: 'owner/repo', fetcher: async () => new Response(JSON.stringify({ ...revision, head: { sha: 'c'.repeat(40) } })) }), /revision changed/);
});

test('verified PR checkout supplies exact source and base without git credentials or hooks', async () => {
  const task = { headers: { type: 'pull_request' }, payload: { repository: { full_name: 'owner/repo' } } };
  const root = await mkdtemp(join(tmpdir(), 'ainsel-pr-checkout-'));
  const execute = promisify(execFile);
  const source = join(root, 'source');
  const cwd = join(root, 'workspace');
  try {
    await execute('git', ['init', '--template=', source]);
    const git = (...args) => execute('git', ['-C', source, ...args]);
    await writeFile(join(source, 'review.txt'), 'base');
    await git('add', '.');
    await git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'base');
    const base = (await git('rev-parse', 'HEAD')).stdout.trim();
    await writeFile(join(source, 'review.txt'), 'exact head');
    await git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-am', 'head');
    const head = (await git('rev-parse', 'HEAD')).stdout.trim();
    await git('update-ref', 'refs/pull/3/head', head);
    const actualTask = { ...task, payload: { ...task.payload, pull_request: {
      number: 3, base: { sha: base }, head: { sha: head }, clone_url: 'https://evil.invalid/repo',
    } } };
    const calls = [];
    const transport = async (command, args, options) => {
      calls.push({ args, options });
      assert.equal(command, 'git');
      assert.deepEqual(Object.keys(options.env).sort(), ['GIT_CONFIG_GLOBAL', 'GIT_CONFIG_NOSYSTEM', 'GIT_LFS_SKIP_SMUDGE', 'GIT_TERMINAL_PROMPT', 'HOME', 'PATH']);
      assert.equal(options.env.HOME, options.cwd);
      assert.ok(args.includes('http.followRedirects=false'));
      assert.ok(args.includes('core.hooksPath=/dev/null'));
      // Substitute only transport in this test; execute real fetch, revision checks and checkout.
      if (args.includes('fetch')) {
        args = calls.at(-1).args.map(arg => arg === 'origin' ? source : arg);
        args.unshift('-c', 'protocol.file.allow=always');
      }
      return execute(command, args, options);
    };
    const sourceDirectory = await preparePullRequestWorkspace(actualTask, { cwd, execute: transport });
    assert.equal(sourceDirectory, join(cwd, 'repository'));
    assert.equal((await stat(sourceDirectory)).uid, process.getuid());
    // The executor must also be able to use Git without ownership exceptions.
    assert.equal((await execute('git', ['-C', sourceDirectory, 'rev-parse', 'HEAD'])).stdout.trim(), head);
    assert.equal(await readFile(join(sourceDirectory, 'review.txt'), 'utf8'), 'exact head');
    assert.equal((await execute('git', ['-C', sourceDirectory, 'cat-file', '-t', base])).stdout.trim(), 'commit');
    assert.equal((await execute('git', ['-C', sourceDirectory, 'remote', 'get-url', 'origin'])).stdout.trim(), 'https://github.com/owner/repo.git');
    const reader = await createRepositoryReader(sourceDirectory, head, base);
    assert.match(await reader({ operation: 'read', path: 'review.txt' }), /exact head/);
    assert.equal(calls.some(({ args }) => args.some(arg => arg.includes('evil.invalid'))), false);
    await assert.rejects(preparePullRequestWorkspace(actualTask, { cwd, execute: transport }), /must be empty/);
    const mismatch = { ...actualTask, payload: { ...actualTask.payload, pull_request: { ...actualTask.payload.pull_request, head: { sha: 'c'.repeat(40) } } } };
    await assert.rejects(preparePullRequestWorkspace(mismatch, { cwd: join(root, 'mismatch'), execute: transport }), /head changed/);
    await assert.rejects(readFile(join(root, 'mismatch', 'review.txt')), /ENOENT/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
