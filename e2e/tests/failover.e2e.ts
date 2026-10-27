import { createHash, randomUUID } from 'node:crypto';
import { expect, test } from 'e2e';
import { input, mockUrl, post, upstreamRequests } from '../lib/proxy.ts';

const hermetic = { platforms: ['hermetic'] };

// The auth index of a config Codex key (sdk/cliproxy/auth/types.go, stableAuthIndex/indexSeed).
const authIndex = (key: string) =>
  createHash('sha256').update(`codex-api-key:${mockUrl()}/v1+${key}`).digest('hex').slice(0, 16);

test('a usage-limited Codex credential fails over to the next one', hermetic, async ({ app }) => {
  const marker = randomUUID();
  const response = await post(app.baseUrl, '/v1/responses', {
    model: 'e2e-failover',
    input: input(marker),
  });
  expect(response.status).toBe(200);
  expect((await response.json()).status).toBe('completed');
  const [, servedBy] = /^\d{14}-([0-9a-f]{16})-/.exec(response.headers.get('x-cpa-trace-id')!)!;
  expect(servedBy).toBe(authIndex('failover-b'));
  const keys = (await upstreamRequests(marker)).map((request) => request.key);
  expect(keys).toEqual(['failover-a-limited', 'failover-b']);
});

test(
  'all Codex credentials cooled returns the local 429 model_cooldown',
  hermetic,
  async ({ app }) => {
    const first = await post(app.baseUrl, '/v1/responses', {
      model: 'e2e-cooled',
      input: input(randomUUID()),
    });
    expect(first.status).toBe(429);

    const marker = randomUUID();
    const response = await post(app.baseUrl, '/v1/responses', {
      model: 'e2e-cooled',
      input: input(marker),
    });
    expect(response.status).toBe(429);
    expect((await response.json()).error).toMatchObject({
      code: 'model_cooldown',
      model: 'e2e-cooled',
    });
    expect(await upstreamRequests(marker)).toEqual([]);
  },
);
