param([switch]$ConfirmSyntheticReset,[string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
if(-not $ConfirmSyntheticReset){throw 'Explicit ConfirmSyntheticReset required. This destroys only the dedicated synthetic DB/media/cache volumes.'}
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
& docker compose --env-file (Join-Path $privateRuntime 'compose.env') -f (Join-Path $addonSource 'deploy/local/compose.yaml') --profile processing down
if($LASTEXITCODE -ne 0){throw 'Could not stop dedicated stack'}
foreach($volume in @('whatomate-synthetic-local_db','whatomate-synthetic-local_media','whatomate-synthetic-local_gomod','whatomate-synthetic-local_gobuild')){
 $project=& docker volume inspect $volume --format '{{index .Labels "com.docker.compose.project"}}'
 if($LASTEXITCODE -ne 0 -or $project -ne 'whatomate-synthetic-local'){throw "Unexpected volume ownership: $volume"}
 & docker volume rm $volume
 if($LASTEXITCODE -ne 0){throw "Could not remove dedicated volume $volume"}
}
Write-Output 'Dedicated synthetic volumes reset. Private credentials, snapshots and document replay markers retained; archive identities.json manually before re-seeding. No unrelated volume removed.'
