param(
    [Parameter(Mandatory = $true)]
    [string] $ProjectRoot,

    [Parameter(Mandatory = $true)]
    [string] $EnvironmentFile
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Load-EnvironmentFile.ps1')
$resolvedRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
Import-EnvironmentFile -Path $EnvironmentFile

$env:APP_ENV = 'production'
$env:PROJECT_ROOT = $resolvedRoot

$backend = Join-Path $resolvedRoot 'bin\algentra-backend.exe'
if (-not (Test-Path -LiteralPath $backend -PathType Leaf)) { $backend = Join-Path $resolvedRoot 'algentra-backend.exe' }
if (-not (Test-Path -LiteralPath $backend -PathType Leaf)) {
    throw "Binary Go Core API tidak ditemukan: $backend. Build dengan go build terlebih dahulu."
}

Set-Location -LiteralPath $resolvedRoot
& $backend
exit $LASTEXITCODE
