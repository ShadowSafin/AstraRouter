'use client';

/**
 * The Playground's execution hook.
 *
 * One place owns the run lifecycle — build, send, stream, cancel, record — so the
 * single-run console and compare mode behave identically and neither re-implements
 * streaming. Runs are kept in memory only: a test prompt can contain production
 * data, and a dashboard should not quietly persist that to disk.
 */
import * as React from 'react';

import {
  applySseEvent,
  buildChatBody,
  emptyStreamState,
  extractContent,
  extractError,
  extractFinishReason,
  extractRoute,
  extractToolCalls,
  extractUsage,
  intentHeaders,
  parseSseChunk,
  runLabel,
  type PlaygroundConfig,
  type PlaygroundError,
  type PlaygroundMessage,
  type PlaygroundRun,
  type PlaygroundToolCall,
  type StreamState,
} from '@/lib/playground';

export type PlaygroundLane = 'primary' | 'compare';

/** Same-origin proxy that fronts the public inference API. */
const RUN_ENDPOINT = '/api/playground/v1/chat/completions';

interface RunArgs {
  lane: PlaygroundLane;
  config: PlaygroundConfig;
  messages: PlaygroundMessage[];
  apiKey: string;
}

function emptyLanes<T>(value: T): Record<PlaygroundLane, T> {
  return { primary: value, compare: value };
}

function newId(): string {
  return `run_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`;
}

/** Keep the stored stream transcript bounded, in characters. */
const TRANSCRIPT_LIMIT = 120_000;

export function usePlayground() {
  const [running, setRunning] = React.useState<Record<PlaygroundLane, boolean>>(
    emptyLanes(false),
  );
  const [partial, setPartial] = React.useState<Record<PlaygroundLane, StreamState>>(() =>
    emptyLanes(emptyStreamState()),
  );
  const [runs, setRuns] = React.useState<PlaygroundRun[]>([]);
  const controllers = React.useRef<Partial<Record<PlaygroundLane, AbortController>>>({});

  const cancel = React.useCallback((lane: PlaygroundLane) => {
    controllers.current[lane]?.abort();
    controllers.current[lane] = undefined;
  }, []);

  const clearHistory = React.useCallback(() => setRuns([]), []);

  const run = React.useCallback(async ({ lane, config, messages, apiKey }: RunArgs) => {
    const { body, error } = buildChatBody(config, messages);
    const startedAt = Date.now();

    const record = (fields: {
      content: string;
      raw: unknown;
      usage: PlaygroundRun['usage'];
      route: PlaygroundRun['route'];
      failure: PlaygroundError | null;
      firstTokenMs?: number;
      toolCalls?: PlaygroundToolCall[];
      finishReason?: string;
    }): PlaygroundRun => {
      const entry: PlaygroundRun = {
        id: newId(),
        config,
        messages,
        startedAt,
        durationMs: Date.now() - startedAt,
        firstTokenMs: fields.firstTokenMs ?? 0,
        content: fields.content,
        raw: fields.raw,
        usage: fields.usage,
        route: fields.route,
        toolCalls: fields.toolCalls ?? [],
        finishReason: fields.finishReason ?? '',
        error: fields.failure,
        lane,
        label: runLabel(config, messages),
      };
      setRuns((current) => [entry, ...current].slice(0, 40));
      return entry;
    };

    if (!body) {
      return record({
        content: '',
        raw: null,
        usage: null,
        route: emptyStreamState().route,
        failure: {
          message: error ?? 'the request could not be built',
          status: 0,
          code: 'playground_invalid_request',
          type: 'invalid_request_error',
        },
      });
    }

    const controller = new AbortController();
    controllers.current[lane] = controller;
    setRunning((current) => ({ ...current, [lane]: true }));
    setPartial((current) => ({ ...current, [lane]: emptyStreamState() }));

    try {
      const response = await fetch(RUN_ENDPOINT, {
        method: 'POST',
        headers: {
          'content-type': 'application/json',
          accept: config.streaming ? 'text/event-stream' : 'application/json',
          'x-astrarouter-key': apiKey,
          // The routing intent travels as headers, exactly as a real client
          // would send it, so the console cannot express an intent the
          // inference API does not accept.
          ...intentHeaders(config),
        },
        body: JSON.stringify(body),
        credentials: 'same-origin',
        signal: controller.signal,
      });

      if (!response.ok) {
        const text = await response.text().catch(() => '');
        const failure = extractError(response.status, text);
        return record({
          content: '',
          raw: null,
          usage: null,
          route: emptyStreamState().route,
          failure,
        });
      }

      if (config.streaming && response.body) {
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        let state = emptyStreamState();
        // Time to the first token is recorded once, when content first appears.
        let firstTokenMs = 0;
        // The raw payloads, so "raw response" is the wire transcript and not a
        // reconstruction of it. Capped: a long generation is thousands of
        // frames, and the console is not a log store.
        const transcript: string[] = [];
        let transcriptBytes = 0;

        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const { events, rest } = parseSseChunk(buffer);
          buffer = rest;
          for (const event of events) {
            state = applySseEvent(state, event);
            if (transcriptBytes < TRANSCRIPT_LIMIT) {
              transcript.push(event.data);
              transcriptBytes += event.data.length;
            }
          }
          if (firstTokenMs === 0 && state.content.length > 0) {
            firstTokenMs = Date.now() - startedAt;
          }
          // One state update per network read rather than per frame: a fast
          // model can emit dozens of frames per read and re-rendering each one
          // individually makes the stream look slower than it is.
          setPartial((current) => ({ ...current, [lane]: state }));
        }

        return record({
          content: state.content,
          raw: {
            streamed: true,
            finish_reason: state.finishReason,
            usage: state.usage,
            route: state.route,
            tool_calls: state.toolCalls,
            content: state.content,
            frames: transcript.join('\n'),
          },
          usage: state.usage,
          route: state.route,
          failure: null,
          firstTokenMs,
          toolCalls: state.toolCalls,
          finishReason: state.finishReason,
        });
      }

      const payload: unknown = await response.json();
      return record({
        content: extractContent(payload),
        raw: payload,
        usage: extractUsage(payload),
        route: extractRoute(payload),
        failure: null,
        toolCalls: extractToolCalls(payload),
        finishReason: extractFinishReason(payload),
      });
    } catch (cause) {
      // An aborted run is not a failure of the gateway, and saying so would send
      // the operator looking for a bug that is not there.
      const aborted = cause instanceof DOMException && cause.name === 'AbortError';
      const failure: PlaygroundError = aborted
        ? {
            message: 'the run was cancelled',
            status: 0,
            code: 'playground_cancelled',
            type: 'cancelled',
          }
        : {
            message:
              cause instanceof Error
                ? cause.message
                : 'the dashboard could not complete the run',
            status: 0,
            code: 'playground_transport_error',
            type: 'upstream_error',
          };
      return record({
        content: '',
        raw: null,
        usage: null,
        route: emptyStreamState().route,
        failure,
      });
    } finally {
      controllers.current[lane] = undefined;
      setRunning((current) => ({ ...current, [lane]: false }));
    }
  }, []);

  return { running, partial, runs, run, cancel, clearHistory };
}
