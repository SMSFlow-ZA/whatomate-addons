param([Parameter(Mandatory)][ValidatePattern('^[a-z0-9-]+$')][string]$Name,[Parameter(Mandatory)][ValidatePattern('^synthetic_restore_[0-9]{14}$')][string]$Target,[string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'))
$ErrorActionPreference='Stop'
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
$output=Join-Path $privateRuntime 'restore-authority';$archive=Join-Path $output "$Name.backup"
$manifest=Get-Content -Raw -LiteralPath (Join-Path $output "$Name-manifest.json")|ConvertFrom-Json
if(-not $manifest.synthetic_only -or $manifest.source_database -ne 'whatomate_synthetic_local' -or $manifest.archive_name -ne "$Name.backup" -or (Get-FileHash -LiteralPath $archive).Hash.ToLower() -ne $manifest.archive_sha256){throw 'Authority archive identity mismatch'}
$identity=Get-Content -Raw -LiteralPath (Join-Path $privateRuntime 'identities.json')|ConvertFrom-Json
if($identity.OrganizationID -ne $manifest.organization_id -or $manifest.organization_id -notmatch '^[a-f0-9-]{36}$'){throw 'Authority organisation identity mismatch'}
$marker=@(& docker exec whatomate-synthetic-local-db-1 psql -U synthetic_local -d $Target -At -c "SELECT count(*) FROM organizations; SELECT count(*) FROM organizations WHERE id::text='$($manifest.organization_id)' AND slug='synthetic-er-local'; SELECT count(*) FROM addon_governance_document; SELECT count(*) FROM addon_governance_event;")
if($LASTEXITCODE -ne 0 -or $marker.Count -ne 4 -or [int]$marker[0] -ne 1 -or [int]$marker[1] -ne 1 -or [int]$marker[2] -ne 0 -or [int]$marker[3] -ne 0){throw 'Target must be a matching dedicated offline snapshot with BOTH governance tables empty. No upsert/overwrite permitted.'}
$schemaQuery="SELECT string_agg(table_name||':'||column_name||':'||udt_name||':'||is_nullable||':'||coalesce(column_default,''), E'\n' ORDER BY table_name,ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('addon_governance_document','addon_governance_event'); SELECT string_agg(r.relname||':'||c.contype::text||':'||pg_get_constraintdef(c.oid), E'\n' ORDER BY r.relname,c.conname) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid WHERE r.relname IN ('addon_governance_document','addon_governance_event');"
$schema=(& docker exec whatomate-synthetic-local-db-1 psql -U synthetic_local -d $Target -At -c $schemaQuery)|Out-String
$schemaSha=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($schema))).ToLower()
if($LASTEXITCODE -ne 0 -or $schemaSha -ne $manifest.schema_sha256){throw 'Authority schema fingerprint mismatch'}
& docker cp $archive whatomate-synthetic-local-db-1:/tmp/trusted-authority-import.backup
if($LASTEXITCODE -ne 0){throw 'Authority copy failed'}
& docker exec whatomate-synthetic-local-db-1 pg_restore -U synthetic_local --single-transaction --exit-on-error --data-only --no-owner -d $Target /tmp/trusted-authority-import.backup
if($LASTEXITCODE -ne 0){throw 'Authority import failed; keep application offline'}
$counts=@(& docker exec whatomate-synthetic-local-db-1 psql -U synthetic_local -d $Target -At -c 'SELECT count(*) FROM addon_governance_document; SELECT count(*) FROM addon_governance_event;')
if($LASTEXITCODE -ne 0 -or [int]$counts[0] -ne $manifest.documents -or [int]$counts[1] -ne $manifest.events){throw 'Authority import count mismatch; keep application offline'}
@{synthetic_only=$true;target_database=$Target;archive_sha256=$manifest.archive_sha256;schema_sha256=$schemaSha;documents=$manifest.documents;events=$manifest.events;imported_utc=[DateTime]::UtcNow.ToString('o');state='authority imported; physical owner reapplication pending; no restored application allowed'}|ConvertTo-Json|Set-Content -LiteralPath (Join-Path $output "$Name-$Target-import.json") -Encoding utf8NoBOM
Write-Output 'Independent full authority imported atomically into verified empty matching offline tables. Owner reapplication still required before access.'
