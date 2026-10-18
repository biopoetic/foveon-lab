# Exports the SPP calibration ladders still missing for the older cameras.
#
# SPP takes the screen while this runs (one photo at a time, ~9 min for an A
# ladder, ~4 min for a fill ladder), so start it and leave the machine alone.
# It is resumable: exports that already exist are skipped, so stopping and
# re-running is safe.
#
#   cd C:\Users\Korisnik\PycharmProjects\foveon-lab
#   .\calib-batch.ps1
#
# Afterwards the analyse step is entirely offline:
#   .\foveon-lab.exe analyze -dir "$env:USERPROFILE\Desktop\foveon-calibration" -photos ".\testdata"

$ErrorActionPreference = 'Continue'
$exe = Join-Path $PSScriptRoot 'foveon-lab.exe'
$dir = Join-Path ([Environment]::GetFolderPath('Desktop')) 'foveon-calibration'

if (-not (Test-Path $exe)) { throw "build it first: go build -o foveon-lab.exe ." }
if (-not (Test-Path $dir)) { throw "calibration folder not found: $dir" }

# Keep a transcript: when an export fails the message must survive the window.
$log = Join-Path $dir ("calib-batch-" + (Get-Date -Format 'yyyyMMdd-HHmmss') + ".log")
Start-Transcript -Path $log | Out-Null
Write-Host ("Log: " + $log) -ForegroundColor DarkGray

# The three SD1 fill photos still need their neutral reference; the cameras
# need a full A ladder so each family can be measured against SPP, and then
# their own Fill Light ladder (the fill model is sensor-specific). The X
# steps combine the colour adjust with a colour mode / white balance, which
# is the only way to see where SPP applies R/G/B.
$fill = 'B14,A16,A17,B15,B16'
$combo = 'X01,X02,X03,X04'
$jobs = @(
	# The combo steps first: they are quick, and they are what the next
	# measurement needs. The sd-Quattro sample is left out: SPP needs minutes
	# per setting on it and it hung the previous run; the dp2-Quattro covers
	# that family.
	@{ p = 'C:\Users\Korisnik\Desktop\SD1\SDIM0031.X3F';                    s = $combo },
	@{ p = 'C:\Users\Korisnik\Desktop\SD1\SDIM0138.X3F';                    s = $combo },
	@{ p = 'C:\Users\Korisnik\Desktop\SD15\SDIM1021.X3F';                   s = $combo },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD14\20120818-SDIM4579.X3F'); s = $combo },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD9\RAW_SIGMA_SD9_SRGB.X3F'); s = 'A' },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD10\RAW_SIGMA_SD10.X3F');    s = 'A' },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD14\20120818-SDIM4579.X3F'); s = 'A' },
	@{ p = (Join-Path $PSScriptRoot 'testdata\dp2-Quattro\SDIM4388.X3F');   s = 'A' },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD9\RAW_SIGMA_SD9_SRGB.X3F'); s = $fill },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD10\RAW_SIGMA_SD10.X3F');    s = $fill },
	@{ p = (Join-Path $PSScriptRoot 'testdata\SD14\20120818-SDIM4579.X3F'); s = $fill },
	@{ p = (Join-Path $PSScriptRoot 'testdata\dp2-Quattro\SDIM4388.X3F');   s = $fill }
)

# The X presets must be in SPP's list before they can be exported (SPP closed).
Write-Host "Adding the [CAL] presets to SPP (this closes it; its settings are backed up) ..." -ForegroundColor Cyan
Get-Process SIGMA_PhotoPro6 -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 3
& $exe calibrate -dir $dir -spp

$start = Get-Date
foreach ($j in $jobs) {
	if (-not (Test-Path $j.p)) { Write-Host ("missing photo, skipping: " + $j.p) -ForegroundColor Yellow; continue }
	Write-Host ("=== " + (Split-Path -Leaf $j.p) + "  steps " + $j.s + "  (" + (Get-Date -Format 'HH:mm:ss') + ")") -ForegroundColor Cyan
	& $exe calibrate -auto -photo $j.p -steps $j.s -dir $dir
	Write-Host ("    exit " + $LASTEXITCODE + " after " + [int]((Get-Date) - $start).TotalMinutes + " min") -ForegroundColor DarkGray
}

Write-Host ("All done in " + [int]((Get-Date) - $start).TotalMinutes + " min.") -ForegroundColor Green
Write-Host ("Exports: " + $dir + "\exports")
Write-Host ("Log: " + $log)
Stop-Transcript | Out-Null
