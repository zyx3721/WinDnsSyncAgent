package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

type PowerShellProvider struct{}

func NewPowerShellProvider() *PowerShellProvider {
	return &PowerShellProvider{}
}

func (p *PowerShellProvider) ListZones(ctx context.Context) ([]Zone, error) {
	script := `
Import-Module DnsServer -ErrorAction Stop
$conditionalForwarders = @{}
try {
  foreach ($forwarder in @(Get-DnsServerConditionalForwarderZone -ErrorAction SilentlyContinue)) {
    $forwarderName = [string]$forwarder.Name
    if ([string]::IsNullOrWhiteSpace($forwarderName)) { $forwarderName = [string]$forwarder.ZoneName }
    if (-not [string]::IsNullOrWhiteSpace($forwarderName)) { $conditionalForwarders[$forwarderName.ToLowerInvariant()] = $true }
  }
} catch {}
$zones = Get-DnsServerZone | Where-Object {
  $_.ZoneType -in @("Primary", "Secondary", "Stub") -and
  -not $conditionalForwarders.ContainsKey(([string]$_.ZoneName).ToLowerInvariant())
}
$result = @($zones | ForEach-Object {
  [PSCustomObject]@{
    id = $_.ZoneName
    name = $_.ZoneName
    type = [string]$_.ZoneType
    reverse = [bool]$_.IsReverseLookupZone
    dynamicUpdate = if ($_.DynamicUpdate -eq "NonsecureAndSecure") { "Nonsecure" } else { [string]$_.DynamicUpdate }
    serverId = "local"
  }
})
ConvertTo-Json -InputObject $result -Depth 8
`

	var zones []Zone
	if err := runJSON(ctx, script, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

func (p *PowerShellProvider) CreateZone(ctx context.Context, zone Zone) error {
	if strings.TrimSpace(zone.Name) == "" {
		return fmt.Errorf("zone name is required")
	}
	if zone.DynamicUpdate == "" {
		zone.DynamicUpdate = "None"
	}

	script := fmt.Sprintf(`
Import-Module DnsServer -ErrorAction Stop
$name = %s
$dynamicUpdate = %s
$reverse = %s
if ($dynamicUpdate -eq "Nonsecure") { $dynamicUpdate = "NonsecureAndSecure" }
if ($reverse) {
  try {
    Add-DnsServerPrimaryZone -NetworkId $name -ReplicationScope "Domain" -DynamicUpdate $dynamicUpdate -ErrorAction Stop | Out-Null
  } catch {
    Add-DnsServerPrimaryZone -NetworkId $name -ZoneFile ("$name.dns") -DynamicUpdate $dynamicUpdate -ErrorAction Stop | Out-Null
  }
} else {
  try {
    Add-DnsServerPrimaryZone -Name $name -ReplicationScope "Domain" -DynamicUpdate $dynamicUpdate -ErrorAction Stop | Out-Null
  } catch {
    Add-DnsServerPrimaryZone -Name $name -ZoneFile ("$name.dns") -DynamicUpdate $dynamicUpdate -ErrorAction Stop | Out-Null
  }
}
`, psString(zone.Name), psString(zone.DynamicUpdate), psBool(zone.Reverse))

	return run(ctx, script)
}

func (p *PowerShellProvider) DeleteZone(ctx context.Context, name string) error {
	script := fmt.Sprintf(`
Import-Module DnsServer -ErrorAction Stop
Remove-DnsServerZone -Name %s -Force -ErrorAction Stop
`, psString(name))
	return run(ctx, script)
}

func (p *PowerShellProvider) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	script := fmt.Sprintf(`
Import-Module DnsServer -ErrorAction Stop
$zoneName = %s
$now = (Get-Date).ToString("o")
$items = Get-DnsServerResourceRecord -ZoneName $zoneName -ErrorAction Stop
$result = @($items | ForEach-Object {
  $record = $_
  $type = [string]$record.RecordType
  $name = [string]$record.HostName
  if ([string]::IsNullOrEmpty($name) -or $name -eq ".") { $name = "@" }
  $ttl = [int][Math]::Round($record.TimeToLive.TotalSeconds)
  $value = ""
  switch ($type) {
    "A" { $value = [string]$record.RecordData.IPv4Address }
    "AAAA" { $value = [string]$record.RecordData.IPv6Address }
    "CNAME" { $value = [string]$record.RecordData.HostNameAlias }
    "MX" { $value = "$($record.RecordData.Preference) $($record.RecordData.MailExchange)" }
    "TXT" { $value = ($record.RecordData.DescriptiveText -join " ") }
    "PTR" { $value = [string]$record.RecordData.PtrDomainName }
    "NS" { $value = [string]$record.RecordData.NameServer }
    "SRV" { $value = "$($record.RecordData.Priority) $($record.RecordData.Weight) $($record.RecordData.Port) $($record.RecordData.DomainName)" }
    default { return }
  }
  [PSCustomObject]@{
    id = "$zoneName|$type|$name|$value"
    zoneId = $zoneName
    name = $name
    type = $type
    value = $value
    ttl = $ttl
    updatedAt = $now
  }
})
ConvertTo-Json -InputObject $result -Depth 8
`, psString(zone))

	var records []Record
	if err := runJSON(ctx, script, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (p *PowerShellProvider) CreateRecord(ctx context.Context, zone string, record Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	if record.TTL == 0 {
		record.TTL = 3600
	}

	script := fmt.Sprintf(`
Import-Module DnsServer -ErrorAction Stop
$zoneName = %s
$name = %s
$type = %s
$value = %s
$createPtr = %s
$ttl = New-TimeSpan -Seconds %d

function Get-DnsNameCandidates {
  param([string]$Name)
  $normalized = Convert-DnsRecordName -Name $Name
  if ($normalized -eq "@") { return @("", "@", ".") }
  return @($normalized)
}

function Get-DnsParentNodesForCleanup {
  param([string]$Name)
  $normalized = Convert-DnsRecordName -Name $Name
  if ($normalized -eq "@") { return @() }
  $parts = $normalized -split "\."
  $nodes = New-Object System.Collections.ArrayList
  for ($i = 0; $i -lt $parts.Count; $i++) {
    $node = ([string]::Join(".", [string[]]$parts[$i..($parts.Count - 1)])).Trim()
    if (-not [string]::IsNullOrEmpty($node)) { [void]$nodes.Add($node) }
  }
  return $nodes.ToArray()
}

function Test-DnsNodeHasAnyRecord {
  param([string]$NodeName)
  $node = Convert-DnsRecordName -Name $NodeName
  if ($node -eq "@") { return $true }
  try {
    $records = @(Get-DnsServerResourceRecord -ZoneName $zoneName -Name $node -ErrorAction Stop)
    if ($records.Count -gt 0) { return $true }
  } catch {}
  try {
    $prefix = $node + "."
    $records = @(Get-DnsServerResourceRecord -ZoneName $zoneName -ErrorAction Stop | Where-Object {
      $hostName = Convert-DnsRecordName -Name ([string]$_.HostName)
      $hostName.ToLowerInvariant().EndsWith($prefix.ToLowerInvariant())
    } | Select-Object -First 1)
    if ($records.Count -gt 0) { return $true }
  } catch {
    return $true
  }
  return $false
}

function Remove-EmptyDnsNodesAfterDelete {
  param([string]$Name)
  foreach ($node in @(Get-DnsParentNodesForCleanup -Name $Name)) {
    if (Test-DnsNodeHasAnyRecord -NodeName $node) { continue }
    try {
      $output = & dnscmd.exe . /NodeDelete $zoneName $node /f 2>&1
      if ($LASTEXITCODE -ne 0) { throw ([string]::Join("; ", [string[]]$output)) }
    } catch {
      Write-Warning ("Empty DNS node cleanup skipped: " + $node + " " + $_.Exception.Message)
      continue
    }
  }
}

function Convert-DnsRecordName {
  param([string]$Name)
  $nameText = ([string]$Name).Trim().TrimEnd('.')
  $zoneText = ([string]$zoneName).Trim().TrimEnd('.')
  if ([string]::IsNullOrEmpty($nameText) -or $nameText -eq "@" -or $nameText -eq ".") { return "@" }
  if ($nameText.ToLowerInvariant() -eq $zoneText.ToLowerInvariant()) { return "@" }
  $suffix = "." + $zoneText
  if ($nameText.ToLowerInvariant().EndsWith($suffix.ToLowerInvariant())) {
    return $nameText.Substring(0, $nameText.Length - $suffix.Length)
  }
  return $nameText
}

function Get-RecordDataValue {
  param($Record, [string]$Type)
  switch ($Type) {
    "A" { return [string]$Record.RecordData.IPv4Address }
    "AAAA" { return [string]$Record.RecordData.IPv6Address }
    "CNAME" { return [string]$Record.RecordData.HostNameAlias }
    "MX" { return "$($Record.RecordData.Preference) $($Record.RecordData.MailExchange)" }
    "TXT" { return ($Record.RecordData.DescriptiveText -join " ") }
    "PTR" { return [string]$Record.RecordData.PtrDomainName }
    "NS" { return [string]$Record.RecordData.NameServer }
    "SRV" { return "$($Record.RecordData.Priority) $($Record.RecordData.Weight) $($Record.RecordData.Port) $($Record.RecordData.DomainName)" }
    default { return "" }
  }
}

function Convert-RecordHostName {
  param([string]$HostName)
  return Convert-DnsRecordName -Name $HostName
}

function Test-RecordNameMatches {
  param($Record, [string]$Name)
  return (Convert-RecordHostName -HostName ([string]$Record.HostName)).ToLowerInvariant() -eq (Convert-RecordHostName -HostName $Name).ToLowerInvariant()
}

function Find-Record {
  param([string]$Name, [string]$Type, [string]$Value)
  $normalizedName = Convert-DnsRecordName -Name $Name
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      $records = Get-DnsServerResourceRecord -ZoneName $zoneName -RRType $Type -Name $candidate -ErrorAction Stop
    } catch {
      continue
    }
    $target = @($records | Where-Object { (Test-RecordNameMatches -Record $_ -Name $normalizedName) -and ((Get-RecordDataValue -Record $_ -Type $Type) -eq $Value) } | Select-Object -First 1)[0]
    if ($null -ne $target) { return $target }
  }
  try {
    $records = Get-DnsServerResourceRecord -ZoneName $zoneName -RRType $Type -ErrorAction Stop
    $target = @($records | Where-Object { (Test-RecordNameMatches -Record $_ -Name $normalizedName) -and ((Get-RecordDataValue -Record $_ -Type $Type) -eq $Value) } | Select-Object -First 1)[0]
    if ($null -ne $target) { return $target }
  } catch {}
  return $null
}

function Add-RecordByNameCandidates {
  param([string]$Name, [string]$Type, [string]$Value, $Ttl)
  $errors = New-Object System.Collections.ArrayList
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      switch ($Type) {
        "CNAME" { Add-DnsServerResourceRecordCName -ZoneName $zoneName -Name $candidate -HostNameAlias $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "MX" {
          $parts = $Value -split "\s+", 2
          if ($parts.Count -lt 2) { throw "MX value format: preference mail.example.com" }
          Add-DnsServerResourceRecordMX -ZoneName $zoneName -Name $candidate -Preference ([int]$parts[0]) -MailExchange $parts[1] -TimeToLive $Ttl -ErrorAction Stop | Out-Null
        }
        "TXT" { Add-DnsServerResourceRecord -Txt -ZoneName $zoneName -Name $candidate -DescriptiveText $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "PTR" { Add-DnsServerResourceRecordPtr -ZoneName $zoneName -Name $candidate -PtrDomainName $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "NS" { Add-DnsServerResourceRecord -NS -ZoneName $zoneName -Name $candidate -NameServer $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "SRV" {
          $parts = $Value -split "\s+", 4
          if ($parts.Count -lt 4) { throw "SRV value format: priority weight port target" }
          Add-DnsServerResourceRecord -Srv -ZoneName $zoneName -Name $candidate -Priority ([int]$parts[0]) -Weight ([int]$parts[1]) -Port ([int]$parts[2]) -DomainName $parts[3] -TimeToLive $Ttl -ErrorAction Stop | Out-Null
        }
        default { throw "Unsupported record type: $Type" }
      }
    } catch {
      [void]$errors.Add($_.Exception.Message)
    }
    if ($null -ne (Find-Record -Name $Name -Type $Type -Value $Value)) { return }
  }
  throw ($Type + " record add verification failed: " + $Name + " " + $Value + "; " + ([string]::Join("; ", [string[]]$errors.ToArray())))
}

function Add-ARecordWithFallback {
  param([string]$Name, [string]$Value, $Ttl)
  $errors = New-Object System.Collections.ArrayList
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      Add-DnsServerResourceRecordA -ZoneName $zoneName -Name $candidate -IPv4Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
    } catch {
      [void]$errors.Add($_.Exception.Message)
      try {
        Add-DnsServerResourceRecord -A -ZoneName $zoneName -Name $candidate -IPv4Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
      } catch {
        [void]$errors.Add($_.Exception.Message)
      }
    }
    if ($null -ne (Find-Record -Name $Name -Type "A" -Value $Value)) { return }
  }
  throw ("A record add verification failed: " + $Name + " " + $Value + "; " + ([string]::Join("; ", [string[]]$errors.ToArray())))
}

function Add-AAAARecordWithFallback {
  param([string]$Name, [string]$Value, $Ttl)
  $errors = New-Object System.Collections.ArrayList
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      Add-DnsServerResourceRecordAAAA -ZoneName $zoneName -Name $candidate -IPv6Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
    } catch {
      [void]$errors.Add($_.Exception.Message)
      try {
        Add-DnsServerResourceRecord -AAAA -ZoneName $zoneName -Name $candidate -IPv6Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
      } catch {
        [void]$errors.Add($_.Exception.Message)
      }
    }
    if ($null -ne (Find-Record -Name $Name -Type "AAAA" -Value $Value)) { return }
  }
  throw ("AAAA record add verification failed: " + $Name + " " + $Value + "; " + ([string]::Join("; ", [string[]]$errors.ToArray())))
}

$dnsRecordName = Convert-DnsRecordName -Name $name

switch ($type) {
  "A" {
    Add-ARecordWithFallback -Name $dnsRecordName -Value $value -Ttl $ttl
    if ($createPtr) {
      try {
        $ip = [System.Net.IPAddress]::Parse($value)
        $bytes = $ip.GetAddressBytes()
        if ($bytes.Length -eq 4) {
          $reverseZone = "$($bytes[2]).$($bytes[1]).$($bytes[0]).in-addr.arpa"
          $ptrName = [string]$bytes[3]
          $fqdn = if ($dnsRecordName -eq "@") { $zoneName } else { "$dnsRecordName.$zoneName" }
          if (-not $fqdn.EndsWith(".")) { $fqdn = "$fqdn." }
          Add-DnsServerResourceRecordPtr -ZoneName $reverseZone -Name $ptrName -PtrDomainName $fqdn -TimeToLive $ttl -ErrorAction Stop | Out-Null
        }
      }
      catch {
        Write-Warning ("PTR record creation skipped: " + $_.Exception.Message)
      }
    }
  }
  "AAAA" { Add-AAAARecordWithFallback -Name $dnsRecordName -Value $value -Ttl $ttl }
  "CNAME" { Add-RecordByNameCandidates -Name $dnsRecordName -Type $type -Value $value -Ttl $ttl }
  "MX" { Add-RecordByNameCandidates -Name $dnsRecordName -Type $type -Value $value -Ttl $ttl }
  "TXT" { Add-RecordByNameCandidates -Name $dnsRecordName -Type $type -Value $value -Ttl $ttl }
  "PTR" { Add-RecordByNameCandidates -Name $dnsRecordName -Type $type -Value $value -Ttl $ttl }
  "NS" { Add-RecordByNameCandidates -Name $dnsRecordName -Type $type -Value $value -Ttl $ttl }
  "SRV" { Add-RecordByNameCandidates -Name $dnsRecordName -Type $type -Value $value -Ttl $ttl }
  default { throw "Unsupported record type: $type" }
}
`, psString(zone), psString(record.Name), psString(strings.ToUpper(record.Type)), psString(record.Value), psBool(record.CreatePTR), record.TTL)

	return run(ctx, script)
}

func (p *PowerShellProvider) DeleteRecord(ctx context.Context, zone string, record Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}

	script := fmt.Sprintf(`
Import-Module DnsServer -ErrorAction Stop
$zoneName = %s
$name = %s
$type = %s
$value = %s

function Convert-DnsRecordName {
  param([string]$Name)
  $nameText = ([string]$Name).Trim().TrimEnd('.')
  $zoneText = ([string]$zoneName).Trim().TrimEnd('.')
  if ([string]::IsNullOrEmpty($nameText) -or $nameText -eq "@" -or $nameText -eq ".") { return "@" }
  if ($nameText.ToLowerInvariant() -eq $zoneText.ToLowerInvariant()) { return "@" }
  $suffix = "." + $zoneText
  if ($nameText.ToLowerInvariant().EndsWith($suffix.ToLowerInvariant())) {
    return $nameText.Substring(0, $nameText.Length - $suffix.Length)
  }
  return $nameText
}

function Get-DnsNameCandidates {
  param([string]$Name)
  $normalized = Convert-DnsRecordName -Name $Name
  if ($normalized -eq "@") { return @("", "@", ".") }
  return @($normalized)
}

function Get-DnsParentNodesForCleanup {
  param([string]$Name)
  $normalized = Convert-DnsRecordName -Name $Name
  if ($normalized -eq "@") { return @() }
  $parts = $normalized -split "\."
  $nodes = New-Object System.Collections.ArrayList
  for ($i = 0; $i -lt $parts.Count; $i++) {
    $node = ([string]::Join(".", [string[]]$parts[$i..($parts.Count - 1)])).Trim()
    if (-not [string]::IsNullOrEmpty($node)) { [void]$nodes.Add($node) }
  }
  return $nodes.ToArray()
}

function Test-DnsNodeHasAnyRecord {
  param([string]$NodeName)
  $node = Convert-DnsRecordName -Name $NodeName
  if ($node -eq "@") { return $true }
  try {
    $records = @(Get-DnsServerResourceRecord -ZoneName $zoneName -Name $node -ErrorAction Stop)
    if ($records.Count -gt 0) { return $true }
  } catch {}
  try {
    $prefix = $node + "."
    $records = @(Get-DnsServerResourceRecord -ZoneName $zoneName -ErrorAction Stop | Where-Object {
      $hostName = Convert-DnsRecordName -Name ([string]$_.HostName)
      $hostName.ToLowerInvariant().EndsWith($prefix.ToLowerInvariant())
    } | Select-Object -First 1)
    if ($records.Count -gt 0) { return $true }
  } catch {
    return $true
  }
  return $false
}

function Remove-EmptyDnsNodesAfterDelete {
  param([string]$Name)
  foreach ($node in @(Get-DnsParentNodesForCleanup -Name $Name)) {
    if (Test-DnsNodeHasAnyRecord -NodeName $node) { continue }
    try {
      $output = & dnscmd.exe . /NodeDelete $zoneName $node /f 2>&1
      if ($LASTEXITCODE -ne 0) { throw ([string]::Join("; ", [string[]]$output)) }
    } catch {
      Write-Warning ("Empty DNS node cleanup skipped: " + $node + " " + $_.Exception.Message)
      continue
    }
  }
}

function Get-RecordDataValue {
  param($Record, [string]$Type)
  switch ($Type) {
    "A" { return [string]$Record.RecordData.IPv4Address }
    "AAAA" { return [string]$Record.RecordData.IPv6Address }
    "CNAME" { return [string]$Record.RecordData.HostNameAlias }
    "MX" { return "$($Record.RecordData.Preference) $($Record.RecordData.MailExchange)" }
    "TXT" { return ($Record.RecordData.DescriptiveText -join " ") }
    "PTR" { return [string]$Record.RecordData.PtrDomainName }
    "NS" { return [string]$Record.RecordData.NameServer }
    "SRV" { return "$($Record.RecordData.Priority) $($Record.RecordData.Weight) $($Record.RecordData.Port) $($Record.RecordData.DomainName)" }
    default { return "" }
  }
}

function Convert-RecordHostName {
  param([string]$HostName)
  return Convert-DnsRecordName -Name $HostName
}

function Find-Record {
  param([string]$Name, [string]$Type, [string]$Value)
  $normalizedName = Convert-DnsRecordName -Name $Name
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      $records = Get-DnsServerResourceRecord -ZoneName $zoneName -RRType $Type -Name $candidate -ErrorAction Stop
    } catch {
      continue
    }
    $target = @($records | Where-Object { (Convert-RecordHostName -HostName ([string]$_.HostName)).ToLowerInvariant() -eq $normalizedName.ToLowerInvariant() -and ((Get-RecordDataValue -Record $_ -Type $Type) -eq $Value) } | Select-Object -First 1)[0]
    if ($null -ne $target) { return $target }
  }
  try {
    $records = Get-DnsServerResourceRecord -ZoneName $zoneName -RRType $Type -ErrorAction Stop
    $target = @($records | Where-Object { (Convert-RecordHostName -HostName ([string]$_.HostName)).ToLowerInvariant() -eq $normalizedName.ToLowerInvariant() -and ((Get-RecordDataValue -Record $_ -Type $Type) -eq $Value) } | Select-Object -First 1)[0]
    if ($null -ne $target) { return $target }
  } catch {}
  return $null
}

$target = Find-Record -Name $name -Type $type -Value $value
if (-not $target) {
  Write-Warning "Record not found, skip delete"
  } else {
  Remove-DnsServerResourceRecord -ZoneName $zoneName -InputObject $target -Force -ErrorAction Stop
  Remove-EmptyDnsNodesAfterDelete -Name $name
}
`, psString(zone), psString(record.Name), psString(strings.ToUpper(record.Type)), psString(record.Value))

	return run(ctx, script)
}

func (p *PowerShellProvider) ApplyRecordBatch(ctx context.Context, zone string, batch RecordBatch) error {
	if batch.Add == nil {
		batch.Add = []Record{}
	}
	if batch.Delete == nil {
		batch.Delete = []Record{}
	}
	if batch.Update == nil {
		batch.Update = []RecordUpdate{}
	}

	for _, record := range batch.Add {
		if err := validateRecord(record); err != nil {
			return err
		}
	}
	for _, record := range batch.Delete {
		if err := validateRecord(record); err != nil {
			return err
		}
	}
	for _, update := range batch.Update {
		if err := validateRecord(update.Old); err != nil {
			return err
		}
		if err := validateRecord(update.New); err != nil {
			return err
		}
	}

	payload, err := json.Marshal(batch)
	if err != nil {
		return err
	}

	script := fmt.Sprintf(`
Import-Module DnsServer -ErrorAction Stop
$zoneName = %s
$batch = %s | ConvertFrom-Json

function Get-RecordDataValue {
  param($Record, [string]$Type)
  switch ($Type) {
    "A" { return [string]$Record.RecordData.IPv4Address }
    "AAAA" { return [string]$Record.RecordData.IPv6Address }
    "CNAME" { return [string]$Record.RecordData.HostNameAlias }
    "MX" { return "$($Record.RecordData.Preference) $($Record.RecordData.MailExchange)" }
    "TXT" { return ($Record.RecordData.DescriptiveText -join " ") }
    "PTR" { return [string]$Record.RecordData.PtrDomainName }
    "NS" { return [string]$Record.RecordData.NameServer }
    "SRV" { return "$($Record.RecordData.Priority) $($Record.RecordData.Weight) $($Record.RecordData.Port) $($Record.RecordData.DomainName)" }
    default { return "" }
  }
}

function Convert-DnsRecordName {
  param([string]$Name)
  $nameText = ([string]$Name).Trim().TrimEnd('.')
  $zoneText = ([string]$zoneName).Trim().TrimEnd('.')
  if ([string]::IsNullOrEmpty($nameText) -or $nameText -eq "@" -or $nameText -eq ".") { return "@" }
  if ($nameText.ToLowerInvariant() -eq $zoneText.ToLowerInvariant()) { return "@" }
  $suffix = "." + $zoneText
  if ($nameText.ToLowerInvariant().EndsWith($suffix.ToLowerInvariant())) {
    return $nameText.Substring(0, $nameText.Length - $suffix.Length)
  }
  return $nameText
}

function Convert-RecordHostName {
  param([string]$HostName)
  return Convert-DnsRecordName -Name $HostName
}

function Test-RecordNameMatches {
  param($Record, [string]$Name)
  return (Convert-RecordHostName -HostName ([string]$Record.HostName)).ToLowerInvariant() -eq (Convert-RecordHostName -HostName $Name).ToLowerInvariant()
}

function Find-Record {
  param([string]$Name, [string]$Type, [string]$Value)
  $normalizedName = Convert-DnsRecordName -Name $Name
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      $records = Get-DnsServerResourceRecord -ZoneName $zoneName -RRType $Type -Name $candidate -ErrorAction Stop
    } catch {
      continue
    }
    $target = @($records | Where-Object { (Test-RecordNameMatches -Record $_ -Name $normalizedName) -and ((Get-RecordDataValue -Record $_ -Type $Type) -eq $Value) } | Select-Object -First 1)[0]
    if ($null -ne $target) { return $target }
  }
  try {
    $records = Get-DnsServerResourceRecord -ZoneName $zoneName -RRType $Type -ErrorAction Stop
    $target = @($records | Where-Object { (Test-RecordNameMatches -Record $_ -Name $normalizedName) -and ((Get-RecordDataValue -Record $_ -Type $Type) -eq $Value) } | Select-Object -First 1)[0]
    if ($null -ne $target) { return $target }
  } catch {}
  return $null
}

function Get-DnsNameCandidates {
  param([string]$Name)
  $normalized = Convert-DnsRecordName -Name $Name
  if ($normalized -eq "@") { return @("", "@", ".") }
  return @($normalized)
}

function Get-DnsParentNodesForCleanup {
  param([string]$Name)
  $normalized = Convert-DnsRecordName -Name $Name
  if ($normalized -eq "@") { return @() }
  $parts = $normalized -split "\."
  $nodes = New-Object System.Collections.ArrayList
  for ($i = 0; $i -lt $parts.Count; $i++) {
    $node = ([string]::Join(".", [string[]]$parts[$i..($parts.Count - 1)])).Trim()
    if (-not [string]::IsNullOrEmpty($node)) { [void]$nodes.Add($node) }
  }
  return $nodes.ToArray()
}

function Test-DnsNodeHasAnyRecord {
  param([string]$NodeName)
  $node = Convert-DnsRecordName -Name $NodeName
  if ($node -eq "@") { return $true }
  try {
    $records = @(Get-DnsServerResourceRecord -ZoneName $zoneName -Name $node -ErrorAction Stop)
    if ($records.Count -gt 0) { return $true }
  } catch {}
  try {
    $prefix = $node + "."
    $records = @(Get-DnsServerResourceRecord -ZoneName $zoneName -ErrorAction Stop | Where-Object {
      $hostName = Convert-DnsRecordName -Name ([string]$_.HostName)
      $hostName.ToLowerInvariant().EndsWith($prefix.ToLowerInvariant())
    } | Select-Object -First 1)
    if ($records.Count -gt 0) { return $true }
  } catch {
    return $true
  }
  return $false
}

function Remove-EmptyDnsNodesAfterDelete {
  param([string]$Name)
  foreach ($node in @(Get-DnsParentNodesForCleanup -Name $Name)) {
    if (Test-DnsNodeHasAnyRecord -NodeName $node) { continue }
    try {
      $output = & dnscmd.exe . /NodeDelete $zoneName $node /f 2>&1
      if ($LASTEXITCODE -ne 0) { throw ([string]::Join("; ", [string[]]$output)) }
    } catch {
      Write-Warning ("Empty DNS node cleanup skipped: " + $node + " " + $_.Exception.Message)
      continue
    }
  }
}

function Add-RecordByNameCandidates {
  param([string]$Name, [string]$Type, [string]$Value, $Ttl)
  $errors = New-Object System.Collections.ArrayList
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      switch ($Type) {
        "CNAME" { Add-DnsServerResourceRecordCName -ZoneName $zoneName -Name $candidate -HostNameAlias $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "MX" {
          $parts = $Value -split "\s+", 2
          if ($parts.Count -lt 2) { throw "MX value format: preference mail.example.com" }
          Add-DnsServerResourceRecordMX -ZoneName $zoneName -Name $candidate -Preference ([int]$parts[0]) -MailExchange $parts[1] -TimeToLive $Ttl -ErrorAction Stop | Out-Null
        }
        "TXT" { Add-DnsServerResourceRecord -Txt -ZoneName $zoneName -Name $candidate -DescriptiveText $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "PTR" { Add-DnsServerResourceRecordPtr -ZoneName $zoneName -Name $candidate -PtrDomainName $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "NS" { Add-DnsServerResourceRecord -NS -ZoneName $zoneName -Name $candidate -NameServer $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null }
        "SRV" {
          $parts = $Value -split "\s+", 4
          if ($parts.Count -lt 4) { throw "SRV value format: priority weight port target" }
          Add-DnsServerResourceRecord -Srv -ZoneName $zoneName -Name $candidate -Priority ([int]$parts[0]) -Weight ([int]$parts[1]) -Port ([int]$parts[2]) -DomainName $parts[3] -TimeToLive $Ttl -ErrorAction Stop | Out-Null
        }
        default { throw "Unsupported record type: $Type" }
      }
    } catch {
      [void]$errors.Add($_.Exception.Message)
    }
    if ($null -ne (Find-Record -Name $Name -Type $Type -Value $Value)) { return }
  }
  throw ($Type + " record add verification failed: " + $Name + " " + $Value + "; " + ([string]::Join("; ", [string[]]$errors.ToArray())))
}

function Add-ARecordWithFallback {
  param([string]$Name, [string]$Value, $Ttl)
  $errors = New-Object System.Collections.ArrayList
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      Add-DnsServerResourceRecordA -ZoneName $zoneName -Name $candidate -IPv4Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
    } catch {
      [void]$errors.Add($_.Exception.Message)
      try {
        Add-DnsServerResourceRecord -A -ZoneName $zoneName -Name $candidate -IPv4Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
      } catch {
        [void]$errors.Add($_.Exception.Message)
      }
    }
    if ($null -ne (Find-Record -Name $Name -Type "A" -Value $Value)) { return }
  }
  throw ("A record add verification failed: " + $Name + " " + $Value + "; " + ([string]::Join("; ", [string[]]$errors.ToArray())))
}

function Add-AAAARecordWithFallback {
  param([string]$Name, [string]$Value, $Ttl)
  $errors = New-Object System.Collections.ArrayList
  foreach ($candidate in @(Get-DnsNameCandidates -Name $Name)) {
    try {
      Add-DnsServerResourceRecordAAAA -ZoneName $zoneName -Name $candidate -IPv6Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
    } catch {
      [void]$errors.Add($_.Exception.Message)
      try {
        Add-DnsServerResourceRecord -AAAA -ZoneName $zoneName -Name $candidate -IPv6Address $Value -TimeToLive $Ttl -ErrorAction Stop | Out-Null
      } catch {
        [void]$errors.Add($_.Exception.Message)
      }
    }
    if ($null -ne (Find-Record -Name $Name -Type "AAAA" -Value $Value)) { return }
  }
  throw ("AAAA record add verification failed: " + $Name + " " + $Value + "; " + ([string]::Join("; ", [string[]]$errors.ToArray())))
}

function Add-Record {
  param($Record)
  if ($null -eq $Record) { return }
  $name = Convert-DnsRecordName -Name ([string]$Record.name)
  $type = ([string]$Record.type).ToUpperInvariant()
  $value = [string]$Record.value
  if ([string]::IsNullOrEmpty($type) -or $type.Trim().Length -eq 0) { return }
  $ttlSeconds = [int]$Record.ttl
  if ($ttlSeconds -le 0) { $ttlSeconds = 3600 }
  $ttl = New-TimeSpan -Seconds $ttlSeconds
  $createPtr = $false
  if ($Record.PSObject.Properties.Name -contains "createPtr") { $createPtr = [bool]$Record.createPtr }

  $existing = Find-Record -Name $name -Type $type -Value $value
  if ($null -ne $existing) {
    Write-Warning ("Record already exists, skip add: " + $type + " " + $name + " " + $value)
    return
  }

  switch ($type) {
    "A" {
      Add-ARecordWithFallback -Name $name -Value $value -Ttl $ttl
      if ($createPtr) {
        try {
          $ip = [System.Net.IPAddress]::Parse($value)
          $bytes = $ip.GetAddressBytes()
          if ($bytes.Length -eq 4) {
            $reverseZone = "$($bytes[2]).$($bytes[1]).$($bytes[0]).in-addr.arpa"
            $ptrName = [string]$bytes[3]
            $fqdn = if ($name -eq "@") { $zoneName } else { "$name.$zoneName" }
            if (-not $fqdn.EndsWith(".")) { $fqdn = "$fqdn." }
            Add-DnsServerResourceRecordPtr -ZoneName $reverseZone -Name $ptrName -PtrDomainName $fqdn -TimeToLive $ttl -ErrorAction Stop | Out-Null
          }
        } catch {
          Write-Warning ("PTR record creation skipped: " + $_.Exception.Message)
        }
      }
    }
    "AAAA" { Add-AAAARecordWithFallback -Name $name -Value $value -Ttl $ttl }
    "CNAME" { Add-RecordByNameCandidates -Name $name -Type $type -Value $value -Ttl $ttl }
    "MX" { Add-RecordByNameCandidates -Name $name -Type $type -Value $value -Ttl $ttl }
    "TXT" { Add-RecordByNameCandidates -Name $name -Type $type -Value $value -Ttl $ttl }
    "PTR" { Add-RecordByNameCandidates -Name $name -Type $type -Value $value -Ttl $ttl }
    "NS" { Add-RecordByNameCandidates -Name $name -Type $type -Value $value -Ttl $ttl }
    "SRV" { Add-RecordByNameCandidates -Name $name -Type $type -Value $value -Ttl $ttl }
    default { throw "Unsupported record type: $type" }
  }
  $created = Find-Record -Name $name -Type $type -Value $value
  if ($null -eq $created) { throw ("Record add verification failed: " + $type + " " + $name + " " + $value) }
}

function Remove-RecordSafe {
  param($Record)
  if ($null -eq $Record) { return }
  $name = Convert-DnsRecordName -Name ([string]$Record.name)
  $type = ([string]$Record.type).ToUpperInvariant()
  $value = [string]$Record.value
  if ([string]::IsNullOrEmpty($type) -or $type.Trim().Length -eq 0) { return }
  $target = Find-Record -Name $name -Type $type -Value $value
  if ($null -eq $target) {
    Write-Warning ("Record not found, skip delete: " + $type + " " + $name + " " + $value)
    return
  }
  Remove-DnsServerResourceRecord -ZoneName $zoneName -InputObject $target -Force -ErrorAction Stop
  Remove-EmptyDnsNodesAfterDelete -Name $name
}

function Update-RecordSafe {
  param($Update)
  if ($null -eq $Update) { return }
  $old = $Update.old
  $new = $Update.new
  if (($null -eq $old) -or ($null -eq $new)) { return }
  $type = ([string]$old.type).ToUpperInvariant()
  if ([string]::IsNullOrEmpty($type) -or $type.Trim().Length -eq 0) { return }
  $oldName = Convert-DnsRecordName -Name ([string]$old.name)
  $newName = Convert-DnsRecordName -Name ([string]$new.name)
  $target = Find-Record -Name $oldName -Type $type -Value ([string]$old.value)
  if ($null -eq $target) {
    Write-Warning ("Record not found, fallback add: " + $type + " " + $newName + " " + [string]$new.value)
    Add-Record -Record $new
    return
  }

  try {
    $next = $target.Clone()
    $ttlSeconds = [int]$new.ttl
    if ($ttlSeconds -le 0) { $ttlSeconds = 3600 }
    $next.TimeToLive = New-TimeSpan -Seconds $ttlSeconds
    switch ($type) {
      "A" { $next.RecordData.IPv4Address = [System.Net.IPAddress]::Parse([string]$new.value) }
      "AAAA" { $next.RecordData.IPv6Address = [System.Net.IPAddress]::Parse([string]$new.value) }
      default { throw "Update is only supported for A and AAAA records" }
    }
    Set-DnsServerResourceRecord -ZoneName $zoneName -OldInputObject $target -NewInputObject $next -ErrorAction Stop | Out-Null
    if (($type -eq "A") -and ($new.PSObject.Properties.Name -contains "createPtr") -and [bool]$new.createPtr) {
      try {
        $ip = [System.Net.IPAddress]::Parse([string]$new.value)
        $bytes = $ip.GetAddressBytes()
        if ($bytes.Length -eq 4) {
          $reverseZone = "$($bytes[2]).$($bytes[1]).$($bytes[0]).in-addr.arpa"
          $ptrName = [string]$bytes[3]
          $fqdn = if ($newName -eq "@") { $zoneName } else { "$newName.$zoneName" }
          if (-not $fqdn.EndsWith(".")) { $fqdn = "$fqdn." }
          Add-DnsServerResourceRecordPtr -ZoneName $reverseZone -Name $ptrName -PtrDomainName $fqdn -TimeToLive $next.TimeToLive -ErrorAction Stop | Out-Null
        }
      } catch {
        Write-Warning ("PTR record creation skipped: " + $_.Exception.Message)
      }
    }
  } catch {
    $newExisting = Find-Record -Name $newName -Type $type -Value ([string]$new.value)
    if ($null -ne $newExisting) {
      Write-Warning ("Set-DnsServerResourceRecord returned an error, but target record already exists; treat as success: " + $_.Exception.Message)
      return
    }
    Write-Warning ("Set-DnsServerResourceRecord failed, fallback delete/add: " + $_.Exception.Message)
    Remove-RecordSafe -Record $old
    Add-Record -Record $new
  }
}

foreach ($update in @($batch.update)) { if ($null -ne $update) { Update-RecordSafe -Update $update } }
foreach ($record in @($batch.add)) { if ($null -ne $record) { Add-Record -Record $record } }
foreach ($record in @($batch.delete)) { if ($null -ne $record) { Remove-RecordSafe -Record $record } }
`, psString(zone), psString(string(payload)))

	return run(ctx, script)
}

func validateRecord(record Record) error {
	if strings.TrimSpace(record.Name) == "" {
		return fmt.Errorf("record name is required")
	}
	if strings.TrimSpace(record.Type) == "" {
		return fmt.Errorf("record type is required")
	}
	if strings.TrimSpace(record.Value) == "" {
		return fmt.Errorf("record value is required")
	}
	if record.TTL < 0 {
		return fmt.Errorf("ttl must not be negative")
	}
	if strings.EqualFold(record.Type, "A") && net.ParseIP(record.Value).To4() == nil {
		return fmt.Errorf("invalid IPv4 address: %s", record.Value)
	}
	if strings.EqualFold(record.Type, "AAAA") && net.ParseIP(record.Value).To16() == nil {
		return fmt.Errorf("invalid IPv6 address: %s", record.Value)
	}
	return nil
}

func runJSON(ctx context.Context, script string, dst any) error {
	out, err := runOutput(ctx, script)
	if err != nil {
		return err
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		out = []byte("[]")
	}
	if err := json.Unmarshal(out, dst); err != nil {
		return fmt.Errorf("parse powershell json: %w; output=%s", err, string(out))
	}
	return nil
}

func run(ctx context.Context, script string) error {
	_, err := runOutput(ctx, script)
	return err
}

func runOutput(ctx context.Context, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	scriptFile, err := os.CreateTemp("", "windnssyncagent-*.ps1")
	if err != nil {
		return nil, fmt.Errorf("create powershell script file: %w", err)
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)

	// Windows PowerShell reads BOM-less .ps1 files as the system ANSI code page.
	// UTF-8 with BOM keeps DNS names and record values intact when they are non-ASCII.
	if _, err := scriptFile.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		scriptFile.Close()
		return nil, fmt.Errorf("write powershell script file: %w", err)
	}
	if _, err := scriptFile.WriteString(script); err != nil {
		scriptFile.Close()
		return nil, fmt.Errorf("write powershell script file: %w", err)
	}
	if err := scriptFile.Close(); err != nil {
		return nil, fmt.Errorf("close powershell script file: %w", err)
	}

	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("powershell failed: %s", message)
	}
	return stdout.Bytes(), nil
}

func psString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func psBool(value bool) string {
	if value {
		return "$true"
	}
	return "$false"
}
