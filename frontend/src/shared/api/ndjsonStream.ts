import { accessTokenHelper } from './accessTokenHelper';

// A stream that sends nothing (not even its 10 s heartbeat) this long is
// dead: the connection dropped somewhere without closing.
const STREAM_STALL_MS = 60_000;

/** The stream couldn't open (an older Site without the route, a 4xx, an expired token). */
export class StreamUnavailableError extends Error {
  constructor(readonly status: number) {
    super(`stream failed to open (${status})`);
  }
}

/** The stream went silent past STREAM_STALL_MS; cause is the abort it ended in. */
export class StreamStalledError extends Error {
  constructor(readonly cause: unknown) {
    super(
      'The query stopped responding (no data for a minute) - try again, or narrow the time range',
    );
  }
}

interface Frame {
  type: string;
  error?: string;
}

/**
 * Reads an NDJSON response; onFrame returns a value to finish the stream. The
 * stall timeout only arms after a heartbeat, since older Sites send none.
 */
export async function streamNdjson<F extends Frame, R>(
  url: string,
  body: unknown,
  signal: AbortSignal,
  onFrame: (frame: F) => R | undefined,
): Promise<R> {
  // Our own controller, so a stalled stream can be cut off too.
  const controller = new AbortController();
  const forwardAbort = () => controller.abort();
  signal.addEventListener('abort', forwardAbort);
  let lastData = Date.now();
  let heartbeats = false;
  let stalled = false;
  const watchdog = setInterval(() => {
    if (heartbeats && Date.now() - lastData > STREAM_STALL_MS) {
      stalled = true;
      controller.abort();
    }
  }, 5000);

  try {
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: accessTokenHelper.getAccessToken() ?? '',
      },
      body: JSON.stringify(body),
      signal: controller.signal,
    });
    if (!response.ok || !response.body) {
      throw new StreamUnavailableError(response.status);
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      lastData = Date.now();
      buffer += decoder.decode(value, { stream: true });
      let newlineIndex = buffer.indexOf('\n');
      while (newlineIndex >= 0) {
        const line = buffer.slice(0, newlineIndex);
        buffer = buffer.slice(newlineIndex + 1);
        newlineIndex = buffer.indexOf('\n');
        if (!line.trim()) continue;

        const frame = JSON.parse(line) as F;
        if (frame.type === 'heartbeat') {
          heartbeats = true;
          continue;
        }
        if (frame.type === 'error') {
          throw new Error(frame.error || 'Query execution failed');
        }
        const result = onFrame(frame);
        if (result !== undefined) return result;
      }
    }
    throw new Error('The stream ended before it finished - try again');
  } catch (error) {
    if (stalled) throw new StreamStalledError(error);
    throw error;
  } finally {
    clearInterval(watchdog);
    signal.removeEventListener('abort', forwardAbort);
  }
}
