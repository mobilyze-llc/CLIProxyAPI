import { expect, test } from 'e2e';
import { clientKey } from '../lib/proxy.ts';

const hermetic = { platforms: ['hermetic'] };

test('GET /v1/models without a key returns 401', hermetic, async ({ app }) => {
  const response = await fetch(new URL('/v1/models', app.baseUrl));
  expect(response.status).toBe(401);
});

test(
  'GET /v1/models with the client key lists the configured models',
  hermetic,
  async ({ app }) => {
    const response = await fetch(new URL('/v1/models', app.baseUrl), {
      headers: { authorization: `Bearer ${clientKey}` },
    });
    expect(response.status).toBe(200);
    const { data } = await response.json();
    const ids: string[] = data.map((model: { id: string }) => model.id);
    // Selector scenarios (tests/selector.e2e.ts) add their own e2e-s* models.
    expect(ids.filter((id) => !id.startsWith('e2e-s')).sort()).toEqual([
      'claude-sonnet-4-6',
      'e2e-cooled',
      'e2e-failover',
      'gpt-5.6-sol',
    ]);
  },
);
