param([string]$BaseUrl='http://127.0.0.1:18280')
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'assert-network.ps1')
if($BaseUrl -notmatch '^http://(127\.0\.0\.1|localhost|synthetic-demo\.localhost):18280$'){throw 'Health probe is limited to the dedicated local profile'}
$response=Invoke-WebRequest "$BaseUrl/health" -TimeoutSec 10
if($response.StatusCode -ne 200){throw 'App health failed'}
$index=Invoke-WebRequest "$BaseUrl/" -TimeoutSec 10
if($index.Content -notmatch '(?i)<!DOCTYPE html>'){throw 'Embedded frontend missing'}
$installed=Invoke-RestMethod 'http://127.0.0.1:11434/api/tags' -TimeoutSec 10
$expected=@{ 'qwen3:14b'='bdbd181c33f2ed1b31c972991882db3cf4d192569092138a7d29e973cd9debe8';'nomic-embed-text:latest'='0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f' }
foreach($model in $expected.Keys){$actual=@($installed.models|Where-Object name -eq $model);if($actual.Count -ne 1 -or $actual[0].digest -ne $expected[$model]){throw "Local model digest mismatch: $model"}}
& docker exec whatomate-synthetic-local-db-1 psql -U synthetic_local -d whatomate_synthetic_local -At -c 'SELECT 1' | Out-Null
if($LASTEXITCODE -ne 0){throw 'Dedicated database unavailable'}
$running=& docker inspect whatomate-synthetic-local-controller-1 --format '{{.State.Running}}'
if($LASTEXITCODE -ne 0 -or $running -ne 'true'){throw 'Isolated processing controller unavailable'}
Write-Output 'Local app, embedded frontend, DB, controller and pinned installed model digests checked. This is infrastructure health, not functional acceptance.'
