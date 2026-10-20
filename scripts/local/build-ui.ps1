param([string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'),[string]$Modules='directory,cases,knowledge,ai,conversation,reports,governance')
$ErrorActionPreference='Stop'
$hostSource=[IO.Path]::GetFullPath($HostSource)
$frontend=Join-Path $hostSource 'frontend'
$embedded=Join-Path $hostSource 'internal/frontend/dist'
if(-not (Test-Path -LiteralPath (Join-Path $frontend 'package.json'))){throw 'Expected managed host frontend'}
if($Modules -notmatch '^[a-z,]+$'){throw 'Invalid module selection'}
$node=(Get-Command node -ErrorAction Stop).Source
$oldModules=$env:VITE_EXTENSION_MODULES
try {
 $env:VITE_EXTENSION_MODULES=$Modules
 Push-Location $frontend
 try { & $node node_modules/vite/bin/vite.js build; if($LASTEXITCODE -ne 0){throw 'Frontend build failed'} } finally {Pop-Location}
 New-Item -ItemType Directory -Force -Path $embedded|Out-Null
 # No deletion: replace the generated index/assets from the successful build.
 Copy-Item -Path (Join-Path $frontend 'dist/*') -Destination $embedded -Recurse -Force
} finally { $env:VITE_EXTENSION_MODULES=$oldModules }
Write-Output 'Built selected static extension modules and copied generated frontend into host embed directory. Restart the local app to rebuild its binary.'
