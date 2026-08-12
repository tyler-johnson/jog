# jog installer for Windows (PowerShell 5+):
#   irm https://raw.githubusercontent.com/tyler-johnson/jog/main/install.ps1 | iex
#
# Downloads the latest release binary, verifies its sha256 against the
# release's checksums.txt, installs to %LOCALAPPDATA%\Programs\jog, and
# adds that directory to your user PATH. Pin a version by setting
# $env:JOG_VERSION (e.g. v1.3.0) first.
$ErrorActionPreference = 'Stop'

$repo = 'tyler-johnson/jog'
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\jog'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "jog installer: unsupported architecture $env:PROCESSOR_ARCHITECTURE" }
}

$version = $env:JOG_VERSION
if (-not $version) {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ 'User-Agent' = 'jog-installer' }
    $version = $release.tag_name
}
if (-not $version) { throw 'jog installer: could not determine the latest release' }

$archive = "jog_$($version.TrimStart('v'))_windows_$arch.zip"
$base = "https://github.com/$repo/releases/download/$version"

$tmp = Join-Path $env:TEMP "jog-install-$PID"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    Write-Host "downloading jog $version (windows/$arch)..."
    Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match [regex]::Escape($archive) }
    if (-not $line) { throw "jog installer: checksums.txt has no entry for $archive" }
    $want = ($line -split '\s+')[0]
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash
    if ($got -ne $want) { throw "jog installer: checksum mismatch for $archive - refusing to install" }

    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item (Join-Path $tmp 'jog.exe') (Join-Path $installDir 'jog.exe') -Force
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Host "installed jog $version to $installDir\jog.exe"

# Put the install directory on the user PATH so new terminals find jog.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $installDir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Write-Host "added $installDir to your user PATH - open a new terminal to pick it up"
}
if (($env:Path -split ';') -notcontains $installDir) {
    $env:Path = "$env:Path;$installDir"
}

# Finish with the guided setup — `irm | iex` runs in the user's own
# session, so the wizard can ask its questions directly; redirected
# stdin (CI) leaves setup as a next step instead of running blind.
Write-Host ''
if (-not [Console]::IsInputRedirected) {
    & "$installDir\jog.exe" install
} else {
    Write-Host 'next steps:'
    Write-Host '  jog install     # guided setup: the shell wiring, agent hooks, editor hooks'
    Write-Host '  jog doctor      # verify the wiring'
    Write-Host ''
    Write-Host 'non-interactive setup (CI, coding agents) - flags answer every question:'
    Write-Host '  jog install --yes                             # every default'
    Write-Host '  jog install --yes --agents claude             # scoped; see: jog help install'
}
