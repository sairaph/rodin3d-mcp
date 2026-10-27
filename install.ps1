# rodin3d-mcp installer (Windows). Run in PowerShell:
#   irm https://github.com/sairaph/rodin3d-mcp/releases/latest/download/install.ps1 | iex
# Extra flags for the configure step: set $env:CONFIGURE_ARGS first, e.g. "--yes --all".
#
# The script runs inside the user's own PowerShell (iex), so it ends with
# return, never exit: exit would close that window.
$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

$Owner = 'sairaph'
$Repo  = 'rodin3d-mcp'
$Bin   = 'rodin3d-mcp'
$ConfigureArgs = @()
if ($env:CONFIGURE_ARGS) { $ConfigureArgs = $env:CONFIGURE_ARGS -split '\s+' | Where-Object { $_ } }

switch ($env:PROCESSOR_ARCHITECTURE) {
  { $_ -in 'AMD64','x64' } { $arch = 'amd64' }
  'ARM64'                  { $arch = 'arm64' }
  default { Write-Host "  Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" -ForegroundColor Red; return }
}

$asset = "$Bin-windows-$arch.exe"
$url = "https://github.com/$Owner/$Repo/releases/latest/download/$asset"
$localAppData = if ($env:LOCALAPPDATA) { $env:LOCALAPPDATA } else { Join-Path $env:USERPROFILE 'AppData\Local' }
$installDir = Join-Path $localAppData "$Repo\bin"
$target = Join-Path $installDir "$Bin.exe"
$temp = "$target.new"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

Write-Host ""
Write-Host "  $Bin installer" -ForegroundColor Cyan
Write-Host ""

# A running syncer holds the executable open; ask it to stop first.
if (Test-Path $target) {
  try { & $target daemon --stop 2>$null | Out-Null } catch {}
  Start-Sleep -Milliseconds 300
}

# Binaries moved aside by earlier installs, once nothing has them open.
Get-ChildItem -Path $installDir -Filter "$Bin.exe.old-*" -ErrorAction SilentlyContinue |
  ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }

Write-Host "  Downloading $asset..."
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  Invoke-WebRequest -Uri $url -OutFile $temp -UseBasicParsing
} catch {
  Write-Host "  Download failed. Please check your connection and try again." -ForegroundColor Red
  Write-Host "  URL: $url" -ForegroundColor Red
  Write-Host "  Reason: $_" -ForegroundColor Red
  Remove-Item $temp -ErrorAction SilentlyContinue
  return
}
if (-not (Test-Path $temp) -or (Get-Item $temp).Length -eq 0) {
  Remove-Item $temp -ErrorAction SilentlyContinue
  Write-Host "  Download did not complete; nothing was installed." -ForegroundColor Red
  return
}

# Verify the download against the release's SHA256SUMS.txt.
$verified = $false
try {
  $checksums = (Invoke-WebRequest -Uri ($url.Substring(0, $url.LastIndexOf('/')) + '/SHA256SUMS.txt') -UseBasicParsing).Content
  if ($checksums -is [byte[]]) { $checksums = [Text.Encoding]::UTF8.GetString($checksums) }
  $pattern = ' \*?' + [regex]::Escape($asset) + '\s*$'
  $line = $checksums -split "`n" | ForEach-Object { $_.TrimEnd("`r") } | Where-Object { $_ -match $pattern } | Select-Object -First 1
  if ($line) {
    $expected = ($line -split '\s+')[0].ToLower()
    $actual = (Get-FileHash -Path $temp -Algorithm SHA256).Hash.ToLower()
    if ($expected -ne $actual) {
      Write-Host "  SHA256 mismatch: the download is not the released file; nothing was installed." -ForegroundColor Red
      Remove-Item $temp -ErrorAction SilentlyContinue
      return
    }
    $verified = $true
  }
} catch {}
if (-not $verified) {
  Write-Host "  Warning: could not fetch SHA256SUMS.txt; the download was not verified." -ForegroundColor Yellow
}

# Move the old binary aside, then the new one in: Windows refuses to delete or
# overwrite a running executable but allows renaming it.
$old = "$target.old-$([System.Guid]::NewGuid().ToString('N').Substring(0,8))"
$movedAside = $false
if (Test-Path $target) {
  try { Move-Item $target $old; $movedAside = $true } catch {
    Write-Host "  Could not replace $Bin.exe: $_" -ForegroundColor Red
    Write-Host "  Close the programs using it and run the installer again." -ForegroundColor Yellow
    Remove-Item $temp -ErrorAction SilentlyContinue
    return
  }
}
try { Move-Item $temp $target } catch {
  Write-Host "  Could not replace $Bin.exe: $_" -ForegroundColor Red
  if ($movedAside) { Move-Item $old $target -ErrorAction SilentlyContinue }
  Remove-Item $temp -ErrorAction SilentlyContinue
  return
}
if ($movedAside) { Remove-Item $old -Force -ErrorAction SilentlyContinue }

# Stop the syncer again: a tool call during the download may have started one
# from the old binary. The next tool call starts the new one.
try {
  $stopped = (& $target daemon --stop 2>$null | Out-String).Trim()
  if ($stopped -match 'stopped') {
    Write-Host ""
    Write-Host "  $stopped"
    Write-Host "  Restart your AI client so it picks up the new tool descriptions." -ForegroundColor Yellow
  }
} catch {}

$pathChanged = $false
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
  $newPath = if ($userPath) { "$installDir;$userPath" } else { $installDir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  $pathChanged = $true
}
if ($env:Path -notlike "*$installDir*") { $env:Path = "$installDir;$env:Path" }

try {
  & $target configure @ConfigureArgs
  if ($LASTEXITCODE -ne 0) { throw "exit code $LASTEXITCODE" }
} catch {
  Write-Host "  configure did not complete: $_" -ForegroundColor Red
  Write-Host "  Re-run ``$Bin configure`` later to finish setup." -ForegroundColor Yellow
}

Write-Host ""
Write-Host "  Installed $Bin to $target"
if ($pathChanged) {
  Write-Host "  Added $installDir to your PATH: open a NEW terminal before running $Bin." -ForegroundColor Yellow
  Write-Host "  Or right now:  & '$target'"
}
