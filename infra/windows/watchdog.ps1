param([Parameter(Mandatory=$true)][string]$ProjectRoot,[Parameter(Mandatory=$true)][string]$EnvironmentFile)
$ErrorActionPreference='SilentlyContinue'
. (Join-Path $PSScriptRoot 'Load-EnvironmentFile.ps1')
$ProjectRoot=(Resolve-Path -LiteralPath $ProjectRoot).Path
Import-EnvironmentFile -Path $EnvironmentFile
$stateFile=Join-Path $ProjectRoot 'tmp\watchdog-state.json';$state=@{}
if(Test-Path $stateFile){$saved=Get-Content -Raw $stateFile|ConvertFrom-Json;foreach($property in $saved.PSObject.Properties){$state[$property.Name]=$property.Value}}
$issues=[Collections.Generic.List[string]]::new()
foreach($service in @('AlgentraCoreApi','AlgentraWeb','AlgentraTunnel')){$item=Get-Service $service;if(-not $item -or $item.Status -ne 'Running'){$issues.Add("Service $service tidak berjalan.")}}
foreach($uri in @('http://127.0.0.1:8000/health','http://127.0.0.1:8000/ready','http://127.0.0.1:3000/')){try{$null=Invoke-WebRequest $uri -UseBasicParsing -TimeoutSec 8}catch{$issues.Add("Endpoint lokal tidak merespons: $uri")}}
$drive=[IO.Path]::GetPathRoot($ProjectRoot).TrimEnd('\').TrimEnd(':');$disk=Get-PSDrive $drive;if($disk){$freeGB=$disk.Free/1GB;$freePct=100*$disk.Free/($disk.Free+$disk.Used);if($freeGB -lt 2 -or $freePct -lt 15){$issues.Add('Ruang disk rendah; periksa kapasitas server.')}}
$backup=Get-ScheduledTaskInfo -TaskName 'Algentra SQLite Backup';if(-not $backup -or $backup.LastRunTime -lt (Get-Date).AddHours(-36) -or $backup.LastTaskResult -ne 0){$issues.Add('Backup SQLite terakhir gagal atau lebih lama dari 36 jam.')}
if($env:PUBLIC_DOMAIN){try{$null=Invoke-WebRequest "https://$($env:PUBLIC_DOMAIN)/" -UseBasicParsing -TimeoutSec 10}catch{$issues.Add('Domain publik tidak merespons melalui Cloudflare Tunnel.')}}
$workerConfigs=@((Join-Path $ProjectRoot 'mt5\copier-worker\config\managed-worker.json'),(Join-Path $ProjectRoot 'apps\copier-worker\config\managed-worker.json'))
foreach($workerConfigPath in $workerConfigs){
  if(-not(Test-Path -LiteralPath $workerConfigPath -PathType Leaf)){continue}
  try{$workerConfig=Get-Content -LiteralPath $workerConfigPath -Raw|ConvertFrom-Json -ErrorAction Stop;$runtimeDirectory=[string]$workerConfig.runtime_dir;if(-not[IO.Path]::IsPathRooted($runtimeDirectory)){$runtimeDirectory=Join-Path (Split-Path -Parent $workerConfigPath) $runtimeDirectory};$heartbeat=Get-ChildItem -LiteralPath (Join-Path $runtimeDirectory 'heartbeats') -Filter 'heartbeat_*.txt' -File -ErrorAction SilentlyContinue|Sort-Object LastWriteTime -Descending|Select-Object -First 1;if(-not $heartbeat -or $heartbeat.LastWriteTime -lt (Get-Date).AddMinutes(-5)){$issues.Add('Heartbeat copier worker tidak ditemukan atau kedaluwarsa.')};break}catch{$issues.Add('Konfigurasi atau runtime copier worker tidak dapat diperiksa.');break}
}
if($env:TELEGRAM_BOT_TOKEN -and $env:TELEGRAM_ADMIN_CHAT_ID){foreach($issue in $issues){$sha=[Security.Cryptography.SHA256]::Create();$key=[BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($issue))).Replace('-','');$sha.Dispose();$last=$null;if($state.ContainsKey($key)){try{$last=[datetime]$state[$key]}catch{}};if(-not $last -or $last -lt (Get-Date).AddMinutes(-30)){$body=@{chat_id=$env:TELEGRAM_ADMIN_CHAT_ID;text=$issue}|ConvertTo-Json -Compress;try{$null=Invoke-RestMethod -Method Post -Uri "https://api.telegram.org/bot$($env:TELEGRAM_BOT_TOKEN)/sendMessage" -ContentType 'application/json' -Body $body -TimeoutSec 10;$state[$key]=(Get-Date).ToString('o')}catch{}}};$state|ConvertTo-Json|Set-Content -LiteralPath $stateFile -Encoding utf8}

