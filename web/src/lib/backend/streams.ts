// SPDX-License-Identifier: AGPL-3.0-or-later
// Streaming transports for the embedded backend. bridgeFetch already streams
// response bodies chunk-by-chunk; these are THIN shims that present that stream
// through the browser's EventSource / WebSocket interfaces, so the application
// code (the agents streaming page) runs unchanged:
//
// - EventSource over any /v1 SSE endpoint: the shim fetches through the bridge
//   and parses the standard SSE frames the Go handler writes.
// - WebSocket over /v1/agents/{name}/run/ws only: a WebSocket upgrade needs a
//   real socket to hijack, which no worker port offers. The backend exposes the
//   SAME run as SSE (GET .../run/stream), so the shim drives that endpoint and
//   re-frames the events in the WS handler's message shape ({type, ...}) —
//   identical run, identical recording, different framing.
//
// Anything that is not an embedded-backend URL falls through to the real
// browser classes untouched.
import { bridgeFetch } from './bridge';

// parseSSE consumes a streamed text/event-stream body, invoking emit once per
// frame with the event name ('message' when unnamed) and the joined data lines.
async function parseSSE(res: Response, emit: (event: string, data: string) => void): Promise<void> {
  if (!res.ok) throw new Error(`stream failed: HTTP ${res.status}`);
  if (!res.body) throw new Error('stream failed: empty body');
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += value;
    let sep;
    while ((sep = buffer.indexOf('\n\n')) >= 0) {
      const frame = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      let event = 'message';
      const data: string[] = [];
      for (const line of frame.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim();
        else if (line.startsWith('data:')) data.push(line.slice(5).trim());
      }
      if (data.length > 0) emit(event, data.join('\n'));
    }
  }
}

function isEmbeddedPath(pathname: string): boolean {
  return pathname === '/v1' || pathname.startsWith('/v1/');
}

// BridgedEventSource presents a bridged SSE response through the EventSource
// interface. It does not auto-reconnect: re-opening the agent stream would
// start a second run, and the page closes the source on its terminal event.
class BridgedEventSource extends EventTarget {
  onerror: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  private closed = false;

  constructor(url: string | URL) {
    super();
    void this.pump(new URL(url, window.location.origin));
  }

  private async pump(url: URL): Promise<void> {
    try {
      const res = await bridgeFetch(url.pathname + url.search, {
        headers: { Accept: 'text/event-stream' }
      });
      if (this.closed) return;
      this.dispatch(new Event('open'), this.onopen);
      await parseSSE(res, (event, data) => {
        if (this.closed) return;
        const e = new MessageEvent(event, { data });
        // Route named frames to their property handlers like a real EventSource:
        // unnamed → onmessage, and a server `event: error` frame → onerror.
        this.dispatch(
          e,
          event === 'message' ? this.onmessage : event === 'error' ? this.onerror : null
        );
      });
    } catch {
      if (!this.closed) this.dispatch(new Event('error'), this.onerror);
    }
  }

  private dispatch(e: Event, handler: ((e: never) => void) | null): void {
    this.dispatchEvent(e);
    (handler as ((e: Event) => void) | null)?.(e);
  }

  close(): void {
    this.closed = true;
  }
}

// BridgedAgentSocket adapts the agents WS framing onto the backend's SSE run
// endpoint: send({prompt}) opens the stream; SSE `chunk`/`done`/`error` frames
// are re-emitted as the {type: ...} messages the WS handler would write.
class BridgedAgentSocket {
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  onclose: ((e: Event) => void) | null = null;
  private closed = false;

  constructor(private readonly agent: string) {
    // The page attaches onopen right after construction; open on the next tick.
    setTimeout(() => {
      if (!this.closed) this.onopen?.(new Event('open'));
    }, 0);
  }

  send(data: string): void {
    const prompt = String((JSON.parse(data) as { prompt?: unknown }).prompt ?? '');
    void this.pump(prompt);
  }

  private async pump(prompt: string): Promise<void> {
    try {
      const res = await bridgeFetch(
        `/v1/agents/${encodeURIComponent(this.agent)}/run/stream?prompt=${encodeURIComponent(prompt)}`,
        { headers: { Accept: 'text/event-stream' } }
      );
      await parseSSE(res, (event, data) => {
        if (this.closed) return;
        const payload = JSON.parse(data) as Record<string, unknown>;
        this.onmessage?.(
          new MessageEvent('message', { data: JSON.stringify({ type: event, ...payload }) })
        );
      });
      if (!this.closed) this.onclose?.(new Event('close'));
    } catch {
      if (!this.closed) this.onerror?.(new Event('error'));
    }
  }

  close(): void {
    this.closed = true;
  }
}

const AGENT_WS = /^\/v1\/agents\/([^/]+)\/run\/ws$/;

/**
 * Replaces window.EventSource / window.WebSocket with shims that route
 * embedded-backend URLs through the bridge and delegate everything else to
 * the real classes. Installed only in the embedded (demo) build.
 */
export function installEmbeddedStreams(): void {
  const RealES = window.EventSource;
  const RealWS = window.WebSocket;

  window.EventSource = function (url: string | URL, init?: EventSourceInit) {
    const u = new URL(url, window.location.origin);
    if (u.origin === window.location.origin && isEmbeddedPath(u.pathname)) {
      return new BridgedEventSource(u) as unknown as EventSource;
    }
    return new RealES(url, init);
  } as unknown as typeof EventSource;

  window.WebSocket = function (url: string | URL, protocols?: string | string[]) {
    const u = new URL(url, window.location.origin);
    const m = u.host === window.location.host ? AGENT_WS.exec(u.pathname) : null;
    if (m) {
      return new BridgedAgentSocket(decodeURIComponent(m[1])) as unknown as WebSocket;
    }
    return new RealWS(url, protocols);
  } as unknown as typeof WebSocket;
}
