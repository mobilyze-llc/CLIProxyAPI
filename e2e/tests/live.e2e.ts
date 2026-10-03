import { randomUUID } from 'node:crypto';
import { expect, test } from 'e2e';
import { expectTrace, live } from '../lib/live.ts';
import { input } from '../lib/proxy.ts';
import { readSse } from '../lib/sse.ts';

const onLive = { platforms: ['live'] };
const anthropic = { 'anthropic-version': '2023-06-01' };
const say = 'Reply with the word ok.';

test('live: GET /v1/models without a key returns 401', onLive, async () => {
  const { response } = await live('/v1/models', { keyless: true });
  expect(response.status).toBe(401);
});

test('live: GET /v1/models lists Codex and Claude models', onLive, async () => {
  const { response } = await live('/v1/models');
  expect(response.status).toBe(200);
  const ids: string[] = (await response.json()).data.map((model: { id: string }) => model.id);
  expect(ids.some((id) => id.startsWith('gpt-'))).toBe(true);
  expect(ids.some((id) => id.startsWith('claude-'))).toBe(true);
});

test('live: Open SWE shape, non-streaming Responses on gpt-5.6-sol', onLive, async () => {
  const { response } = await live('/v1/responses', {
    body: {
      model: 'gpt-5.6-sol',
      stream: false,
      store: false,
      include: ['reasoning.encrypted_content'],
      reasoning: { effort: 'low' }, // the lowest level the catalog lists for gpt-5.6-sol
      tools: [
        {
          type: 'function',
          name: 'read_file',
          description: 'Read a file',
          parameters: {
            type: 'object',
            properties: { path: { type: 'string' } },
            required: ['path'],
          },
        },
      ],
      input: input(say),
    },
  });
  expect(response.status).toBe(200);
  expectTrace(response);
  expect((await response.json()).status).toBe('completed');
});

test('live: streaming Responses on gpt-5.6-sol ends with response.completed', onLive, async () => {
  const { response, signal } = await live('/v1/responses', {
    body: {
      model: 'gpt-5.6-sol',
      stream: true,
      store: false,
      reasoning: { effort: 'low' },
      input: input(say),
    },
  });
  expect(response.status).toBe(200);
  expectTrace(response);
  const events = await readSse(response, signal);
  expect(JSON.parse(events.at(-1)!.data).type).toBe('response.completed');
});

test(
  'live: Claude Code shape, streaming Messages on Haiku ends with message_stop',
  onLive,
  async () => {
    const { response, signal } = await live('/v1/messages?beta=true', {
      body: {
        model: 'claude-haiku-4-5-20251001',
        max_tokens: 16,
        stream: true,
        messages: [{ role: 'user', content: say }],
      },
      headers: { ...anthropic, 'X-Claude-Code-Session-Id': randomUUID() },
    });
    expect(response.status).toBe(200);
    expectTrace(response);
    const events = await readSse(response, signal);
    expect(events.at(-1)?.event).toBe('message_stop');
  },
);

test('live: Responses on claude-sonnet-4-6 completes', onLive, async () => {
  const { response } = await live('/v1/responses', {
    body: { model: 'claude-sonnet-4-6', input: input(say) },
  });
  expect(response.status).toBe(200);
  expectTrace(response);
  expect((await response.json()).status).toBe('completed');
});

// OSWE-278's response as captured from studio2. A genuine rate limit carries a descriptive
// message, so it stays an infrastructure failure (exit 3).
const isOswe278 = (status: number, body: string) => {
  try {
    const { error } = JSON.parse(body);
    return status === 429 && error?.type === 'rate_limit_error' && error?.message === 'Error';
  } catch {
    return false;
  }
};

// Known failure: this asserts the bug. When OSWE-278 is fixed the status changes and the test
// fails visibly; then assert 200 here instead.
test(
  'live: OSWE-278 known failure, native Messages on claude-sonnet-4-6 returns 429',
  onLive,
  async () => {
    const { response } = await live(
      '/v1/messages',
      {
        body: {
          model: 'claude-sonnet-4-6',
          max_tokens: 16,
          messages: [{ role: 'user', content: say }],
        },
        headers: anthropic,
      },
      isOswe278,
    );
    expect(response.status).toBe(429);
    expect((await response.json()).error).toEqual({ type: 'rate_limit_error', message: 'Error' });
  },
);
