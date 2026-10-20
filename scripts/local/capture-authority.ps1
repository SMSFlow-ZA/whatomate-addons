param([Parameter(Mandatory)][ValidatePattern('^[a-z0-9-]+$')][string]$Name,[string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
$output=Join-Path $privateRuntime 'restore-authority';New-Item -ItemType Directory -Force -Path $output|Out-Null
$archive=Join-Path $output "$Name.backup";if(Test-Path -LiteralPath $archive){throw 'Authority artifact already exists; preserve previous evidence'}
$schemaQuery="SELECT string_agg(table_name||':'||column_name||':'||udt_name||':'||is_nullable||':'||coalesce(column_default,''), E'\n' ORDER BY table_name,ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('addon_governance_document','addon_governance_event'); SELECT string_agg(r.relname||':'||c.contype::text||':'||pg_get_constraintdef(c.oid), E'\n' ORDER BY r.relname,c.conname) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid WHERE r.relname IN ('addon_governance_document','addon_governance_event');"
$schema=(& docker exec whatomate-synthetic-local-db-1 psql -U synthetic_local -d whatomate_synthetic_local -At -c $schemaQuery)|Out-String
if($LASTEXITCODE -ne 0 -or -not $schema.Trim()){throw 'Authority schema fingerprint failed'}
$schemaSha=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($schema))).ToLower()
$counts=@(& docker exec whatomate-synthetic-local-db-1 psql -U synthetic_local -d whatomate_synthetic_local -At -c 'SELECT count(*) FROM addon_governance_document; SELECT count(*) FROM addon_governance_event;')
if($LASTEXITCODE -ne 0 -or $counts.Count -ne 2){throw 'Authority count query failed'}
& docker exec whatomate-synthetic-local-db-1 pg_dump -U synthetic_local -d whatomate_synthetic_local -Fc -t addon_governance_document -t addon_governance_event -f /tmp/trusted-authority.backup
if($LASTEXITCODE -ne 0){throw 'Authority export failed'}
& docker cp whatomate-synthetic-local-db-1:/tmp/trusted-authority.backup $archive
if($LASTEXITCODE -ne 0){throw 'Authority copy failed'}
$identities=Get-Content -Raw -LiteralPath (Join-Path $privateRuntime 'identities.json')|ConvertFrom-Json
@{synthetic_only=$true;source_database='whatomate_synthetic_local';organization_id=$identities.OrganizationID;captured_utc=[DateTime]::UtcNow.ToString('o');archive_name="$Name.backup";archive_sha256=(Get-FileHash -LiteralPath $archive).Hash.ToLower();schema_sha256=$schemaSha;documents=[int]$counts[0];events=[int]$counts[1];state='independent authority captured; offline import/reconciliation pending'}|ConvertTo-Json|Set-Content -LiteralPath (Join-Path $output "$Name-manifest.json") -Encoding utf8NoBOM
Write-Output 'Full independent authority archive and integrity/schema/count manifest captured privately.'
