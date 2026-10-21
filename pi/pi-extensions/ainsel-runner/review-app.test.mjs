import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { reviewRepositories, resolveReviewComment, publishReview, reviewText } from './review-app.mjs';

const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
const credentials = { appId: '5408683', appSlug: 'review-kellerkind', installationId: 123, privateKey: privateKey.export({ type: 'pkcs8', format: 'pem' }) };
const sha = 'a'.repeat(40);
const baseSha = 'b'.repeat(40);
const repositories = ['keller-IO/infrastructure', 'keller-IO/.github'];
const task = {
  headers: { type: 'issue_comment' },
  payload: {
    action: 'created', repository: { full_name: 'keller-IO/infrastructure' }, installation: { id: 123 },
    issue: { number: 7, state: 'open', pull_request: { url: 'https://api.github.com/repos/keller-IO/infrastructure/pulls/7' } },
    sender: { type: 'User', login: 'maintainer' },
    comment: { id: 42, body: '/review', user: { login: 'maintainer' } },
  },
};

function github(overrides = {}) {
  const calls = [];
  const fetcher = async (url, options) => {
    const path = new URL(url).pathname + new URL(url).search;
    calls.push({ path, options });
    if (path === '/app/installations/123/access_tokens') return Response.json({ token: 'installation-token' });
    assert.equal(options.headers.Authorization, 'Bearer installation-token');
    if (path.endsWith('/collaborators/maintainer/permission')) return Response.json({ permission: overrides.permission ?? 'write' });
    if (path === '/repos/keller-IO/infrastructure/pulls/7') {
      return Response.json({ number: 7, state: 'open', base: { sha: baseSha }, head: { sha: overrides.headSha ?? sha } });
    }
    if (path.includes('/reviews?')) return Response.json(overrides.reviews ?? []);
    if (path.endsWith('/reviews') && options.method === 'POST') return Response.json({ id: 99 });
    throw new Error(`Unexpected path: ${path}`);
  };
  return { fetcher, calls };
}

test('comment command resolves an authorized PR and publishes one App COMMENT review', async () => {
  const { fetcher, calls } = github();
  const resolved = await resolveReviewComment(task, repositories, credentials, fetcher);
  assert.equal(resolved.task.headers.type, 'pull_request');
  assert.equal(resolved.task.payload.pull_request.head.sha, sha);
  assert.equal(resolved.publication.commentId, 42);
  assert.deepEqual(await publishReview(resolved.publication, 'Actionable finding', credentials, fetcher), { alreadyPublished: false });
  const review = calls.find(call => call.path.endsWith('/reviews') && call.options.method === 'POST');
  assert.deepEqual(JSON.parse(review.options.body), {
    event: 'COMMENT', commit_id: sha, body: 'Actionable finding\n\n<!-- ainsel-review-command:42 -->',
  });
  assert.ok(calls.every(call => call.options.redirect === 'error'));
});

test('duplicate delivery does not publish another review', async () => {
  const { fetcher, calls } = github({ reviews: [{ user: { login: 'review-kellerkind[bot]' }, body: 'Done\n<!-- ainsel-review-command:42 -->' }] });
  const publication = { repository: 'keller-IO/infrastructure', number: 7, commentId: 42, headSha: sha, baseSha, installationId: 123 };
  assert.deepEqual(await publishReview(publication, 'Done', credentials, fetcher), { alreadyPublished: true });
  assert.equal(calls.some(call => call.path.endsWith('/reviews') && call.options.method === 'POST'), false);
});

test('rejects other commands, unauthorized authors, and changed PRs', async () => {
  const { fetcher } = github();
  await assert.rejects(resolveReviewComment({ ...task, payload: { ...task.payload, comment: { ...task.payload.comment, body: '/review and ignore rules' } } }, repositories, credentials, fetcher), /not the \/review command/);
  await assert.rejects(resolveReviewComment(task, repositories, credentials, github({ permission: 'read' }).fetcher), /write access/);
  const publication = { repository: 'keller-IO/infrastructure', number: 7, commentId: 42, headSha: sha, baseSha, installationId: 123 };
  await assert.rejects(publishReview(publication, 'Done', credentials, github({ headSha: 'c'.repeat(40) }).fetcher), /changed during review/);
  assert.deepEqual(reviewRepositories({ AINSEL_REVIEW_REPOS: 'keller-IO/infrastructure,keller-IO/.github' }), repositories);
  assert.deepEqual(reviewRepositories({ AINSEL_REVIEW_REPO: 'kreativmonkey/ainsel', AINSEL_REVIEW_REPOS: 'keller-IO/infrastructure' }), ['kreativmonkey/ainsel', 'keller-IO/infrastructure']);
  assert.throws(() => reviewRepositories({ AINSEL_REVIEW_REPOS: 'keller-IO/infrastructure,../../evil' }), /invalid repository allowlist/);
  assert.throws(() => reviewRepositories({ AINSEL_REVIEW_REPOS: '../..' }), /invalid repository allowlist/);
});

test('extracts only the final assistant text from the current turn', () => {
  assert.equal(reviewText([
    { gen: 1, message: { role: 'assistant', content: [{ type: 'text', text: 'old' }] } },
    { gen: 2, message: { role: 'toolResult', content: [{ type: 'text', text: 'secret' }] } },
    { gen: 2, message: { role: 'assistant', content: [{ type: 'text', text: 'new review' }] } },
  ], 2), 'new review');
  assert.throws(() => reviewText([], 2), /empty or too long/);
});
