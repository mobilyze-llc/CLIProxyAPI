// Starts the mock upstream, writes a proxy config whose API-key credentials point at it, and
// runs the candidate binary on $PORT. Tests read the mock URL from .e2e/mock.json.
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { startMock } from './mock-upstream.mjs';

const url = await startMock();
mkdirSync('.e2e', { recursive: true });
writeFileSync('.e2e/mock.json', JSON.stringify({ url }));

const dir = mkdtempSync(join(tmpdir(), 'cliproxy-e2e-'));
mkdirSync(join(dir, 'auths'));
const codex = (key, model, priority = 0) => ({
  'api-key': key,
  'base-url': `${url}/v1`,
  priority,
  models: [{ name: model }],
});
// YAML accepts JSON. The failover and cooldown tests own their models and credentials;
// failover-a has the higher priority, so the proxy tries it first.
const config = {
  host: '127.0.0.1',
  port: Number(process.env.PORT),
  'auth-dir': join(dir, 'auths'),
  'api-keys': ['e2e-client-key'],
  'codex-api-key': [
    codex('codex-main', 'gpt-5.6-sol'),
    codex('failover-a-limited', 'e2e-failover', 1),
    codex('failover-b', 'e2e-failover'),
    codex('cooled-a-limited', 'e2e-cooled'),
    codex('cooled-b-limited', 'e2e-cooled'),
  ],
  'claude-api-key': [
    { 'api-key': 'claude-main', 'base-url': url, models: [{ name: 'claude-sonnet-4-6' }] },
  ],
};
writeFileSync(join(dir, 'config.yaml'), JSON.stringify(config, null, 2));

const proxy = spawn('.bin/cli-proxy-api', ['-config', join(dir, 'config.yaml'), '-local-model'], {
  stdio: 'inherit',
});
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => proxy.kill(signal));
proxy.on('exit', (code) => process.exit(code ?? 1));
