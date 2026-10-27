import { readFileSync } from 'node:fs';

export const clientKey = 'e2e-client-key';

/** POSTs JSON to the proxy with the client key. */
export const post = (baseUrl: string | undefined, path: string, body: unknown, headers = {}) =>
  fetch(new URL(path, baseUrl), {
    method: 'POST',
    headers: {
      authorization: `Bearer ${clientKey}`,
      'content-type': 'application/json',
      ...headers,
    },
    body: JSON.stringify(body),
  });

/** A Responses `input` holding one user message. */
export const input = (text: string) => [
  { type: 'message', role: 'user', content: [{ type: 'input_text', text }] },
];

/** The mock upstream's URL, written by hermetic/start.mjs. */
export const mockUrl = (): string =>
  JSON.parse(readFileSync(new URL('../.e2e/mock.json', import.meta.url), 'utf8')).url;

export type UpstreamRequest = { path: string; body: Record<string, unknown>; key: string };

/** Requests the mock received whose body contains `marker`, oldest first. */
export async function upstreamRequests(marker: string): Promise<UpstreamRequest[]> {
  const all: UpstreamRequest[] = await (await fetch(`${mockUrl()}/_mock/requests`)).json();
  return all.filter((request) => JSON.stringify(request.body).includes(marker));
}

export type Script = { headers?: Record<string, string>; limited?: boolean };

/** Sets what the mock answers for each credential key (hermetic/mock-upstream.mjs). */
export async function script(scripts: Record<string, Script>): Promise<void> {
  const response = await fetch(`${mockUrl()}/_mock/script`, {
    method: 'POST',
    body: JSON.stringify(scripts),
  });
  if (!response.ok) throw new Error(`mock script failed: ${response.status}`);
}
