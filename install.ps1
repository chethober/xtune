# xtune installer for Windows.
#   irm https://raw.githubusercontent.com/chethober/xtune/main/install.ps1 | iex
# Set $env:XTUNE_VERSION = "v0.1.0" to pin a release.
$ErrorActionPreference = 'Stop'
$repo = 'chethober/xtune'

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$version = $env:XTUNE_VERSION
if (-not $version) {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
}
$file = "xtune_windows_$arch.zip"
$base = "https://github.com/$repo/releases/download/$version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("xtune-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Write-Host "downloading xtune $version (windows/$arch)"
    Invoke-WebRequest "$base/$file" -OutFile (Join-Path $tmp $file) -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($file))$" }
    $want = ($line -split ' ')[0]
    $got = (Get-FileHash (Join-Path $tmp $file) -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want -ne $got) { throw "checksum mismatch for $file" }

    $dest = Join-Path $env:LOCALAPPDATA 'xtune'
    Expand-Archive (Join-Path $tmp $file) -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Path $dest -Force | Out-Null
    Copy-Item (Join-Path $tmp 'xtune.exe') (Join-Path $dest 'xtune.exe') -Force

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $dest) {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$dest", 'User')
        Write-Host "added $dest to your PATH (open a new terminal)"
    }
    Write-Host "installed $dest\xtune.exe - run: xtune"
}
finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
