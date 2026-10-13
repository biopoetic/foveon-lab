param([string]$path)
# Opens $path in Sigma Photo Pro: if SPP runs, asks every window of it to
# close (like clicking X — never a forced kill), waits, then starts SPP with
# the file and waits for its review window.
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
Add-Type @"
using System; using System.Runtime.InteropServices;
public static class WO { [DllImport("user32.dll")] public static extern bool PostMessageW(IntPtr h, uint m, IntPtr w, IntPtr l); }
"@
$A = [System.Windows.Automation.AutomationElement]; $TS = [System.Windows.Automation.TreeScope]
$exe = Join-Path ${env:ProgramFiles} 'SIGMA\SIGMA Photo Pro 6\SIGMA_PhotoPro6.exe'
# closeSPP asks every SPP window to close (like clicking X), up to 60 s.
function closeSPP {
  for ($i = 0; $i -lt 60; $i++) {
    $p = Get-Process SIGMA_PhotoPro6 -ErrorAction SilentlyContinue | Select -First 1
    if (-not $p) { return $true }
    if ($i % 5 -eq 0) {
      $c = New-Object System.Windows.Automation.PropertyCondition($A::ProcessIdProperty, $p.Id)
      foreach ($w in $A::RootElement.FindAll($TS::Children, $c)) { [WO]::PostMessageW([IntPtr]$w.Current.NativeWindowHandle, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null }
    }
    Start-Sleep 1
  }
  return -not (Get-Process SIGMA_PhotoPro6 -ErrorAction SilentlyContinue)
}
$name = Split-Path -Leaf $path
# A start right after a close can come up without the photo (the old
# process still letting go): wait a moment first, and if the photo doesn't
# open, close SPP and try once more.
for ($attempt = 1; $attempt -le 2; $attempt++) {
  if (-not (closeSPP)) { "ERROR SPP did not close (a dialog may be asking something)"; exit 1 }
  Start-Sleep 4
  Start-Process -FilePath $exe -ArgumentList ('"' + $path + '"')
  for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep 1
    $p = Get-Process SIGMA_PhotoPro6 -ErrorAction SilentlyContinue | Select -First 1
    if (-not $p) { continue }
    $c = New-Object System.Windows.Automation.PropertyCondition($A::ProcessIdProperty, $p.Id)
    if ($A::RootElement.FindAll($TS::Children, $c) | ? { $_.Current.Name -eq $name }) { Start-Sleep 3; "opened $name"; exit 0 }
  }
}
"ERROR $name did not open"; exit 1
