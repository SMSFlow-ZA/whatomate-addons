param([string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$hostSource=[IO.Path]::GetFullPath($HostSource)
$privateRuntime=Join-Path $hostSource '.work/synthetic-local'
function Get-SourceIdentity([string]$path){
 $head= & git -C $path rev-parse HEAD
 $dirty=@(& git -C $path status --porcelain)
 $files=@(& git -C $path ls-files --cached --others --exclude-standard)|Sort-Object -Unique
 $entries=@(foreach($relative in $files){$full=Join-Path $path $relative;if(Test-Path -LiteralPath $full -PathType Leaf){[ordered]@{path=$relative;sha256=(Get-FileHash -Algorithm SHA256 -LiteralPath $full).Hash.ToLower()}}else{[ordered]@{path=$relative;sha256='MISSING'}}})
 $bytes=[Text.Encoding]::UTF8.GetBytes(($entries|ConvertTo-Json -Depth 5 -Compress))
 $treeHash=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLower()
 return @{head=$head;dirty=($dirty.Count -gt 0);changed_paths=$dirty;source_tree_sha256=$treeHash;source_files=$entries}
}
$result=@{
 synthetic_only=$true;recorded_utc=[DateTime]::UtcNow.ToString('o');
 host=(Get-SourceIdentity $hostSource);addons=(Get-SourceIdentity $addonSource);
 configuration_sha256=(Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $privateRuntime 'host.toml')).Hash.ToLower();
 qwen_digest='bdbd181c33f2ed1b31c972991882db3cf4d192569092138a7d29e973cd9debe8';
 embedding_digest='0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f';
 images=@(& docker inspect whatomate-synthetic-local-app-1 whatomate-synthetic-local-db-1 whatomate-synthetic-local-redis-1 --format '{{.Name}} {{.Image}}');
 running_binary_sha256=((& docker exec whatomate-synthetic-local-app-1 sha256sum /tmp/whatomate) -split ' ')[0];
 transport='Live carriers disabled; local adapter tests only';
 approved_production_locality=$false
}
if(Test-Path -LiteralPath (Join-Path $privateRuntime 'knowledge-controller.json')){$profile=Get-Content -Raw -LiteralPath (Join-Path $privateRuntime 'knowledge-controller.json')|ConvertFrom-Json;$result.worker=@{image=$profile.Supervisor.Image;definitions_sha256=$profile.Supervisor.DefinitionsHash;signature_acceptance_expiry=$profile.Supervisor.Worker.SignaturesValidUntil}}
$installed=Invoke-RestMethod 'http://127.0.0.1:11434/api/tags' -TimeoutSec 10
$result.actual_installed_models=@($installed.models|Where-Object name -in @('qwen3:14b','nomic-embed-text:latest')|ForEach-Object{@{name=$_.name;digest=$_.digest}})
if(Test-Path -LiteralPath (Join-Path $privateRuntime 'SYNTHETIC-roster.xlsx')){$result.roster_sha256=(Get-FileHash -LiteralPath (Join-Path $privateRuntime 'SYNTHETIC-roster.xlsx')).Hash.ToLower()}
$result|ConvertTo-Json -Depth 8|Set-Content -LiteralPath (Join-Path $privateRuntime 'release-manifest.json') -Encoding utf8NoBOM
Write-Output 'Private runtime release manifest recorded without secret values.'
