<#
.SYNOPSIS
  CoreRouter Phase 3 smoke test: verify a running stack end to end.

.DESCRIPTION
  Checks gateway health/readiness, datastores (via readiness), dashboard +
  its gateway proxy, metrics, workers, then ensures a demo API key exists,
  sends a sample OpenAI-compatible chat request, and confirms the request was
  recorded in usage metrics and the request log. Finally it cycles the Phase 3
  management plane (provider + credential + model + tenant + key, rotate, test,
  audit, cleanup).

  No provider keys are required: without upstream credentials the sample call
  is expected to fail AFTER routing (e.g. upstream 401), which still proves
  the full pipeline (auth -> policy -> classify -> shape -> route -> execute
  -> record). The script fails only when the pipeline itself is broken.

  Reads .env in the repository root for CR_ADMIN_KEY and GATEWAY_PORT.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1
#>
[CmdletBinding()]
param(
  [string]$RepoRoot = "",
  [string]$GatewayUrl = "",
  [string]$DashboardUrl = "http://127.0.0.1:3000"
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
  if ($PSScriptRoot) { $RepoRoot = (Split-Path -Parent $PSScriptRoot) }
  else { $RepoRoot = (Get-Location).Path }
}
$passed = 0
$failed = 0

function Get-DotEnvValue([string]$Name, [string]$Default = "") {
  $envFile = Join-Path $RepoRoot ".env"
  if (Test-Path -LiteralPath $envFile) {
    foreach ($line in (Get-Content -LiteralPath $envFile)) {
      $t = $line.Trim()
      if ($t -eq "" -or $t.StartsWith("#")) { continue }
      $i = $t.IndexOf("=")
      if ($i -gt 0 -and $t.Substring(0, $i).Trim() -eq $Name) {
        return $t.Substring($i + 1).Trim()
      }
    }
  }
  $ev = [Environment]::GetEnvironmentVariable($Name)
  if ($ev) { return $ev }
  return $Default
}

function Step([string]$Name, [scriptblock]$Body) {
  Write-Host ""
  Write-Host "== $Name" -ForegroundColor Cyan
  try {
    & $Body
    $script:passed++
    Write-Host "PASS: $Name" -ForegroundColor Green
  } catch {
    $script:failed++
    Write-Host "FAIL: $Name" -ForegroundColor Red
    Write-Host ("      " + $_.Exception.Message) -ForegroundColor Red
  }
}

function Invoke-Json([string]$Uri, [string]$Method = "GET", $Body = $null, [hashtable]$Headers = @{}, $WebSession = $null) {
  $params = @{ Uri = $Uri; Method = $Method; TimeoutSec = 30; UseBasicParsing = $true }
  if ($WebSession) { $params["WebSession"] = $WebSession }
  $h = @{}
  foreach ($k in $Headers.Keys) { $h[$k] = $Headers[$k] }
  if ($Body -ne $null) {
    $params["Body"] = ($Body | ConvertTo-Json -Depth 10)
    $h["Content-Type"] = "application/json"
  }
  if ($h.Count -gt 0) { $params["Headers"] = $h }
  try {
    $resp = Invoke-RestMethod @params
    return @{ ok = $true; status = 200; body = $resp }
  } catch {
    # $_.ErrorDetails.Message carries the response body for HTTP errors in
    # Windows PowerShell 5.1 (the response stream is unreliable there).
    if ($_.ErrorDetails -and $_.ErrorDetails.Message) {
      try { $parsed = $_.ErrorDetails.Message | ConvertFrom-Json } catch { $parsed = $null }
      $code = 0
      if ($_.Exception.Response) { try { $code = [int]$_.Exception.Response.StatusCode } catch {} }
      return @{ ok = $false; status = $code; body = $parsed; raw = $_.ErrorDetails.Message }
    }
    $r = $_.Exception.Response
    if ($r -ne $null) {
      $code = [int]$r.StatusCode
      try {
        $sr = New-Object IO.StreamReader($r.GetResponseStream())
        $text = $sr.ReadToEnd()
        $parsed = $text | ConvertFrom-Json
      } catch { $parsed = $null }
      return @{ ok = $false; status = $code; body = $parsed; raw = $text }
    }
    throw
  }
}

$adminKey = Get-DotEnvValue "CR_ADMIN_KEY"
if ([string]::IsNullOrWhiteSpace($adminKey)) { throw "CR_ADMIN_KEY is empty. Copy .env.example to .env and set it." }
$gatewayPort = Get-DotEnvValue "GATEWAY_PORT" "8080"
if ([string]::IsNullOrWhiteSpace($GatewayUrl)) { $GatewayUrl = "http://127.0.0.1:$gatewayPort" }
$adminHeaders = @{ Authorization = "Bearer $adminKey" }
$script:apiKeyPlaintext = ""
$script:sampleRequestId = ""

Step "gateway liveness (GET /health)" {
  $r = Invoke-Json "$GatewayUrl/health"
  if (-not $r.ok -or $r.body.status -ne "ok") { throw "unexpected: $($r | ConvertTo-Json -Depth 4)" }
  Write-Host ("      version={0} components={1}" -f $r.body.version.version, (($r.body.components | Get-Member -MemberType NoteProperty).Name -join ","))
}

Step "gateway readiness (GET /ready)" {
  $r = Invoke-Json "$GatewayUrl/ready"
  if (-not $r.ok) { throw "not ready: $($r.body | ConvertTo-Json -Depth 4)" }
  Write-Host ("      checks: " + (($r.body.checks.PSObject.Properties | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join "; "))
  if ($r.body.checks.providers -match "0 configured") { throw "no providers configured" }
}

Step "admin providers + policies load" {
  $p = Invoke-Json "$GatewayUrl/admin/v1/providers" -Headers $adminHeaders
  if (-not $p.ok) { throw "providers: HTTP $($p.status)" }
  $pol = Invoke-Json "$GatewayUrl/admin/v1/policies" -Headers $adminHeaders
  if (-not $pol.ok) { throw "policies: HTTP $($pol.status)" }
  Write-Host ("      providers={0} policies={1}" -f $p.body.providers.Count, $pol.body.policies.Count)
  foreach ($pr in $p.body.providers) { Write-Host ("      - {0} ({1}) adapter_ready={2}" -f $pr.name, $pr.status, $pr.adapter_ready) }
}

Step "admin models list" {
  $m = Invoke-Json "$GatewayUrl/admin/v1/models" -Headers $adminHeaders
  if (-not $m.ok -or $m.body.models.Count -eq 0) { throw "no models registered" }
  # Later steps must use a real registered model: an unknown name fails before
  # routing with a 404, which proves nothing about the pipeline.
  $script:sampleModel = $m.body.models[0].name
  Write-Host ("      models: " + (($m.body.models | ForEach-Object { $_.name }) -join ", "))
  Write-Host ("      smoke model: {0}" -f $script:sampleModel)
}

Step "demo tenant + API key (seed if missing)" {
  $t = Invoke-Json "$GatewayUrl/admin/v1/tenants" -Headers $adminHeaders
  if (-not $t.ok -or $t.body.tenants.Count -eq 0) { throw "no tenants (bootstrap seed missing?)" }
  $tenantId = $t.body.tenants[0].id
  Write-Host ("      tenant: {0} ({1})" -f $t.body.tenants[0].slug, $tenantId.Substring(0, 8))
  $k = Invoke-Json ("$GatewayUrl/admin/v1/keys?tenant_id={0}" -f $tenantId) -Headers $adminHeaders
  if ($k.ok -and $k.body.keys.Count -gt 0) {
    Write-Host ("      reusing key prefix {0} (plaintext not retrievable; set SMOKE_API_KEY or keep .smoke_key)" -f $k.body.keys[0].prefix)
    $envKey = [Environment]::GetEnvironmentVariable("SMOKE_API_KEY")
    if ($envKey) { $script:apiKeyPlaintext = $envKey }
    else {
      $keyFile = Join-Path $RepoRoot ".smoke_key"
      if (Test-Path -LiteralPath $keyFile) { $script:apiKeyPlaintext = (Get-Content -LiteralPath $keyFile -Raw).Trim() }
    }
  }
  if ([string]::IsNullOrWhiteSpace($script:apiKeyPlaintext)) {
    $created = Invoke-Json "$GatewayUrl/admin/v1/keys" "POST" @{ tenant_id = $tenantId; name = "smoke-test"; scopes = @("inference") } -Headers $adminHeaders
    if (-not $created.ok) { throw "key creation failed: HTTP $($created.status)" }
    $script:apiKeyPlaintext = $created.body.plaintext
    $created.body.plaintext | Out-File -FilePath (Join-Path $RepoRoot ".smoke_key") -NoNewline -Encoding ascii
    Write-Host ("      minted key prefix {0} (plaintext saved to .smoke_key, git-ignored)" -f $created.body.key.prefix)
  }
  if ([string]::IsNullOrWhiteSpace($script:apiKeyPlaintext)) { throw "no API key available (set SMOKE_API_KEY env to reuse an existing key)" }
}

Step "sample inference request (pipeline proof)" {
  $h = @{ Authorization = "Bearer $script:apiKeyPlaintext" }
  $r = Invoke-Json "$GatewayUrl/v1/chat/completions" "POST" @{ model = $script:sampleModel; messages = @(@{ role = "user"; content = "hello from smoke test" }) } -Headers $h
  if ($r.ok) {
    Write-Host ("      200 OK via {0}, cost {1}" -f $r.body.corerouter.provider, $r.body.corerouter.estimated_cost_usd)
    $script:sampleRequestId = $r.body.corerouter.request_id
  } else {
    $code = $r.body.error.code
    $reqId = $r.body.corerouter.request_id
    $script:sampleRequestId = $reqId
    # Without upstream credentials the provider call fails AFTER routing: that
    # still proves auth, policy, classification, shaping, routing, execution
    # and recording all ran. Only local failures are fatal here.
    if ($code -in @("authentication_error", "quota_exceeded", "upstream_error", "provider_unavailable", "timeout", "rate_limited")) {
      Write-Host ("      HTTP {0} ({1}) after routing -- pipeline OK, request {2}" -f $r.status, $code, $reqId) -ForegroundColor Yellow
    } else {
      throw "HTTP $($r.status) ($code): $($r.body.error.message)"
    }
  }
}

Step "request recorded (GET /admin/v1/requests)" {
  Start-Sleep -Seconds 4
  $r = Invoke-Json "$GatewayUrl/admin/v1/requests?limit=5" -Headers $adminHeaders
  if (-not $r.ok) { throw "requests: HTTP $($r.status)" }
  $found = $r.body.requests | Where-Object { $_.request_id -eq $script:sampleRequestId }
  if ($null -eq $found) { throw "sample request $($script:sampleRequestId) not in request log" }
  Write-Host ("      found request {0} outcome={1} provider={2}" -f $found.request_id.Substring(0, 8), $found.outcome, $found.provider)
}

Step "metrics flowing (GET /metrics)" {
  $resp = Invoke-WebRequest -Uri "$GatewayUrl/metrics" -UseBasicParsing -TimeoutSec 15
  $text = $resp.Content
  foreach ($name in @("corerouter_gateway_requests_total", "corerouter_async_flushed_total")) {
    if ($text -notmatch $name) { throw "metric $name missing" }
  }
  Write-Host "      requests_total + async_flushed_total present"
}

Step "workers metrics (GET :9101/metrics)" {
  try {
    $resp = Invoke-WebRequest -Uri "http://127.0.0.1:9101/metrics" -UseBasicParsing -TimeoutSec 10
    Write-Host ("      workers metrics OK ({0} bytes)" -f $resp.RawContentLength)
  } catch {
    throw "workers metrics unreachable: $($_.Exception.Message)"
  }
}

Step "dashboard home + gateway proxy" {
  $homeResp = Invoke-WebRequest -Uri $DashboardUrl -UseBasicParsing -TimeoutSec 15
  if ($homeResp.StatusCode -ne 200) { throw "dashboard home HTTP $($homeResp.StatusCode)" }

  # The proxy is gated on an operator session, so an anonymous call must be
  # refused. Asserting that first means this step fails loudly if the gate is
  # ever removed, which is the entire point of the check.
  $anon = Invoke-Json "$DashboardUrl/api/gateway/system"
  if ($anon.status -ne 401) {
    throw "the admin proxy answered without a session (HTTP $($anon.status)); the authentication gate is missing"
  }

  # Authenticated path, when a console password is available. That password is
  # chosen interactively on first run, so CI sets CR_DASHBOARD_PASSWORD and a
  # developer machine skips this half.
  $dashPassword = $env:CR_DASHBOARD_PASSWORD
  $dashUser = if ($env:CR_DASHBOARD_USERNAME) { $env:CR_DASHBOARD_USERNAME } else { "admin" }
  if ($dashPassword) {
    $webSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $login = Invoke-Json "$DashboardUrl/api/gateway/auth/login" "POST" @{ username = $dashUser; password = $dashPassword } -WebSession $webSession
    if (-not $login.ok) { throw "dashboard login failed: HTTP $($login.status)" }

    $sys = Invoke-Json "$DashboardUrl/api/gateway/system" -WebSession $webSession
    if (-not $sys.ok) { throw "dashboard proxy failed after login: $($sys | ConvertTo-Json -Depth 3)" }
    Write-Host ("      proxy refuses anonymous, login ok, providers={0}" -f $sys.body.providers.Count)

    $out = Invoke-Json "$DashboardUrl/api/gateway/auth/logout" "POST" @{} -WebSession $webSession
    if (-not $out.ok) { throw "dashboard logout failed: HTTP $($out.status)" }

    $after = Invoke-Json "$DashboardUrl/api/gateway/system" -WebSession $webSession
    if ($after.status -ne 401) { throw "the proxy still answered after logout (HTTP $($after.status))" }
    Write-Host "      logout revokes the session"
  } else {
    Write-Host "      proxy refuses anonymous; set CR_DASHBOARD_PASSWORD to also exercise login"
  }
}

Step "phase 3 management cycle (provider/model/tenant/key CRUD)" {
  # A closed local port fails fast and proves the write path without any
  # upstream account. Everything created here is deleted before the step ends.
  $prov = Invoke-Json "$GatewayUrl/admin/v1/providers" "POST" @{
    name = "smoke-phase3"; kind = "openai_compatible"; base_url = "http://127.0.0.1:9/v1"; environment = "test"; notes = "smoke test row"
  } -Headers $adminHeaders
  if (-not $prov.ok -or $prov.body.managed_by -ne "api") { throw "provider create failed: HTTP $($prov.status)" }
  $provId = $prov.body.id
  $cred = Invoke-Json "$GatewayUrl/admin/v1/providers/$provId/credential" "PUT" @{ secret = "smoke-secret"; name = "smoke" } -Headers $adminHeaders
  if (-not $cred.ok -or -not $cred.body.has_credential) { throw "credential store failed: HTTP $($cred.status)" }
  if (($cred.body | ConvertTo-Json -Depth 5) -match "smoke-secret") { throw "credential plaintext leaked in response" }
  $mod = Invoke-Json "$GatewayUrl/admin/v1/models" "POST" @{ provider_id = $provId; name = "smoke-model"; context_window = 4096; environment = "test" } -Headers $adminHeaders
  if (-not $mod.ok) { throw "model create failed: HTTP $($mod.status)" }
  $mid = $mod.body.id
  $dis = Invoke-Json "$GatewayUrl/admin/v1/providers/$provId" "PATCH" @{ status = "disabled" } -Headers $adminHeaders
  if (-not $dis.ok -or $dis.body.status -ne "disabled") { throw "provider disable failed" }
  $tst = Invoke-Json "$GatewayUrl/admin/v1/providers/$provId/test" "POST" @{ checks = @("connectivity") } -Headers $adminHeaders
  if (-not $tst.ok) { throw "provider test failed: HTTP $($tst.status)" }
  $ten = Invoke-Json "$GatewayUrl/admin/v1/tenants" "POST" @{ slug = "smoke-phase3"; name = "Smoke Phase 3" } -Headers $adminHeaders
  if (-not $ten.ok) { throw "tenant create failed: HTTP $($ten.status)" }
  $tid = $ten.body.id
  $kk = Invoke-Json "$GatewayUrl/admin/v1/keys" "POST" @{ tenant_id = $tid; name = "smoke-phase3" } -Headers $adminHeaders
  if (-not $kk.ok) { throw "key mint failed: HTTP $($kk.status)" }
  $rot = Invoke-Json "$GatewayUrl/admin/v1/keys/$($kk.body.key.id)/rotate" "POST" @{} -Headers $adminHeaders
  if (-not $rot.ok -or -not $rot.body.plaintext) { throw "key rotate failed: HTTP $($rot.status)" }
  # Cleanup in reverse dependency order.
  $dm = Invoke-Json "$GatewayUrl/admin/v1/models/$mid" "DELETE" -Headers $adminHeaders
  $dp = Invoke-Json "$GatewayUrl/admin/v1/providers/$provId" "DELETE" -Headers $adminHeaders
  $dt = Invoke-Json "$GatewayUrl/admin/v1/tenants/$tid`?force=true" "DELETE" -Headers $adminHeaders
  if (-not ($dm.ok -and $dp.ok -and $dt.ok)) { throw "cleanup failed (model=$($dm.ok) provider=$($dp.ok) tenant=$($dt.ok))" }
  $audit = Invoke-Json "$GatewayUrl/admin/v1/audit?limit=100" -Headers $adminHeaders
  if (-not $audit.ok) { throw "audit read failed" }
  Write-Host "      provider+model+tenant+key cycled, rotated, cleaned up, audited"
}

Step "phase 4 tool plane (registry/policy/validation/history)" {
  # Gateway-side execution is opt-in (tools.gateway_execution). Read the flag
  # once so every assertion below matches the running configuration.
  $gwExec = $false
  $sys = Invoke-Json "$GatewayUrl/admin/v1/system" -Headers $adminHeaders
  if ($sys.ok -and $sys.body.config -and $sys.body.config.tools) {
    $gwExec = [bool]$sys.body.config.tools.gateway_execution
  }
  Write-Host ("      gateway_execution={0}" -f $gwExec)

  $tools = Invoke-Json "$GatewayUrl/admin/v1/tools" -Headers $adminHeaders
  if (-not $tools.ok) { throw "tools list failed: HTTP $($tools.status)" }
  if ($gwExec) {
    # Seeded built-ins must be present and gateway-executable.
    foreach ($need in @("now", "echo")) {
      $found = $tools.body.tools | Where-Object { $_.name -eq $need }
      if ($null -eq $found -or -not $found.executable) { throw "seeded tool $need missing or not executable" }
    }
    Write-Host ("      seeded: " + (($tools.body.tools | ForEach-Object { "$($_.name)($($_.kind))" }) -join ", "))
  } else {
    # With client-executed tools nothing is seeded: the registry is purely
    # operator-managed discovery for tools the client runs itself.
    Write-Host "      no built-in seeding (client-executed tools); registry is operator-managed"
  }

  # External tool lifecycle: create, disable, delete. External tools are
  # discovery-only, so executability must stay false no matter what is sent.
  $ext = Invoke-Json "$GatewayUrl/admin/v1/tools" "POST" @{
    name = "smoke-weather"; description = "Smoke test external tool."; kind = "external";
    safety_level = "sensitive"; enabled = $true;
    parameters = @{ type = "object"; properties = @{ city = @{ type = "string" } }; required = @("city") }
  } -Headers $adminHeaders
  if (-not $ext.ok -or $ext.body.executable) { throw "external tool create failed or executable: HTTP $($ext.status)" }
  $toolId = $ext.body.id
  $dis = Invoke-Json "$GatewayUrl/admin/v1/tools/$toolId/enabled" "POST" @{ enabled = $false } -Headers $adminHeaders
  if (-not $dis.ok -or $dis.body.enabled) { throw "tool disable failed" }
  $del = Invoke-Json "$GatewayUrl/admin/v1/tools/$toolId" "DELETE" -Headers $adminHeaders
  if (-not $del.ok) { throw "tool delete failed" }

  # Policy lifecycle on an existing tenant, manual mode so live behaviour is
  # unchanged. Everything created here is deleted before the step ends.
  $t = Invoke-Json "$GatewayUrl/admin/v1/tenants" -Headers $adminHeaders
  $tenantId = $t.body.tenants[0].id
  $pol = Invoke-Json "$GatewayUrl/admin/v1/tool-policies" "PUT" @{
    name = "smoke-phase4"; tenant_id = $tenantId; mode = "manual";
    max_steps = 2; max_tool_calls = 2; allowed_tools = @("now")
  } -Headers $adminHeaders
  # Omitting 'enabled' must mean enabled: a silently inert policy is the worst
  # possible failure mode for a control-plane setting.
  if (-not $pol.ok -or -not $pol.body.enabled -or $pol.body.mode -ne "manual") {
    throw "policy upsert failed or inert: HTTP $($pol.status)"
  }
  $dp = Invoke-Json "$GatewayUrl/admin/v1/tool-policies/$($pol.body.id)" "DELETE" -Headers $adminHeaders
  if (-not $dp.ok) { throw "policy delete failed" }

  # Request validation happens before routing, so none of these need a live
  # provider: each must fail fast with a named error, not an upstream timeout.
  $h = @{ Authorization = "Bearer $script:apiKeyPlaintext" }
  $bad1 = Invoke-Json "$GatewayUrl/v1/chat/completions" "POST" @{
    model = $script:sampleModel; messages = @(@{ role = "user"; content = "hi" });
    tools = @(@{ type = "function"; function = @{ description = "no name" } })
  } -Headers $h
  if ($bad1.ok -or $bad1.status -ne 400) { throw "nameless tool must be a 400, got $($bad1.status)" }
  $bad2 = Invoke-Json "$GatewayUrl/v1/chat/completions" "POST" @{
    model = $script:sampleModel; messages = @(@{ role = "user"; content = "hi" });
    tools = @(@{ type = "function"; function = @{ name = "known" } });
    tool_choice = @{ type = "function"; function = @{ name = "missing" } }
  } -Headers $h
  if ($bad2.ok -or $bad2.status -ne 400) { throw "unknown tool_choice must be a 400, got $($bad2.status)" }
  $bad3 = Invoke-Json "$GatewayUrl/v1/chat/completions" "POST" @{
    model = $script:sampleModel; messages = @(@{ role = "user"; content = "hi" }); stream = $true;
    tool_execution = @{ mode = "automatic" };
    tools = @(@{ type = "function"; function = @{ name = "now" } })
  } -Headers $h
  if ($gwExec) {
    if ($bad3.ok -or $bad3.status -ne 400) { throw "streaming automatic execution must be a 400, got $($bad3.status)" }
  } else {
    # Gateway execution off: automatic is clamped to manual, so the streamed
    # tool calls reach the client instead of being refused. Anything but the
    # stream:false refusal proves the clamp (upstream may still answer or fail
    # on its own terms).
    $refused = (-not $bad3.ok) -and ($bad3.status -eq 400) -and ("$($bad3.raw)" -match "needs stream:false")
    if ($refused) { throw "streaming automatic must be clamped to manual when gateway execution is off" }
    Write-Host "      streaming automatic clamped to manual (client executes its own tools)"
  }
  $bad4 = Invoke-Json "$GatewayUrl/v1/chat/completions" "POST" @{
    model = $script:sampleModel; messages = @(@{ role = "user"; content = "hi" });
    response_format = @{ type = "yaml" }
  } -Headers $h
  if ($bad4.ok -or $bad4.status -ne 400) { throw "unknown response_format must be a 400, got $($bad4.status)" }

  # History endpoints must answer, even when empty.
  $inv = Invoke-Json "$GatewayUrl/admin/v1/tool-invocations?limit=5" -Headers $adminHeaders
  if (-not $inv.ok) { throw "invocation history failed: HTTP $($inv.status)" }
  $runs = Invoke-Json "$GatewayUrl/admin/v1/agent-runs?limit=5" -Headers $adminHeaders
  if (-not $runs.ok) { throw "agent-run history failed: HTTP $($runs.status)" }
  Write-Host "      registry cycled, policy cycled, 4 validation guards hold, history readable"
  if (-not $gwExec) { Write-Host "      client-executed tools verified (no gateway runs; apps run their own tools)" }
}

Write-Host ""
Write-Host ("RESULT: {0} passed, {1} failed" -f $passed, $failed) -ForegroundColor $(if ($failed -eq 0) { "Green" } else { "Red" })
Write-Host ""
Write-Host "Open these to inspect the running app:"
Write-Host "  Dashboard   $DashboardUrl"
Write-Host "  Gateway     $GatewayUrl/health  $GatewayUrl/ready  $GatewayUrl/metrics"
Write-Host "  Prometheus  http://127.0.0.1:9090"
Write-Host "  Grafana     http://127.0.0.1:3001"
if ($failed -gt 0) { exit 1 }
