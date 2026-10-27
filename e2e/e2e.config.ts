import type { E2EConfig } from 'e2e';
import { defineEngine } from 'e2e/engine';

// No UI to drive: the runner starts app.command, probes readyUrl and exposes app.baseUrl.
const http = (platform: string) =>
  defineEngine({ name: 'http', version: '1', spiVersion: 1, platform });

export default {
  tests: 'tests/**/*.e2e.ts',
  retries: 0,
  trace: 'off',
  targets: [
    {
      engine: http('hermetic'),
      app: {
        url: 'http://127.0.0.1:0',
        readyUrl: 'http://127.0.0.1:{port}/healthz',
        command: {
          executable: 'node',
          args: ['hermetic/start.mjs'],
          // Loopback is never proxied, so this blocks only the binary's own internet fetches.
          env: {
            PORT: '{port}',
            HTTPS_PROXY: 'http://127.0.0.1:9',
            MASTRA_TELEMETRY_DISABLED: '1',
          },
          log: '.e2e/logs/hermetic.log',
        },
      },
    },
    // Live tests read CLIPROXY_LIVE_URL themselves: the runner refuses plain HTTP off loopback.
    { engine: http('live') },
  ],
} satisfies E2EConfig;
