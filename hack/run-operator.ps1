<#
.SYNOPSIS
  Load .env and run the agentic-autoscaler operator locally with those vars.

.DESCRIPTION
  The operator reads its configuration (AI_PROVIDER, ANTHROPIC_API_KEY, etc.)
  from its process environment — it does not parse .env itself. This script
  loads .env into the current process environment, then launches the operator
  binary. The API key is never printed.

.EXAMPLE
  pwsh hack/run-operator.ps1
  pwsh hack/run-operator.ps1 -EnvFile .env -Background
#>
param(
    [string]$EnvFile = (Join-Path $PSScriptRoot '..\.env'),
    [string]$Manager = (Join-Path $PSScriptRoot '..\bin\manager.exe'),
    [switch]$Background
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $EnvFile)) {
    Write-Error ".env not found at $EnvFile  (copy .env.example to .env and fill it in)"
    exit 1
}

# Parse KEY=VALUE lines; ignore comments and blanks; strip surrounding quotes.
foreach ($line in Get-Content $EnvFile) {
    $t = $line.Trim()
    if ($t -eq '' -or $t.StartsWith('#')) { continue }
    $i = $t.IndexOf('=')
    if ($i -lt 1) { continue }
    $name = $t.Substring(0, $i).Trim()
    $val = $t.Substring($i + 1).Trim().Trim('"').Trim("'")
    Set-Item -Path "Env:$name" -Value $val
}

# Echo non-secret config only.
Write-Host ("Loaded {0}: AI_PROVIDER={1} ANTHROPIC_MODEL={2} OPERATOR_NAMESPACE={3}" -f `
    $EnvFile, $env:AI_PROVIDER, $env:ANTHROPIC_MODEL, $env:OPERATOR_NAMESPACE)
if ($env:AI_PROVIDER -eq 'anthropic' -and ($env:ANTHROPIC_API_KEY -like 'sk-ant-REPLACE_ME*' -or -not $env:ANTHROPIC_API_KEY)) {
    Write-Error "ANTHROPIC_API_KEY is not set in $EnvFile"
    exit 1
}

if (-not (Test-Path $Manager)) {
    Write-Host "Building operator binary..."
    go build -o $Manager (Join-Path $PSScriptRoot '..\cmd\main.go')
}

Write-Host "Starting operator: $Manager"
if ($Background) {
    Start-Process -FilePath $Manager -RedirectStandardOutput (Join-Path $PSScriptRoot '..\bin\op.out') `
        -RedirectStandardError (Join-Path $PSScriptRoot '..\bin\op.err') -PassThru | Select-Object Id, ProcessName
} else {
    & $Manager
}
