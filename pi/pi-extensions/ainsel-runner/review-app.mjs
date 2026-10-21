import { createPrivateKey, sign } from 'node:crypto';

const api = 'https://api.github.com';
const shaPattern = /^[a-f0-9]{40}$/i;

function fail(message) { throw new Error(`GitHub review: ${message}`); }

export function reviewRepositories(env = process.env) {
  const names = [env.AINSEL_REVIEW_REPO, ...(env.AINSEL_REVIEW_REPOS?.split(',') ?? [])]
    .map(value => value?.trim()).filter(Boolean);
  if (!names.length) return [];
  if (new Set(names.map(value => value.toLowerCase())).size !== names.length
      || names.some(value => !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(value)
        || value.split('/').some(part => part === '.' || part === '..'))) {
    fail('invalid repository allowlist');
  }
  return names;
}

export function reviewText(messages, generation) {
  const last = messages.filter(entry => entry.gen === generation && entry.message?.role === 'assistant').at(-1)?.message;
  const body = last?.content?.filter(part => part.type === 'text').map(part => part.text).join('\n').trim();
  if (!body || body.length > 60000) fail('assistant review is empty or too long');
  return body;
}

export function inlineReview(text) {
  let review;
  try { review = JSON.parse(text); } catch { fail('inline review must be JSON'); }
  if (!review || typeof review !== 'object' || Array.isArray(review)
      || typeof review.summary !== 'string' || !review.summary.trim() || review.summary.length > 60000
      || typeof review.reviewable !== 'boolean'
      || !Array.isArray(review.findings) || review.findings.length > 20) fail('invalid inline review');
  if (!review.reviewable && review.findings.length) fail('incomplete review cannot contain findings');
  let blockers = 0;
  const comments = review.findings.map(finding => {
    if (!finding || typeof finding !== 'object' || Array.isArray(finding)
        || !['blocker', 'suggestion'].includes(finding.severity)
        || typeof finding.path !== 'string' || !finding.path
        || !Number.isSafeInteger(finding.line) || finding.line <= 0
        || typeof finding.body !== 'string' || !finding.body.trim() || finding.body.length > 8000) {
      fail('invalid inline finding');
    }
    if (finding.severity === 'blocker') blockers++;
    const label = finding.severity === 'blocker' ? 'Blocker' : 'Suggestion';
    return { path: finding.path, line: finding.line, side: 'RIGHT', body: `**${label}:** ${finding.body}` };
  });
  const recommendation = !review.reviewable ? 'No Merge Recommendation (Review Incomplete)'
    : blockers ? 'Do Not Merge' : 'Ready to Merge';
  const suggestions = comments.length - blockers;
  const counts = `${blockers} ${blockers === 1 ? 'blocker' : 'blockers'} · ${suggestions} ${suggestions === 1 ? 'suggestion' : 'suggestions'}`;
  return { body: `## Recommendation: ${recommendation}\n\n${review.summary}\n\n${counts}`, comments };
}

function appJwt(appId, privateKey, now = Date.now()) {
  if (!/^[1-9][0-9]*$/.test(String(appId)) || !privateKey) fail('App credentials are missing');
  const timestamp = Math.floor(now / 1000);
  const encoded = [
    { alg: 'RS256', typ: 'JWT' },
    { iat: timestamp - 60, exp: timestamp + 540, iss: String(appId) },
  ].map(value => Buffer.from(JSON.stringify(value)).toString('base64url')).join('.');
  return `${encoded}.${sign('RSA-SHA256', Buffer.from(encoded), createPrivateKey(privateKey)).toString('base64url')}`;
}

async function responseJSON(fetcher, path, { token, method = 'GET', body } = {}) {
  const response = await fetcher(`${api}${path}`, {
    method,
    headers: {
      Accept: 'application/vnd.github+json',
      Authorization: `Bearer ${token}`,
      'X-GitHub-Api-Version': '2022-11-28',
      ...(body ? { 'Content-Type': 'application/json' } : {}),
    },
    ...(body ? { body: JSON.stringify(body) } : {}),
    redirect: 'error',
    signal: AbortSignal.timeout(15000),
  });
  if (!response.ok) fail(`GitHub API returned HTTP ${response.status}`);
  return response.json();
}

export async function installationToken({ appId, privateKey, installationId, fetcher = fetch }) {
  if (!Number.isSafeInteger(installationId) || installationId <= 0) fail('invalid installation ID');
  const result = await responseJSON(fetcher, `/app/installations/${installationId}/access_tokens`, {
    token: appJwt(appId, privateKey), method: 'POST',
  });
  if (typeof result.token !== 'string' || !result.token) fail('installation token missing');
  return result.token;
}

function requestIdentity(task, repositories) {
  const payload = task.payload ?? {};
  const repository = payload.repository?.full_name;
  const number = payload.issue?.number ?? payload.number;
  if (!repositories.some(value => value.toLowerCase() === repository?.toLowerCase())) fail('repository is not allowed');
  if (!Number.isSafeInteger(number) || number <= 0) fail('invalid pull request number');
  if (!Number.isSafeInteger(payload.installation?.id) || payload.installation.id <= 0) fail('installation is missing');
  return { repository, number, installationId: payload.installation.id };
}

export async function resolveReviewComment(task, repositories, credentials, fetcher = fetch) {
  if (task.headers?.type !== 'issue_comment' || task.payload?.action !== 'created') fail('expected a new issue comment');
  const payload = task.payload;
  if (!payload.issue?.pull_request || payload.issue.state !== 'open') fail('comment is not on an open pull request');
  const command = payload.comment?.body?.trim();
  if (command !== '/review' && command !== '/review-inline') fail('comment is not a review command');
  if (payload.sender?.type !== 'User' || payload.sender.login !== payload.comment?.user?.login) fail('invalid command author');
  if (!Number.isSafeInteger(payload.comment?.id) || payload.comment.id <= 0) fail('invalid comment ID');

  const identity = requestIdentity(task, repositories);
  if (identity.installationId !== credentials.installationId) fail('unexpected app installation');
  const token = await installationToken({ ...credentials, fetcher });
  const path = `/repos/${identity.repository}`;
  const permission = await responseJSON(fetcher, `${path}/collaborators/${encodeURIComponent(payload.sender.login)}/permission`, { token });
  if (!['admin', 'maintain', 'write'].includes(permission.permission)) fail('command author needs repository write access');
  const pr = await responseJSON(fetcher, `${path}/pulls/${identity.number}`, { token });
  if (pr.state !== 'open' || !shaPattern.test(pr.head?.sha ?? '') || !shaPattern.test(pr.base?.sha ?? '')) {
    fail('pull request is closed or has invalid revisions');
  }
  return {
    task: {
      ...task,
      headers: { ...task.headers, type: 'pull_request' },
      payload: { ...payload, number: identity.number, pull_request: { ...pr, number: identity.number } },
    },
    publication: { ...identity, command, commentId: payload.comment.id, headSha: pr.head.sha, baseSha: pr.base.sha },
  };
}

function addedLines(patch) {
  const lines = new Set();
  let line;
  for (const part of patch.split('\n')) {
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(part);
    if (hunk) { line = Number(hunk[1]); continue; }
    if (line === undefined) continue;
    if (part.startsWith('+')) { lines.add(line); line++; }
    else if (part.startsWith(' ')) line++;
  }
  return lines;
}

async function validateComments(fetcher, path, token, comments) {
  const pending = new Map();
  for (const comment of comments) {
    if (!pending.has(comment.path)) pending.set(comment.path, new Set());
    pending.get(comment.path).add(comment.line);
  }
  for (let page = 1; page <= 30 && pending.size; page++) {
    const files = await responseJSON(fetcher, `${path}/files?per_page=100&page=${page}`, { token });
    if (!Array.isArray(files)) fail('invalid pull request file list');
    for (const file of files) {
      const requested = pending.get(file.filename);
      if (!requested) continue;
      if (typeof file.patch !== 'string') fail(`diff unavailable for ${file.filename}`);
      const allowed = addedLines(file.patch);
      if ([...requested].some(line => !allowed.has(line))) fail(`line is not added in ${file.filename}`);
      pending.delete(file.filename);
    }
    if (files.length < 100) break;
  }
  if (pending.size) fail('inline finding refers to a file outside the pull request diff');
}

export async function publishReview(publication, review, credentials, fetcher = fetch) {
  const { repository, number, commentId, headSha, baseSha, installationId } = publication;
  if (installationId !== credentials.installationId || !shaPattern.test(headSha) || !shaPattern.test(baseSha)) fail('review identity changed');
  if (!/^[a-z0-9-]+$/.test(credentials.appSlug ?? '')) fail('App slug is missing');
  const token = await installationToken({ ...credentials, fetcher });
  const path = `/repos/${repository}/pulls/${number}`;
  const pr = await responseJSON(fetcher, path, { token });
  if (pr.state !== 'open' || pr.head?.sha !== headSha || pr.base?.sha !== baseSha) fail('pull request changed during review');
  const marker = `<!-- ainsel-review-command:${commentId} -->`;
  for (let page = 1; page <= 10; page++) {
    const reviews = await responseJSON(fetcher, `${path}/reviews?per_page=100&page=${page}`, { token });
    if (!Array.isArray(reviews)) fail('invalid review list');
    if (reviews.some(review => review.user?.login?.toLowerCase() === `${credentials.appSlug}[bot]`
        && review.body?.includes(marker))) return { alreadyPublished: true };
    if (reviews.length < 100) break;
    if (page === 10) fail('too many reviews to check for duplicate delivery');
  }
  const content = publication.command === '/review-inline' ? inlineReview(review) : { body: review, comments: [] };
  if (content.comments.length) {
    await validateComments(fetcher, path, token, content.comments);
    const current = await responseJSON(fetcher, path, { token });
    if (current.state !== 'open' || current.head?.sha !== headSha || current.base?.sha !== baseSha) fail('pull request changed during review');
  }
  await responseJSON(fetcher, `${path}/reviews`, {
    token, method: 'POST', body: { event: 'COMMENT', commit_id: headSha,
      body: `${content.body}\n\n${marker}`,
      ...(content.comments.length ? { comments: content.comments } : {}) },
  });
  return { alreadyPublished: false };
}
