param([string]$HostSource = (Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference = 'Stop'
$addonSource = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$hostSource = [IO.Path]::GetFullPath($HostSource)
$privateRuntime = Join-Path $hostSource '.work/synthetic-local'
New-Item -ItemType Directory -Force -Path $privateRuntime | Out-Null
if (Test-Path -LiteralPath (Join-Path $privateRuntime 'host.toml')) { throw 'Runtime already initialized; refusing to replace keys.' }
function New-Secret { $bytes = New-Object byte[] 32; [Security.Cryptography.RandomNumberGenerator]::Fill($bytes); return [Convert]::ToBase64String($bytes) }
$dbPassword = (New-Secret).Replace('+','A').Replace('/','B').Replace('=','')
$jwt = New-Secret
$encryption = New-Secret
$security = New-Secret
@"
[app]
name = "SYNTHETIC ER local demonstration"
environment = "development"
debug = false
encryption_key = "$encryption"
[server]
host = "0.0.0.0"
port = 8080
read_timeout = 130
write_timeout = 130
allowed_origins = "http://synthetic-demo.localhost:18280,http://synthetic-demo.localhost:18573"
[database]
host = "db"
port = 5432
user = "synthetic_local"
password = "$dbPassword"
name = "whatomate_synthetic_local"
ssl_mode = "disable"
max_open_conns = 12
max_idle_conns = 3
conn_max_lifetime = 300
[redis]
host = "redis"
port = 6379
[jwt]
secret = "$jwt"
access_expiry_mins = 60
refresh_expiry_days = 1
[storage]
type = "local"
local_path = "/runtime/media"
[cookie]
secure = false
[default_admin]
email = "administrator@synthetic.invalid"
password = "$jwt"
full_name = "Synthetic administrator"
[channels]
stop_dispatch = true
[extensions]
security_key = "$security"
mfa_freshness_seconds = 43200
modules = "directory,cases,knowledge,ai"
roster_freshness_seconds = 86400
synthetic_development = true
development_ai_endpoint = "http://host.docker.internal:11434"
development_model_digest = "bdbd181c33f2ed1b31c972991882db3cf4d192569092138a7d29e973cd9debe8"
development_embedding_digest = "0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f"
"@ | Set-Content -LiteralPath (Join-Path $privateRuntime 'host.toml') -Encoding utf8NoBOM
@"
HOST_SOURCE=$($hostSource.Replace('\','/'))
ADDONS_SOURCE=$($addonSource.Replace('\','/'))
PRIVATE_RUNTIME=$($privateRuntime.Replace('\','/'))
LOCAL_DB_PASSWORD=$dbPassword
"@ | Set-Content -LiteralPath (Join-Path $privateRuntime 'compose.env') -Encoding utf8NoBOM
Write-Output 'Synthetic private configuration created under host .work/synthetic-local. Secrets were not printed.'
