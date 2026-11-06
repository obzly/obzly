// Live Tail's push transport: a streaming POST to the NDJSON tail endpoint via
// fetch, so it carries the normal Authorization header (EventSource can't set
// headers). If the "ready" frame doesn't arrive within readyTimeoutMs (e.g.
// behind a buffering proxy), the caller falls back to polling.
import { getApiBase } from '../../../constants';
import type { LogItem, QueryNode } from '../../../entity/query';
import { accessTokenHelper } from '../../../shared/api';

const READY_TIMEOUT_MS = 4000;

interface TailFrame {
  type: 'ready' | 'heartbeat' | 'log';
  log?: LogItem;
}

export interface PushLiveTailCallbacks {
  /** The stream is confirmed working (the "ready" frame arrived in time). */
  onReady: () => void;
  onLog: (log: LogItem) => void;
  /**
   * The push path failed (no "ready" in time, or the stream dropped); fall
   * back to polling. Never called after `stop()`.
   */
  onError: () => void;
}

export interface PushLiveTailHandle {
  stop: () => void;
}

export function startPushLiveTail(
  projectId: string,
  query: QueryNode | null,
  callbacks: PushLiveTailCallbacks,
): PushLiveTailHandle {
  const controller = new AbortController();
  let stopped = false;
  let sawReady = false;

  const readyTimer = setTimeout(() => {
    if (stopped || sawReady) return;
    // Set before abort() so the rejected fetch isn't reported as a second error.
    stopped = true;
    controller.abort();
    callbacks.onError();
  }, READY_TIMEOUT_MS);

  const stop = () => {
    stopped = true;
    clearTimeout(readyTimer);
    controller.abort();
  };

  (async () => {
    try {
      const response = await fetch(`${getApiBase()}/logs/tail/${projectId}`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: accessTokenHelper.getAccessToken() ?? '',
        },
        body: JSON.stringify({ query }),
        signal: controller.signal,
      });

      if (!response.ok || !response.body) {
        throw new Error(`live tail stream failed to open (${response.status})`);
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        let newlineIndex = buffer.indexOf('\n');
        while (newlineIndex >= 0) {
          const line = buffer.slice(0, newlineIndex);
          buffer = buffer.slice(newlineIndex + 1);
          newlineIndex = buffer.indexOf('\n');

          if (!line.trim()) continue;
          const frame = JSON.parse(line) as TailFrame;

          if (frame.type === 'ready') {
            sawReady = true;
            clearTimeout(readyTimer);
            callbacks.onReady();
          } else if (frame.type === 'log' && frame.log) {
            callbacks.onLog(frame.log);
          }
          // Heartbeats only keep the connection alive.
        }
      }

      // The server closed the stream; treat it as a failure so the caller falls back.
      if (!stopped) {
        callbacks.onError();
      }
    } catch {
      if (stopped) return; // our own abort, not a real error
      clearTimeout(readyTimer);
      callbacks.onError();
    }
  })();

  return { stop };
}
