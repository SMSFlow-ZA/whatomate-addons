param([string]$Network='whatomate-synthetic-local_default')
$ErrorActionPreference='Stop'
if($Network -ne 'whatomate-synthetic-local_default'){throw 'Only the dedicated synthetic network is supported'}
$raw=& docker network inspect $Network
if($LASTEXITCODE -ne 0){throw 'Synthetic network inspection failed'}
$networkInfo=($raw|ConvertFrom-Json)[0]
$checked=0
foreach($entry in $networkInfo.Containers.PSObject.Properties){
    $rawContainer=& docker inspect $entry.Name
    if($LASTEXITCODE -ne 0){throw 'Synthetic container inspection failed'}
    $container=($rawContainer|ConvertFrom-Json)[0]
    if(-not $container.State.Running){continue}
    $aliases=@($container.NetworkSettings.Networks.$Network.Aliases)
    $project=$container.Config.Labels.'com.docker.compose.project'
    $service=$container.Config.Labels.'com.docker.compose.service'
    foreach($reserved in @('redis','app','db')){
        if($aliases -contains $reserved -and ($project -ne 'whatomate-synthetic-local' -or $service -ne $reserved)){
            throw "Synthetic network alias collision: reserved $reserved is supplied by $project/$service"
        }
    }
    if($project -eq 'whatomate-synthetic-rollback' -and $service -ne 'rollbackapp'){
        throw 'Only uniquely named rollbackapp may join the main synthetic network'
    }
    $checked++
}
Write-Output "Synthetic network isolation guard passed ($checked running containers checked)."
