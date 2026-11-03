param(
    [Parameter(Mandatory = $true)] [string] $PublicDomain,
    [Parameter(Mandatory = $true)] [string] $ApiToken,
    [string] $ExistingTunnelToken
)

$ErrorActionPreference = 'Stop'
$apiRoot = 'https://api.cloudflare.com/client/v4'
$headers = @{ Authorization = "Bearer $ApiToken" }
$domain = $PublicDomain.Trim().TrimEnd('.').ToLowerInvariant()

function Invoke-CloudflareApi([string] $Method, [string] $Path, $Body = $null) {
    $arguments = @{
        Method = $Method
        Uri = "$apiRoot$Path"
        Headers = $headers
        ErrorAction = 'Stop'
    }
    if ($null -ne $Body) {
        $arguments.ContentType = 'application/json'
        $arguments.Body = ConvertTo-Json -InputObject $Body -Depth 20 -Compress
    }
    try {
        $response = Invoke-RestMethod @arguments
    } catch {
        throw "Cloudflare API gagal untuk $Method $Path. Periksa token dan izin Tunnel/Zone/DNS."
    }
    if (-not $response.success) {
        throw "Cloudflare API menolak $Method $Path. Periksa token dan izin Tunnel/Zone/DNS."
    }
    return $response
}

function ConvertFrom-CloudflareTunnelToken([string] $Value) {
    $encoded = $Value.Trim()
    $parts = $encoded.Split('.')
    if ($parts.Count -gt 1) { $encoded = $parts[1] }
    $encoded = $encoded.Replace('-', '+').Replace('_', '/')
    switch ($encoded.Length % 4) {
        2 { $encoded += '==' }
        3 { $encoded += '=' }
        1 { throw 'Format token tunnel tidak dikenali.' }
    }
    try {
        $json = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))
        return $json | ConvertFrom-Json
    } catch {
        throw 'Tidak dapat membaca account dan tunnel dari token yang tersimpan.'
    }
}

if ([Uri]::CheckHostName($domain) -eq [UriHostNameType]::Unknown) {
    throw 'PUBLIC_DOMAIN harus berupa hostname valid tanpa skema atau path.'
}

# Resolve the zone first; its account owns the new tunnel.
$zones = [Collections.Generic.List[object]]::new()
$page = 1
do {
    $zoneResponse = Invoke-CloudflareApi 'GET' "/zones?per_page=50&page=$page"
    foreach ($zoneItem in @($zoneResponse.result)) { $zones.Add($zoneItem) }
    $totalPages = [int]$zoneResponse.result_info.total_pages
    $page++
} while ($page -le $totalPages)
$zone = $zones |
    Where-Object { $domain -eq $_.name -or $domain.EndsWith('.' + $_.name, [StringComparison]::OrdinalIgnoreCase) } |
    Sort-Object { $_.name.Length } -Descending |
    Select-Object -First 1
if (-not $zone) { throw 'Domain tidak ditemukan pada akun Cloudflare dari API token.' }
$accountId = [string]$zone.account.id
if (-not $accountId) { throw 'API Cloudflare tidak mengembalikan account ID untuk zone domain.' }

$tunnelName = "algentra-$domain"
$tunnelToken = $null
$tunnelId = $null
if ($ExistingTunnelToken) {
    $claims = ConvertFrom-CloudflareTunnelToken $ExistingTunnelToken
    if ([string]$claims.a -ne $accountId) {
        throw 'Token tunnel tersimpan berasal dari account Cloudflare yang berbeda dengan domain.'
    }
    $tunnelId = [string]$claims.t
    $tunnelToken = $ExistingTunnelToken.Trim()
} else {
    $tunnelResponse = Invoke-CloudflareApi 'GET' "/accounts/$accountId/cfd_tunnel?name=$([Uri]::EscapeDataString($tunnelName))&per_page=100"
    $tunnel = @($tunnelResponse.result | Where-Object { $_.name -eq $tunnelName -and -not $_.deleted_at } | Select-Object -First 1)
    if ($tunnel.Count -gt 0) {
        $tunnelId = [string]$tunnel[0].id
        $tokenResponse = Invoke-CloudflareApi 'GET' "/accounts/$accountId/cfd_tunnel/$tunnelId/token"
        $tunnelToken = [string]$tokenResponse.result
    } else {
        $createBody = @{ name = $tunnelName; config_src = 'cloudflare' }
        $createResponse = Invoke-CloudflareApi 'POST' "/accounts/$accountId/cfd_tunnel" $createBody
        $tunnelId = [string]$createResponse.result.id
        $tunnelToken = [string]$createResponse.result.token
    }
}
if (-not $tunnelId -or -not $tunnelToken) { throw 'Cloudflare tidak mengembalikan ID atau token tunnel.' }

# Keep rules for other hostnames, then configure this app's domain and a final 404.
$configResponse = Invoke-CloudflareApi 'GET' "/accounts/$accountId/cfd_tunnel/$tunnelId/configurations"
$currentIngress = @($configResponse.result.config.ingress)
$otherHostRules = @($currentIngress | Where-Object {
    $_.hostname -and ([string]$_.hostname).TrimEnd('.').ToLowerInvariant() -ne $domain
})
$ingress = @(
    @{ hostname = $domain; path = '/api/*'; service = 'http://127.0.0.1:8000' }
    @{ hostname = $domain; path = '/downloads/*'; service = 'http://127.0.0.1:8000' }
    @{ hostname = $domain; path = '/ws/*'; service = 'http://127.0.0.1:8000' }
    @{ hostname = $domain; service = 'http://127.0.0.1:3000' }
)
$ingress += $otherHostRules
$ingress += @{ service = 'http_status:404' }
$null = Invoke-CloudflareApi 'PUT' "/accounts/$accountId/cfd_tunnel/$tunnelId/configurations" @{ config = @{ ingress = $ingress } }

# Point the hostname at the tunnel. Existing A/AAAA records are replaced.
$recordResponse = Invoke-CloudflareApi 'GET' "/zones/$($zone.id)/dns_records?name=$domain&per_page=100"
$records = @($recordResponse.result | Where-Object { $_.name -eq $domain })
$target = "$tunnelId.cfargotunnel.com"
$cname = $records | Where-Object { $_.type -eq 'CNAME' } | Select-Object -First 1
foreach ($record in $records) {
    if ($record.type -eq 'A' -or $record.type -eq 'AAAA' -or ($record.type -eq 'CNAME' -and $record.id -ne $cname.id)) {
        $null = Invoke-CloudflareApi 'DELETE' "/zones/$($zone.id)/dns_records/$($record.id)"
    }
}
$dnsBody = @{ type = 'CNAME'; name = $domain; content = $target; ttl = 1; proxied = $true }
if ($cname) {
    $null = Invoke-CloudflareApi 'PUT' "/zones/$($zone.id)/dns_records/$($cname.id)" $dnsBody
} else {
    $null = Invoke-CloudflareApi 'POST' "/zones/$($zone.id)/dns_records" $dnsBody
}

Write-Host "Cloudflare otomatis disiapkan untuk https://$domain"
ConvertTo-Json -InputObject @{ tunnel_id = $tunnelId; tunnel_token = $tunnelToken } -Compress
