param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Ledger'),
    [switch]$NoPathUpdate
)

$ErrorActionPreference = 'Stop'
$source = $PSScriptRoot
if (-not (Test-Path -LiteralPath (Join-Path $source 'go.mod'))) {
    throw 'Run install.ps1 from the Ledger source checkout.'
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go is required to build Ledger. Install Go, then rerun install.ps1.'
}

$destination = [IO.Path]::GetFullPath($InstallDir)
New-Item -ItemType Directory -Path $destination -Force | Out-Null
$staged = Join-Path $destination ('ledger-' + [guid]::NewGuid().ToString('N') + '.exe')
$installed = Join-Path $destination 'ledger.exe'

try {
    Push-Location $source
    try {
        & go build -o $staged .
        if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
    } finally {
        Pop-Location
    }

    Move-Item -LiteralPath $staged -Destination $installed -Force
    & $installed --help | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Installed Ledger failed its smoke check.' }

    if (-not $NoPathUpdate) {
        $currentUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $parts = @($currentUserPath -split ';' | Where-Object { $_.Trim() -ne '' })
        $alreadyPresent = @($parts | Where-Object {
            [string]::Equals($_.TrimEnd('\', '/'), $destination.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase)
        }).Count -gt 0
        if (-not $alreadyPresent) {
            [Environment]::SetEnvironmentVariable('Path', (($parts + $destination) -join ';'), 'User')
        }
        $processParts = @($env:Path -split ';' | Where-Object { $_.Trim() -ne '' })
        if (-not @($processParts | Where-Object {
            [string]::Equals($_.TrimEnd('\', '/'), $destination.TrimEnd('\', '/'), [StringComparison]::OrdinalIgnoreCase)
        }).Count) {
            $env:Path = ($processParts + $destination) -join ';'
        }
    }

    Write-Host "Ledger installed at $installed"
    if ($NoPathUpdate) {
        Write-Host 'PATH was not changed.'
    } else {
        Write-Host 'User PATH updated. Open a new terminal to use ledger everywhere.'
    }
} finally {
    if (Test-Path -LiteralPath $staged) { Remove-Item -LiteralPath $staged -Force }
}
