param(
    [string] $InstallDir = 'C:\AppServer',
    [switch] $KeepData,
    [switch] $Force
)
$ErrorActionPreference = 'Continue'
$InstallDir = [IO.Path]::GetFullPath($InstallDir).TrimEnd('\')
$Services = @('AlgentraTunnel', 'AlgentraWeb', 'AlgentraCoreApi')
$TaskNames = @('Algentra Managed Copy Worker', 'Algentra SQLite Backup', 'Algentra Watchdog', 'CopyTrade MT5 Launcher')
$EnvNames = @('COPIER_WORKER_API_KEY', 'MASTER_PUBLISHER_TOKEN')

function Step([string]$Message) { Write-Host "==> $Message" -ForegroundColor Cyan }

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Jalankan uninstall.ps1 dari PowerShell Administrator.'
}

# Guard: never remove a drive root, the source repository, or an unrelated folder.
if ($InstallDir.Length -le 3 -or $InstallDir -eq [IO.Path]::GetFullPath($env:SystemRoot).TrimEnd('\')) {
    throw "InstallDir tidak aman untuk dihapus: $InstallDir"
}
$scriptRoot = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\')
if ($scriptRoot -eq $InstallDir -or $scriptRoot.StartsWith($InstallDir + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'uninstall.ps1 berada di dalam InstallDir. Jalankan dari salinan repositori di luar folder instalasi.'
}
$markers = @('bin\algentra-backend.exe', 'bin\nssm.exe', '.env', 'mt5\copier-worker') | ForEach-Object { Join-Path $InstallDir $_ }
if (-not $Force -and -not ($markers | Where-Object { Test-Path -LiteralPath $_ })) {
    throw "Tidak ada instalasi Algentra di $InstallDir. Gunakan -Force jika folder ini memang harus dihapus."
}

Write-Host "Folder instalasi : $InstallDir"
Write-Host ("Data & secret    : " + $(if ($KeepData) { 'DIPERTAHANKAN (data, .env, secrets)' } else { 'DIHAPUS PERMANEN (database, .env, token)' }))
Write-Host 'Terminal MT5 bawaan sistem (Program Files) dan tunnel/DNS Cloudflare tidak ikut dihapus.'
if (-not $Force) {
    $answer = Read-Host 'Ketik HAPUS untuk melanjutkan'
    if ($answer -ne 'HAPUS') { Write-Host 'Dibatalkan.'; exit 1 }
}

Step 'Menghapus scheduled task'
foreach ($name in $TaskNames) {
    if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $name -Confirm:$false -ErrorAction SilentlyContinue
        Write-Host "    task dihapus: $name"
    }
}

Step 'Menghentikan dan menghapus service'
$nssm = Join-Path $InstallDir 'bin\nssm.exe'
foreach ($name in $Services) {
    if (-not (Get-Service $name -ErrorAction SilentlyContinue)) { continue }
    if (Test-Path -LiteralPath $nssm) { & $nssm stop $name 2>&1 | Out-Null } else { Stop-Service $name -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $nssm) { & $nssm remove $name confirm 2>&1 | Out-Null }
    if (Get-Service $name -ErrorAction SilentlyContinue) { & "$env:windir\System32\sc.exe" delete $name | Out-Null }
    Write-Host "    service dihapus: $name"
}

Step 'Menghentikan proses yang berjalan dari folder instalasi'
$prefix = $InstallDir + '\'
Get-Process -ErrorAction SilentlyContinue | Where-Object {
    $_.Path -and $_.Path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
} | ForEach-Object {
    Write-Host "    stop: $($_.Name) ($($_.Id))"
    Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 2

Step 'Menghapus environment variable worker'
foreach ($name in $EnvNames) {
    foreach ($scope in @('User', 'Machine')) {
        if ([Environment]::GetEnvironmentVariable($name, $scope)) {
            try { [Environment]::SetEnvironmentVariable($name, $null, $scope); Write-Host "    $name ($scope) dihapus" } catch { Write-Warning "Gagal menghapus $name ($scope): $_" }
        }
    }
}

Step 'Menghapus file sementara'
Get-ChildItem -Path $env:TEMP -Force -ErrorAction SilentlyContinue | Where-Object {
    $_.Name -like 'algentra-mt5-login-*' -or $_.Name -like 'nssm-*.zip' -or $_.Name -like 'mt5setup-*.exe'
} | ForEach-Object { Remove-Item -LiteralPath $_.FullName -Recurse -Force -ErrorAction SilentlyContinue }
$commonFiles = Join-Path $env:APPDATA 'MetaQuotes\Terminal\Common\Files'
Get-ChildItem -Path $commonFiles -Filter 'algentra-login-probe*.json' -Force -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue

function Remove-Tree([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $true }
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        Remove-Item -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue
        if (-not (Test-Path -LiteralPath $Path)) { return $true }
        # Files locked down by installer ACLs: take ownership, then retry.
        & "$env:windir\System32\takeown.exe" /F $Path /R /D Y 2>&1 | Out-Null
        & "$env:windir\System32\icacls.exe" $Path /grant '*S-1-5-32-544:(OI)(CI)F' /T /C 2>&1 | Out-Null
        Start-Sleep -Seconds 2
    }
    return -not (Test-Path -LiteralPath $Path)
}

Step 'Menghapus folder instalasi'
$failed = @()
if ($KeepData) {
    $preserve = @('data', 'secrets', '.env') | ForEach-Object { Join-Path $InstallDir $_ }
    Get-ChildItem -LiteralPath $InstallDir -Force -ErrorAction SilentlyContinue | Where-Object { $preserve -notcontains $_.FullName } | ForEach-Object {
        if (-not (Remove-Tree $_.FullName)) { $failed += $_.FullName }
    }
} else {
    if (-not (Remove-Tree $InstallDir)) { $failed += $InstallDir }
}

if ($failed.Count -gt 0) {
    Write-Warning 'Sebagian item tidak dapat dihapus (masih dipakai proses lain). Tutup terminal MT5 terkait lalu jalankan ulang skrip ini:'
    $failed | ForEach-Object { Write-Warning "    $_" }
    exit 1
}
Write-Host 'Uninstall Algentra selesai.' -ForegroundColor Green
if (-not $KeepData) { Write-Host 'Tunnel dan DNS di dashboard Cloudflare tidak dihapus otomatis; hapus manual jika tidak dipakai lagi.' }
