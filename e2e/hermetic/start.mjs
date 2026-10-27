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
const models = (names) => [names].flat().map((name) => ({ name }));
const codex = (key, names, priority = 0) => ({
  'api-key': key,
  'base-url': `${url}/v1`,
  priority,
  models: models(names),
});
const claude = (key, names) => ({ 'api-key': key, 'base-url': url, models: models(names) });
// A selector scenario's credentials share its model; `<model>-<credential>` reaches one of them.
const scenario = (make, model, credentials) =>
  credentials.map((name) => make(`${model}-${name}`, [model, `${model}-${name}`]));
// YAML accepts JSON. The failover, cooldown and selector tests own their models and
// credentials; failover-a has the higher priority, so the proxy tries it first.
const config = {
  host: '127.0.0.1',
  port: Number(process.env.PORT),
  'auth-dir': join(dir, 'auths'),
  'api-keys': ['e2e-client-key'],
  routing: { strategy: 'soonest-reset', 'session-affinity': true },
  'save-cooldown-status': true,
  'codex-api-key': [
    codex('codex-main', 'gpt-5.6-sol'),
    codex('failover-a-limited', 'e2e-failover', 1),
    codex('failover-b', 'e2e-failover'),
    codex('cooled-a-limited', 'e2e-cooled'),
    codex('cooled-b-limited', 'e2e-cooled'),
    ...scenario(codex, 'e2e-s1', ['a', 'b', 'c']),
    ...scenario(codex, 'e2e-s2', ['a', 'b']),
    ...scenario(codex, 'e2e-s5', ['known', 'unknown']),
    ...scenario(codex, 'e2e-s7', ['a', 'b']),
    ...scenario(codex, 'e2e-s8', ['a', 'b']),
  ],
  'claude-api-key': [
    // Cloaks every client except confirmed Claude Code, so messages.e2e.ts can tell them apart.
    { ...claude('claude-main', 'claude-sonnet-4-6'), cloak: { mode: 'always' } },
    ...scenario(claude, 'e2e-s3', ['a', 'b']),
    ...scenario(claude, 'e2e-s5c', ['x', 'y', 'z']),
  ],
};
writeFileSync(join(dir, 'config.yaml'), JSON.stringify(config, null, 2));

const proxy = spawn('.bin/cli-proxy-api', ['-config', join(dir, 'config.yaml'), '-local-model'], {
  stdio: 'inherit',
});
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => proxy.kill(signal));
proxy.on('exit', (code) => process.exit(code ?? 1));
