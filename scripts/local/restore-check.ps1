param([Parameter(Mandatory)][string]$Backup,[string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
$backupPath=[IO.Path]::GetFullPath($Backup)
$expected=[IO.Path]::GetFullPath((Join-Path $privateRuntime 'backups'))+[IO.Path]::DirectorySeparatorChar
if(-not $backupPath.StartsWith($expected,[StringComparison]::OrdinalIgnoreCase)){throw 'Restore accepts only this dedicated local backup directory'}
$compose=@('compose','--env-file',(Join-Path $privateRuntime 'compose.env'),'-f',(Join-Path $addonSource 'deploy/local/compose.yaml'))
& docker @compose cp $backupPath db:/tmp/restore-check.backup
if($LASTEXITCODE -ne 0){throw 'Backup copy failed'}
$restoreDb='synthetic_restore_'+(Get-Date -Format yyyyMMddHHmmss)
& docker @compose exec -T db createdb -U synthetic_local $restoreDb
if($LASTEXITCODE -ne 0){throw 'Disposable restore DB creation failed'}
& docker @compose exec -T db pg_restore -U synthetic_local --exit-on-error --no-owner -d $restoreDb /tmp/restore-check.backup
if($LASTEXITCODE -ne 0){throw 'Restore failed; retain disposable DB for diagnosis'}
& docker @compose exec -T db psql -U synthetic_local -d $restoreDb -c 'SELECT count(*) AS restored_users FROM users; SELECT count(*) AS restored_cases FROM addon_cases_case;'
if($LASTEXITCODE -ne 0){throw 'Restore verification failed'}
Write-Output "Backup restored into separate database $restoreDb. Active database unchanged. Privacy journal reconciliation is a separate release gate."
