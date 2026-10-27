export type SseEvent = { event: string | undefined; data: string };

/** Reads a server-sent event stream to its end, or until `signal` aborts. */
export async function readSse(response: Response, signal: AbortSignal): Promise<SseEvent[]> {
  const reader = response.body!.pipeThrough(new TextDecoderStream()).getReader();
  const cancel = () => void reader.cancel(signal.reason);
  signal.addEventListener('abort', cancel, { once: true });
  const events: SseEvent[] = [];
  let buffer = '';
  try {
    for (;;) {
      const { value, done } = await reader.read();
      signal.throwIfAborted();
      if (done) break;
      buffer += value.replaceAll('\r\n', '\n');
      let end;
      while ((end = buffer.indexOf('\n\n')) >= 0) {
        const lines = buffer.slice(0, end).split('\n');
        buffer = buffer.slice(end + 2);
        const field = (name: string) =>
          lines
            .filter((line) => line.startsWith(`${name}:`))
            .map((line) => line.slice(name.length + 1).trimStart());
        const data = field('data');
        if (data.length > 0) events.push({ event: field('event')[0], data: data.join('\n') });
      }
    }
  } finally {
    signal.removeEventListener('abort', cancel);
  }
  return events;
}
