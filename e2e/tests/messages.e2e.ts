import { randomUUID } from 'node:crypto';
import { expect, test } from 'e2e';
import { clientKey, post } from '../lib/proxy.ts';
import { readSse } from '../lib/sse.ts';

test(
  'Claude Code shape: streaming Messages with a session id',
  { platforms: ['hermetic'] },
  async ({ app }) => {
    const response = await post(
      app.baseUrl,
      '/v1/messages?beta=true',
      {
        model: 'claude-sonnet-4-6',
        max_tokens: 64,
        stream: true,
        messages: [{ role: 'user', content: 'hi' }],
      },
      {
        'x-api-key': clientKey,
        'anthropic-version': '2023-06-01',
        'X-Claude-Code-Session-Id': randomUUID(),
      },
    );
    expect(response.status).toBe(200);
    const events = await readSse(response, AbortSignal.timeout(10_000));
    expect(events.map((event) => event.event)).toEqual([
      'message_start',
      'content_block_start',
      'content_block_delta',
      'content_block_stop',
      'message_delta',
      'message_stop',
    ]);
  },
);
