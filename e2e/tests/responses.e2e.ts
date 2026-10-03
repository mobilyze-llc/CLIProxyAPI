import { randomUUID } from 'node:crypto';
import { expect, test } from 'e2e';
import { z } from 'zod';
import { input, post, upstreamRequests } from '../lib/proxy.ts';
import { readSse } from '../lib/sse.ts';

const hermetic = { platforms: ['hermetic'] };

const CompletedResponse = z.object({
  status: z.literal('completed'),
  output: z.array(
    z.object({ type: z.literal('message'), content: z.array(z.object({ text: z.string() })) }),
  ),
});

test(
  'Open SWE shape: non-streaming Responses keeps tools, store and include',
  hermetic,
  async ({ app }) => {
    const marker = randomUUID();
    const tool = {
      type: 'function',
      name: 'read_file',
      description: 'Read a file',
      parameters: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
      strict: false,
    };
    const response = await post(app.baseUrl, '/v1/responses', {
      model: 'gpt-5.6-sol',
      stream: false,
      store: false,
      include: ['reasoning.encrypted_content'],
      reasoning: { effort: 'xhigh', summary: 'auto' },
      tools: [tool],
      input: input(marker),
    });
    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toContain('application/json');
    const body = expect(await response.json()).toMatchSchema(CompletedResponse);
    expect(body.output[0].content[0].text).toBe('{"ok":true}');

    const [upstream] = await upstreamRequests(marker);
    expect(upstream.body).toMatchObject({
      store: false,
      include: ['reasoning.encrypted_content'],
      tools: expect.arrayContaining([tool]),
    });
  },
);

test('streaming Responses relays the upstream events in order', hermetic, async ({ app }) => {
  const response = await post(app.baseUrl, '/v1/responses', {
    model: 'gpt-5.6-sol',
    stream: true,
    input: input('hi'),
  });
  expect(response.status).toBe(200);
  expect(response.headers.get('content-type')).toContain('text/event-stream');
  const events = await readSse(response, AbortSignal.timeout(10_000));
  expect(events.map((event) => JSON.parse(event.data).type)).toEqual([
    'response.created',
    'response.output_item.added',
    'response.output_text.delta',
    'response.output_item.done',
    'response.completed',
  ]);
});

test('Responses with a Claude model returns a complete response', hermetic, async ({ app }) => {
  const response = await post(app.baseUrl, '/v1/responses', {
    model: 'claude-sonnet-4-6',
    input: input('hi'),
  });
  expect(response.status).toBe(200);
  const body = expect(await response.json()).toMatchSchema(CompletedResponse);
  expect(body.output[0].content[0].text).toBe('ok');
});
