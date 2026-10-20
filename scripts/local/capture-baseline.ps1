param([string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
$release=Join-Path $privateRuntime ('rollback/baseline-'+(Get-Date -Format yyyyMMdd-HHmmss))
New-Item -ItemType Directory -Force -Path $release|Out-Null
& docker cp whatomate-synthetic-local-app-1:/tmp/whatomate (Join-Path $release 'whatomate')
if($LASTEXITCODE -ne 0){throw 'Could not capture running local binary'}
Copy-Item -LiteralPath (Join-Path $privateRuntime 'host.toml') -Destination (Join-Path $release 'host.toml')
$manifest=@{synthetic_only=$true;captured_utc=[DateTime]::UtcNow.ToString('o');binary_sha256=(Get-FileHash -LiteralPath (Join-Path $release 'whatomate')).Hash.ToLower();configuration_sha256=(Get-FileHash -LiteralPath (Join-Path $release 'host.toml')).Hash.ToLower();state='baseline captured; not yet rollback accepted';active_container_image=(& docker inspect whatomate-synthetic-local-app-1 --format '{{.Image}}')}
$manifest|ConvertTo-Json -Depth 5|Set-Content -LiteralPath (Join-Path $release 'baseline-manifest.json') -Encoding utf8NoBOM
Write-Output "Private running binary/configuration captured: $release"
