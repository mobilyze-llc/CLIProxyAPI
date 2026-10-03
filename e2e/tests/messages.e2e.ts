import { randomBytes, randomUUID } from 'node:crypto';
import { expect, test } from 'e2e';
import { clientKey, post, upstreamRequests } from '../lib/proxy.ts';
import { readSse } from '../lib/sse.ts';

test(
  'Claude Code shape: streaming Messages with a session id',
  { platforms: ['hermetic'] },
  async ({ app }) => {
    const session = randomUUID();
    const marker = randomUUID();
    // The four signals DetectClaudeCodeRequest requires to confirm a Claude Code client.
    const userId = {
      device_id: randomBytes(32).toString('hex'),
      account_uuid: '',
      session_id: session,
    };
    const response = await post(
      app.baseUrl,
      '/v1/messages?beta=true',
      {
        model: 'claude-sonnet-4-6',
        max_tokens: 64,
        stream: true,
        messages: [{ role: 'user', content: marker }],
        metadata: { user_id: JSON.stringify(userId) },
      },
      {
        'x-api-key': clientKey,
        'anthropic-version': '2023-06-01',
        'anthropic-beta': 'claude-code-20250219',
        'user-agent': 'claude-cli/2.1.280 (external, cli)',
        'x-app': 'cli',
        'X-Claude-Code-Session-Id': session,
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
    // claude-main cloaks every unconfirmed client, and cloaking installs a Claude Code system prompt.
    const [upstream] = await upstreamRequests(marker);
    expect(upstream.body.system).toBeUndefined();
  },
);
