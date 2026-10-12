param([string]$dir, [string]$file)
# Fills SPP's open "Save Images As" dialog (folder + file name) and clicks OK.
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
Add-Type @"
using System; using System.Runtime.InteropServices;
public static class W3 {
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr SendMessageW(IntPtr h, uint m, IntPtr w, string l);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint x, uint y, int d, IntPtr e);
}
"@
[W3]::SetProcessDPIAware() | Out-Null
$A = [System.Windows.Automation.AutomationElement]; $TS = [System.Windows.Automation.TreeScope]
$dlg = $null
for ($i = 0; $i -lt 50 -and -not $dlg; $i++) {
  $dlg = $A::RootElement.FindAll($TS::Children, [System.Windows.Automation.Condition]::TrueCondition) | ? { $_.Current.Name -eq 'Save Images As' } | Select -First 1
  if (-not $dlg) { $dlg = $A::RootElement.FindAll($TS::Descendants, (New-Object System.Windows.Automation.PropertyCondition($A::NameProperty, 'Save Images As'))) | Select -First 1 }
  if (-not $dlg) { Start-Sleep -Milliseconds 200 }
}
if (-not $dlg) { "ERROR no Save Images As dialog"; exit 1 }
$all = $dlg.FindAll($TS::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
$edits = $all | ? { $_.Current.ClassName -match 'EDIT' }
$fileEdit = $edits | ? { $_.Current.Name -match '\.(tif|tiff|jpg)$' } | Select -First 1
$dirEdit = $edits | ? { $_.Current.Name -match '^[A-Za-z]:\\' } | Select -First 1
if (-not $fileEdit -or -not $dirEdit) { "ERROR edits not found"; exit 1 }
[W3]::SendMessageW([IntPtr]$dirEdit.Current.NativeWindowHandle, 0x000C, [IntPtr]::Zero, $dir) | Out-Null
[W3]::SendMessageW([IntPtr]$fileEdit.Current.NativeWindowHandle, 0x000C, [IntPtr]::Zero, $file) | Out-Null
Start-Sleep -Milliseconds 300
$ok = $all | ? { $_.Current.Name -eq 'OK' } | Select -First 1
$r = $ok.Current.BoundingRectangle
$hwnd = [IntPtr]$dlg.Current.NativeWindowHandle
# The first click can just activate the dialog: click until it closes.
for ($try = 1; $try -le 3; $try++) {
  [W3]::SetForegroundWindow($hwnd) | Out-Null
  Start-Sleep -Milliseconds 250
  [W3]::SetCursorPos([int]($r.X + $r.Width/2), [int]($r.Y + $r.Height/2)) | Out-Null
  Start-Sleep -Milliseconds 120
  [W3]::mouse_event(2,0,0,0,[IntPtr]::Zero); Start-Sleep -Milliseconds 60; [W3]::mouse_event(4,0,0,0,[IntPtr]::Zero)
  $open = $true
  for ($i = 0; $i -lt 15 -and $open; $i++) {
    Start-Sleep -Milliseconds 200
    $open = [bool]($A::RootElement.FindAll($TS::Descendants, (New-Object System.Windows.Automation.PropertyCondition($A::NameProperty, 'Save Images As'))) | Select -First 1)
  }
  if (-not $open) { "OK clicked: $dir\$file"; exit 0 }
}
"ERROR the Save dialog did not close"; exit 1
