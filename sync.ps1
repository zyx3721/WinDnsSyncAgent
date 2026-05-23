$ErrorActionPreference = "Stop"

$Config = ""

for ($i = 0; $i -lt $args.Count; $i++) {
  $arg = [string]$args[$i]
  switch -Regex ($arg) {
    "^-Config$" {
      if ($i + 1 -ge $args.Count) { throw "-Config requires a value." }
      $i++
      $Config = [string]$args[$i]
      continue
    }
    "^-NoPause$" {
      continue
    }
    default {
      throw "Unknown argument: $arg"
    }
  }
}

try {
  $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

  $osVersion = [Environment]::OSVersion.Version
  if (($osVersion.Major -lt 6) -or (($osVersion.Major -eq 6) -and ($osVersion.Minor -le 1))) {
    throw "Windows Server 2008/2008 R2 cannot run windnssyncagent.exe sync. Please run sync.cmd on a Windows Server 2012+ or Windows 10/11 management machine, and keep this 2008/2008 R2 server running agent.cmd -LegacySource only."
  }

  $exe = Join-Path $scriptRoot "windnssyncagent.exe"

  if (-not (Test-Path -LiteralPath $exe)) {
    throw "windnssyncagent.exe not found: $exe"
  }

  if ([string]::IsNullOrEmpty($Config) -or $Config.Trim().Length -eq 0) {
    $Config = Join-Path $scriptRoot "config\sync.json"
    if (-not (Test-Path -LiteralPath $Config)) {
      $Config = Join-Path $scriptRoot "sync.json"
    }
  }
  elseif (-not [System.IO.Path]::IsPathRooted($Config)) {
    $Config = Join-Path $scriptRoot $Config
  }

  if (-not (Test-Path -LiteralPath $Config)) {
    throw "Sync config file not found: $Config"
  }

  Write-Host "Running WinDnsSyncAgent sync..."
  Write-Host "Config: $Config"
  Write-Host ""
  & $exe sync -config $Config
  $code = $LASTEXITCODE
  exit $code
}
catch {
  Write-Host "Sync failed: $($_.Exception.Message)" -ForegroundColor Red
  exit 1
}
