$ErrorActionPreference = "Stop"

$Config = ""
$LegacySource = $false
$NoPause = $false

for ($i = 0; $i -lt $args.Count; $i++) {
  $arg = [string]$args[$i]
  switch -Regex ($arg) {
    "^-Config$" {
      if ($i + 1 -ge $args.Count) { throw "-Config requires a value." }
      $i++
      $Config = [string]$args[$i]
      continue
    }
    "^-LegacySource$" {
      $LegacySource = $true
      continue
    }
    "^-NoPause$" {
      $NoPause = $true
      continue
    }
    default {
      throw "Unknown argument: $arg"
    }
  }
}

function Pause-IfNeeded {
  param([bool]$Skip)
  if (-not $Skip) {
    Write-Host ""
    [void](Read-Host "Press Enter to exit")
  }
}

try {
  $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

  $osVersion = [Environment]::OSVersion.Version
  if ((-not $LegacySource) -and (($osVersion.Major -lt 6) -or (($osVersion.Major -eq 6) -and ($osVersion.Minor -le 1)))) {
    Write-Host "Windows version $($osVersion.ToString()) detected; using Legacy Agent because Go Agent is not supported on Windows Server 2008/2008 R2."
    $LegacySource = $true
  }

  if ($LegacySource) {
    $legacyScript = Join-Path $scriptRoot "legacy\source-agent.ps1"
    if (-not (Test-Path -LiteralPath $legacyScript)) {
      $legacyScript = Join-Path $scriptRoot "source-agent.ps1"
    }
    if (-not (Test-Path -LiteralPath $legacyScript)) {
      throw "Legacy source agent script not found. Expected legacy\source-agent.ps1 or source-agent.ps1."
    }

    Write-Host "Starting WinDnsSyncAgent legacy source agent..."
    Write-Host "Script: $legacyScript"
    & $legacyScript
    return
  }

  $exe = Join-Path $scriptRoot "windnssyncagent.exe"
  if (-not (Test-Path -LiteralPath $exe)) {
    throw "windnssyncagent.exe not found: $exe"
  }

  if ([string]::IsNullOrEmpty($Config) -or $Config.Trim().Length -eq 0) {
    $Config = Join-Path $scriptRoot "config\agent.json"
    if (-not (Test-Path -LiteralPath $Config)) {
      $Config = Join-Path $scriptRoot "agent.json"
    }
  }
  elseif (-not [System.IO.Path]::IsPathRooted($Config)) {
    $Config = Join-Path $scriptRoot $Config
  }

  if (-not (Test-Path -LiteralPath $Config)) {
    throw "Agent config file not found: $Config"
  }

  Write-Host "Starting WinDnsSyncAgent Go agent..."
  Write-Host "Config: $Config"
  & $exe agent -config $Config
  exit $LASTEXITCODE
}
catch {
  Write-Host "Start agent failed: $($_.Exception.Message)" -ForegroundColor Red
  Pause-IfNeeded -Skip:$NoPause
  exit 1
}
