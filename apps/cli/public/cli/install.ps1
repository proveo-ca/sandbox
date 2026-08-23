# SPEC: _spec/internal/cdn/distribution-update.puml
# Product installer (Windows): download checksum-verified Go proveo into %USERPROFILE%\.proveo\bin.
# Usage: irm https://proveo.ca/cli/install.ps1 | iex
#    or: powershell -ExecutionPolicy Bypass -File install.ps1
$ErrorActionPreference = 'Stop'

$InstallRoot  = if ($env:PROVEO_INSTALL_ROOT) { $env:PROVEO_INSTALL_ROOT } else { Join-Path $env:USERPROFILE '.proveo' }
$BinDir       = Join-Path $InstallRoot 'bin'
$AssetBaseUrl = if ($env:PROVEO_ASSET_BASE_URL) { $env:PROVEO_ASSET_BASE_URL } else { 'https://proveo.ca/cli' }

function Get-ProveoArch {
  $raw = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  switch ($raw) {
    'ARM64' { 'arm64' }
    'AMD64' { 'amd64' }
    default { throw "unsupported architecture: $raw (need AMD64 or ARM64)" }
  }
}

function Get-ExpectedSum([string]$ChecksumsFile, [string]$AssetName) {
  foreach ($line in Get-Content $ChecksumsFile) {
    $parts = $line -split '\s+', 2
    if ($parts.Count -eq 2 -and $parts[1].Trim() -eq $AssetName) { return $parts[0].Trim() }
  }
  throw "no checksum entry for $AssetName in checksums.txt"
}

function Get-ChannelVersion([string]$LatestFile) {
  if ($env:PROVEO_VERSION) { return $env:PROVEO_VERSION.TrimStart('v') }
  if (-not (Test-Path $LatestFile)) { return 'unknown' }
  try {
    $j = Get-Content $LatestFile -Raw | ConvertFrom-Json
    if ($j.version) { return ([string]$j.version).TrimStart('v') }
  } catch {}
  return 'unknown'
}

$arch      = Get-ProveoArch
$assetName = "proveo-windows-$arch.exe"
$tmp       = Join-Path ([System.IO.Path]::GetTempPath()) ("proveo-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

try {
  $latest = Join-Path $tmp 'latest.json'
  try {
    Invoke-WebRequest -Uri "$AssetBaseUrl/latest.json" -OutFile $latest -UseBasicParsing
  } catch {
    Write-Host "latest.json not available — falling back to checksums.txt only"
  }
  $Version = Get-ChannelVersion $latest

  Write-Host "Downloading $assetName (v$Version)..."
  $checksums = Join-Path $tmp 'checksums.txt'
  $binary    = Join-Path $tmp $assetName
  Invoke-WebRequest -Uri "$AssetBaseUrl/checksums.txt"      -OutFile $checksums -UseBasicParsing
  Invoke-WebRequest -Uri "$AssetBaseUrl/bin/$assetName"     -OutFile $binary    -UseBasicParsing

  $expected = Get-ExpectedSum $checksums $assetName
  $actual   = (Get-FileHash -Algorithm SHA256 $binary).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) {
    throw "checksum mismatch for $assetName (expected $expected, got $actual)"
  }

  Copy-Item $binary (Join-Path $BinDir 'proveo.exe') -Force
}
finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not ($userPath -split ';' | Where-Object { $_ -eq $BinDir })) {
  [Environment]::SetEnvironmentVariable('Path', "$BinDir;$userPath", 'User')
  Write-Host "Added $BinDir to your user PATH."
}
$env:Path = "$BinDir;$env:Path"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  Write-Host ""
  Write-Host "Docker was not found. proveo runs published Docker images, so install Docker Desktop:"
  Write-Host "  https://docs.docker.com/get-docker/"
}

Write-Host ""
Write-Host "proveo v$Version installed to:"
Write-Host "  $BinDir\proveo.exe"
Write-Host ""
Write-Host "Open a new terminal, then try:"
Write-Host "  proveo version"
Write-Host "  proveo update --check"
Write-Host "  proveo ls"
Write-Host "  proveo uninstall"
