param(
    [Parameter(Mandatory = $true)]
    [string] $EnvironmentFile,

    [string] $ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Load-EnvironmentFile.ps1')
$ProjectRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
$backup = Join-Path $ProjectRoot 'bin\backup-database.exe'
if (-not (Test-Path -LiteralPath $backup -PathType Leaf)) { $backup = Join-Path $ProjectRoot 'backup-database.exe' }
if (-not (Test-Path -LiteralPath $backup -PathType Leaf)) { throw "Executable backup Go tidak ditemukan: $backup. Build dengan: go build -o backup-database.exe ./cmd/backup-database" }
Import-EnvironmentFile -Path $EnvironmentFile

$env:APP_ENV = 'production'
$env:PROJECT_ROOT = $ProjectRoot
Set-Location -LiteralPath $ProjectRoot
& $backup
exit $LASTEXITCODE
