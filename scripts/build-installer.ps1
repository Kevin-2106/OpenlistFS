[CmdletBinding()]
param(
    [ValidatePattern('^\d+\.\d+\.\d+(\.\d+)?$')]
    [string]$Version = '0.2.0',
    [string]$MakensisPath,
    [string]$WailsPath,
    [ValidatePattern('^[A-Za-z0-9._-]+\.exe$')]
    [string]$GuiFilename = 'openlistfs-gui.exe',
    [switch]$SkipAppBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'The installer must be built on Windows to verify the WinFsp Authenticode signature.'
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$binDir = Join-Path $repoRoot 'build\bin'
$dependencyDir = Join-Path $repoRoot 'build\dependencies'
$manifestPath = Join-Path $repoRoot 'internal\winfspinstall\winfsp.json'
$licensePath = Join-Path $repoRoot 'internal\winfspinstall\WinFsp-License.txt'
$noticesPath = Join-Path $repoRoot 'THIRD_PARTY_NOTICES.md'
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json

function Invoke-CheckedNative {
    param([string]$Command, [string[]]$Arguments)
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command failed with exit code $LASTEXITCODE."
    }
}

function Test-OfficialWinFsp {
    param([string]$Path)
    $actualHash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $manifest.sha256.ToLowerInvariant()) {
        throw "WinFsp SHA-256 mismatch: expected $($manifest.sha256), got $actualHash. Remove the cached MSI and retry."
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne 'Valid' -or $null -eq $signature.SignerCertificate) {
        throw "WinFsp MSI Authenticode verification failed: $($signature.Status). $($signature.StatusMessage)"
    }
    Write-Host "Verified official WinFsp $($manifest.version): SHA-256 and Authenticode ($($signature.SignerCertificate.Subject))."
}

if ([string]::IsNullOrWhiteSpace($MakensisPath)) {
    $portableCandidates = @(
        (Join-Path $repoRoot 'build\tools\nsis\makensis.exe'),
        (Join-Path $repoRoot 'build\tools\nsis\nsis-3.11\makensis.exe')
    )
    $MakensisPath = $portableCandidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
    if ([string]::IsNullOrWhiteSpace($MakensisPath)) {
        $compiler = Get-Command makensis.exe -ErrorAction SilentlyContinue
        if ($null -ne $compiler) { $MakensisPath = $compiler.Source }
    }
}
if ([string]::IsNullOrWhiteSpace($MakensisPath) -or -not (Test-Path -LiteralPath $MakensisPath -PathType Leaf)) {
    throw 'NSIS 3.11 or newer is required. Extract the official portable ZIP under build\tools\nsis, or supply -MakensisPath. No compiler is installed automatically.'
}
$MakensisPath = (Resolve-Path -LiteralPath $MakensisPath).Path

New-Item -ItemType Directory -Path $binDir, $dependencyDir -Force | Out-Null
$msiPath = Join-Path $dependencyDir $manifest.filename
if ([IO.Path]::GetFileName($manifest.filename) -ne $manifest.filename -or $manifest.filename -ne 'winfsp.msi') {
    throw 'The dependency manifest must use the expected filename winfsp.msi.'
}
if ($manifest.sha256 -notmatch '^[0-9a-fA-F]{64}$' -or $manifest.url -notmatch '^https://github\.com/winfsp/winfsp/releases/download/') {
    throw 'The WinFsp manifest must pin an official HTTPS release URL and SHA-256.'
}
if (-not (Test-Path -LiteralPath $msiPath -PathType Leaf)) {
    $downloadPath = Join-Path $dependencyDir 'winfsp.download.msi'
    try {
        Write-Host "Downloading official WinFsp $($manifest.version)..."
        Invoke-WebRequest -Uri $manifest.url -OutFile $downloadPath -UseBasicParsing
        Test-OfficialWinFsp -Path $downloadPath
        Move-Item -LiteralPath $downloadPath -Destination $msiPath
    } finally {
        if (Test-Path -LiteralPath $downloadPath) { Remove-Item -LiteralPath $downloadPath }
    }
} else {
    Test-OfficialWinFsp -Path $msiPath
}

Push-Location $repoRoot
$previousGoos = [Environment]::GetEnvironmentVariable('GOOS', 'Process')
$previousGoarch = [Environment]::GetEnvironmentVariable('GOARCH', 'Process')
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    if (-not $SkipAppBuild) {
        if ([string]::IsNullOrWhiteSpace($WailsPath)) {
            $wailsCommand = Get-Command wails.exe -ErrorAction SilentlyContinue
            if ($null -ne $wailsCommand) {
                $WailsPath = $wailsCommand.Source
            } else {
                $cachedWails = Join-Path ([Environment]::GetFolderPath('UserProfile')) 'go\bin\wails.exe'
                if (Test-Path -LiteralPath $cachedWails -PathType Leaf) { $WailsPath = $cachedWails }
            }
        }
        if ([string]::IsNullOrWhiteSpace($WailsPath) -or -not (Test-Path -LiteralPath $WailsPath -PathType Leaf)) {
            throw 'Wails CLI is required to build the GUI. Supply -WailsPath, or install the version used by this project.'
        }
        Invoke-CheckedNative -Command $WailsPath -Arguments @('build', '-platform', 'windows/amd64', '-trimpath', '-m', '-nosyncgomod', '-o', $GuiFilename)
        Invoke-CheckedNative -Command 'go' -Arguments @('build', '-trimpath', '-o', (Join-Path $binDir 'openlistfs.exe'), './cmd/openlistfs')
    }
    # Always rebuild the checker: it and the embedded manifest must agree with this installer.
    Invoke-CheckedNative -Command 'go' -Arguments @('build', '-trimpath', '-o', (Join-Path $binDir 'winfsp-check.exe'), './cmd/winfsp-check')

    $requiredFiles = @(
        (Join-Path $binDir $GuiFilename),
        (Join-Path $binDir 'openlistfs.exe'),
        (Join-Path $binDir 'winfsp-check.exe'),
        $msiPath, $manifestPath, $licensePath, $noticesPath
    )
    foreach ($file in $requiredFiles) {
        if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing installer input: $file" }
    }
    $setupPath = Join-Path $binDir "OpenListFS-$Version-windows-amd64-setup.exe"
    $nsisScript = Join-Path $repoRoot 'build\windows\installer\OpenListFS.nsi'
    $versionParts = $Version.Split('.')
    while ($versionParts.Count -lt 4) { $versionParts += '0' }
    Invoke-CheckedNative -Command $MakensisPath -Arguments @(
        '/V3', '/NOCD',
        "/DREPO_ROOT=$repoRoot", "/DBIN_DIR=$binDir", "/DWINFSP_MSI=$msiPath",
        "/DGUI_BINARY=$(Join-Path $binDir $GuiFilename)",
        "/DWINFSP_MANIFEST=$manifestPath", "/DWINFSP_LICENSE=$licensePath", "/DTHIRD_PARTY_NOTICES=$noticesPath",
        "/DPRODUCT_VERSION=$Version", "/DFILE_VERSION=$($versionParts -join '.')", "/DOUTPUT_FILE=$setupPath",
        $nsisScript
    )
    if (-not (Test-Path -LiteralPath $setupPath -PathType Leaf)) { throw 'NSIS did not produce the expected installer.' }

    $hashPath = "$setupPath.sha256"
    $hashLine = '{0}  {1}' -f (Get-FileHash -LiteralPath $setupPath -Algorithm SHA256).Hash.ToLowerInvariant(), [IO.Path]::GetFileName($setupPath)
    [IO.File]::WriteAllText($hashPath, "$hashLine`n", [Text.UTF8Encoding]::new($false))
    Write-Host "Installer: $setupPath"
    Write-Host "SHA-256: $hashPath"
    Write-Host 'The setup is unsigned. This script only builds artifacts; it never installs WinFsp or OpenListFS on the build machine.'
} finally {
    [Environment]::SetEnvironmentVariable('GOOS', $previousGoos, 'Process')
    [Environment]::SetEnvironmentVariable('GOARCH', $previousGoarch, 'Process')
    Pop-Location
}
