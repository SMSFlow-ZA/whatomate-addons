param([Parameter(Mandatory)][ValidateSet('capture-provenance','erase-worker','restore')][string]$Mode,[Parameter(Mandatory)][string]$Target,[ValidateSet('publisher','administrator')][string]$Role='publisher',[string]$RestoreKey='',[string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
if($Target -ne 'source' -and $Target -notmatch '^synthetic_restore_[0-9]{14}$'){throw 'Expected dedicated synthetic restore database'}
if($Target -eq 'source' -and $Mode -ne 'erase-worker'){throw 'Active synthetic source permits only exact worker completion'}
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$hostSource=[IO.Path]::GetFullPath($HostSource)
$privateRuntime=Join-Path $hostSource '.work/synthetic-local'
$output=Join-Path $privateRuntime 'restore-authority'
New-Item -ItemType Directory -Force -Path $output|Out-Null
$argsDocker=@('run','--rm','--network','whatomate-synthetic-local_default','-e','SYNTHETIC_LOCAL_ONLY=true',
 '-v',"${hostSource}:/workspace/whatomate-addons-host",'-v',"${addonSource}:/workspace/whatomate-addons",'-v',"${privateRuntime}:/run/private:ro",'-v',"${output}:/run/output",'-v','whatomate-synthetic-local-controllerconfig:/run/config:ro',
 '-v','whatomate-addons-docjobs:/var/lib/docker/volumes/whatomate-addons-docjobs/_data','-v','whatomate-synthetic-local_gomod:/go/pkg/mod','-v','whatomate-synthetic-local_gobuild:/root/.cache/go-build','-w','/workspace/whatomate-addons-host',
 'golang@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade','go','run','./cmd/extensions-reconcile','-mode',$Mode,'-target',$Target,'-role',$Role)
if($RestoreKey){$argsDocker+=@('-restore-key',$RestoreKey)}
$result=& docker @argsDocker
if($LASTEXITCODE -ne 0){throw 'Trusted reconciliation failed; retain restored application offline and inspect the private journal/receipt.'}
$stamp=Get-Date -Format yyyyMMdd-HHmmss
if($result){$result|Set-Content -LiteralPath (Join-Path $output "$Mode-$Target-$stamp.json") -Encoding utf8NoBOM}
Write-Output "Trusted local owner operation succeeded: $Mode / $Target. Original signed session/MFA/current grants were checked; receipts remain private."
