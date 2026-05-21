<#
.SYNOPSIS
  Stand up a local Grafana, point the operator's .env at it, and create a
  dashboard that visualises agentic-autoscaler scaling decisions as annotations.
  Used by the "Full local walkthrough" in README.md. Not for production.

.DESCRIPTION
  Grafana runs with anonymous Editor access so the operator can POST annotations
  without a token and you can view dashboards without logging in. Run this BEFORE
  starting the operator so GRAFANA_URL is in .env when the operator launches.
#>
$ErrorActionPreference = 'Stop'
$g = 'http://localhost:3000'
$envFile = Join-Path $PSScriptRoot '..\.env'

# 1. (Re)start Grafana with anonymous Editor access.
docker rm -f grafana 2>$null | Out-Null
docker run -d --name grafana -p 3000:3000 `
    -e GF_AUTH_ANONYMOUS_ENABLED=true `
    -e GF_AUTH_ANONYMOUS_ORG_ROLE=Editor `
    grafana/grafana | Out-Null

Write-Host "waiting for Grafana to become healthy..."
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Seconds 3
    try { $h = Invoke-RestMethod "$g/api/health" -TimeoutSec 3; if ($h.database -eq 'ok') { break } } catch {}
}
Write-Host "Grafana UP (v$($h.version))"

# 2. Point the operator at this Grafana (no API key needed with anon Editor).
if (Test-Path $envFile) {
    (Get-Content $envFile) -replace '^GRAFANA_URL=.*', "GRAFANA_URL=$g" | Set-Content $envFile
    Write-Host "set GRAFANA_URL=$g in $envFile"
}

# 3. Admin basic auth for datasource + dashboard creation (anon Editor can't create these).
$headers = @{ Authorization = 'Basic ' + [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes('admin:admin')) }

# 4. TestData datasource — gives the panel a time axis for the annotation overlay.
try {
    $body = @{ name = 'TestData'; type = 'grafana-testdata-datasource'; access = 'proxy' } | ConvertTo-Json
    $ds = Invoke-RestMethod "$g/api/datasources" -Method Post -Headers $headers -ContentType 'application/json' -Body $body
    $dsUid = $ds.datasource.uid
} catch {
    $ds = Invoke-RestMethod "$g/api/datasources/name/TestData" -Headers $headers; $dsUid = $ds.uid
}

# 5. Dashboard with a tag-filtered annotation overlay (tags=agentic-autoscaler).
$dash = @{
    dashboard = @{
        uid           = 'agentic-decisions'
        title         = 'Agentic Autoscaler - Scaling Decisions'
        schemaVersion = 39
        timezone      = 'browser'
        time          = @{ from = 'now-1h'; to = 'now' }
        annotations   = @{ list = @(@{
                    name = 'Scaling decisions'; datasource = @{ type = 'grafana'; uid = '-- Grafana --' }
                    enable = $true; iconColor = 'red'
                    target = @{ type = 'tags'; tags = @('agentic-autoscaler'); matchAny = $true; limit = 100 }
                }) }
        panels        = @(@{
                id = 1; type = 'timeseries'; title = 'Scale events - hover a red line for the AI reason'
                datasource = @{ type = 'grafana-testdata-datasource'; uid = $dsUid }
                gridPos = @{ h = 12; w = 24; x = 0; y = 0 }
                targets = @(@{ refId = 'A'; scenarioId = 'random_walk'; datasource = @{ type = 'grafana-testdata-datasource'; uid = $dsUid } })
            })
    }
    overwrite = $true
} | ConvertTo-Json -Depth 15
$res = Invoke-RestMethod "$g/api/dashboards/db" -Method Post -Headers $headers -ContentType 'application/json' -Body $dash

Write-Host ""
Write-Host "DASHBOARD: $g$($res.url)"
