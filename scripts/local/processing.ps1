param([string]$HostSource=(Join-Path $PSScriptRoot '../../../whatomate-addons-host'), [switch]$AcceptSyntheticSignatureWindow)
$ErrorActionPreference='Stop'
if(-not $AcceptSyntheticSignatureWindow){throw 'Explicit local test signature acceptance is required; this is not production approval.'}
$addonSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$privateRuntime=Join-Path ([IO.Path]::GetFullPath($HostSource)) '.work/synthetic-local'
$image='sha256:deeb600b4673055387c333172c4a420d5ee1ad26fbcb431db92cd8912ff0268b'
& docker run --rm --network none --entrypoint sh -v whatomate-addons-clamav:/defs:ro $image -c 'for f in /defs/*.cvd; do sigtool --info "$f" || exit 1; done'
if($LASTEXITCODE -ne 0){throw 'Definition signature validation failed'}
$profile=@{
 ProfileID='synthetic-local-signature-window-v1';WorkerID='synthetic-local-documents';
 Supervisor=@{
  JobRoot='/var/lib/docker/volumes/whatomate-addons-docjobs/_data/synthetic-local';DockerPath='/usr/local/bin/docker';Image=$image;
  DefinitionsRoot='/var/lib/docker/volumes/whatomate-addons-clamav/_data';DefinitionsHash='0ceb5d53a3980183d880d3e81f68d13bcb1a9ad5bd3ca673e23b3ea6f7350816';
  MaxConcurrent=1;MemoryMiB=1024;CPUs=1;PIDs=64;TmpfsMiB=128;Timeout=180000000000;
  Worker=@{QuarantineRoot='/tmp/quarantine';DatabasePath='/var/lib/clamav';ScannerRevision='clamav-1.4.4-official-corpus-synthetic-local';ExtractionRevision='poppler-25.12.0';OCRRevision='tesseract-5.5.1-eng';
   Tools=@{ClamScan='/usr/bin/clamscan';PDFInfo='/usr/bin/pdfinfo';PDFToText='/usr/bin/pdftotext';PDFToPPM='/usr/bin/pdftoppm';Tesseract='/usr/bin/tesseract'};
   SignaturesValidUntil=[DateTime]::UtcNow.AddHours(1).ToString('o');MaxInputBytes=20971520;MaxPages=100;MaxPageTextBytes=1048576;MaxTotalTextBytes=16777216;MaxRasterBytes=16777216;CommandTimeout=30000000000;JobTimeout=120000000000
  }
 }
}
$profile|ConvertTo-Json -Depth 9|Set-Content -LiteralPath (Join-Path $privateRuntime 'knowledge-controller.json') -Encoding utf8NoBOM
& docker volume create whatomate-synthetic-local-controllerconfig | Out-Null
& docker run --rm --user 0:0 --network none --entrypoint sh -v "${privateRuntime}:/source:ro" -v whatomate-synthetic-local-controllerconfig:/config -v whatomate-addons-docjobs:/jobs $image -c 'cp /source/knowledge-controller.json /config/knowledge-controller.json && chmod 0400 /config/knowledge-controller.json && mkdir -p /jobs/synthetic-local && chmod 0700 /jobs/synthetic-local'
if($LASTEXITCODE -ne 0){throw 'Controller private configuration failed'}
& docker compose --env-file (Join-Path $privateRuntime 'compose.env') -f (Join-Path $addonSource 'deploy/local/compose.yaml') --profile processing up -d controller
if($LASTEXITCODE -ne 0){throw 'Controller start failed'}
Write-Output 'Actual offline PDF scan/extraction controller started. Synthetic signature window expires in one hour.'
