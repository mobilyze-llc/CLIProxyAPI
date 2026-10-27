import { expect, test } from 'e2e';
import { post } from '../lib/proxy.ts';
import { readSse } from '../lib/sse.ts';

test(
  'streaming Chat Completions with json_schema ends with a usage chunk',
  { platforms: ['hermetic'] },
  async ({ app }) => {
    const schema = {
      type: 'object',
      properties: { ok: { type: 'boolean' } },
      required: ['ok'],
      additionalProperties: false,
    };
    const response = await post(app.baseUrl, '/v1/chat/completions', {
      model: 'gpt-5.6-sol',
      stream: true,
      stream_options: { include_usage: true },
      response_format: { type: 'json_schema', json_schema: { name: 'ok', schema, strict: true } },
      messages: [{ role: 'user', content: 'hi' }],
    });
    expect(response.status).toBe(200);
    const events = await readSse(response, AbortSignal.timeout(10_000));
    expect(events.at(-1)?.data).toBe('[DONE]');
    const chunks = events.slice(0, -1).map((event) => JSON.parse(event.data));
    const text = chunks.map((chunk) => chunk.choices[0]?.delta.content ?? '').join('');
    expect(JSON.parse(text)).toEqual({ ok: true });
    expect(chunks.at(-1).usage).toEqual({
      prompt_tokens: 5,
      completion_tokens: 3,
      total_tokens: 8,
    });
  },
);
