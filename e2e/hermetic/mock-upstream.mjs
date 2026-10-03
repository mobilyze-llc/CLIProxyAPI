// Mock Codex and Claude upstream. A credential whose key ends in `-limited` answers Codex
// 429 usage_limit_reached; every other request gets a complete SSE stream (or a Claude JSON
// message when the request does not stream). GET /_mock/requests returns what it received.
import { createServer } from 'node:http';

const requests = [];
const resetAt = () => Math.floor(Date.now() / 1000) + 3600;

const sse = (res, headers, events) => {
  res.writeHead(200, { 'content-type': 'text/event-stream', ...headers });
  for (const data of events) res.write(`event: ${data.type}\ndata: ${JSON.stringify(data)}\n\n`);
  res.end();
};

const codex = (res, key, body) => {
  if (key.endsWith('-limited')) {
    res.writeHead(429, { 'content-type': 'application/json' });
    const error = { type: 'usage_limit_reached', message: 'You have hit your usage limit.' };
    res.end(JSON.stringify({ error: { ...error, resets_at: resetAt(), resets_in_seconds: 3600 } }));
    return;
  }
  const item = {
    type: 'message',
    id: 'msg_mock',
    role: 'assistant',
    status: 'completed',
    content: [{ type: 'output_text', text: '{"ok":true}', annotations: [] }],
  };
  const response = { id: 'resp_mock', object: 'response', model: body.model, output: [] };
  const usage = { input_tokens: 5, output_tokens: 3, total_tokens: 8 };
  sse(
    res,
    {
      'x-codex-primary-used-percent': '10',
      'x-codex-primary-window-minutes': '300',
      'x-codex-primary-reset-at': String(resetAt()),
      'x-codex-secondary-used-percent': '20',
      'x-codex-secondary-window-minutes': '10080',
      'x-codex-secondary-reset-at': String(resetAt()),
    },
    [
      { type: 'response.created', response: { ...response, status: 'in_progress' } },
      { type: 'response.output_item.added', output_index: 0, item: { ...item, content: [] } },
      {
        type: 'response.output_text.delta',
        output_index: 0,
        content_index: 0,
        delta: item.content[0].text,
      },
      { type: 'response.output_item.done', output_index: 0, item },
      { type: 'response.completed', response: { ...response, status: 'completed', usage } },
    ],
  );
};

const claude = (res, body) => {
  const headers = {
    'anthropic-ratelimit-unified-status': 'allowed',
    'anthropic-ratelimit-unified-5h-status': 'allowed',
    'anthropic-ratelimit-unified-5h-utilization': '0.1',
    'anthropic-ratelimit-unified-5h-reset': String(resetAt()),
    'anthropic-ratelimit-unified-7d-status': 'allowed',
    'anthropic-ratelimit-unified-7d-utilization': '0.2',
    'anthropic-ratelimit-unified-7d-reset': String(resetAt()),
  };
  const message = {
    id: 'msg_mock',
    type: 'message',
    role: 'assistant',
    model: body.model,
    content: [],
    stop_reason: null,
    stop_sequence: null,
    usage: { input_tokens: 5, output_tokens: 0 },
  };
  if (!body.stream) {
    res.writeHead(200, { 'content-type': 'application/json', ...headers });
    const content = [{ type: 'text', text: 'ok' }];
    res.end(JSON.stringify({ ...message, content, stop_reason: 'end_turn' }));
    return;
  }
  sse(res, headers, [
    { type: 'message_start', message },
    { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
    { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'ok' } },
    { type: 'content_block_stop', index: 0 },
    { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 1 } },
    { type: 'message_stop' },
  ]);
};

export const startMock = () =>
  new Promise((resolve) => {
    const server = createServer(async (req, res) => {
      if (req.method === 'GET' && req.url === '/_mock/requests') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify(requests));
        return;
      }
      let raw = '';
      for await (const chunk of req) raw += chunk;
      const body = JSON.parse(raw);
      const key = (req.headers['x-api-key'] ?? req.headers.authorization ?? '').replace(
        'Bearer ',
        '',
      );
      requests.push({ path: req.url, headers: req.headers, body, key });
      if (req.url === '/v1/responses') codex(res, key, body);
      else claude(res, body);
    });
    server.listen(0, '127.0.0.1', () => resolve(`http://127.0.0.1:${server.address().port}`));
  });
