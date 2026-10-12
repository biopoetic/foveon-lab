# Brings Sigma Photo Pro's review window (the one showing an X3F) to the front.
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
Add-Type @"
using System; using System.Runtime.InteropServices;
public static class WF {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int c);
  [DllImport("user32.dll")] public static extern void keybd_event(byte k, byte s, uint f, IntPtr e);
}
"@
$A = [System.Windows.Automation.AutomationElement]
$p = Get-Process SIGMA_PhotoPro6 -ErrorAction Stop
$c = New-Object System.Windows.Automation.PropertyCondition($A::ProcessIdProperty, $p.Id)
foreach ($w in $A::RootElement.FindAll([System.Windows.Automation.TreeScope]::Children, $c)) {
  if ($w.Current.Name -match '\.(X3F|x3f)$') {
    $h = [IntPtr]$w.Current.NativeWindowHandle
    # A tap of Alt lets SetForegroundWindow take the focus from another app.
    [WF]::keybd_event(0x12, 0, 0, [IntPtr]::Zero); [WF]::keybd_event(0x12, 0, 2, [IntPtr]::Zero)
    [WF]::ShowWindow($h, 9) | Out-Null; [WF]::SetForegroundWindow($h) | Out-Null
    "front: $($w.Current.Name)"; exit 0
  }
}
"ERROR no SPP review window (open the photo in SPP first)"; exit 1
