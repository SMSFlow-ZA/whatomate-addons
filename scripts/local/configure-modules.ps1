param([string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'),[string]$Modules='directory,cases,knowledge,ai,conversation,reports,governance')
$ErrorActionPreference='Stop'
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
$identities=Get-Content -Raw -LiteralPath (Join-Path $privateRuntime 'identities.json')|ConvertFrom-Json
if($identities.Synthetic -ne $true -or $Modules -notmatch '^[a-z,]+$'){throw 'Synthetic marker and valid module list required'}
$primary=($identities.Logins|Where-Object Role -eq er).UserID
$backup=($identities.Logins|Where-Object Role -eq hrbp).UserID
if($primary -notmatch '^[a-f0-9-]{36}$' -or $backup -notmatch '^[a-f0-9-]{36}$'){throw 'Expected synthetic routing identities'}
$path=Join-Path $privateRuntime 'host.toml'
$text=Get-Content -Raw -LiteralPath $path
if($text -notmatch '(?m)^\[default_admin\]'){$text=$text.Replace('[extensions]',"[default_admin]`nemail = `"administrator@synthetic.invalid`"`nfull_name = `"Synthetic administrator`"`n`n[extensions]")}
$text=[regex]::Replace($text,'(?m)^modules = .*$',"modules = `"$Modules`"")
$settings=@{reports_min_group='5';conversation_employee_audience='"employees"';escalation_after_seconds='86400';escalation_worker_ids="`"$primary,$backup`"";routing_primary_ids="`"$primary`"";routing_backup_ids="`"$backup`""}
foreach($key in $settings.Keys){$line="$key = $($settings[$key])";if($text -match "(?m)^$key = .*$"){$text=[regex]::Replace($text,"(?m)^$key = .*$",$line)}else{$text += "`n$line"}}
$text|Set-Content -LiteralPath $path -Encoding utf8NoBOM
Write-Output 'Synthetic module/routing configuration updated. Timings/disclosure settings are provisional, not buyer approved. Restart app to activate.'
