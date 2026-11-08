param(
    [string] $InstallDir = 'C:\AppServer',
    [switch] $Repair,
    [switch] $RepairCloudflare,
    [switch] $RepairWeb,
    [Alias('RepairApiCore')][switch] $RepairCoreApi,
    [string] $Update,
    [switch] $Uninstall,
    [switch] $PurgeData
)
$ErrorActionPreference = 'Stop'
$Source = $PSScriptRoot
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
if($InstallDir.StartsWith('\\') -or $InstallDir -match '(?i)\\OneDrive(\\|$)'){throw 'InstallDir harus berada di disk lokal dan di luar folder sinkronisasi.'}
$DataDir = Join-Path $InstallDir 'data'
$EnvFile = Join-Path $InstallDir '.env'
$BinDir = Join-Path $InstallDir 'bin'
$SecretsDir = Join-Path $InstallDir 'secrets'
$TunnelTokenFile = Join-Path $SecretsDir 'cloudflare-tunnel.token'
$Nssm = Join-Path $BinDir 'nssm.exe'
$InstallLog = Join-Path (Join-Path $InstallDir 'logs') ("install-" + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.log')
function Write-InstallLog([string]$Message) { Add-Content -LiteralPath $InstallLog -Value ("{0} {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'),$Message) -Encoding utf8 }

function Assert-Admin {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  if (-not ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Jalankan install.ps1 dari PowerShell Administrator.' }
}
function Stop-AlgentraServices {
  $previousErrorActionPreference=$ErrorActionPreference
  try{$ErrorActionPreference='Continue';foreach ($name in @('AlgentraTunnel','AlgentraWeb','AlgentraCoreApi')) { if (Get-Service $name -ErrorAction SilentlyContinue) { & $Nssm stop $name 2>&1 | Out-Null } }}finally{$ErrorActionPreference=$previousErrorActionPreference}
}
function Get-ServiceFailureDetails([string]$Name) {
  $parts=[Collections.Generic.List[string]]::new()
  $service=Get-Service $Name -ErrorAction SilentlyContinue
  if($service){$parts.Add("status=$($service.Status)")}
  $logDirectory=Join-Path $InstallDir "logs\$(@{AlgentraCoreApi='core-api';AlgentraWeb='web';AlgentraTunnel='tunnel'}[$Name])"
  $logNames=@("$Name.err.log","$Name.out.log")
  if($Name -eq 'AlgentraTunnel'){$logNames+='cloudflared.log'}
  foreach($logName in $logNames){
    $log=Join-Path $logDirectory $logName
    if(Test-Path -LiteralPath $log){$tail=Get-Content -LiteralPath $log -Tail 30 -ErrorAction SilentlyContinue;if($tail){$parts.Add("${logName}:`n$($tail -join "`n")")}}
  }
  if($Name -eq 'AlgentraTunnel' -and (Test-Path -LiteralPath $Nssm)){
    $previousErrorActionPreference=$ErrorActionPreference
    try{$ErrorActionPreference='Continue';foreach($setting in @('Application','AppParameters','AppDirectory','ObjectName')){$value=@(& $Nssm get $Name $setting 2>&1);if($LASTEXITCODE -eq 0){$parts.Add("NSSM $setting=$($value -join ' ') ")}}}finally{$ErrorActionPreference=$previousErrorActionPreference}
  }
  return ($parts -join "`n")
}
function Test-PublicDomain([string]$Domain,[int]$Attempts=6,[int]$DelaySeconds=5) {
  $lastStatus='tidak ada respons'
  for($attempt=1;$attempt -le $Attempts;$attempt++){
    try{
      $response=Invoke-WebRequest "https://$Domain/" -UseBasicParsing -TimeoutSec 8
      if($response.StatusCode -eq 200){return [pscustomobject]@{Success=$true;Status='HTTP 200'}}
      $lastStatus="HTTP $($response.StatusCode)"
    }catch{
      $statusCode=$null
      if($_.Exception.Response -and $_.Exception.Response.StatusCode){$statusCode=[int]$_.Exception.Response.StatusCode}
      if($statusCode){$lastStatus="HTTP $statusCode"}else{$lastStatus='tidak ada respons'}
    }
    if($attempt -lt $Attempts){Start-Sleep -Seconds $DelaySeconds}
  }
  return [pscustomobject]@{Success=$false;Status=$lastStatus}
}
function Assert-CloudflareTunnelToken([string]$Value) {
  $encoded=$Value.Trim()
  if($encoded -match '^(?i:cfut_|cfat_|cfk_)'){throw 'Token yang dimasukkan adalah kredensial API Cloudflare, bukan token connector Tunnel. Untuk -RepairCloudflare, gunakan token tunnel berawalan eyJ dari perintah Add a replica di Cloudflare Zero Trust.'}
  $parts=$encoded.Split('.')
  if($parts.Count -gt 1){$encoded=$parts[1]}
  $encoded=$encoded.Replace('-','+').Replace('_','/')
  switch($encoded.Length % 4){2{$encoded+='=='}3{$encoded+='='}1{throw 'Format token Cloudflare Tunnel tidak valid.'}}
  try{$claims=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))|ConvertFrom-Json -ErrorAction Stop}catch{throw 'Token yang dimasukkan bukan token connector Cloudflare Tunnel yang valid.'}
  if(-not $claims.a -or -not $claims.t -or -not $claims.s){throw 'Token yang dimasukkan bukan token connector Cloudflare Tunnel. Gunakan token tunnel dari Cloudflare Zero Trust.'}
}
function Start-AlgentraServices([string[]]$Names=@('AlgentraCoreApi','AlgentraWeb','AlgentraTunnel')) {
  foreach($name in $Names){
    $service=Get-Service $name -ErrorAction SilentlyContinue
    if(-not $service){throw "Windows service $name tidak terdaftar."}
    if($service.Status -ne 'Running'){
      $previousErrorActionPreference=$ErrorActionPreference
      try{$ErrorActionPreference='Continue';$startOutput=@(& $Nssm start $name 2>&1);$startExit=$LASTEXITCODE}finally{$ErrorActionPreference=$previousErrorActionPreference}
      $deadline=(Get-Date).AddSeconds(20)
      do{
        Start-Sleep -Milliseconds 500
        $service=Get-Service $name -ErrorAction SilentlyContinue
        if($service -and $service.Status -eq 'Running'){break}
      }while((Get-Date) -lt $deadline)
      # NSSM may return exit 1 when Start is issued during START_PENDING even
      # though the service finishes starting successfully. The final SCM state
      # is authoritative after the wait above.
      if(-not $service -or $service.Status -ne 'Running'){
        $details=Get-ServiceFailureDetails $name
        throw "Gagal menjalankan $name (NSSM exit $startExit). $($startOutput -join ' ')`n$details"
      }
    }
  }
}
function Enable-ServiceSid([string]$Name) {
  & "$env:windir\System32\sc.exe" sidtype $Name unrestricted | Out-Null
  if($LASTEXITCODE -ne 0){throw "Windows gagal mengaktifkan service SID untuk $Name."}
}
function Get-ServiceSid([string]$Name) {
  Enable-ServiceSid $Name
  $sidOutput=& "$env:windir\System32\sc.exe" showsid $Name 2>&1
  if($LASTEXITCODE -ne 0){throw "Windows gagal membaca service SID untuk ${Name}: $($sidOutput -join ' ')"}
  $sidText=$sidOutput -join ' '
  if($sidText -notmatch '(?i)\bS-1-5-80(?:-\d+){5}\b'){throw "Windows mengembalikan service SID yang tidak valid untuk ${Name}: $sidText"}
  return $Matches[0]
}
function Set-EnvAcl {
  $grant=@('*S-1-5-18:(F)','*S-1-5-32-544:(F)')
  if(Get-Service AlgentraCoreApi -ErrorAction SilentlyContinue){$serviceSid=Get-ServiceSid 'AlgentraCoreApi';$grant+=('*'+$serviceSid+':(R)')}
  & icacls $EnvFile /inheritance:r /grant:r $grant | Out-Null
  if($LASTEXITCODE -ne 0){throw 'Gagal mengatur ACL file .env.'}
}
function Set-TunnelTokenAcl {
  $serviceSid=Get-ServiceSid 'AlgentraTunnel'
  & icacls $TunnelTokenFile /inheritance:r /grant:r '*S-1-5-18:(F)' '*S-1-5-32-544:(F)' ('*'+$serviceSid+':(R)') '*S-1-5-19:(R)' | Out-Null
  if($LASTEXITCODE -ne 0){throw 'Gagal mengatur ACL token Cloudflare Tunnel.'}
  $cloudflared=Join-Path $BinDir 'cloudflared.exe'
  $aclTargets=@(
    @{Path=$InstallDir;Grants=@('*S-1-5-19:(X)',('*'+$serviceSid+':(X)'));Recursive=$false},
    @{Path=$BinDir;Grants=@('*S-1-5-19:(RX)',('*'+$serviceSid+':(RX)'));Recursive=$false},
    @{Path=$cloudflared;Grants=@('*S-1-5-19:(RX)',('*'+$serviceSid+':(RX)'));Recursive=$false},
    @{Path=$SecretsDir;Grants=@('*S-1-5-19:(RX)',('*'+$serviceSid+':(RX)'));Recursive=$false},
    @{Path=(Join-Path $InstallDir 'logs\tunnel');Grants=@('*S-1-5-19:(OI)(CI)M',('*'+$serviceSid+':(OI)(CI)M'));Recursive=$true}
  )
  foreach($target in $aclTargets){
    $arguments=@($target.Path,'/grant')+$target.Grants
    if($target.Recursive){$arguments+=@('/T','/C')}
    & icacls @arguments | Out-Null
    if($LASTEXITCODE -ne 0){throw "Gagal memberi akses service AlgentraTunnel pada $($target.Path)."}
  }
}
function Assert-CloudflaredSupportsTokenFile {
  $cloudflared=Join-Path $BinDir 'cloudflared.exe'
  $versionText=(& $cloudflared version 2>&1 | Out-String)
  if($versionText -notmatch '(\d{4}\.\d+\.\d+)'){throw 'Versi cloudflared tidak dapat dibaca; token-file memerlukan versi 2025.4.0 atau lebih baru.'}
  if([version]$Matches[1] -lt [version]'2025.4.0'){throw 'Perbarui cloudflared ke versi 2025.4.0 atau lebih baru untuk menyimpan token tunnel secara aman.'}
}
function Read-Env {
  $values = @{}
  if (Test-Path -LiteralPath $EnvFile) { foreach ($line in Get-Content -LiteralPath $EnvFile) { if ($line -match '^\s*([A-Z0-9_]+)=(.*)$') { $values[$Matches[1]] = $Matches[2] } } }
  return $values
}
function Existing-OrNew($key) { $v=Read-Env; if($v[$key]){return $v[$key]}; return New-Secret }
function New-Secret { $bytes = [byte[]]::new(32); $rng=[Security.Cryptography.RandomNumberGenerator]::Create(); try {$rng.GetBytes($bytes)} finally {$rng.Dispose()}; return [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant() }
function Ensure-NssmBinary {
  if(Test-Path -LiteralPath $Nssm -PathType Leaf){return}
  New-Item -ItemType Directory -Path $BinDir -Force|Out-Null
  $packaged=Join-Path $Source 'nssm.exe'
  if(Test-Path -LiteralPath $packaged -PathType Leaf){Copy-Item -LiteralPath $packaged -Destination $Nssm -Force;return}
  [Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  $archivePath=Join-Path $env:TEMP ("nssm-"+[guid]::NewGuid().ToString('N')+'.zip')
  $extractPath=Join-Path $env:TEMP ("nssm-"+[guid]::NewGuid().ToString('N')+'.exe')
  try {
    Invoke-WebRequest -Uri 'https://nssm.cc/ci/nssm-2.24-101-g897c7ad.zip' -OutFile $archivePath -UseBasicParsing -TimeoutSec 180
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive=[IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
      $entry=$archive.GetEntry('nssm-2.24-101-g897c7ad/win64/nssm.exe')
      if(-not $entry){throw 'Arsip resmi NSSM tidak berisi binary win64.'}
      $inputStream=$entry.Open();$outputStream=[IO.File]::Create($extractPath)
      try{$inputStream.CopyTo($outputStream)}finally{$outputStream.Dispose();$inputStream.Dispose()}
    } finally {$archive.Dispose()}
    $actual=(Get-FileHash -LiteralPath $extractPath -Algorithm SHA256).Hash
    $expected='EEE9C44C29C2BE011F1F1E43BB8C3FCA888CB81053022EC5A0060035DE16D848'
    if($actual -ne $expected){throw 'Checksum NSSM tidak cocok; file hasil unduh ditolak.'}
    Move-Item -LiteralPath $extractPath -Destination $Nssm -Force
    Write-Host 'NSSM 64-bit diunduh dan checksum terverifikasi.'
  } catch {
    Remove-Item -LiteralPath $Nssm,$extractPath -Force -ErrorAction SilentlyContinue
    throw "NSSM tidak tersedia dan unduhan otomatis gagal: $_"
  } finally {Remove-Item -LiteralPath $archivePath -Force -ErrorAction SilentlyContinue}
}
function Ensure-CloudflaredBinary {
  $cloudflared=Join-Path $BinDir 'cloudflared.exe'
  if(Test-Path -LiteralPath $cloudflared -PathType Leaf){return}
  New-Item -ItemType Directory -Path $BinDir -Force|Out-Null
  $packaged=Join-Path $Source 'cloudflared.exe'
  if(Test-Path -LiteralPath $packaged -PathType Leaf){Copy-Item -LiteralPath $packaged -Destination $cloudflared -Force;return}
  [Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  $downloadPath=Join-Path $env:TEMP ("cloudflared-"+[guid]::NewGuid().ToString('N')+'.exe')
  try {
    $release=Invoke-RestMethod -Uri 'https://api.github.com/repos/cloudflare/cloudflared/releases/latest' -Headers @{ 'User-Agent'='AlgentraInstaller'; Accept='application/vnd.github+json' } -TimeoutSec 60
    $asset=$release.assets|Where-Object{$_.name -eq 'cloudflared-windows-amd64.exe'}|Select-Object -First 1
    if(-not $asset -or $asset.digest -notmatch '^sha256:([0-9a-fA-F]{64})$'){throw 'Release resmi Cloudflare tidak menyediakan binary Windows x64 dan SHA-256.'}
    $expected=$Matches[1].ToUpperInvariant()
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $downloadPath -UseBasicParsing -TimeoutSec 300
    $actual=(Get-FileHash -LiteralPath $downloadPath -Algorithm SHA256).Hash
    if($actual -ne $expected){throw 'Checksum cloudflared tidak cocok; file hasil unduh ditolak.'}
    Move-Item -LiteralPath $downloadPath -Destination $cloudflared -Force
    Write-Host "cloudflared $($release.tag_name) diunduh dan checksum terverifikasi."
  } catch {
    Remove-Item -LiteralPath $cloudflared,$downloadPath -Force -ErrorAction SilentlyContinue
    throw "cloudflared.exe tidak tersedia dan unduhan otomatis gagal: $_"
  } finally {Remove-Item -LiteralPath $downloadPath -Force -ErrorAction SilentlyContinue}
}
function Ensure-CopierBinaries([switch]$Rebuild) {
  $targets = @(
    @{ Name = 'copier-worker.exe'; Aliases = @('copier-worker.exe','algentra-copier-worker.exe'); Package = './cmd/copier-worker' },
    @{ Name = 'copier-master.exe'; Aliases = @('copier-master.exe','algentra-copier-master.exe'); Package = './cmd/copier-master' },
    @{ Name = 'copier-launcher.exe'; Aliases = @('copier-launcher.exe','algentra-copier-launcher.exe'); Package = './cmd/copier-launcher'; Optional = $true }
  )
  foreach($target in $targets){
    $destination=Join-Path $BinDir $target.Name
    if(-not $Rebuild -and (Test-Path -LiteralPath $destination -PathType Leaf)){continue}
    $packaged=$null
    if(-not $Rebuild){foreach($alias in $target.Aliases){$candidate=Join-Path $Source $alias;if(Test-Path -LiteralPath $candidate -PathType Leaf){$packaged=$candidate;break}}}
    if($packaged){Copy-Item -LiteralPath $packaged -Destination $destination -Force;continue}
    if(-not(Test-Path -LiteralPath (Join-Path $Source 'go.mod') -PathType Leaf) -or -not(Test-Path -LiteralPath (Join-Path $Source ($target.Package -replace '^\./','')) -PathType Container)){
      if($target.Optional){continue}
      throw "Source Go untuk $($target.Name) tidak ditemukan pada paket installer."
    }
    $go=Get-Command go.exe -ErrorAction SilentlyContinue
    if(-not $go){
      if($target.Optional){continue}
      throw "$($target.Name) tidak tersedia. Installer dapat mengompilasinya otomatis, tetapi Go untuk Windows x64 belum terpasang. Tambahkan Go ke PATH atau gunakan paket rilis yang menyertakan binary copier."
    }
    $oldGoos=$env:GOOS;$oldGoarch=$env:GOARCH;$oldCgo=$env:CGO_ENABLED
    try {
      $env:GOOS='windows';$env:GOARCH='amd64';$env:CGO_ENABLED='0'
      Push-Location $Source
      try { & $go.Source build -trimpath -ldflags='-s -w' -o $destination $target.Package }
      finally { Pop-Location }
      if($LASTEXITCODE -ne 0 -or -not(Test-Path -LiteralPath $destination -PathType Leaf)){
        Remove-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue
        if($target.Optional){Write-Warning "Build $($target.Name) gagal; launcher opsional dilewati.";continue}
        throw "Build $($target.Name) gagal. Periksa koneksi unduh modul Go dan output compiler di atas."
      }
      Write-Host "$($target.Name) dibuat otomatis dari source Go."
    } finally { $env:GOOS=$oldGoos;$env:GOARCH=$oldGoarch;$env:CGO_ENABLED=$oldCgo }
  }
}
function Ensure-GoBinary([string]$Name,[string]$Package,[switch]$Rebuild) {
  $destination=Join-Path $BinDir $Name
  if(-not $Rebuild -and (Test-Path -LiteralPath $destination -PathType Leaf)){return}
  $packaged=Join-Path $Source $Name
  if(-not $Rebuild -and (Test-Path -LiteralPath $packaged -PathType Leaf)){Copy-Item -LiteralPath $packaged -Destination $destination -Force;return}
  $sourceDirectory=Join-Path $Source ($Package -replace '^\./','')
  if(-not(Test-Path -LiteralPath (Join-Path $Source 'go.mod') -PathType Leaf) -or -not(Test-Path -LiteralPath $sourceDirectory -PathType Container)){
    throw "Binary $Name dan source Go $Package tidak ditemukan dalam paket installer."
  }
  $go=Get-Command go.exe -ErrorAction SilentlyContinue
  if(-not $go){throw "$Name tidak tersedia. Installer dapat membangunnya otomatis, tetapi Go belum terpasang atau tidak tersedia di PATH."}
  $oldGoos=$env:GOOS;$oldGoarch=$env:GOARCH;$oldCgo=$env:CGO_ENABLED
  try {
    $env:GOOS='windows';$env:GOARCH='amd64';$env:CGO_ENABLED='0'
    Push-Location $Source
    try { & $go.Source build -trimpath -ldflags='-s -w' -o $destination $Package }
    finally { Pop-Location }
    if($LASTEXITCODE -ne 0 -or -not(Test-Path -LiteralPath $destination -PathType Leaf)){
      Remove-Item -LiteralPath $destination -Force -ErrorAction SilentlyContinue
      throw "Build $Name gagal. Periksa koneksi unduh modul Go dan output compiler di atas."
    }
    Write-Host "$Name dibuat otomatis dari source Go."
  } finally { $env:GOOS=$oldGoos;$env:GOARCH=$oldGoarch;$env:CGO_ENABLED=$oldCgo }
}
function Build-WebPackage([string]$Destination=(Join-Path $InstallDir 'web')) {
  $webSource=Join-Path $Source 'apps\web'
  if(-not(Test-Path -LiteralPath (Join-Path $webSource 'package.json') -PathType Leaf) -or -not(Test-Path -LiteralPath (Join-Path $webSource 'package-lock.json') -PathType Leaf)){throw 'Source web Next.js (apps\web beserta package.json dan package-lock.json) tidak ditemukan.'}
  $webDir=[IO.Path]::GetFullPath($Destination)
  New-Item -ItemType Directory -Path $webDir -Force|Out-Null
  Get-ChildItem -LiteralPath $webSource -Force | Copy-Item -Destination $webDir -Recurse -Force
  $npm=Get-Command npm.cmd -ErrorAction SilentlyContinue
  if(-not $npm){$npm=Get-Command npm.exe -ErrorAction SilentlyContinue}
  if(-not $npm){throw 'npm tidak ditemukan. Pastikan instalasi Node.js menyertakan npm dan folder Node.js tersedia di PATH, lalu jalankan installer kembali.'}
  Push-Location $webDir
  try {
    & $npm.Source ci --no-audit --no-fund
    if($LASTEXITCODE -ne 0){throw "npm ci gagal dengan exit code $LASTEXITCODE. Periksa koneksi registry npm dan log di atas."}
    & $npm.Source run build
    if($LASTEXITCODE -ne 0){throw "Build Next.js gagal dengan exit code $LASTEXITCODE. Periksa output build di atas."}
  } finally { Pop-Location }
}
function Write-EnvTemplate {
  $v = Read-Env
  $auto = [ordered]@{
    APP_SECRET_KEY=(Existing-OrNew 'APP_SECRET_KEY'); EA_API_KEY=(Existing-OrNew 'EA_API_KEY'); COPIER_WORKER_API_KEY=(Existing-OrNew 'COPIER_WORKER_API_KEY'); GEMINI_MODEL='gemini-3.5-flash-lite'; MANAGEMENT_FEE_IDR='150000'; CONTACT_PERSON_NAME='Algentra Capital'; CONTACT_WHATSAPP='0852000000'; CONTACT_EMAIL='algentracapital@gmail.com'; CONTACT_INSTAGRAM='https://www.instagram.com/algentracapital.id/'; CONTACT_TELEGRAM='https://t.me/algentra_capital'; CONTACT_FACEBOOK='https://www.facebook.com/algentracapital.id/'; CONTACT_LINKEDIN='https://www.linkedin.com/algentracapital.id/'; CONTACT_X='https://www.instagram.com/algentracapital.id/'; CONTACT_YOUTUBE='https://www.instagram.com/algentracapital.id/'; CONTACT_TIKTOK='https://www.tiktok.com/algentracapital.id/'; CONTACT_TELEGRAM_ADMIN='https://t.me/algentracapital'; CONTACT_WEBSITE='https://algentracapital.my.id'
  }
  $manual = @('PUBLIC_DOMAIN','CLOUDFLARE_API_TOKEN','ADMIN_USERNAME','ADMIN_PASSWORD','GEMINI_API_KEY','TELEGRAM_API_ID','TELEGRAM_API_HASH','TELEGRAM_BOT_TOKEN','TELEGRAM_ADMIN_CHAT_ID','EMAIL_SMTP_HOST','EMAIL_SMTP_PORT','EMAIL_SMTP_USERNAME','EMAIL_SMTP_PASSWORD','EMAIL_FROM','EMAIL_SMTP_STARTTLS','PAYMENT_BANK_NAME','PAYMENT_NUMBER','PAYMENT_NAME','AIRPAY_API_KEY','AIRPAY_MERCHANT_UID')
  $lines = [Collections.Generic.List[string]]::new()
  $lines.Add('# Otomatis dibuat atau diisi dengan nilai default oleh installer.')
  foreach($key in $auto.Keys){$value=$auto[$key];if($v[$key]){$value=$v[$key]};$lines.Add("$key=$value")}
  $lines.Add('')
  $lines.Add('# Diisi manual. API token Cloudflare hanya dipakai sekali untuk membuat tunnel/route/DNS lalu dihapus installer.')
  foreach($key in $manual){if($key -eq 'ADMIN_USERNAME' -and $v[$key]){$value=$v[$key]}elseif($key -eq 'ADMIN_USERNAME'){$value='admin'}elseif($v[$key]){$value=$v[$key]}else{$value=''};$lines.Add("$key=$value")}
  Set-Content -LiteralPath $EnvFile -Value $lines -Encoding utf8
}
function Validate-Env {
  while($true){
    Set-EnvAcl
    Start-Process notepad.exe -ArgumentList "`"$EnvFile`"" -Wait
    $v=Read-Env; $errors=[Collections.Generic.List[string]]::new()
    foreach($key in @('PUBLIC_DOMAIN','CLOUDFLARE_API_TOKEN','ADMIN_USERNAME','ADMIN_PASSWORD')){if(-not $v[$key]){$errors.Add("$key wajib diisi")}}
    if($v.PUBLIC_DOMAIN -and [Uri]::CheckHostName($v.PUBLIC_DOMAIN) -eq [UriHostNameType]::Unknown){$errors.Add('PUBLIC_DOMAIN harus berupa hostname valid tanpa skema atau path')}
    if($v.ADMIN_PASSWORD.Length -lt 12){$errors.Add('ADMIN_PASSWORD minimal 12 karakter')}
    foreach($pair in @(@('TELEGRAM_API_ID','TELEGRAM_API_HASH'),@('TELEGRAM_BOT_TOKEN','TELEGRAM_ADMIN_CHAT_ID'))){if([bool]$v[$pair[0]] -ne [bool]$v[$pair[1]]){$errors.Add("$($pair -join ' dan ') harus diisi bersama")}}
    if([bool]$v.AIRPAY_API_KEY -ne [bool]$v.AIRPAY_MERCHANT_UID){$errors.Add('AIRPAY_API_KEY dan AIRPAY_MERCHANT_UID harus diisi bersama')}
    $smtp=@('EMAIL_SMTP_HOST','EMAIL_SMTP_PORT','EMAIL_SMTP_USERNAME','EMAIL_SMTP_PASSWORD','EMAIL_FROM');$filled=@($smtp|Where-Object{$v[$_]});if($filled.Count -ne 0 -and $filled.Count -ne $smtp.Count){$errors.Add('Kelompok SMTP harus diisi lengkap')}
    foreach($key in @('CONTACT_INSTAGRAM','CONTACT_FACEBOOK','CONTACT_X','CONTACT_TIKTOK','CONTACT_YOUTUBE')){if($v[$key] -and -not $v[$key].StartsWith('https://')){$errors.Add("$key harus memakai HTTPS")}}
    if($errors.Count -eq 0){return};Write-Host ($errors -join "`n") -ForegroundColor Red
  }
}
function Install-Service($name,$app,$appArgs,$workingDir,$logDir) {
  if(Get-Service $name -ErrorAction SilentlyContinue){$previousErrorActionPreference=$ErrorActionPreference;try{$ErrorActionPreference='Continue';& $Nssm stop $name 2>&1 | Out-Null;& $Nssm remove $name confirm 2>&1 | Out-Null}finally{$ErrorActionPreference=$previousErrorActionPreference}}
  & $Nssm install $name $app $appArgs | Out-Null
  Enable-ServiceSid $name
  & $Nssm set $name AppDirectory $workingDir | Out-Null
  & $Nssm set $name Start SERVICE_AUTO_START | Out-Null
  & $Nssm set $name AppExit Default Restart | Out-Null
  & $Nssm set $name AppRestartDelay 5000 | Out-Null
  & $Nssm set $name AppStdout (Join-Path $logDir "$name.out.log") | Out-Null
  & $Nssm set $name AppStderr (Join-Path $logDir "$name.err.log") | Out-Null
  & $Nssm set $name AppRotateFiles 1 | Out-Null;& $Nssm set $name AppRotateBytes 10485760 | Out-Null
  & $Nssm set $name ObjectName 'NT AUTHORITY\LocalService' '' | Out-Null
  if($LASTEXITCODE -ne 0){throw "NSSM gagal mengatur akun LocalService untuk $name."}
  $serviceSid=Get-ServiceSid $name
  & icacls $InstallDir /grant ('*'+$serviceSid+':(OI)(CI)M') /T /C | Out-Null
  if($LASTEXITCODE -ne 0){throw "Gagal memberi izin folder instalasi kepada service $name."}
}
function Register-AlgentraTasks {
  $v=Read-Env
  if($v.TELEGRAM_BOT_TOKEN -and $v.TELEGRAM_ADMIN_CHAT_ID){
    $backupAction=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$(Join-Path $InstallDir 'infra\windows\run-backup.ps1')`" -ProjectRoot `"$InstallDir`" -EnvironmentFile `"$EnvFile`""
    $backupTrigger=New-ScheduledTaskTrigger -Daily -At 2:00am
    Register-ScheduledTask -TaskName 'Algentra SQLite Backup' -Action $backupAction -Trigger $backupTrigger -User 'SYSTEM' -RunLevel Highest -Description 'Snapshot SQLite terenkripsi dan unggah ke Telegram.' -Force|Out-Null
    $watchAction=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$(Join-Path $InstallDir 'infra\windows\watchdog.ps1')`" -ProjectRoot `"$InstallDir`" -EnvironmentFile `"$EnvFile`""
    $watchTrigger=New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 5) -RepetitionDuration (New-TimeSpan -Days 3650)
    Register-ScheduledTask -TaskName 'Algentra Watchdog' -Action $watchAction -Trigger $watchTrigger -User 'SYSTEM' -RunLevel Highest -Description 'Memeriksa service, health, domain, disk, dan backup.' -Force|Out-Null
  }
}
function Install-ManagedWorkerTask {
  $workerSetup=Join-Path $InstallDir 'mt5\copier-worker\scripts\install_managed_worker_task.ps1'
  if(-not(Test-Path -LiteralPath $workerSetup -PathType Leaf)){throw "Setup worker tidak ditemukan: $workerSetup"}
  & $workerSetup -ProjectRoot $InstallDir -ApiBaseUrl 'http://127.0.0.1:8000'
  Write-InstallLog 'Scheduled Task managed copier MT5 dibuat dan dimulai.'
}

Assert-Admin
if($Uninstall){if(Test-Path $Nssm){Stop-AlgentraServices;foreach($n in @('AlgentraCoreApi','AlgentraWeb','AlgentraTunnel')){if(Get-Service $n -ErrorAction SilentlyContinue){& $Nssm remove $n confirm|Out-Null}}};Get-ScheduledTask -ErrorAction SilentlyContinue|Where-Object{$_.TaskName -like 'Algentra*'}|Unregister-ScheduledTask -Confirm:$false -ErrorAction SilentlyContinue;if($PurgeData){$confirm=Read-Host "Ketik PURGE untuk menghapus data dan secret dari $InstallDir";if($confirm -eq 'PURGE'){Remove-Item -LiteralPath $DataDir -Recurse -Force -ErrorAction SilentlyContinue;Remove-Item -LiteralPath $EnvFile -Force -ErrorAction SilentlyContinue;Remove-Item -LiteralPath $TunnelTokenFile -Force -ErrorAction SilentlyContinue}};exit 0}
if($RepairCloudflare -or $RepairWeb -or $RepairCoreApi){
  if($Repair){throw 'Gunakan -Repair penuh atau switch repair terpisah dalam satu pemanggilan, jangan keduanya.'}
  if(-not(Test-Path -LiteralPath $EnvFile -PathType Leaf)){throw ".env tidak ditemukan: $EnvFile"}
  Ensure-NssmBinary
  $v=Read-Env
  $selected=@()
  foreach($log in @('core-api','web','tunnel')){New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir "logs\$log")|Out-Null}
  if($RepairCoreApi){
    $workerPackageSource=Join-Path $Source 'apps\copier-worker'
    if(-not(Test-Path -LiteralPath $workerPackageSource -PathType Container)){throw "Source copier worker tidak ditemukan: $workerPackageSource"}
    $apiScriptSource=Join-Path $Source 'infra\windows\start-core-api.ps1'
    if(-not(Test-Path -LiteralPath $apiScriptSource -PathType Leaf)){throw "Source startup Core API tidak ditemukan: $apiScriptSource"}
    if(-not(Get-Command go.exe -ErrorAction SilentlyContinue)){throw 'Go belum tersedia di PATH; -RepairCoreApi memerlukan Go untuk membangun Core API dan copier worker terbaru.'}
    foreach($requiredSource in @('go.mod','cmd\algentra-backend','cmd\copier-worker')){if(-not(Test-Path -LiteralPath (Join-Path $Source $requiredSource))){throw "Source untuk build terbaru tidak ditemukan: $(Join-Path $Source $requiredSource)"}}
    Get-ScheduledTask -ErrorAction SilentlyContinue|Where-Object{$_.TaskName -match 'Algentra.*Copier'}|ForEach-Object{Stop-ScheduledTask -TaskName $_.TaskName -ErrorAction SilentlyContinue}
    Get-Process -ErrorAction SilentlyContinue|Where-Object{$_.Path -and $_.Path.StartsWith($BinDir,[StringComparison]::OrdinalIgnoreCase) -and $_.Name -like 'copier-*'}|Stop-Process -Force -ErrorAction SilentlyContinue
    if(Get-Service AlgentraCoreApi -ErrorAction SilentlyContinue){
      $previousErrorActionPreference=$ErrorActionPreference
      try{$ErrorActionPreference='Continue';& $Nssm stop AlgentraCoreApi 2>&1|Out-Null}finally{$ErrorActionPreference=$previousErrorActionPreference}
      $stopDeadline=(Get-Date).AddSeconds(30)
      do{Start-Sleep -Milliseconds 500;$coreService=Get-Service AlgentraCoreApi -ErrorAction SilentlyContinue}while($coreService -and $coreService.Status -ne 'Stopped' -and (Get-Date) -lt $stopDeadline)
      if($coreService -and $coreService.Status -ne 'Stopped'){throw 'Core API belum berhenti; binary tidak akan ditimpa saat service masih berjalan.'}
    }
    Ensure-GoBinary 'algentra-backend.exe' './cmd/algentra-backend' -Rebuild
    Ensure-CopierBinaries -Rebuild
    $workerPackageInstall=Join-Path $InstallDir 'mt5\copier-worker'
    New-Item -ItemType Directory -Path $workerPackageInstall -Force|Out-Null
    Get-ChildItem -LiteralPath $workerPackageSource -Force|Copy-Item -Destination $workerPackageInstall -Recurse -Force
    $apiScript=Join-Path $InstallDir 'infra\windows\start-core-api.ps1'
    New-Item -ItemType Directory -Path (Split-Path -Parent $apiScript) -Force|Out-Null
    Copy-Item -LiteralPath $apiScriptSource -Destination $apiScript -Force
    Install-Service 'AlgentraCoreApi' 'powershell.exe' "-NoProfile -ExecutionPolicy Bypass -File `"$apiScript`" -ProjectRoot `"$InstallDir`" -EnvironmentFile `"$EnvFile`"" $InstallDir (Join-Path $InstallDir 'logs\core-api')
    Set-EnvAcl
    $selected+='AlgentraCoreApi'
  }
  if($RepairWeb){
    $webDir=Join-Path $InstallDir 'web'
    $webStage=Join-Path $InstallDir ('.web-repair-'+[guid]::NewGuid().ToString('N'))
    $webPrevious=Join-Path $InstallDir ('.web-previous-'+[guid]::NewGuid().ToString('N'))
    $installRoot=[IO.Path]::GetFullPath($InstallDir).TrimEnd('\')+'\'
    try{Build-WebPackage -Destination $webStage}catch{
      $stageFull=[IO.Path]::GetFullPath($webStage)
      if((Test-Path -LiteralPath $webStage) -and $stageFull.StartsWith($installRoot,[StringComparison]::OrdinalIgnoreCase)){Remove-Item -LiteralPath $webStage -Recurse -Force}
      throw
    }
    $next=Join-Path $webStage 'node_modules\next\dist\bin\next'
    $node=Join-Path $webStage 'node.exe'
    if(-not(Test-Path -LiteralPath $node)){$node=(Get-Command node.exe -ErrorAction SilentlyContinue).Source}
    if(-not $node -or -not(Test-Path -LiteralPath $next)){
      $stageFull=[IO.Path]::GetFullPath($webStage)
      if((Test-Path -LiteralPath $webStage) -and $stageFull.StartsWith($installRoot,[StringComparison]::OrdinalIgnoreCase)){Remove-Item -LiteralPath $webStage -Recurse -Force}
      throw 'Web build atau Node.js tidak ditemukan; build lama tetap dipakai.'
    }
    $service=Get-Service AlgentraWeb -ErrorAction SilentlyContinue
    if($service){
      $previousErrorActionPreference=$ErrorActionPreference
      try{$ErrorActionPreference='Continue';& $Nssm stop AlgentraWeb 2>&1|Out-Null}finally{$ErrorActionPreference=$previousErrorActionPreference}
      $stopDeadline=(Get-Date).AddSeconds(30)
      do{Start-Sleep -Milliseconds 500;$service=Get-Service AlgentraWeb -ErrorAction SilentlyContinue}while($service -and $service.Status -ne 'Stopped' -and (Get-Date) -lt $stopDeadline)
      if($service -and $service.Status -ne 'Stopped'){
        $stageFull=[IO.Path]::GetFullPath($webStage)
        if((Test-Path -LiteralPath $webStage) -and $stageFull.StartsWith($installRoot,[StringComparison]::OrdinalIgnoreCase)){Remove-Item -LiteralPath $webStage -Recurse -Force}
        throw 'AlgentraWeb belum berhenti; build lama dibiarkan utuh.'
      }
    }
    $hadPrevious=Test-Path -LiteralPath $webDir -PathType Container
    if($hadPrevious){Move-Item -LiteralPath $webDir -Destination $webPrevious}
    try{
      Move-Item -LiteralPath $webStage -Destination $webDir
      $next=Join-Path $webDir 'node_modules\next\dist\bin\next'
      $node=Join-Path $webDir 'node.exe'
      if(-not(Test-Path -LiteralPath $node)){$node=(Get-Command node.exe -ErrorAction SilentlyContinue).Source}
      Install-Service 'AlgentraWeb' $node "`"$next`" start -p 3000" $webDir (Join-Path $InstallDir 'logs\web')
      Start-AlgentraServices @('AlgentraWeb')
      $null=Invoke-WebRequest 'http://127.0.0.1:3000/' -UseBasicParsing -TimeoutSec 30
    }catch{
      $repairFailure=$_
      if(Get-Service AlgentraWeb -ErrorAction SilentlyContinue){$previousErrorActionPreference=$ErrorActionPreference;try{$ErrorActionPreference='Continue';& $Nssm stop AlgentraWeb 2>&1|Out-Null;& $Nssm remove AlgentraWeb confirm 2>&1|Out-Null}finally{$ErrorActionPreference=$previousErrorActionPreference}}
      if(Test-Path -LiteralPath $webDir -PathType Container){Move-Item -LiteralPath $webDir -Destination (Join-Path $InstallDir ('.web-failed-'+[guid]::NewGuid().ToString('N')))}
      if($hadPrevious -and (Test-Path -LiteralPath $webPrevious -PathType Container)){
        Move-Item -LiteralPath $webPrevious -Destination $webDir
        $oldNext=Join-Path $webDir 'node_modules\next\dist\bin\next'
        $oldNode=Join-Path $webDir 'node.exe'
        if(-not(Test-Path -LiteralPath $oldNode)){$oldNode=(Get-Command node.exe -ErrorAction SilentlyContinue).Source}
        if($oldNode -and (Test-Path -LiteralPath $oldNext)){Install-Service 'AlgentraWeb' $oldNode "`"$oldNext`" start -p 3000" $webDir (Join-Path $InstallDir 'logs\web');Start-AlgentraServices @('AlgentraWeb')}
      }
      $stageFull=[IO.Path]::GetFullPath($webStage)
      if((Test-Path -LiteralPath $webStage) -and $stageFull.StartsWith($installRoot,[StringComparison]::OrdinalIgnoreCase)){Remove-Item -LiteralPath $webStage -Recurse -Force}
      throw "Build web baru gagal; build sebelumnya dipulihkan jika tersedia. $repairFailure"
    }
    $previousFull=[IO.Path]::GetFullPath($webPrevious)
    if((Test-Path -LiteralPath $webPrevious -PathType Container) -and $previousFull.StartsWith($installRoot,[StringComparison]::OrdinalIgnoreCase)){Remove-Item -LiteralPath $webPrevious -Recurse -Force -ErrorAction SilentlyContinue}
    $selected+='AlgentraWeb'
  }
  if($RepairCloudflare){
    $secureApiToken=Read-Host 'Masukkan ulang API Token Cloudflare' -AsSecureString
    if($secureApiToken.Length -eq 0){throw 'API Token Cloudflare tidak boleh kosong.'}
    $tokenPointer=[Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureApiToken)
    try{
      $apiToken=[Runtime.InteropServices.Marshal]::PtrToStringBSTR($tokenPointer).Trim()
      if(-not $apiToken){throw 'API Token Cloudflare tidak boleh kosong.'}
      $cloudflareSetup=Join-Path $InstallDir 'infra\windows\configure-cloudflare-tunnel.ps1'
      if(-not(Test-Path -LiteralPath $cloudflareSetup -PathType Leaf)){throw "Setup Cloudflare tidak ditemukan: $cloudflareSetup"}
      Ensure-CloudflaredBinary
      Assert-CloudflaredSupportsTokenFile
      $cloudflareResultRaw=& $cloudflareSetup -PublicDomain $v.PUBLIC_DOMAIN -ApiToken $apiToken
      $cloudflareResult=($cloudflareResultRaw -join "`n")|ConvertFrom-Json -ErrorAction Stop
      if(-not $cloudflareResult.tunnel_token){throw 'Cloudflare API tidak mengembalikan token tunnel.'}
      Assert-CloudflareTunnelToken ([string]$cloudflareResult.tunnel_token)
      New-Item -ItemType Directory -Path $SecretsDir -Force|Out-Null
      Set-Content -LiteralPath $TunnelTokenFile -Value ([string]$cloudflareResult.tunnel_token) -NoNewline -Encoding ascii
    }finally{[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($tokenPointer);Remove-Variable apiToken -ErrorAction SilentlyContinue;$secureApiToken.Dispose()}
    $tunnelLogFile=Join-Path $InstallDir 'logs\tunnel\cloudflared.log'
    Install-Service 'AlgentraTunnel' (Join-Path $BinDir 'cloudflared.exe') "tunnel --protocol http2 --loglevel info --logfile `"$tunnelLogFile`" run --token-file `"$TunnelTokenFile`"" $BinDir (Join-Path $InstallDir 'logs\tunnel')
    Set-TunnelTokenAcl
    $selected+='AlgentraTunnel'
  }
  Start-AlgentraServices $selected
  if($RepairCoreApi){$null=Invoke-RestMethod 'http://127.0.0.1:8000/ready' -TimeoutSec 45;Install-ManagedWorkerTask;Write-Host 'Core API dan copier worker berhasil dibangun ulang dari source dan siap.'}
  if($RepairWeb){Write-Host 'Web berhasil dibangun ulang, dipasang, dan siap.'}
  if($RepairCloudflare){
    $publicCheck=Test-PublicDomain $v.PUBLIC_DOMAIN
    if($publicCheck.Success){Write-Host "Cloudflare Tunnel aktif untuk $($v.PUBLIC_DOMAIN)."}
    else{Write-Warning "Domain belum merespons setelah beberapa percobaan ($($publicCheck.Status)). Periksa status tunnel dan DNS Cloudflare."}
  }
  exit 0
}
if($Repair){
  if(-not(Test-Path $EnvFile)){throw '.env tidak ditemukan'}
  Ensure-NssmBinary;Ensure-CloudflaredBinary
  Stop-AlgentraServices
  Ensure-GoBinary 'algentra-backend.exe' './cmd/algentra-backend' -Rebuild
  Get-ScheduledTask -ErrorAction SilentlyContinue|Where-Object{$_.TaskName -match 'Algentra.*Copier'}|ForEach-Object{Stop-ScheduledTask -TaskName $_.TaskName -ErrorAction SilentlyContinue}
  Get-Process -ErrorAction SilentlyContinue|Where-Object{$_.Path -and $_.Path.StartsWith($BinDir,[StringComparison]::OrdinalIgnoreCase) -and $_.Name -like 'copier-*'}|Stop-Process -Force -ErrorAction SilentlyContinue
  Ensure-CopierBinaries -Rebuild
  Build-WebPackage
  $expertSource=Join-Path $Source 'mt5\experts';$expertInstall=Join-Path $InstallDir 'mt5\experts'
  foreach($relative in @('MT5FollowerCopyEA.mq5','Algentra\MT5LoginProbe.mq5')){
    $sourceFile=Join-Path $expertSource $relative
    if(-not(Test-Path -LiteralPath $sourceFile -PathType Leaf)){throw "Source EA MT5 tidak ditemukan: $sourceFile"}
    $destinationFile=Join-Path $expertInstall $relative
    New-Item -ItemType Directory -Path (Split-Path -Parent $destinationFile) -Force|Out-Null
    Copy-Item -LiteralPath $sourceFile -Destination $destinationFile -Force
  }
  Write-Host 'Source EA MT5 diperbarui dari repositori sebelum kompilasi template.'
  $workerScriptsSource=Join-Path $Source 'apps\copier-worker\scripts'
  if(-not(Test-Path -LiteralPath $workerScriptsSource -PathType Container)){throw 'Source apps\copier-worker\scripts diperlukan untuk memperbaiki instalasi worker.'}
  $workerScriptsInstall=Join-Path $InstallDir 'mt5\copier-worker\scripts'
  New-Item -ItemType Directory -Path $workerScriptsInstall -Force|Out-Null
  Get-ChildItem -LiteralPath $workerScriptsSource -Force|Copy-Item -Destination $workerScriptsInstall -Recurse -Force
  $v=Read-Env;$webDir=Join-Path $InstallDir 'web';$next=Join-Path $webDir 'node_modules\next\dist\bin\next';$node=Join-Path $webDir 'node.exe';if(-not(Test-Path $node)){$node=(Get-Command node.exe -ErrorAction SilentlyContinue).Source}
  foreach($log in @('core-api','web','tunnel')){New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir "logs\$log")|Out-Null}
  if(-not $node -or -not(Test-Path $next)){throw 'Web build atau Node.js tidak ditemukan untuk memperbaiki AlgentraWeb.'}
  Install-Service 'AlgentraCoreApi' 'powershell.exe' "-NoProfile -ExecutionPolicy Bypass -File `"$(Join-Path $InstallDir 'infra\windows\start-core-api.ps1')`" -ProjectRoot `"$InstallDir`" -EnvironmentFile `"$EnvFile`"" $InstallDir (Join-Path $InstallDir 'logs\core-api')
  Install-Service 'AlgentraWeb' $node "`"$next`" start -p 3000" $webDir (Join-Path $InstallDir 'logs\web')
  if(-not(Test-Path -LiteralPath $TunnelTokenFile -PathType Leaf) -and $v.CLOUDFLARE_TUNNEL_TOKEN){New-Item -ItemType Directory -Path $SecretsDir -Force|Out-Null;Set-Content -LiteralPath $TunnelTokenFile -Value $v.CLOUDFLARE_TUNNEL_TOKEN.Trim() -NoNewline -Encoding ascii;Set-TunnelTokenAcl;$envContent=@(Get-Content -LiteralPath $EnvFile|Where-Object{$_ -notmatch '^\s*CLOUDFLARE_TUNNEL_TOKEN='});Set-Content -LiteralPath $EnvFile -Value $envContent -Encoding utf8}
  if(-not(Test-Path -LiteralPath $TunnelTokenFile -PathType Leaf)){throw 'Token tunnel belum dibuat. Jalankan instalasi awal dengan CLOUDFLARE_API_TOKEN.'}
  Assert-CloudflaredSupportsTokenFile
  $tunnelLogFile=Join-Path $InstallDir 'logs\tunnel\cloudflared.log'
  Install-Service 'AlgentraTunnel' (Join-Path $BinDir 'cloudflared.exe') "tunnel --protocol http2 --loglevel info --logfile `"$tunnelLogFile`" run --token-file `"$TunnelTokenFile`"" $BinDir (Join-Path $InstallDir 'logs\tunnel')
  Set-TunnelTokenAcl
  Set-EnvAcl;Register-AlgentraTasks;Start-AlgentraServices
  $null=Invoke-RestMethod 'http://127.0.0.1:8000/ready' -TimeoutSec 45
  Install-ManagedWorkerTask
  Write-Host 'ACL, service NSSM, dan task backup/watchdog diperiksa.';exit 0
}
if($Update){
  $package=(Resolve-Path -LiteralPath $Update).Path
  $manifestFile=Join-Path $package 'release.manifest.json'
  if(-not(Test-Path $manifestFile)){throw 'Paket update memerlukan release.manifest.json dengan daftar SHA-256.'}
  $manifest=Get-Content -Raw $manifestFile|ConvertFrom-Json
  $packageRoot=[IO.Path]::GetFullPath($package).TrimEnd('\')+'\';foreach($file in $manifest.files){$candidate=[IO.Path]::GetFullPath((Join-Path $package $file.path));if(-not $candidate.StartsWith($packageRoot,[StringComparison]::OrdinalIgnoreCase)){throw 'Path pada manifest keluar dari direktori paket.'};if((Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash -ne $file.sha256){throw "Checksum tidak cocok: $($file.path)"}}
  if(-not(Test-Path $EnvFile)){throw '.env belum tersedia; jalankan instalasi awal terlebih dahulu.'}
  $previousBin=Join-Path $BinDir '_previous';$previousWeb=Join-Path $InstallDir 'web_previous';$tmpDir=Join-Path $InstallDir 'tmp'
  $backup=Join-Path $BinDir 'backup-database.exe';if(-not(Test-Path $backup)){throw 'Executable backup SQLite tidak ditemukan; update dibatalkan.'};$values=Read-Env;if(-not $values.TELEGRAM_BOT_TOKEN -or -not $values.TELEGRAM_ADMIN_CHAT_ID){throw 'Konfigurasi backup Telegram diperlukan sebelum update.'};foreach($key in $values.Keys){[Environment]::SetEnvironmentVariable($key,$values[$key],'Process')};$env:PROJECT_ROOT=$InstallDir;& $backup;if($LASTEXITCODE -ne 0){throw 'Backup sebelum update gagal.'};& $backup -kill-switch on;if($LASTEXITCODE -ne 0){throw 'Kill switch gagal aktif atau masih ada sinyal yang diproses; update dibatalkan.'}
  Copy-Item $EnvFile (Join-Path $tmpDir '.env.update-backup') -Force
  $workerTasks=Get-ScheduledTask -ErrorAction SilentlyContinue|Where-Object{$_.TaskName -match 'Algentra.*Copier'};foreach($task in $workerTasks){Stop-ScheduledTask -TaskName $task.TaskName -ErrorAction SilentlyContinue};Stop-AlgentraServices
  Remove-Item $previousBin,$previousWeb -Recurse -Force -ErrorAction SilentlyContinue;New-Item -ItemType Directory -Path $previousBin -Force|Out-Null;Copy-Item (Join-Path $BinDir 'algentra-backend.exe') $previousBin -Force;Copy-Item (Join-Path $InstallDir 'web') $previousWeb -Recurse -Force
  try {Copy-Item (Join-Path $package 'algentra-backend.exe') (Join-Path $BinDir 'algentra-backend.exe') -Force;Remove-Item (Join-Path $InstallDir 'web') -Recurse -Force;Copy-Item (Join-Path $package 'web') (Join-Path $InstallDir 'web') -Recurse -Force;& $Nssm start AlgentraCoreApi|Out-Null;Start-Sleep 2;& $Nssm start AlgentraWeb|Out-Null;Start-Sleep 2;& $Nssm start AlgentraTunnel|Out-Null;$null=Invoke-RestMethod 'http://127.0.0.1:8000/ready' -TimeoutSec 45;$null=Invoke-WebRequest 'http://127.0.0.1:3000' -UseBasicParsing -TimeoutSec 30}
  catch {Stop-AlgentraServices;Copy-Item (Join-Path $previousBin 'algentra-backend.exe') (Join-Path $BinDir 'algentra-backend.exe') -Force;Remove-Item (Join-Path $InstallDir 'web') -Recurse -Force;Copy-Item $previousWeb (Join-Path $InstallDir 'web') -Recurse -Force;& $Nssm start AlgentraCoreApi|Out-Null;& $Nssm start AlgentraWeb|Out-Null;& $Nssm start AlgentraTunnel|Out-Null;throw "Update gagal; binary dan web sebelumnya dipulihkan; database tidak diubah: $_"}
  finally {foreach($task in $workerTasks){Start-ScheduledTask -TaskName $task.TaskName -ErrorAction SilentlyContinue}}
  Write-Host "Update $($manifest.version) terpasang. Kill switch masih aktif. Uji login dan copier, lalu nonaktifkan kill switch dari dashboard admin.";exit 0
}
$drive=[IO.Path]::GetPathRoot($InstallDir);$free=(Get-PSDrive ([IO.Path]::GetPathRoot($InstallDir).TrimEnd('\').TrimEnd(':'))).Free/1GB
if($free -lt 6){throw 'Ruang disk bebas kurang dari 6 GB.'};if($free -lt 10){Write-Warning 'Ruang disk bebas di bawah rekomendasi 10 GB.'}
$timeService=Get-Service W32Time -ErrorAction SilentlyContinue
if(-not $timeService){throw 'Layanan Windows Time (W32Time) tidak tersedia di server ini.'}
Set-Service -Name W32Time -StartupType Automatic
if((Get-Service W32Time).Status -ne 'Running'){Start-Service W32Time}
Start-Sleep -Seconds 2
$syncOutput=& w32tm.exe /resync /force 2>&1
if($LASTEXITCODE -ne 0){throw "Sinkronisasi Windows/NTP gagal. Pastikan server dapat mengakses NTP keluar pada UDP 123. Detail: $($syncOutput -join ' ')"}
$clockStatus=& w32tm.exe /query /status 2>&1
if($LASTEXITCODE -ne 0){throw "Layanan waktu aktif tetapi status sinkronisasi tidak dapat dibaca: $($clockStatus -join ' ')"}
foreach($port in @(3000,8000)){if(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue){throw "Port $port sudah dipakai."}}
New-Item -ItemType Directory -Force -Path $InstallDir,$BinDir,$DataDir,$SecretsDir,(Join-Path $InstallDir 'web'),(Join-Path $InstallDir 'tmp'),(Join-Path $InstallDir 'mt5'),(Join-Path $InstallDir 'mt5\copier-worker'),(Join-Path $InstallDir 'logs\core-api'),(Join-Path $InstallDir 'logs\web'),(Join-Path $InstallDir 'logs\tunnel')|Out-Null
Write-InstallLog 'Installer memulai pemeriksaan paket dan konfigurasi.'
Ensure-GoBinary 'algentra-backend.exe' './cmd/algentra-backend'
Ensure-GoBinary 'backup-database.exe' './cmd/backup-database'
Ensure-CopierBinaries
if(-not(Test-Path (Join-Path $Source 'apps\copier-worker\scripts\install_managed_worker_task.ps1'))){throw 'Paket rilis harus menyertakan apps\copier-worker beserta scripts\install_managed_worker_task.ps1.'}
if(-not(Test-Path (Join-Path $Source 'mt5\experts\MT5FollowerCopyEA.mq5')) -or -not(Test-Path (Join-Path $Source 'mt5\experts\Algentra\MT5LoginProbe.mq5'))){throw 'Paket rilis harus menyertakan source EA follower dan MT5LoginProbe di mt5\experts.'}
Ensure-NssmBinary;Ensure-CloudflaredBinary
Assert-CloudflaredSupportsTokenFile
New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir 'infra\windows')|Out-Null
Copy-Item (Join-Path $Source 'infra\windows\*') (Join-Path $InstallDir 'infra\windows') -Recurse -Force
if(Test-Path (Join-Path $Source 'mt5')){Copy-Item (Join-Path $Source 'mt5\*') (Join-Path $InstallDir 'mt5') -Recurse -Force}
if(Test-Path (Join-Path $Source 'apps\copier-worker')){Copy-Item (Join-Path $Source 'apps\copier-worker\*') (Join-Path $InstallDir 'mt5\copier-worker') -Recurse -Force}
Build-WebPackage
if(-not(Test-Path $EnvFile)){New-Item -ItemType File -Path $EnvFile|Out-Null};Write-EnvTemplate;Validate-Env
$v=Read-Env
$cloudflareSetup=Join-Path $InstallDir 'infra\windows\configure-cloudflare-tunnel.ps1'
$existingTunnelToken=if(Test-Path -LiteralPath $TunnelTokenFile -PathType Leaf){Get-Content -LiteralPath $TunnelTokenFile -Raw}else{''}
$cloudflareResultRaw=& $cloudflareSetup -PublicDomain $v.PUBLIC_DOMAIN -ApiToken $v.CLOUDFLARE_API_TOKEN -ExistingTunnelToken $existingTunnelToken
$cloudflareResult=$cloudflareResultRaw|ConvertFrom-Json
Set-Content -LiteralPath $TunnelTokenFile -Value $cloudflareResult.tunnel_token -NoNewline -Encoding ascii
$envContent=@(Get-Content -LiteralPath $EnvFile | Where-Object { $_ -notmatch '^\s*CLOUDFLARE_API_TOKEN=' -and $_ -notmatch '^\s*CLOUDFLARE_TUNNEL_TOKEN=' })
Set-Content -LiteralPath $EnvFile -Value $envContent -Encoding utf8
$v=Read-Env
$webDir=Join-Path $InstallDir 'web';$next=Join-Path $webDir 'node_modules\next\dist\bin\next'
if(-not(Test-Path "$next")){throw 'node_modules Next.js tidak ditemukan dalam web package.'}
$node=Join-Path $webDir 'node.exe';if(-not(Test-Path $node)){$node=(Get-Command node.exe -ErrorAction SilentlyContinue).Source};if(-not $node){throw 'Bundel node.exe atau Node.js runtime diperlukan untuk menjalankan next start.'}
Install-Service 'AlgentraCoreApi' 'powershell.exe' "-NoProfile -ExecutionPolicy Bypass -File `"$(Join-Path $InstallDir 'infra\windows\start-core-api.ps1')`" -ProjectRoot `"$InstallDir`" -EnvironmentFile `"$EnvFile`"" $InstallDir (Join-Path $InstallDir 'logs\core-api')
Install-Service 'AlgentraWeb' $node "`"$next`" start -p 3000" $webDir (Join-Path $InstallDir 'logs\web')
$tunnelLogFile=Join-Path $InstallDir 'logs\tunnel\cloudflared.log'
Install-Service 'AlgentraTunnel' (Join-Path $BinDir 'cloudflared.exe') "tunnel --protocol http2 --loglevel info --logfile `"$tunnelLogFile`" run --token-file `"$TunnelTokenFile`"" $BinDir (Join-Path $InstallDir 'logs\tunnel')
Set-TunnelTokenAcl
Set-EnvAcl
Start-AlgentraServices
Write-InstallLog 'Service Core API, web, dan tunnel telah dimulai.'
Register-AlgentraTasks;if(-not $v.TELEGRAM_BOT_TOKEN -or -not $v.TELEGRAM_ADMIN_CHAT_ID){Write-Warning 'Backup dan watchdog Telegram belum dijadwalkan; lengkapi konfigurasi bot lalu jalankan install.ps1 -Repair.'}
try{$ready=Invoke-RestMethod 'http://127.0.0.1:8000/ready' -TimeoutSec 30;$web=Invoke-WebRequest 'http://127.0.0.1:3000' -UseBasicParsing -TimeoutSec 30;if($ready.status -ne 'ready' -or $web.StatusCode -ne 200){throw 'Smoke check service gagal'}}catch{throw "Service terpasang tetapi verifikasi gagal: $_"}
Install-ManagedWorkerTask
$publicCheck=Test-PublicDomain $v.PUBLIC_DOMAIN
if(-not $publicCheck.Success){Write-Warning "Domain belum merespons setelah beberapa percobaan ($($publicCheck.Status)). Periksa status tunnel dan DNS Cloudflare."}
Write-Host "Instalasi Algentra siap di $InstallDir. Domain: https://$($v.PUBLIC_DOMAIN)"
Write-InstallLog 'Pemeriksaan lokal selesai; domain publik harus dilihat pada output instalasi.'

