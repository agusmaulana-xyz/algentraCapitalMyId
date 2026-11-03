param(
    [Parameter(Mandatory = $true)]
    [string] $ProjectRoot,

    [string] $TerminalTemplateDir,
    [string] $MT5SourceTerminalDir,
    [string] $MasterTerminalPath
)

$ErrorActionPreference = 'Stop'
$resolvedRoot = (Resolve-Path -LiteralPath $ProjectRoot).Path
$sourceRoot = Join-Path $resolvedRoot 'mt5\experts'
$templateMarker = '.algentra-follower-template.json'
$script:SourceMqlRoot = $null

function Resolve-TerminalDirectory([string] $Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return $null }
    $candidate = $Value.Trim().Trim('"')
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { return (Split-Path -Parent (Resolve-Path -LiteralPath $candidate).Path) }
    if (Test-Path -LiteralPath (Join-Path $candidate 'terminal64.exe') -PathType Leaf) { return (Resolve-Path -LiteralPath $candidate).Path }
    return $null
}

function Find-MT5SourceDirectory {
    $candidates = [Collections.Generic.List[string]]::new()
    if ($env:MT5_SOURCE_TERMINAL_DIR) { $candidates.Add($env:MT5_SOURCE_TERMINAL_DIR) }
    if ($MT5SourceTerminalDir) { $candidates.Add($MT5SourceTerminalDir) }
    if ($MasterTerminalPath) { $candidates.Add($MasterTerminalPath) }

    try {
        Get-CimInstance Win32_Process -Filter "Name='terminal64.exe'" -ErrorAction Stop |
            Where-Object { $_.ExecutablePath } |
            ForEach-Object { $candidates.Add($_.ExecutablePath) }
    } catch { }

    foreach ($hive in @('HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*')) {
        Get-ItemProperty $hive -ErrorAction SilentlyContinue |
            Where-Object { $_.DisplayName -match 'MetaTrader 5|MetaQuotes.*5' -and $_.InstallLocation } |
            ForEach-Object { $candidates.Add([string]$_.InstallLocation) }
    }

    foreach ($base in @($env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:SystemDrive)) {
        if (-not $base -or -not (Test-Path -LiteralPath $base -PathType Container)) { continue }
        foreach ($directory in Get-ChildItem -LiteralPath $base -Directory -ErrorAction SilentlyContinue) {
            if ($directory.Name -match 'MetaTrader|MetaQuotes|MT5|Trading') { $candidates.Add($directory.FullName) }
        }
    }

    foreach ($candidate in $candidates) {
        $directory = Resolve-TerminalDirectory $candidate
        if ($directory -and
            (Test-Path -LiteralPath (Join-Path $directory 'terminal64.exe') -PathType Leaf) -and
            (Test-Path -LiteralPath (Join-Path $directory 'metaeditor64.exe') -PathType Leaf)) {
            $localMqlRoot = Join-Path $directory 'MQL5'
            if (Test-Path -LiteralPath (Join-Path $localMqlRoot 'Include') -PathType Container) {
                $script:SourceMqlRoot = $localMqlRoot
                return $directory
            }

            $dataDirectories = Join-Path $env:APPDATA 'MetaQuotes\Terminal'
            foreach ($dataDirectory in Get-ChildItem -LiteralPath $dataDirectories -Directory -ErrorAction SilentlyContinue) {
                $originFile = Join-Path $dataDirectory.FullName 'origin.txt'
                $dataMqlRoot = Join-Path $dataDirectory.FullName 'MQL5'
                if ((Test-Path -LiteralPath $originFile -PathType Leaf) -and
                    (Test-Path -LiteralPath (Join-Path $dataMqlRoot 'Include') -PathType Container)) {
                    $origin = (Get-Content -LiteralPath $originFile -Raw).Trim().TrimEnd('\')
                    if ($origin.Equals($directory.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
                        $script:SourceMqlRoot = $dataMqlRoot
                        return $directory
                    }
                }
            }
        }
    }
    return $null
}

function Find-MT5DataDirectory([string] $TerminalDirectory) {
    $localMqlRoot = Join-Path $TerminalDirectory 'MQL5'
    if (Test-Path -LiteralPath (Join-Path $localMqlRoot 'Include') -PathType Container) { return $localMqlRoot }
    $dataDirectories = Join-Path $env:APPDATA 'MetaQuotes\Terminal'
    foreach ($dataDirectory in Get-ChildItem -LiteralPath $dataDirectories -Directory -ErrorAction SilentlyContinue) {
        $originFile = Join-Path $dataDirectory.FullName 'origin.txt'
        $dataMqlRoot = Join-Path $dataDirectory.FullName 'MQL5'
        if ((Test-Path -LiteralPath $originFile -PathType Leaf) -and
            (Test-Path -LiteralPath (Join-Path $dataMqlRoot 'Include') -PathType Container)) {
            $origin = (Get-Content -LiteralPath $originFile -Raw).Trim().TrimEnd('\')
            if ($origin.Equals($TerminalDirectory.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
                return $dataMqlRoot
            }
        }
    }
    return $null
}

function Install-MetaQuotesTerminal {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $setupPath = Join-Path $env:TEMP ("mt5setup-" + [guid]::NewGuid().ToString('N') + '.exe')
    try {
        Invoke-WebRequest -Uri 'https://download.mql5.com/cdn/web/metaquotes.software.corp/mt5/mt5setup.exe' -OutFile $setupPath -UseBasicParsing -TimeoutSec 300
        $signature = Get-AuthenticodeSignature -LiteralPath $setupPath
        if ($signature.Status -ne 'Valid' -or
            $signature.SignerCertificate.Subject -notmatch '(?i)MetaQuotes') {
            throw 'Tanda tangan digital installer MT5 tidak valid atau bukan milik MetaQuotes; unduhan ditolak.'
        }

        Write-Host 'MT5 belum ditemukan; memasang terminal MetaQuotes resmi untuk template follower.'
        $process = Start-Process -FilePath $setupPath -ArgumentList '/auto' -PassThru -WindowStyle Hidden
        if (-not $process.WaitForExit(300000)) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            throw 'Installer MetaTrader 5 melewati batas waktu 5 menit.'
        }
        for ($attempt = 0; $attempt -lt 24; $attempt++) {
            $directory = Find-MT5SourceDirectory
            if ($directory) { return $directory }
            Start-Sleep -Seconds 5
        }
        if ($process.ExitCode -ne 0) { throw "Installer MetaTrader 5 berhenti dengan exit code $($process.ExitCode) dan terminal/MetaEditor/MQL5 Include belum siap." }
        throw 'MT5 selesai dipasang tetapi terminal64.exe, MetaEditor, atau MQL5\Include tidak ditemukan.'
    } finally {
        Remove-Item -LiteralPath $setupPath -Force -ErrorAction SilentlyContinue
    }
}

function Compile-FollowerEA([string] $MetaEditor, [string] $MqlRoot, [string] $SourceFile) {
    $outputFile = [IO.Path]::ChangeExtension($SourceFile, '.ex5')
    $logFile = [IO.Path]::ChangeExtension($SourceFile, '.compile.log')
    Remove-Item -LiteralPath $outputFile, $logFile -Force -ErrorAction SilentlyContinue
    $arguments = '/compile:"{0}" /include:"{1}" /log:"{2}"' -f $SourceFile, $MqlRoot, $logFile
    $process = Start-Process -FilePath $MetaEditor -ArgumentList $arguments -PassThru -WindowStyle Hidden
    if (-not $process.WaitForExit(120000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw "MetaEditor melewati batas waktu saat mengompilasi $(Split-Path -Leaf $SourceFile)."
    }
    if (-not (Test-Path -LiteralPath $outputFile -PathType Leaf)) {
        $logText = if (Test-Path -LiteralPath $logFile) { Get-Content -LiteralPath $logFile -Raw } else { 'log compiler tidak dibuat' }
        throw "EA tidak berhasil dikompilasi: $SourceFile`n$logText"
    }
    if (Test-Path -LiteralPath $logFile) {
        $logText = Get-Content -LiteralPath $logFile -Raw
        if ($logText -match '(?im)\b([1-9][0-9]*)\s+errors?\b') {
            throw "MetaEditor melaporkan error untuk $SourceFile`n$logText"
        }
    }
    Write-Host "EA follower berhasil dikompilasi: $(Split-Path -Leaf $outputFile)"
    return $outputFile
}

$sourceTerminalDir = Find-MT5SourceDirectory
if (-not $sourceTerminalDir) {
    $sourceTerminalDir = Install-MetaQuotesTerminal
}
if (-not $sourceTerminalDir) { throw 'MT5 tidak ditemukan dan terminal otomatis tidak berhasil dipasang.' }

if ([string]::IsNullOrWhiteSpace($TerminalTemplateDir)) {
    $TerminalTemplateDir = Join-Path $resolvedRoot 'mt5\template\follower-clean'
}
$TerminalTemplateDir = [IO.Path]::GetFullPath($TerminalTemplateDir)
$sourceFullPath = [IO.Path]::GetFullPath($sourceTerminalDir).TrimEnd('\')
$sourcePathPrefix = [IO.Path]::GetFullPath($sourceTerminalDir).TrimEnd('\') + '\'
if ($TerminalTemplateDir.TrimEnd('\').Equals($sourceFullPath, [StringComparison]::OrdinalIgnoreCase) -or
    $TerminalTemplateDir.StartsWith($sourcePathPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Template follower tidak boleh dibuat di dalam folder instalasi MT5 sumber.'
}
$templateParent = Split-Path -Parent $TerminalTemplateDir
if (-not (Test-Path -LiteralPath $TerminalTemplateDir -PathType Container)) {
    New-Item -ItemType Directory -Path $templateParent -Force | Out-Null
    $excluded = @(
        (Join-Path $sourceTerminalDir 'config'), (Join-Path $sourceTerminalDir 'Bases'),
        (Join-Path $sourceTerminalDir 'History'), (Join-Path $sourceTerminalDir 'Logs'),
        (Join-Path $sourceTerminalDir 'Profiles'), (Join-Path $sourceTerminalDir 'Tester'),
        (Join-Path $sourceTerminalDir 'Community'), (Join-Path $sourceTerminalDir 'MQL5\Profiles'),
        (Join-Path $sourceTerminalDir 'MQL5\Logs'), (Join-Path $sourceTerminalDir 'MQL5\Files'),
        (Join-Path $sourceTerminalDir 'MQL5\Tester'), (Join-Path $sourceTerminalDir 'MQL5\Shared Projects')
    )
    $copyOutput = & robocopy.exe $sourceTerminalDir $TerminalTemplateDir /E /R:2 /W:1 /XD $excluded /NFL /NDL /NJH /NJS /NP 2>&1
    if ($LASTEXITCODE -ge 8) { throw "Salin folder program MT5 gagal (robocopy exit $LASTEXITCODE): $($copyOutput -join "`n")" }
} elseif (-not (Test-Path -LiteralPath (Join-Path $TerminalTemplateDir $templateMarker) -PathType Leaf)) {
    throw "Folder tujuan sudah ada dan bukan template yang dikelola skrip; pilih path kosong: $TerminalTemplateDir"
}

if (-not (Test-Path -LiteralPath (Join-Path $TerminalTemplateDir 'terminal64.exe') -PathType Leaf)) {
    throw "Terminal template tidak berisi terminal64.exe: $TerminalTemplateDir"
}

# Compile inside the isolated clone so MetaEditor never writes into the source/master terminal.
$metaEditor = Join-Path $TerminalTemplateDir 'metaeditor64.exe'
$mqlRoot = Join-Path $TerminalTemplateDir 'MQL5'
if (-not (Test-Path -LiteralPath $metaEditor -PathType Leaf)) {
    throw "Salinan template MT5 harus berisi metaeditor64.exe: $TerminalTemplateDir"
}
if (-not (Test-Path -LiteralPath (Join-Path $mqlRoot 'Include') -PathType Container)) {
    if (-not $script:SourceMqlRoot) { $script:SourceMqlRoot = Find-MT5DataDirectory $sourceTerminalDir }
    if (-not $script:SourceMqlRoot -or -not (Test-Path -LiteralPath (Join-Path $script:SourceMqlRoot 'Include') -PathType Container)) {
        throw 'Folder MQL5\Include tidak ditemukan di terminal atau direktori data MetaQuotes yang terhubung.'
    }
    New-Item -ItemType Directory -Path $mqlRoot -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $script:SourceMqlRoot 'Include') -Destination $mqlRoot -Recurse -Force
}
$eaFiles = @(
    (Compile-FollowerEA $metaEditor $mqlRoot (Join-Path $sourceRoot 'MT5FollowerCopyEA.mq5')),
    (Compile-FollowerEA $metaEditor $mqlRoot (Join-Path $sourceRoot 'Algentra\MT5LoginProbe.mq5'))
)

$templateExperts = Join-Path $TerminalTemplateDir 'MQL5\Experts'
Remove-Item -LiteralPath $templateExperts -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $templateExperts -Force | Out-Null
$followerTarget = Join-Path $templateExperts 'MT5FollowerCopyEA.ex5'
$probeTarget = Join-Path $templateExperts 'Algentra\MT5LoginProbe.ex5'
New-Item -ItemType Directory -Path (Split-Path -Parent $probeTarget) -Force | Out-Null
Copy-Item -LiteralPath $eaFiles[0] -Destination $followerTarget -Force
Copy-Item -LiteralPath $eaFiles[1] -Destination $probeTarget -Force
$marker = @{
    product = 'Algentra follower MT5 template'
    generated_at_utc = [DateTime]::UtcNow.ToString('o')
    source_terminal = $sourceTerminalDir
    source_master_modified = $false
}
$marker | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $TerminalTemplateDir $templateMarker) -Encoding utf8
Write-Host "Template follower bersih siap: $TerminalTemplateDir"
Write-Output $TerminalTemplateDir
