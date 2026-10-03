import { AgentError, expect } from 'e2e';

// Tiny input at the lowest effort answers in seconds; OSWE-278's 429 takes about 10.5 s (one
// proxy retry). 60 s leaves headroom and stays under the runner's 120 s test timeout, so a
// stall surfaces as this request's error, not as TEST_TIMEOUT.
const BOUND_MS = 60_000;

type Request = { body?: unknown; headers?: Record<string, string>; keyless?: boolean };

/**
 * Sends one request to CLIPROXY_LIVE_URL and classifies failures that are not the product's:
 * missing inputs and a rejected key are configuration (exit 2); an unreachable proxy and a
 * provider 429 or 5xx are infrastructure (exit 3). `known` accepts a failure the test asserts
 * itself, matched on status and body.
 */
export async function live(
  path: string,
  request: Request = {},
  known?: (status: number, body: string) => boolean,
) {
  const base = process.env.CLIPROXY_LIVE_URL;
  if (!base) throw new AgentError('TEST_SETUP_FAILED', 'CLIPROXY_LIVE_URL is not set');
  const key = process.env.CLIPROXY_CLIENT_KEY;
  if (!request.keyless && !key) {
    throw new AgentError('AUTH_CREDENTIAL_UNAVAILABLE', 'CLIPROXY_CLIENT_KEY is not set');
  }
  const headers: Record<string, string> = { ...request.headers };
  if (!request.keyless)
    Object.assign(headers, { authorization: `Bearer ${key}`, 'x-api-key': key });
  if (request.body !== undefined) headers['content-type'] = 'application/json';
  const signal = AbortSignal.timeout(BOUND_MS);
  let response: Response;
  try {
    response = await fetch(new URL(path, base), {
      method: request.body === undefined ? 'GET' : 'POST',
      headers,
      body: request.body === undefined ? undefined : JSON.stringify(request.body),
      redirect: 'manual',
      signal,
    });
  } catch (cause) {
    throw new AgentError('ENVIRONMENT_UNAVAILABLE', `${new URL(base).origin} unreachable`, {
      cause,
    });
  }
  const status = response.status;
  if (!request.keyless && status === 401) {
    throw new AgentError('AUTH_CREDENTIAL_INVALID', `${path} rejected CLIPROXY_CLIENT_KEY (401)`);
  }
  if (status === 429 || status >= 500) {
    const text = await response.clone().text();
    if (!known?.(status, text)) {
      const excerpt = text.slice(0, 300);
      throw new AgentError('ENVIRONMENT_UNAVAILABLE', `${path} answered ${status}: ${excerpt}`);
    }
  }
  return { response, signal };
}

/** v7.3.17 and v8.0.13 set X-CPA-TRACE-ID once a credential is selected (cpa_trace.go). */
export function expectTrace(response: Response) {
  expect(response.headers.get('x-cpa-trace-id')).toMatch(/^\d{14}-\S+-\S+$/);
}
