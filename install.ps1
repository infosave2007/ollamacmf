# ollamacmf installer for Windows.
#
#   irm https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.ps1 | iex
#
# Downloads the latest release archive, verifies its SHA-256 against the
# published checksums, installs ollamacmf.exe and the bundled Cortiq runtime
# into %LOCALAPPDATA%\Programs\ollamacmf, and adds that directory to the
# user's PATH. Set $env:OLLAMACMF_VERSION = 'cmf-v0.1.0' to pin a release.
$ErrorActionPreference = 'Stop'

$repo = if ($env:OLLAMACMF_REPO) { $env:OLLAMACMF_REPO } else { 'infosave2007/ollamacmf' }
$version = if ($env:OLLAMACMF_VERSION) { $env:OLLAMACMF_VERSION } else { 'latest' }
$installDir = if ($env:OLLAMACMF_INSTALL_DIR) { $env:OLLAMACMF_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\ollamacmf' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}
if ($arch -eq 'arm64') {
  Write-Host 'note: no native Windows ARM64 build is published yet; installing the x64 build, which runs under emulation.'
  $arch = 'amd64'
}

$base = if ($version -eq 'latest') {
  "https://github.com/$repo/releases/latest/download"
} else {
  "https://github.com/$repo/releases/download/$version"
}
$archive = "ollamacmf-windows-$arch.zip"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("ollamacmf-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $archive ($version)..."
  Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)
  Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "\s$([regex]::Escape($archive))$" } | Select-Object -First 1
  if (-not $line) { throw "checksums.txt has no entry for $archive" }
  $expected = ($line -split '\s+')[0].ToLowerInvariant()
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash.ToLowerInvariant()
  if ($actual -ne $expected) { throw "checksum mismatch for $archive" }

  $unpacked = Join-Path $tmp 'unpacked'
  Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $unpacked -Force
  if (-not (Test-Path (Join-Path $unpacked 'ollamacmf.exe'))) { throw 'archive does not contain ollamacmf.exe' }

  Write-Host "Installing to $installDir..."
  New-Item -ItemType Directory -Path $installDir -Force | Out-Null
  Get-Process ollamacmf -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
  Copy-Item (Join-Path $unpacked '*') $installDir -Recurse -Force

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not (($userPath -split ';') -contains $installDir)) {
    [Environment]::SetEnvironmentVariable('Path', "$installDir;$userPath", 'User')
    $env:Path = "$installDir;$env:Path"
    Write-Host "Added $installDir to your PATH (open a new terminal to pick it up)."
  }

  Write-Host ''
  Write-Host "Installed $(& (Join-Path $installDir 'ollamacmf.exe') --version 2>$null | Select-Object -First 1)."
  Write-Host ''
  Write-Host 'Next steps:'
  Write-Host '  ollamacmf serve                                              # start the server'
  Write-Host '  ollamacmf run hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp     # chat with a CMF model'
  Write-Host '  ollamacmf cmf search                                         # browse available CMF models'
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
