param([ValidateSet('start','seed','status','stop','backup')][string]$Action = 'status', [string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$hostSource=[IO.Path]::GetFullPath($HostSource)
$privateRuntime=Join-Path $hostSource '.work/synthetic-local'
$argsCompose=@('compose','--env-file',(Join-Path $privateRuntime 'compose.env'),'-f',(Join-Path $addonSource 'deploy/local/compose.yaml'))
switch($Action){
 'start' { & docker @argsCompose up -d db redis app }
 'seed' { & docker @argsCompose run --rm seed }
 'status' { & docker @argsCompose ps }
 'stop' { & docker @argsCompose stop app db redis }
 'backup' {
   $dir=Join-Path $privateRuntime 'backups';New-Item -ItemType Directory -Force -Path $dir|Out-Null
   $stamp=Get-Date -Format 'yyyyMMdd-HHmmss'
   & docker @argsCompose exec -T db pg_dump -U synthetic_local -d whatomate_synthetic_local -Fc -f /tmp/synthetic.backup
   if($LASTEXITCODE -ne 0){throw 'Backup failed'}
   & docker @argsCompose cp db:/tmp/synthetic.backup (Join-Path $dir "$stamp.backup")
 }
}
if($LASTEXITCODE -ne 0){throw "Local runtime action failed: $Action"}
