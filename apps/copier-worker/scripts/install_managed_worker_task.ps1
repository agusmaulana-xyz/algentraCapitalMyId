param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path,
    [string]$ApiBaseUrl,
    [string]$TerminalTemplateDir,
    [string]$MT5SourceTerminalDir,
    [string]$MasterTerminalPath,
    [string]$SignalDirectory
)

$ErrorActionPreference = 'Stop'
$resolvedRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
$workerBinary = Join-Path $resolvedRoot "algentra-copier-worker.exe"
if (-not (Test-Path -LiteralPath $workerBinary -PathType Leaf)) { $workerBinary = Join-Path $resolvedRoot "bin\copier-worker.exe" }
$go = Get-Command go.exe -ErrorAction SilentlyContinue
if ((Test-Path -LiteralPath (Join-Path $resolvedRoot 'go.mod') -PathType Leaf) -and $go) {
	# Always build the worker from the checked-out source during installation.
	# A pre-existing bin\copier-worker.exe may be stale and silently reintroduce
	# a fixed runtime crash (for example, exhausting Windows callback slots).
	New-Item -ItemType Directory -Path (Join-Path $resolvedRoot 'bin') -Force | Out-Null
	$workerBinary = Join-Path $resolvedRoot 'bin\copier-worker.exe'
	Push-Location $resolvedRoot
	try { & $go.Source build -ldflags='-s -w' -o $workerBinary ./cmd/copier-worker } finally { Pop-Location }
	if ($LASTEXITCODE -ne 0) { throw 'Build binary copier-worker dari source terbaru gagal.' }
	Write-Host "Worker dibangun dari source terbaru: $workerBinary"
}
$masterPublisher = Join-Path $resolvedRoot "algentra-copier-master.exe"
if (-not (Test-Path -LiteralPath $masterPublisher -PathType Leaf)) { $masterPublisher = Join-Path $resolvedRoot "bin\copier-master.exe" }
$workerPackage = Join-Path $resolvedRoot "apps\copier-worker"
if (-not (Test-Path -LiteralPath $workerPackage -PathType Container)) { $workerPackage = Join-Path $resolvedRoot "mt5\copier-worker" }
$configDirectory = Join-Path $workerPackage "config"
$workerConfigPath = Join-Path $configDirectory "managed-worker.json"
$workerExamplePath = Join-Path $configDirectory "managed-worker.example.json"
$masterConfigPath = Join-Path $configDirectory "master.json"
$masterExamplePath = Join-Path $configDirectory "master.example.json"
if (-not (Test-Path -LiteralPath $workerBinary -PathType Leaf)) {
	throw "Binary managed worker Go tidak ditemukan: $workerBinary. Sediakan Go dan source repo, atau sertakan bin\copier-worker.exe."
}
if (-not (Test-Path -LiteralPath $masterPublisher -PathType Leaf)) {
    $go = Get-Command go.exe -ErrorAction SilentlyContinue
    if ((Test-Path -LiteralPath (Join-Path $resolvedRoot 'go.mod') -PathType Leaf) -and $go) {
        New-Item -ItemType Directory -Path (Join-Path $resolvedRoot 'bin') -Force | Out-Null
        $masterPublisher = Join-Path $resolvedRoot 'bin\copier-master.exe'
        Push-Location $resolvedRoot
        try { & $go.Source build -ldflags='-s -w' -o $masterPublisher ./cmd/copier-master } finally { Pop-Location }
        if ($LASTEXITCODE -ne 0) { throw 'Build binary copier-master gagal.' }
    } else {
        throw "Binary publisher Go tidak ditemukan: $masterPublisher. Paket produksi harus menyertakan bin\copier-master.exe."
    }
}
$createdConfigs = $false
foreach ($pair in @(
    @{ Path = $workerConfigPath; Example = $workerExamplePath },
    @{ Path = $masterConfigPath; Example = $masterExamplePath }
)) {
    if (-not (Test-Path -LiteralPath $pair.Path -PathType Leaf)) {
        if (-not (Test-Path -LiteralPath $pair.Example -PathType Leaf)) {
            throw "File konfigurasi dan contoh tidak ditemukan: $($pair.Example)"
        }
        Copy-Item -LiteralPath $pair.Example -Destination $pair.Path
        $createdConfigs = $true
    }
}
try {
    $workerConfig = Get-Content -LiteralPath $workerConfigPath -Raw | ConvertFrom-Json -ErrorAction Stop
} catch {
    throw "JSON konfigurasi managed worker tidak valid: $workerConfigPath"
}
if ([string]$workerConfig.worker_api_key_env -ne "COPIER_WORKER_API_KEY") {
    throw "worker_api_key_env harus berisi nama variabel COPIER_WORKER_API_KEY, bukan nilai secret."
}
if (-not [string]::IsNullOrWhiteSpace($ApiBaseUrl)) {
    $workerConfig.api_base_url = $ApiBaseUrl.TrimEnd("/")
}
$prepareFollower = Join-Path $PSScriptRoot 'prepare-managed-follower.ps1'
$prepareArgs = @{ ProjectRoot = $resolvedRoot }
if (-not [string]::IsNullOrWhiteSpace($TerminalTemplateDir)) { $prepareArgs.TerminalTemplateDir = $TerminalTemplateDir }
if (-not [string]::IsNullOrWhiteSpace($MT5SourceTerminalDir)) { $prepareArgs.MT5SourceTerminalDir = $MT5SourceTerminalDir }
if (-not [string]::IsNullOrWhiteSpace($MasterTerminalPath)) { $prepareArgs.MasterTerminalPath = $MasterTerminalPath }
$preparedTemplate = & $prepareFollower @prepareArgs | Select-Object -Last 1
if (-not $preparedTemplate -or -not (Test-Path -LiteralPath (Join-Path $preparedTemplate 'terminal64.exe') -PathType Leaf)) {
    throw 'Template terminal follower tidak berhasil disiapkan.'
}
$workerConfig.terminal_template_dir = $preparedTemplate
if ($workerConfig.PSObject.Properties.Name -contains 'project_root') {
    $workerConfig.project_root = $resolvedRoot
} else {
    $workerConfig | Add-Member -MemberType NoteProperty -Name 'project_root' -Value $resolvedRoot
}
$workerConfig.execution_mode = "mt5_ea"
$workerConfig.feed_address = "127.0.0.1:8766"
$masterConfigValue = [string]$workerConfig.master_config
if ([string]::IsNullOrWhiteSpace($masterConfigValue)) {
    throw "master_config wajib diisi pada managed-worker.json."
}
$masterConfigPath = if ([IO.Path]::IsPathRooted($masterConfigValue)) {
    $masterConfigValue
} else {
    Join-Path $configDirectory $masterConfigValue
}
if (-not (Test-Path -LiteralPath $masterConfigPath -PathType Leaf)) {
    throw "Konfigurasi master tidak ditemukan: $masterConfigPath"
}
try {
    $masterConfig = Get-Content -LiteralPath $masterConfigPath -Raw | ConvertFrom-Json -ErrorAction Stop
} catch {
    throw "JSON konfigurasi master tidak valid: $masterConfigPath"
}
if (-not [string]::IsNullOrWhiteSpace($MasterTerminalPath)) {
    $masterConfig.terminal_path = $MasterTerminalPath
}
if (-not [string]::IsNullOrWhiteSpace($SignalDirectory)) {
    $masterConfig.folder_sinyal = $SignalDirectory
}
if ([string]::IsNullOrWhiteSpace([string]$masterConfig.folder_sinyal) -or
    [string]$masterConfig.folder_sinyal -eq "C:/Algentra/signals" -or
    [string]$masterConfig.folder_sinyal -eq "apps/copier-worker/config/runtime/signals") {
    $masterConfig.folder_sinyal = Join-Path $configDirectory 'runtime\signals'
}
if ([string]::IsNullOrWhiteSpace([string]$workerConfig.api_base_url)) {
    $workerConfig.api_base_url = 'http://127.0.0.1:8000'
}
$encoding = New-Object System.Text.UTF8Encoding($false)
$json = ConvertTo-Json -InputObject $workerConfig -Depth 12
[IO.File]::WriteAllText((Join-Path $configDirectory "managed-worker.json"), $json, $encoding)
$json = ConvertTo-Json -InputObject $masterConfig -Depth 12
[IO.File]::WriteAllText((Join-Path $configDirectory "master.json"), $json, $encoding)
if ([string]::IsNullOrWhiteSpace([string]$workerConfig.api_base_url)) {
    throw "api_base_url wajib diisi pada managed-worker.json."
}
$apiUri = $null
if (-not [Uri]::TryCreate([string]$workerConfig.api_base_url, [UriKind]::Absolute, [ref]$apiUri) -or
    $apiUri.Scheme -notin @("http", "https")) {
    throw "api_base_url harus berupa URL HTTP(S) yang valid."
}
$terminalTemplateDir = [string]$workerConfig.terminal_template_dir
if (-not [IO.Path]::IsPathRooted($terminalTemplateDir)) {
    $terminalTemplateDir = Join-Path $configDirectory $terminalTemplateDir
}
if (-not (Test-Path -LiteralPath $terminalTemplateDir -PathType Container) -or
    -not (Test-Path -LiteralPath (Join-Path $terminalTemplateDir "terminal64.exe") -PathType Leaf)) {
    throw "terminal_template_dir harus menunjuk folder MT5 follower bersih yang berisi terminal64.exe."
}
if ([string]$workerConfig.master_source -eq "publisher_ea" -and -not (Test-Path -LiteralPath ([string]$masterConfig.terminal_path) -PathType Leaf)) {
    throw "terminal_path pada master.json harus menunjuk terminal64.exe master yang tersedia."
}
$signalPath = [string]$masterConfig.folder_sinyal
if (-not [IO.Path]::IsPathRooted($signalPath)) {
    $signalPath = Join-Path $resolvedRoot $signalPath
}
New-Item -ItemType Directory -Path $signalPath -Force | Out-Null
$runtimeDirectory = [string]$workerConfig.runtime_dir
if (-not [IO.Path]::IsPathRooted($runtimeDirectory)) {
    $runtimeDirectory = Join-Path $configDirectory $runtimeDirectory
}
foreach ($directory in @("terminals", "states", "reports", "heartbeats", "logs")) {
    New-Item -ItemType Directory -Path (Join-Path $runtimeDirectory $directory) -Force | Out-Null
}
$workerKey = [Environment]::GetEnvironmentVariable("COPIER_WORKER_API_KEY", "User")
if (-not $workerKey) {
    $workerKey = [Environment]::GetEnvironmentVariable("COPIER_WORKER_API_KEY", "Machine")
}
if (-not $workerKey) {
    foreach ($environmentFile in @((Join-Path $resolvedRoot '.env'), (Join-Path $resolvedRoot '.env.local'))) {
        if (-not (Test-Path -LiteralPath $environmentFile -PathType Leaf)) { continue }
        $keyLine = Get-Content -LiteralPath $environmentFile | Where-Object { $_ -match '^\s*COPIER_WORKER_API_KEY\s*=' } | Select-Object -Last 1
        if ($keyLine) { $workerKey = ($keyLine -replace '^\s*COPIER_WORKER_API_KEY\s*=\s*', '').Trim().Trim('"').Trim("'") }
        if ($workerKey) { break }
    }
    if (-not $workerKey) {
        throw "COPIER_WORKER_API_KEY tidak ditemukan pada environment atau file .env backend."
    }
    [Environment]::SetEnvironmentVariable("COPIER_WORKER_API_KEY", $workerKey, "User")
    $env:COPIER_WORKER_API_KEY = $workerKey
}
if ($workerKey.Length -lt 32) {
    throw "COPIER_WORKER_API_KEY minimal 32 karakter."
}
$publisherToken = [Environment]::GetEnvironmentVariable("MASTER_PUBLISHER_TOKEN", "User")
if (-not $publisherToken) {
    $publisherToken = [Environment]::GetEnvironmentVariable("MASTER_PUBLISHER_TOKEN", "Machine")
}
if (-not $publisherToken) {
    $tokenBytes = [byte[]]::new(32)
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($tokenBytes) } finally { $rng.Dispose() }
    $publisherToken = [BitConverter]::ToString($tokenBytes).Replace('-', '').ToLowerInvariant()
    [Environment]::SetEnvironmentVariable("MASTER_PUBLISHER_TOKEN", $publisherToken, "User")
    $env:MASTER_PUBLISHER_TOKEN = $publisherToken
}
if ($publisherToken.Length -lt 32) {
    throw "MASTER_PUBLISHER_TOKEN minimal 32 karakter."
}
try {
    $health = Invoke-RestMethod -Uri ($apiUri.AbsoluteUri.TrimEnd("/") + "/health") -TimeoutSec 10 -ErrorAction Stop
} catch {
    throw "Backend API tidak dapat dijangkau dari mesin worker. Periksa api_base_url dan koneksi jaringan."
}
if ($health.status -ne "ok") {
    throw "Backend API merespons, tetapi status health bukan OK."
}
try {
    $workerHealth = Invoke-RestMethod `
        -Uri ($apiUri.AbsoluteUri.TrimEnd("/") + "/api/mt5/copier/health") `
        -Headers @{ "X-Copier-Worker-Key" = $workerKey } `
        -TimeoutSec 10 `
        -ErrorAction Stop
} catch {
    throw "Backend menolak COPIER_WORKER_API_KEY atau endpoint worker tidak tersedia. Cocokkan key tanpa membagikannya."
}
if ($workerHealth.status -ne "ok") {
    throw "Backend tidak mengonfirmasi key worker."
}
$taskName = "Algentra Managed Copy Worker"
$taskLogDirectory = Join-Path $runtimeDirectory "logs"
New-Item -ItemType Directory -Path $taskLogDirectory -Force | Out-Null
$taskErrorLog = Join-Path $taskLogDirectory "managed_worker-task-error.log"
$launcherCommand = "`$env:APP_ENV='production'; `$workerKey = [Environment]::GetEnvironmentVariable('COPIER_WORKER_API_KEY', 'User'); if (-not `$workerKey) { `$workerKey = [Environment]::GetEnvironmentVariable('COPIER_WORKER_API_KEY', 'Machine') }; if (-not `$workerKey) { throw 'COPIER_WORKER_API_KEY tidak tersedia pada environment akun worker.' }; `$env:COPIER_WORKER_API_KEY = `$workerKey; Remove-Variable workerKey; `$publisherToken = [Environment]::GetEnvironmentVariable('MASTER_PUBLISHER_TOKEN', 'User'); if (-not `$publisherToken) { `$publisherToken = [Environment]::GetEnvironmentVariable('MASTER_PUBLISHER_TOKEN', 'Machine') }; if (-not `$publisherToken) { throw 'MASTER_PUBLISHER_TOKEN tidak tersedia pada environment akun worker.' }; `$env:MASTER_PUBLISHER_TOKEN = `$publisherToken; Remove-Variable publisherToken; Set-Location -LiteralPath '$resolvedRoot'; & '$workerBinary' '$workerConfigPath' 2> '$taskErrorLog'"
$action = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -Command `"$launcherCommand`"" `
    -WorkingDirectory $resolvedRoot
$trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$principal = New-ScheduledTaskPrincipal `
    -UserId "$env:USERDOMAIN\$env:USERNAME" `
    -LogonType Interactive `
    -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet `
    -RestartCount 5 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -MultipleInstances IgnoreNew

# Repairing/reinstalling the worker must replace an already-running instance;
# otherwise IgnoreNew leaves the old process and its loaded .ex5 files active.
$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existingTask -and $existingTask.State -in @("Running", "StopPending")) {
    if ($existingTask.State -eq "Running") {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction Stop
    }
    $stopDeadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        Start-Sleep -Milliseconds 500
        $existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    } while ($existingTask -and $existingTask.State -in @("Running", "StopPending") -and [DateTime]::UtcNow -lt $stopDeadline)
    if ($existingTask -and $existingTask.State -in @("Running", "StopPending")) {
        throw "Worker lama masih berjalan; task '$taskName' tidak dapat diperbarui dengan aman."
    }
}

# Task Scheduler stops the PowerShell launcher, but its native Go child can be
# orphaned and keep the runtime lock (and managed MT5 terminals) alive. Match
# both the installed binary path and this runtime's config before stopping its
# process tree, so other worker installations are left alone.
$workerBinaryFullPath = [IO.Path]::GetFullPath($workerBinary)
$workerConfigFullPath = [IO.Path]::GetFullPath($workerConfigPath)
$workerProcessName = [IO.Path]::GetFileName($workerBinaryFullPath)
$orphanWorkers = Get-CimInstance Win32_Process -Filter "Name='$workerProcessName'" -ErrorAction Stop |
    Where-Object {
        $_.ExecutablePath -and
        [string]::Equals([IO.Path]::GetFullPath($_.ExecutablePath), $workerBinaryFullPath, [StringComparison]::OrdinalIgnoreCase) -and
        $_.CommandLine -and $_.CommandLine.Contains($workerConfigFullPath)
    }
foreach ($orphan in $orphanWorkers) {
    Write-Host "Menghentikan proses worker lama PID $($orphan.ProcessId) beserta terminal managed-nya."
    & (Join-Path $env:SystemRoot 'System32\taskkill.exe') /PID $orphan.ProcessId /T /F | Out-Null
    if ($LASTEXITCODE -ne 0) {
        $stillRunning = Get-CimInstance Win32_Process -Filter "ProcessId=$($orphan.ProcessId)" -ErrorAction SilentlyContinue
        if ($stillRunning) { throw "Worker lama PID $($orphan.ProcessId) tidak dapat dihentikan; task baru tidak dijalankan." }
    }
}

Register-ScheduledTask `
    -TaskName $taskName `
    -Action $action `
    -Trigger $trigger `
    -Principal $principal `
    -Settings $settings `
    -Description "Menjalankan Algentra managed copier saat Windows startup." `
    -Force | Out-Null
Start-ScheduledTask -TaskName $taskName
$startDeadline = [DateTime]::UtcNow.AddSeconds(45)
$task = $null
$workerProcess = $null
do {
    $task = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
    $workerProcess = Get-CimInstance Win32_Process -Filter "Name='$workerProcessName'" -ErrorAction SilentlyContinue |
        Where-Object {
            $_.ExecutablePath -and
            [string]::Equals([IO.Path]::GetFullPath($_.ExecutablePath), $workerBinaryFullPath, [StringComparison]::OrdinalIgnoreCase) -and
            $_.CommandLine -and $_.CommandLine.IndexOf($workerConfigFullPath, [StringComparison]::OrdinalIgnoreCase) -ge 0
        } | Select-Object -First 1
    if ($workerProcess) { break }
    if ([DateTime]::UtcNow -ge $startDeadline) { break }
    Start-Sleep -Seconds 1
} while ($true)

if (-not $workerProcess) {
    $taskInfo = Get-ScheduledTaskInfo -TaskName $taskName
    $recentError = if (Test-Path -LiteralPath $taskErrorLog -PathType Leaf) {
        $errorHead = (Get-Content -LiteralPath $taskErrorLog -TotalCount 12 -ErrorAction SilentlyContinue) -join "`n"
        $errorTail = (Get-Content -LiteralPath $taskErrorLog -Tail 24 -ErrorAction SilentlyContinue) -join "`n"
        "Log awal:`n$errorHead`nLog akhir:`n$errorTail"
    } else {
        "(log startup belum dibuat)"
    }
    $workerLog = Join-Path $taskLogDirectory "managed_worker.log"
    $recentWorkerLog = if (Test-Path -LiteralPath $workerLog -PathType Leaf) {
        (Get-Content -LiteralPath $workerLog -Tail 24 -ErrorAction SilentlyContinue) -join "`n"
    } else {
        "(log worker belum dibuat)"
    }
    throw "Worker process tidak muncul setelah 45 detik (task state $($task.State), result $($taskInfo.LastTaskResult)). Log startup: $taskErrorLog`n$recentError`nLog worker: $workerLog`n$recentWorkerLog"
}
Write-Output "Scheduled task '$taskName' dibuat dan dimulai untuk $resolvedRoot"
