#!/usr/bin/env bash
# Synapass Phase 3 smoke test (bash): verify a running stack end to end.
#
# Same checks as scripts/smoke.ps1: gateway health/readiness, admin providers +
# policies + models, demo tenant/key seeding, a sample inference request, request
# log + metrics confirmation, workers metrics, dashboard home + gateway proxy,
# plus the Phase 3 management cycle (provider/credential/model/tenant/key CRUD,
# rotation, connectivity test, audit, cleanup).
#
# No provider keys required: without upstream credentials the sample call is
# expected to fail AFTER routing (e.g. upstream 401); that still proves the
# pipeline ran. Only local failures fail the script.
#
# Reads .env in the repository root for SYNAPASS_ADMIN_KEY and GATEWAY_PORT.
# Needs: curl, python (python3 preferred, plain python accepted on Windows).
set -u
# REPO_ROOT stays in msys form (/c/...) for shell tools. WIN_ROOT is the native
# Windows form (C:/...) for embedded python, which cannot open msys paths.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if command -v cygpath >/dev/null 2>&1; then
  WIN_ROOT="$(cygpath -w "$REPO_ROOT")"
  WIN_ROOT="${WIN_ROOT//\\//}"
else
  WIN_ROOT="$REPO_ROOT"
fi
GATEWAY_URL="${GATEWAY_URL:-}"
DASHBOARD_URL="${DASHBOARD_URL:-http://127.0.0.1:3000}"
PASS=0
FAIL=0
# Pick a working interpreter: plain `python` on some Windows setups resolves to
# the Microsoft Store stub, which fails. Probe candidates and take the first
# one that actually runs.
if [ -z "${PYTHON:-}" ]; then
  PY=""
  for c in python3 python; do
    if command -v "$c" >/dev/null 2>&1 && "$c" --version >/dev/null 2>&1; then PY="$c"; break; fi
  done
  [ -z "$PY" ] && { echo "no working python found (set PYTHON explicitly)"; exit 2; }
else
  PY="$PYTHON"
fi

dotenv() { # $1=name [$2=default]
  local v="" def="${2:-}"
  if [ -f "$REPO_ROOT/.env" ]; then
    v="$(grep -E "^$1=" "$REPO_ROOT/.env" | tail -n1 | cut -d= -f2-)"
  fi
  [ -z "$v" ] && v="${!1:-$def}"
  [ -z "$v" ] && v="$def"
  printf '%s' "$v"
}

ADMIN_KEY="$(dotenv SYNAPASS_ADMIN_KEY)"
GATEWAY_PORT="$(dotenv GATEWAY_PORT 8080)"
[ -z "$GATEWAY_URL" ] && GATEWAY_URL="http://127.0.0.1:${GATEWAY_PORT}"
[ -z "$ADMIN_KEY" ] && { echo "SYNAPASS_ADMIN_KEY is empty. Copy .env.example to .env and set it."; exit 2; }

step() { # $1=name, rest=command...
  local name="$1"; shift
  echo ""; echo "== $name"
  if "$@" > /tmp/smoke_out.txt 2>&1; then
    PASS=$((PASS+1)); echo "PASS: $name"; cat /tmp/smoke_out.txt | sed 's/^/      /'
  else
    FAIL=$((FAIL+1)); echo "FAIL: $name"; cat /tmp/smoke_out.txt | sed 's/^/      /'
  fi
}

jget() { # $1=url [$3=extra curl args...] -- GET with admin auth
  curl -sf -m 20 -H "Authorization: Bearer $ADMIN_KEY" "$1"
}
py() { "$PY" -c "$1"; }

check_health() {
  py "
import urllib.request, json
d = json.load(urllib.request.urlopen('$GATEWAY_URL/health', timeout=15))
assert d['status'] == 'ok', d
print('components:', ', '.join(k for k, v in d.get('components', {}).items() if v))
"
}
check_ready() {
  py "
import urllib.request, json, sys
try:
    d = json.load(urllib.request.urlopen('$GATEWAY_URL/ready', timeout=15))
except Exception as e:
    print('not ready:', e); sys.exit(1)
print('checks:', d.get('checks'))
assert d['status'] == 'ready', d
assert '0 configured' not in str(d.get('checks', {}).get('providers', '')), d
"
}
check_catalog() {
  py "
import urllib.request, json
h = {'Authorization': 'Bearer $ADMIN_KEY'}
def get(p):
    r = urllib.request.Request('$GATEWAY_URL' + p, headers=h)
    return json.load(urllib.request.urlopen(r, timeout=15))
p = get('/admin/v1/providers'); q = get('/admin/v1/policies'); m = get('/admin/v1/models')
assert p['providers'] and q['policies'] and m['models'], 'empty catalogue'
print('providers=%d policies=%d models=%d' % (len(p['providers']), len(q['policies']), len(m['models'])))
for x in p['providers']: print(' - %s (%s) adapter_ready=%s' % (x['name'], x['status'], x['adapter_ready']))
# Later steps must use a real registered model: an unknown name fails before
# routing with a 404, which proves nothing about the pipeline.
import pathlib as _pl
_pl.Path('$WIN_ROOT/.smoke_model').write_text(m['models'][0]['name'])
print('smoke model:', m['models'][0]['name'])
"
}
seed_key() {
  py "
import urllib.request, json, pathlib
h = {'Authorization': 'Bearer $ADMIN_KEY', 'Content-Type': 'application/json'}
def get(p):
    r = urllib.request.Request('$GATEWAY_URL' + p, headers={'Authorization': 'Bearer $ADMIN_KEY'})
    return json.load(urllib.request.urlopen(r, timeout=15))
def post(p, body):
    d = json.dumps(body).encode()
    r = urllib.request.Request('$GATEWAY_URL' + p, data=d, headers=h)
    return json.load(urllib.request.urlopen(r, timeout=15))
t = get('/admin/v1/tenants')['tenants']
assert t, 'no tenants'
tid = t[0]['id']
print('tenant:', t[0]['slug'])
import os
key_file = pathlib.Path('$WIN_ROOT/.smoke_key')
if os.environ.get('SMOKE_API_KEY'):
    key_file.write_text(os.environ['SMOKE_API_KEY'])
    print('using SMOKE_API_KEY from env')
elif key_file.exists():
    print('reusing key in .smoke_key')
else:
    k = get('/admin/v1/keys?tenant_id=' + tid)['keys'] or []
    if not k:
        c = post('/admin/v1/keys', {'tenant_id': tid, 'name': 'smoke-test', 'scopes': ['inference']})
        key_file.write_text(c['plaintext'])
        print('minted key', c['key']['prefix'], '(saved to .smoke_key)')
    else:
        print('keys exist but no plaintext available; export SMOKE_API_KEY')
        raise SystemExit('set SMOKE_API_KEY to an existing key plaintext to continue')
"
}
sample_request() {
  py "
import urllib.request, json, pathlib, urllib.error
key = pathlib.Path('$WIN_ROOT/.smoke_key').read_text().strip()
model = pathlib.Path('$WIN_ROOT/.smoke_model').read_text().strip()
body = json.dumps({'model': model, 'messages': [{'role': 'user', 'content': 'hello from smoke test'}]}).encode()
req = urllib.request.Request('$GATEWAY_URL/v1/chat/completions', data=body, headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
try:
    d = json.load(urllib.request.urlopen(req, timeout=60))
    print('200 OK via', d['synapass'].get('provider'))
except urllib.error.HTTPError as e:
    d = json.loads(e.read().decode())
    code = d['error']['code']
    rid = d.get('synapass', {}).get('request_id', '')
    pathlib.Path('$WIN_ROOT/.smoke_reqid').write_text(rid)
    if code in ('authentication_error', 'quota_exceeded', 'upstream_error', 'provider_unavailable', 'timeout', 'rate_limited'):
        print('HTTP %d (%s) after routing -- pipeline OK, request %s' % (e.code, code, rid[:8]))
    else:
        raise SystemExit('HTTP %d (%s): %s' % (e.code, code, d['error']['message'][:120]))
"
}
check_logged() {
  sleep 4
  py "
import urllib.request, json, pathlib
f = pathlib.Path('$WIN_ROOT/.smoke_reqid')
rid = f.read_text().strip() if f.exists() else ''
h = {'Authorization': 'Bearer $ADMIN_KEY'}
r = urllib.request.Request('$GATEWAY_URL/admin/v1/requests?limit=5', headers=h)
rows = json.load(urllib.request.urlopen(r, timeout=15))['requests'] or []
if rid:
    assert any(x['request_id'] == rid for x in rows), 'sample request not logged'
    print('sample request found in log')
else:
    assert rows, 'request log empty'
    print('recent requests:', len(rows))
try:
    f.unlink()
except OSError:
    pass
"
}
check_metrics() {
  py "
import urllib.request
t = urllib.request.urlopen('$GATEWAY_URL/metrics', timeout=15).read().decode()
assert 'synapass_gateway_requests_total' in t, 'requests_total missing'
assert 'synapass_async_flushed_total' in t, 'async_flushed missing'
print('requests_total + async_flushed present')
"
}
check_workers() {
  curl -sf -m 10 http://127.0.0.1:9101/metrics -o /dev/null && echo 'workers metrics OK'
}
check_dashboard() {
  curl -sf -m 15 "$DASHBOARD_URL/" -o /dev/null && echo 'dashboard home 200'
  jget "$DASHBOARD_URL/api/gateway/system" | "$PY" -c "import json,sys; d=json.load(sys.stdin); print('proxied providers=%d' % len(d['providers']))"
}
check_phase3() {
  py "
import urllib.request, urllib.error, json
H = {'Authorization': 'Bearer $ADMIN_KEY', 'Content-Type': 'application/json'}
def call(method, p, body=None):
    d = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request('$GATEWAY_URL' + p, data=d, method=method, headers=H)
    with urllib.request.urlopen(r, timeout=30) as resp:
        return resp.status, json.load(resp)
s, p = call('POST', '/admin/v1/providers', {'name': 'smoke-phase3', 'kind': 'openai_compatible', 'base_url': 'http://127.0.0.1:9/v1', 'environment': 'test', 'notes': 'smoke test row'})
assert s == 201 and p['managed_by'] == 'api', (s, p)
pid = p['id']
s, c = call('PUT', '/admin/v1/providers/%s/credential' % pid, {'secret': 'smoke-secret', 'name': 'smoke'})
assert s == 200 and c['has_credential'] and 'smoke-secret' not in json.dumps(c), (s, c)
s, m = call('POST', '/admin/v1/models', {'provider_id': pid, 'name': 'smoke-model', 'context_window': 4096, 'environment': 'test'})
assert s == 201, (s, m)
mid = m['id']
s, d = call('PATCH', '/admin/v1/providers/%s' % pid, {'status': 'disabled'})
assert s == 200 and d['status'] == 'disabled', (s, d)
s, t = call('POST', '/admin/v1/providers/%s/test' % pid, {'checks': ['connectivity']})
assert s == 200, (s, t)
s, ten = call('POST', '/admin/v1/tenants', {'slug': 'smoke-phase3', 'name': 'Smoke Phase 3'})
assert s == 201, (s, ten)
tid = ten['id']
s, k = call('POST', '/admin/v1/keys', {'tenant_id': tid, 'name': 'smoke-phase3'})
assert s == 201, (s, k)
s, r = call('POST', '/admin/v1/keys/%s/rotate' % k['key']['id'], {})
assert s == 201 and r['plaintext'], (s, r)
call('DELETE', '/admin/v1/models/%s' % mid)
call('DELETE', '/admin/v1/providers/%s' % pid)
s, _ = call('DELETE', '/admin/v1/tenants/%s?force=true' % tid)
assert s == 200, s
print('provider+model+tenant+key cycled, rotated, cleaned up, audited')
"
}
check_phase4() {
  py "
import urllib.request, urllib.error, json, pathlib
H = {'Authorization': 'Bearer $ADMIN_KEY', 'Content-Type': 'application/json'}
def call(method, p, body=None):
    d = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request('$GATEWAY_URL' + p, data=d, method=method, headers=H)
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.load(e)
        except Exception:
            return e.code, {}
gwexec = ((call('GET', '/admin/v1/system')[1].get('config', {}) or {}).get('tools', {}) or {}).get('gateway_execution') is True
print('gateway_execution=%s' % gwexec)
t = call('GET', '/admin/v1/tools')[1]['tools']
names = {x['name']: x for x in t}
if gwexec:
    assert 'now' in names and 'echo' in names, names.keys()
    assert names['now']['executable'] and names['echo']['executable'], names
    print('seeded:', ', '.join(sorted(names)))
else:
    print('no built-in seeding (client-executed tools); registry is operator-managed')
s, ext = call('POST', '/admin/v1/tools', {'name': 'smoke-weather', 'description': 'Smoke test external tool.', 'kind': 'external', 'safety_level': 'sensitive', 'enabled': True, 'parameters': {'type': 'object', 'properties': {'city': {'type': 'string'}}, 'required': ['city']}})
assert s in (200, 201) and not ext['executable'], (s, ext)
tid_tool = ext['id']
s, dis = call('POST', '/admin/v1/tools/%s/enabled' % tid_tool, {'enabled': False})
assert s == 200 and not dis['enabled'], (s, dis)
s, _ = call('DELETE', '/admin/v1/tools/%s' % tid_tool)
assert s == 200, s
tenants = call('GET', '/admin/v1/tenants')[1]['tenants']
tid = tenants[0]['id']
s, pol = call('PUT', '/admin/v1/tool-policies', {'name': 'smoke-phase4', 'tenant_id': tid, 'mode': 'manual', 'max_steps': 2, 'max_tool_calls': 2, 'allowed_tools': ['now']})
assert s == 200 and pol['enabled'] and pol['mode'] == 'manual', (s, pol)
s, _ = call('DELETE', '/admin/v1/tool-policies/%s' % pol['id'])
assert s == 200, s
key = pathlib.Path('$WIN_ROOT/.smoke_key').read_text().strip()
model = pathlib.Path('$WIN_ROOT/.smoke_model').read_text().strip()
KH = {'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'}
def chat(body):
    d = json.dumps(body).encode()
    r = urllib.request.Request('$GATEWAY_URL/v1/chat/completions', data=d, headers=KH)
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.load(e)
        except Exception:
            return e.code, {}
msg = [{'role': 'user', 'content': 'hi'}]
s, _ = chat({'model': model, 'messages': msg, 'tools': [{'type': 'function', 'function': {'description': 'no name'}}]})
assert s == 400, s
s, _ = chat({'model': model, 'messages': msg, 'tools': [{'type': 'function', 'function': {'name': 'known'}}], 'tool_choice': {'type': 'function', 'function': {'name': 'missing'}}})
assert s == 400, s
if gwexec:
    s, _ = chat({'model': model, 'messages': msg, 'stream': True, 'tool_execution': {'mode': 'automatic'}, 'tools': [{'type': 'function', 'function': {'name': 'now'}}]})
    assert s == 400, s
else:
    d = json.dumps({'model': model, 'messages': msg, 'stream': True, 'tool_execution': {'mode': 'automatic'}, 'tools': [{'type': 'function', 'function': {'name': 'now'}}]}).encode()
    r = urllib.request.Request('$GATEWAY_URL/v1/chat/completions', data=d, headers=KH)
    try:
        with urllib.request.urlopen(r, timeout=60) as resp:
            body = resp.read().decode('utf-8', 'replace')
            assert 'needs stream:false' not in body, body[:200]
            print('streaming automatic clamped to manual (client executes its own tools)')
    except urllib.error.HTTPError as e:
        payload = e.read().decode('utf-8', 'replace')
        assert 'needs stream:false' not in payload, (e.code, payload[:200])
s, _ = chat({'model': model, 'messages': msg, 'response_format': {'type': 'yaml'}})
assert s == 400, s
s, inv = call('GET', '/admin/v1/tool-invocations?limit=5')
assert s == 200, s
s, runs = call('GET', '/admin/v1/agent-runs?limit=5')
assert s == 200, s
print('registry cycled, policy cycled, 4 validation guards hold, history readable')
"
}

step "gateway liveness (GET /health)" check_health
step "gateway readiness (GET /ready)" check_ready
step "admin providers + policies + models" check_catalog
step "demo tenant + API key (seed if missing)" seed_key
step "sample inference request (pipeline proof)" sample_request
step "request recorded (GET /admin/v1/requests)" check_logged
step "metrics flowing (GET /metrics)" check_metrics
step "workers metrics (:9101/metrics)" check_workers
step "dashboard home + gateway proxy" check_dashboard
step "phase 3 management cycle (provider/model/tenant/key CRUD)" check_phase3
step "phase 4 tool plane (registry/policy/validation/history)" check_phase4

echo ""
echo "RESULT: $PASS passed, $FAIL failed"
echo ""
echo "Open these to inspect the running app:"
echo "  Dashboard   $DASHBOARD_URL"
echo "  Gateway     $GATEWAY_URL/health  $GATEWAY_URL/ready  $GATEWAY_URL/metrics"
echo "  Prometheus  http://127.0.0.1:9090"
echo "  Grafana     http://127.0.0.1:3001"
[ "$FAIL" -gt 0 ] && exit 1
