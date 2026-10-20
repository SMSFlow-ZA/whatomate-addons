param([Parameter(Mandatory)][string]$Baseline,[Parameter(Mandatory)][ValidatePattern('^synthetic_restore_[0-9]{14}$')][string]$RestoredDatabase,[switch]$ConfirmIndependentJournalReconciled,[string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
if(-not $ConfirmIndependentJournalReconciled){throw 'Reconcile the independent deletion/hold journal offline before starting restored application access.'}
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
# A previous binary may not understand private SMS connection exclusions. Keep
# this generic legacy rehearsal unavailable for an activated carrier profile.
$currentConfig=Get-Content -Raw -LiteralPath (Join-Path $privateRuntime 'host.toml')
if($currentConfig -match '(?m)^\s*carrier_enabled\s*=\s*true\s*(?:#.*)?$'){throw 'Active protected carrier binding requires a reviewed carrier-aware rollback; legacy rehearsal refused.'}
$rollbackGuard=Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'sql/assert-carrier-rollback.sql')
& docker exec whatomate-synthetic-local-db-1 psql -X -v ON_ERROR_STOP=1 -U synthetic_local -d $RestoredDatabase -c $rollbackGuard | Out-Null
if($LASTEXITCODE -ne 0){throw 'Restored carrier state is not accepted by the legacy rollback rehearsal.'}
$baselinePath=[IO.Path]::GetFullPath($Baseline)
$expected=[IO.Path]::GetFullPath((Join-Path $privateRuntime 'rollback'))+[IO.Path]::DirectorySeparatorChar
if(-not $baselinePath.StartsWith($expected,[StringComparison]::OrdinalIgnoreCase)){throw 'Expected this dedicated local private baseline directory'}
$manifest=Get-Content -Raw -LiteralPath (Join-Path $baselinePath 'baseline-manifest.json')|ConvertFrom-Json
if(-not $manifest.synthetic_only -or (Get-FileHash -LiteralPath (Join-Path $baselinePath 'whatomate')).Hash.ToLower() -ne $manifest.binary_sha256){throw 'Baseline binary identity mismatch'}
$release=Join-Path $privateRuntime ('rollback/rehearsal-'+(Get-Date -Format yyyyMMdd-HHmmss))
New-Item -ItemType Directory -Force -Path $release|Out-Null
Copy-Item -LiteralPath (Join-Path $baselinePath 'whatomate') -Destination (Join-Path $release 'whatomate')
$configuration=Get-Content -Raw -LiteralPath (Join-Path $baselinePath 'host.toml')
$configuration=$configuration.Replace('name = "whatomate_synthetic_local"',"name = `"$RestoredDatabase`"").Replace('host = "redis"','host = "rollbackredis"')
$configuration|Set-Content -LiteralPath (Join-Path $release 'host.toml') -Encoding utf8NoBOM
"ROLLBACK_RELEASE=$($release.Replace('\','/'))"|Set-Content -LiteralPath (Join-Path $release 'compose.env') -Encoding utf8NoBOM
& (Join-Path $PSScriptRoot 'assert-network.ps1')
& docker compose --env-file (Join-Path $release 'compose.env') -f (Join-Path $addonSource 'deploy/local/rollback.compose.yaml') up -d --remove-orphans
if($LASTEXITCODE -ne 0){throw 'Restored local application startup failed'}
try { & (Join-Path $PSScriptRoot 'assert-network.ps1') }
catch {
    & docker compose --env-file (Join-Path $release 'compose.env') -f (Join-Path $addonSource 'deploy/local/rollback.compose.yaml') stop rollbackapp rollbackredis
    throw
}
@{synthetic_only=$true;restored_database=$RestoredDatabase;baseline_binary_sha256=$manifest.binary_sha256;independent_journal_reconciled_asserted=$true;status='started; functional verification pending'}|ConvertTo-Json|Set-Content -LiteralPath (Join-Path $release 'rehearsal-manifest.json') -Encoding utf8NoBOM
Write-Output 'Separate restored baseline app started at 127.0.0.1:18281. Active app unchanged. Verify authorization, module/schema compatibility and reconciled deletion before accepting rollback.'
