// Soonest-reset selector scenarios (MASTRA-765, MASTRA-768). Each scenario owns its model and
// credentials (hermetic/start.mjs): credential `<model>-<name>` serves `<model>` and its own
// `<model>-<name>`, which seeds that credential's quota observation. Exhaustion is seeded with
// 200 responses carrying exhausted headers, so the selector's header rule decides, not cooldown.
// Several new sessions must land on one credential, which round-robin cannot do by chance.
import { randomUUID } from 'node:crypto';
import { expect, test } from 'e2e';
import { clientKey, input, post, script, upstreamRequests } from '../lib/proxy.ts';

const hermetic = { platforms: ['hermetic'] };
const hour = 3600;
const day = 24 * hour;
const at = (seconds: number) => String(Math.floor(Date.now() / 1000) + seconds);

// Codex Pro reports its weekly window as the primary one.
const codexWeekly = (usedPercent: number, resetIn: number) => ({
  'x-codex-primary-used-percent': String(usedPercent),
  'x-codex-primary-window-minutes': '10080',
  'x-codex-primary-reset-at': at(resetIn),
});
const claudeWindows = (
  fiveHourUtilization: number,
  fiveHourResetIn: number,
  weekResetIn: number,
) => ({
  'anthropic-ratelimit-unified-status': 'allowed',
  'anthropic-ratelimit-unified-5h-utilization': String(fiveHourUtilization),
  'anthropic-ratelimit-unified-5h-reset': at(fiveHourResetIn),
  'anthropic-ratelimit-unified-7d-utilization': '0.5',
  'anthropic-ratelimit-unified-7d-reset': at(weekResetIn),
});

/** Sends one Responses request in `session`; returns the credential keys the mock saw. */
async function responses(baseUrl: string | undefined, model: string, session = randomUUID()) {
  const marker = randomUUID();
  const response = await post(
    baseUrl,
    '/v1/responses',
    { model, input: input(marker) },
    { Session_id: session },
  );
  expect(response.status).toBe(200);
  await response.text();
  return (await upstreamRequests(marker)).map((request) => request.key);
}

/** Sends one Messages request in `session`; returns the credential keys the mock saw. */
async function messages(baseUrl: string | undefined, model: string, session = randomUUID()) {
  const marker = randomUUID();
  const response = await post(
    baseUrl,
    '/v1/messages',
    { model, max_tokens: 16, messages: [{ role: 'user', content: marker }] },
    {
      'x-api-key': clientKey,
      'anthropic-version': '2023-06-01',
      'X-Claude-Code-Session-Id': session,
    },
  );
  expect(response.status).toBe(200);
  await response.text();
  return (await upstreamRequests(marker)).map((request) => request.key);
}

test('S1: new Codex sessions go to the soonest weekly reset', hermetic, async ({ app }) => {
  await script({
    'e2e-s1-a': { headers: codexWeekly(50, 6 * day) },
    'e2e-s1-b': { headers: codexWeekly(50, 1 * day) },
    'e2e-s1-c': { headers: codexWeekly(50, 3 * day) },
  });
  for (const name of ['a', 'b', 'c']) await responses(app.baseUrl, `e2e-s1-${name}`);
  for (let i = 0; i < 3; i++) expect(await responses(app.baseUrl, 'e2e-s1')).toEqual(['e2e-s1-b']);
});

test(
  'S2: a small remainder resetting soon goes before a fresh late window, then fails over',
  hermetic,
  async ({ app }) => {
    await script({
      'e2e-s2-a': { headers: codexWeekly(98, 6 * hour) },
      'e2e-s2-b': { headers: codexWeekly(0, 5 * day) },
    });
    for (const name of ['a', 'b']) await responses(app.baseUrl, `e2e-s2-${name}`);
    for (let i = 0; i < 2; i++) {
      expect(await responses(app.baseUrl, 'e2e-s2')).toEqual(['e2e-s2-a']);
    }
    await script({ 'e2e-s2-a': { limited: true } });
    expect(await responses(app.baseUrl, 'e2e-s2')).toEqual(['e2e-s2-a', 'e2e-s2-b']);
  },
);

test(
  'S3: an exhausted Claude 5-hour window is skipped until it resets',
  hermetic,
  async ({ app }) => {
    const fiveHourReset = 4;
    await script({
      'e2e-s3-a': { headers: claudeWindows(1, fiveHourReset, 1 * day) },
      'e2e-s3-b': { headers: claudeWindows(0.1, 4 * hour, 3 * day) },
    });
    for (const name of ['a', 'b']) await messages(app.baseUrl, `e2e-s3-${name}`);
    for (let i = 0; i < 2; i++) expect(await messages(app.baseUrl, 'e2e-s3')).toEqual(['e2e-s3-b']);
    await new Promise((resolve) => setTimeout(resolve, (fiveHourReset + 1) * 1000));
    expect(await messages(app.baseUrl, 'e2e-s3')).toEqual(['e2e-s3-a']);
  },
);

test(
  'S5: an unknown Codex reset sorts last and an unknown Claude reset sorts first',
  hermetic,
  async ({ app }) => {
    await script({
      'e2e-s5-known': { headers: codexWeekly(50, 6 * day) },
      'e2e-s5-unknown': { headers: {} },
      'e2e-s5c-x': { headers: claudeWindows(0.1, 4 * hour, 6 * day) },
      'e2e-s5c-y': { headers: claudeWindows(0.1, 4 * hour, 1 * day) },
      'e2e-s5c-z': { headers: claudeWindows(0.1, 4 * hour, 3 * day) },
    });
    await responses(app.baseUrl, 'e2e-s5-known');
    for (let i = 0; i < 2; i++) {
      expect(await responses(app.baseUrl, 'e2e-s5')).toEqual(['e2e-s5-known']);
    }

    for (const name of ['y', 'z']) await messages(app.baseUrl, `e2e-s5c-${name}`);
    expect(await messages(app.baseUrl, 'e2e-s5c')).toEqual(['e2e-s5c-x']);
    // x's first response taught its late reset, so y (soonest) now leads.
    for (let i = 0; i < 2; i++)
      expect(await messages(app.baseUrl, 'e2e-s5c')).toEqual(['e2e-s5c-y']);
  },
);

test(
  'S7 (regression): a bound session stays on its credential when another becomes better',
  hermetic,
  async ({ app }) => {
    await script({
      'e2e-s7-a': { headers: codexWeekly(50, 3 * day) },
      'e2e-s7-b': { headers: codexWeekly(50, 3 * day) },
    });
    const session = randomUUID();
    const [bound] = await responses(app.baseUrl, 'e2e-s7', session);
    const other = bound === 'e2e-s7-a' ? 'e2e-s7-b' : 'e2e-s7-a';
    await script({
      [bound]: { headers: codexWeekly(50, 6 * day) },
      [other]: { headers: codexWeekly(50, 1 * hour) },
    });
    await responses(app.baseUrl, other);
    for (let i = 0; i < 3; i++) {
      expect(await responses(app.baseUrl, 'e2e-s7', session)).toEqual([bound]);
    }
  },
);

test(
  'S8 (regression): a burst of new sessions at exhaustion completes without client errors',
  hermetic,
  async ({ app }) => {
    await script({
      'e2e-s8-a': { headers: codexWeekly(99, 1 * hour) },
      'e2e-s8-b': { headers: codexWeekly(0, 5 * day) },
    });
    for (const name of ['a', 'b']) await responses(app.baseUrl, `e2e-s8-${name}`);
    await script({ 'e2e-s8-a': { limited: true } });
    const served = await Promise.all(
      Array.from({ length: 6 }, () => responses(app.baseUrl, 'e2e-s8')),
    );
    for (const keys of served) expect(keys.at(-1)).toBe('e2e-s8-b');
  },
);
