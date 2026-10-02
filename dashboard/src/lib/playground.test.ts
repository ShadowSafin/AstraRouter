import { describe, expect, it } from 'vitest';

import {
  DEFAULT_CONFIG,
  applySseEvent,
  buildChatBody,
  buildCurl,
  buildHeaders,
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
  splitList,
  type PlaygroundConfig,
  type PlaygroundMessage,
} from './playground';

function config(overrides: Partial<PlaygroundConfig> = {}): PlaygroundConfig {
  return { ...DEFAULT_CONFIG, model: 'gpt-4o-mini', streaming: false, debug: false, ...overrides };
}

function userMessage(content: string): PlaygroundMessage {
  return { id: 'm1', role: 'user', content };
}

describe('buildChatBody', () => {
  it('rejects a run with no model or no messages', () => {
    expect(buildChatBody(config({ model: '' }), [userMessage('hi')]).error).toBeTruthy();

    const noMessages = buildChatBody(config(), []);
    expect(noMessages.body).toBeNull();
    expect(noMessages.error).toContain('at least one message');
  });

  it('rejects an empty message rather than sending it upstream', () => {
    const result = buildChatBody(config(), [userMessage('   ')]);
    expect(result.body).toBeNull();
    expect(result.error).toContain('Message 1 is empty');
  });

  it('folds the system prompt in ahead of the conversation', () => {
    const { body } = buildChatBody(config({ system: 'be terse' }), [userMessage('hi')]);
    expect(body).toMatchObject({
      model: 'gpt-4o-mini',
      messages: [
        { role: 'system', content: 'be terse' },
        { role: 'user', content: 'hi' },
      ],
    });
  });

  it('omits sampling parameters that were not set', () => {
    const { body } = buildChatBody(config(), [userMessage('hi')]);
    expect(body).not.toHaveProperty('temperature');
    expect(body).not.toHaveProperty('top_p');
    expect(body).not.toHaveProperty('max_tokens');
    expect(body).not.toHaveProperty('stream');
    expect(body).not.toHaveProperty('stop');
  });

  it('treats an explicit temperature of 0 as a real value', () => {
    const { body } = buildChatBody(config({ temperature: 0 }), [userMessage('hi')]);
    expect(body?.temperature).toBe(0);
  });

  it('requests usage on a streamed debug run', () => {
    const { body } = buildChatBody(config({ streaming: true, debug: true }), [userMessage('hi')]);
    expect(body).toMatchObject({ stream: true, stream_options: { include_usage: true } });
  });

  it('does not request stream usage when debug is off', () => {
    const { body } = buildChatBody(config({ streaming: true, debug: false }), [userMessage('hi')]);
    expect(body?.stream).toBe(true);
    expect(body).not.toHaveProperty('stream_options');
  });

  it('splits stop sequences on commas and newlines', () => {
    const { body } = buildChatBody(config({ stop: 'END, STOP\n###' }), [userMessage('hi')]);
    expect(body?.stop).toEqual(['END', 'STOP', '###']);
  });

  it('accepts a JSON array of tools and rejects anything else', () => {
    const tools = JSON.stringify([{ type: 'function', function: { name: 'now' } }]);
    expect(buildChatBody(config({ tools }), [userMessage('hi')]).body?.tools).toHaveLength(1);

    const notArray = buildChatBody(config({ tools: '{"a":1}' }), [userMessage('hi')]);
    expect(notArray.body).toBeNull();
    expect(notArray.error).toContain('JSON array');

    const invalid = buildChatBody(config({ tools: '{oops' }), [userMessage('hi')]);
    expect(invalid.error).toContain('not valid JSON');
  });

  it('requires response_format to be a JSON object', () => {
    const good = buildChatBody(
      config({ responseFormat: '{"type":"json_object"}' }),
      [userMessage('hi')],
    );
    expect(good.body?.response_format).toEqual({ type: 'json_object' });

    const bad = buildChatBody(config({ responseFormat: '[1]' }), [userMessage('hi')]);
    expect(bad.error).toContain('JSON object');
  });

  it('sends system messages from the list rather than dropping them', () => {
    const messages: PlaygroundMessage[] = [
      { id: 's', role: 'system', content: 'from the list' },
      userMessage('hi'),
    ];
    const { body } = buildChatBody(config(), messages);
    expect(body?.messages).toEqual([
      { role: 'system', content: 'from the list' },
      { role: 'user', content: 'hi' },
    ]);
  });

  it('places the system prompt field ahead of any system message', () => {
    const messages: PlaygroundMessage[] = [
      { id: 's', role: 'system', content: 'from the list' },
      userMessage('hi'),
    ];
    const { body } = buildChatBody(config({ system: 'from the control' }), messages);
    expect(body?.messages).toEqual([
      { role: 'system', content: 'from the control' },
      { role: 'system', content: 'from the list' },
      { role: 'user', content: 'hi' },
    ]);
  });

  it('rejects an empty system message too', () => {
    const result = buildChatBody(config(), [{ id: 's', role: 'system', content: '  ' }, userMessage('hi')]);
    expect(result.body).toBeNull();
    expect(result.error).toContain('Message 1 is empty');
  });

  it('requires a non-system turn', () => {
    const result = buildChatBody(config(), [{ id: 's', role: 'system', content: 'only this' }]);
    expect(result.body).toBeNull();
    expect(result.error).toContain('at least one message');
  });

  it('merges extra body fields, including ones with no dedicated control', () => {
    const { body } = buildChatBody(
      config({ extraBody: '{"top_k":40,"seed":7}' }),
      [userMessage('hi')],
    );
    expect(body).toMatchObject({ top_k: 40, seed: 7 });
  });

  it('lets extra body override a dedicated control', () => {
    const { body } = buildChatBody(
      config({ temperature: 0.2, extraBody: '{"temperature":0.9}' }),
      [userMessage('hi')],
    );
    expect(body?.temperature).toBe(0.9);
  });

  it('refuses to let extra body redefine the run identity', () => {
    for (const key of ['model', 'messages', 'stream']) {
      const result = buildChatBody(
        config({ extraBody: `{"${key}":null}` }),
        [userMessage('hi')],
      );
      expect(result.body).toBeNull();
      expect(result.error).toContain(key);
    }
  });

  it('rejects malformed or non-object extra body', () => {
    expect(buildChatBody(config({ extraBody: '{oops' }), [userMessage('hi')]).error).toContain(
      'not valid JSON',
    );
    expect(buildChatBody(config({ extraBody: '[1]' }), [userMessage('hi')]).error).toContain(
      'JSON object',
    );
  });
});

describe('buildHeaders', () => {
  it('sends the transport headers and the bearer token', () => {
    const headers = buildHeaders(config({ debug: false }), 'ar_live_x');
    expect(headers['content-type']).toBe('application/json');
    expect(headers.authorization).toBe('Bearer ar_live_x');
    expect(headers).not.toHaveProperty('X-AstraRouter-Debug');
  });

  it('omits the authorization header when no key is present', () => {
    expect(buildHeaders(config(), '   ')).not.toHaveProperty('authorization');
  });

  it('asks for an event stream only when streaming', () => {
    expect(buildHeaders(config({ streaming: true }), '').accept).toBe('text/event-stream');
    expect(buildHeaders(config({ streaming: false }), '').accept).toBe('application/json');
  });

  it('never sends a provider or tenant header, which the gateway ignores', () => {
    const headers = buildHeaders(config({ debug: true, endpoint: 'prod', policy: 'fast' }), 'k');
    expect(headers).not.toHaveProperty('X-AstraRouter-Provider');
    expect(headers).not.toHaveProperty('X-AstraRouter-Tenant');
  });
});

describe('intentHeaders', () => {
  it('sends nothing but the transport when nothing is configured', () => {
    expect(intentHeaders({ ...DEFAULT_CONFIG, debug: false })).toEqual({});
  });

  it('expresses each routing control as the header the gateway reads', () => {
    const headers = intentHeaders(
      config({
        debug: true,
        endpoint: 'prod-chat',
        policy: 'policy_1',
        noFallback: true,
        noCache: true,
        region: 'eu',
        sensitivity: 'pii,public',
        maxCostUsd: 0.02,
        latencyTargetMs: 1500,
      }),
    );
    expect(headers).toEqual({
      'X-AstraRouter-Debug': 'true',
      'X-AstraRouter-Endpoint': 'prod-chat',
      'X-AstraRouter-Policy': 'policy_1',
      'X-AstraRouter-No-Fallback': 'true',
      'X-AstraRouter-No-Cache': 'true',
      'X-AstraRouter-Region': 'eu',
      'X-AstraRouter-Sensitivity': 'pii,public',
      'X-AstraRouter-Max-Cost-USD': '0.02',
      'X-AstraRouter-Latency-Target-Ms': '1500',
    });
  });

  it('treats a zero cost ceiling and a zero latency target as unset', () => {
    const headers = intentHeaders(config({ maxCostUsd: -1, latencyTargetMs: 0 }));
    expect(headers).not.toHaveProperty('X-AstraRouter-Max-Cost-USD');
    expect(headers).not.toHaveProperty('X-AstraRouter-Latency-Target-Ms');
  });
});

describe('buildCurl', () => {
  it('redacts the credential instead of pasting a real key', () => {
    const curl = buildCurl(config(), [userMessage('hello')], 'http://localhost:8080/');
    expect(curl).toContain('$ASTRAROUTER_API_KEY');
    expect(curl).toContain('http://localhost:8080/v1/chat/completions');
    expect(curl).not.toContain('ar_live_');
  });

  it('reproduces the routing intent as real headers', () => {
    const curl = buildCurl(
      config({ endpoint: 'prod-chat', noFallback: true }),
      [userMessage('hello')],
      'http://localhost:8080',
    );
    expect(curl).toContain('X-AstraRouter-Endpoint: prod-chat');
    expect(curl).toContain('X-AstraRouter-No-Fallback: true');
    expect(curl).not.toContain('X-AstraRouter-Provider');
  });
});

describe('response parsing', () => {
  const envelope = {
    choices: [{ message: { role: 'assistant', content: 'hello there' }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 },
    astrarouter: {
      request_id: 'req_1',
      trace_id: 'trace_1',
      fallback_used: true,
      cache_hit: false,
      latency_ms: 812,
      decision: { attempts: 2, chosen: { provider_name: 'openai', model: 'gpt-4o-mini' } },
    },
  };

  it('reads content, usage and route metadata', () => {
    expect(extractContent(envelope)).toBe('hello there');
    expect(extractUsage(envelope)).toEqual({
      prompt_tokens: 10,
      completion_tokens: 3,
      total_tokens: 13,
    });
    const route = extractRoute(envelope);
    expect(route).toMatchObject({
      request_id: 'req_1',
      fallback_used: true,
      latency_ms: 812,
      provider: 'openai',
      model: 'gpt-4o-mini',
      attempts: 2,
    });
  });

  it('returns an empty route when the gateway did not attach one', () => {
    expect(extractRoute({ choices: [] }).request_id).toBe('');
    expect(extractUsage({ choices: [] })).toBeNull();
    expect(extractContent({ choices: [] })).toBe('');
  });

  it('reads the flattened debug block the gateway actually sends', () => {
    const flattened = {
      astrarouter: {
        request_id: 'req_2',
        trace_id: 'trace_2',
        policy_name: 'default',
        provider: 'bynara1',
        requested_model: 'agnes-2.5-flash',
        routed_model: 'agnes-2.5-flash',
        strategy: 'priority',
        attempts: 1,
        fallback_used: false,
        cache_hit: false,
        latency_ms: 4832,
        route_reason: 'routed by registry defaults',
        task: 'chat',
        shaping: 'shaping: normalize,adapt',
      },
    };
    const route = extractRoute(flattened);
    expect(route).toMatchObject({
      request_id: 'req_2',
      trace_id: 'trace_2',
      provider: 'bynara1',
      model: 'agnes-2.5-flash',
      attempts: 1,
      latency_ms: 4832,
    });
    expect(route.decision).not.toBeNull();
  });

  it('reports no decision when debug was not requested', () => {
    // Without the debug header the block is correlation ids and counters only.
    const plain = {
      astrarouter: {
        request_id: 'req_3',
        fallback_used: false,
        cache_hit: false,
        latency_ms: 120,
      },
    };
    const route = extractRoute(plain);
    expect(route.decision).toBeNull();
    expect(route.provider).toBe('');
    expect(route.latency_ms).toBe(120);
  });

  it('reads tool calls and keeps their arguments unparsed', () => {
    const payload = {
      choices: [
        {
          message: {
            tool_calls: [{ function: { name: 'get_weather', arguments: '{"city":"Paris"' } }],
          },
        },
      ],
    };
    expect(extractToolCalls(payload)).toEqual([
      { name: 'get_weather', arguments: '{"city":"Paris"' },
    ]);
  });

  it('tolerates an object form of tool arguments and a missing list', () => {
    const payload = {
      choices: [{ message: { tool_calls: [{ function: { name: 'now', arguments: { tz: 'UTC' } } }] } }],
    };
    expect(extractToolCalls(payload)).toEqual([{ name: 'now', arguments: '{"tz":"UTC"}' }]);
    expect(extractToolCalls({ choices: [{ message: { content: 'hi' } }] })).toEqual([]);
  });
});

describe('extractFinishReason', () => {
  it('reads the finish reason so truncation is visible', () => {
    expect(extractFinishReason({ choices: [{ finish_reason: 'length', delta: {} }] })).toBe(
      'length',
    );
    expect(extractFinishReason({ choices: [{ finish_reason: 'stop' }] })).toBe('stop');
    expect(extractFinishReason({ choices: [] })).toBe('');
    expect(extractFinishReason(null)).toBe('');
  });
});

describe('parseSseChunk', () => {
  it('parses complete frames and keeps the remainder', () => {
    const { events, rest } = parseSseChunk('data: {"a":1}\n\ndata: {"b":2}\n\ndata: {"c"');
    expect(events.map((event) => event.data)).toEqual(['{"a":1}', '{"b":2}']);
    expect(rest).toBe('data: {"c"');
  });

  it('resumes a frame that was split across two network reads', () => {
    const first = parseSseChunk('data: {"cho');
    expect(first.events).toHaveLength(0);
    const second = parseSseChunk(`${first.rest}ices":[]}\n\n`);
    expect(second.events[0]?.data).toBe('{"choices":[]}');
  });

  it('recognizes the end-of-stream sentinel', () => {
    const { events } = parseSseChunk('data: [DONE]\n\n');
    expect(events[0]?.data).toBe('[DONE]');
  });
});

describe('applySseEvent', () => {
  it('accumulates deltas, usage and finish reason', () => {
    let state = emptyStreamState();
    state = applySseEvent(state, {
      data: JSON.stringify({ choices: [{ delta: { content: 'Hel' } }] }),
    });
    state = applySseEvent(state, {
      data: JSON.stringify({ choices: [{ delta: { content: 'lo' }, finish_reason: 'stop' }] }),
    });
    state = applySseEvent(state, {
      data: JSON.stringify({ usage: { prompt_tokens: 1, completion_tokens: 2, total_tokens: 3 } }),
    });

    expect(state.content).toBe('Hello');
    expect(state.finishReason).toBe('stop');
    expect(state.usage?.total_tokens).toBe(3);
    expect(state.done).toBe(false);

    state = applySseEvent(state, { data: '[DONE]' });
    expect(state.done).toBe(true);
  });

  it('skips a malformed frame without losing the answer so far', () => {
    let state = applySseEvent(emptyStreamState(), {
      data: JSON.stringify({ choices: [{ delta: { content: 'kept' } }] }),
    });
    state = applySseEvent(state, { data: '{not json' });
    expect(state.content).toBe('kept');
  });

  it('merges fragmented tool calls arriving under delta.tool_calls', () => {
    let state = emptyStreamState();
    state = applySseEvent(state, {
      data: JSON.stringify({
        choices: [
          { delta: { tool_calls: [{ index: 0, type: 'function', function: { name: 'get_weather', arguments: '{"ci' } }] } },
        ],
      }),
    });
    state = applySseEvent(state, {
      data: JSON.stringify({
        choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: 'ty":"Paris"}' } }] } }],
      }),
    });
    expect(state.toolCalls).toEqual([
      { name: 'get_weather', arguments: '{"city":"Paris"}' },
    ]);
  });

  it('takes a complete message.tool_calls list as authoritative', () => {
    let state = emptyStreamState();
    state = applySseEvent(state, {
      data: JSON.stringify({
        choices: [{ delta: { tool_calls: [{ index: 0, function: { name: 'now', arguments: '{}' } }] } }],
      }),
    });
    state = applySseEvent(state, {
      data: JSON.stringify({
        choices: [
          {
            message: {
              tool_calls: [{ function: { name: 'get_time', arguments: '{"tz":"UTC"}' } }],
            },
          },
        ],
      }),
    });
    expect(state.toolCalls).toEqual([{ name: 'get_time', arguments: '{"tz":"UTC"}' }]);
  });

  it('does not mutate the previous state', () => {
    const before = emptyStreamState();
    const after = applySseEvent(before, {
      data: JSON.stringify({ choices: [{ delta: { content: 'x' } }] }),
    });
    expect(before.content).toBe('');
    expect(after.content).toBe('x');
  });
});

describe('extractError', () => {
  it('unwraps the gateway error envelope', () => {
    const error = extractError(
      429,
      JSON.stringify({ error: { message: 'rate limited', code: 'rate_limit_error', type: 'rate_limit_error' } }),
    );
    expect(error).toMatchObject({ status: 429, message: 'rate limited', code: 'rate_limit_error' });
  });

  it('still produces a message for a non-JSON body', () => {
    expect(extractError(502, '<html>bad gateway</html>').message).toContain('bad gateway');
    expect(extractError(500, '').message).toBe('the gateway returned HTTP 500');
  });
});

describe('splitList and runLabel', () => {
  it('splits and trims, dropping empties', () => {
    expect(splitList('a, ,b\nc')).toEqual(['a', 'b', 'c']);
  });

  it('labels a run by model and prompt preview', () => {
    expect(runLabel(config(), [userMessage('summarise this')])).toBe(
      'gpt-4o-mini · summarise this',
    );
  });
});
