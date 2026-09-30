; CtrlFreak installer (Inno Setup script).
;
; Produces a single CtrlFreak-Setup.exe. Run it on any machine to make that PC
; remotely controllable through your hub at hub.ctrlfreak.us. The hub address is
; pre-filled, so a normal install is just: run it, sign in, done.
;
; v3 architecture (both roles ship in this one installer, one exe):
;   * SUPERVISOR: a SYSTEM Windows service "CtrlFreakSvc" that is always on and
;     keeps the agent alive (restarts it after a crash, a reboot, or the Restart
;     button). It never touches the screen, so it cannot black-screen.
;   * HELPER (the agent): a logon scheduled task "CtrlFreak Helper" that Windows
;     runs ELEVATED and inside your desktop session, which is what lets it
;     capture the screen and inject mouse/keyboard.
;
; The installer also adds a Windows Defender exclusion for the install folder
; (unsigned remote-control exes trip Defender's false-positive heuristics),
; turns off the UAC "secure desktop" dimming so UAC prompts are clickable
; remotely by the elevated agent, and stops the PC from sleeping on AC power so
; it stays reachable (the display can still turn off).
;
; GitHub builds this for you (see .github/workflows/build.yml).

#define AppName "CtrlFreak"
#define AppVersion "3.0.2"
#define DefaultHubWs "wss://hub.ctrlfreak.us/ws"
#define DefaultHubWeb "https://hub.ctrlfreak.us/"

[Setup]
AppName={#AppName}
AppVersion={#AppVersion}
DefaultDirName={autopf}\CtrlFreak
DisableProgramGroupPage=yes
PrivilegesRequired=admin
OutputDir=Output
OutputBaseFilename=CtrlFreak-Setup
UninstallDisplayName={#AppName}
WizardStyle=modern
SetupIconFile=..\branding\CtrlFreak.ico
ArchitecturesInstallIn64BitMode=x64compatible

[Types]
Name: "standard"; Description: "Standard (control this PC, plus a web shortcut)"
Name: "custom";   Description: "Custom"; Flags: iscustom

[Components]
Name: "host";   Description: "Controlled machine (remote into this PC)"; Types: standard custom; Flags: fixed
Name: "client"; Description: "Controller shortcut (open the web app)";   Types: standard custom

[Files]
Source: "..\dist\ctrlfreak-host.exe"; DestDir: "{app}"; Components: host; Flags: ignoreversion

[Code]
var
  HostPage:   TInputQueryWizardPage;
  ClientPage: TInputQueryWizardPage;

// Stop any running agent and clean up every artifact from older installs so a
// reinstall is clean. Runs before files are copied (avoids the "close all
// applications" prompt).
procedure Sh(cmd: String);
var
  rc: Integer;
begin
  Exec(ExpandConstant('{cmd}'), '/c ' + cmd, '', SW_HIDE, ewWaitUntilTerminated, rc);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  // Stop the v3 service and both possible old service names.
  Sh('sc stop CtrlFreakSvc');
  Sh('sc delete CtrlFreakSvc');
  Sh('sc stop CtrlFreakHost');
  Sh('sc delete CtrlFreakHost');
  // Remove old and current scheduled tasks (recreated fresh below).
  Sh('schtasks /delete /tn "CtrlFreak Host" /f');
  Sh('schtasks /delete /tn "CtrlFreak Helper" /f');
  // Remove the old Startup shortcut left by <=2.3 installs.
  DeleteFile(ExpandConstant('{commonstartup}\CtrlFreak Host.lnk'));
  // Finally, stop any running agent so its exe can be overwritten.
  Sh('taskkill /f /im ctrlfreak-host.exe');
  Result := '';
end;

procedure InitializeWizard;
begin
  HostPage := CreateInputQueryPage(wpSelectComponents,
    'Sign in', 'Connect this PC to your CtrlFreak hub',
    'The hub address is already filled in. Enter the account this machine should sign in with.');
  HostPage.Add('Hub address:', False);
  HostPage.Add('Username:', False);
  HostPage.Add('Password:', True);
  HostPage.Add('Machine name  (optional; blank uses this PC''s name):', False);
  HostPage.Values[0] := '{#DefaultHubWs}';

  ClientPage := CreateInputQueryPage(HostPage.ID,
    'Controller shortcut', 'Where is the web app?',
    'A desktop shortcut will open this address in your browser.');
  ClientPage.Add('Web app address:', False);
  ClientPage.Values[0] := '{#DefaultHubWeb}';
end;

function ShouldSkipPage(PageID: Integer): Boolean;
begin
  Result := False;
  if PageID = ClientPage.ID then
    Result := not WizardIsComponentSelected('client');
end;

function NextButtonClick(CurPageID: Integer): Boolean;
begin
  Result := True;
  if CurPageID = HostPage.ID then
  begin
    if (Trim(HostPage.Values[0]) = '') or (Trim(HostPage.Values[1]) = '') or (Trim(HostPage.Values[2]) = '') then
    begin
      MsgBox('Please fill in the hub address, username, and password.', mbError, MB_OK);
      Result := False;
    end;
  end;
end;

function JsonEsc(s: String): String;
begin
  StringChangeEx(s, '\', '\\', True);
  StringChangeEx(s, '"', '\"', True);
  Result := s;
end;

procedure WriteHostConfig();
var
  s: String;
begin
  s :=
    '{' + #13#10 +
    '  "relay": "' + JsonEsc(Trim(HostPage.Values[0])) + '",' + #13#10 +
    '  "user": "'  + JsonEsc(Trim(HostPage.Values[1])) + '",' + #13#10 +
    '  "pass": "'  + JsonEsc(HostPage.Values[2]) + '",' + #13#10 +
    '  "name": "'  + JsonEsc(Trim(HostPage.Values[3])) + '",' + #13#10 +
    '  "verify": true' + #13#10 +
    '}';
  SaveStringToFile(ExpandConstant('{app}\ctrlfreak-host.json'), s, False);
end;

procedure WriteClientShortcut();
var
  s: String;
begin
  s := '[InternetShortcut]' + #13#10 + 'URL=' + Trim(ClientPage.Values[0]) + #13#10;
  SaveStringToFile(ExpandConstant('{commondesktop}\CtrlFreak Controller.url'), s, False);
end;

// AddDefenderExclusion whitelists the install folder so Defender stops
// quarantining the unsigned agent exe (its heuristics flag remote-control apps).
procedure AddDefenderExclusion();
var
  rc: Integer;
begin
  Exec('powershell.exe',
    '-NoProfile -Command "Add-MpPreference -ExclusionPath ''' +
      ExpandConstant('{app}') + '''"',
    '', SW_HIDE, ewWaitUntilTerminated, rc);
end;

// DisableSecureDesktop makes UAC prompts appear on the normal desktop instead of
// the dimmed secure desktop, so the elevated agent can see and click them
// remotely. Without this, a UAC prompt freezes the remote session.
procedure DisableSecureDesktop();
begin
  Sh('reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System"' +
     ' /v PromptOnSecureDesktop /t REG_DWORD /d 0 /f');
end;

// KeepAwake stops the PC from sleeping or hibernating while on AC power, so the
// agent stays connected and the machine is always reachable. The display can
// still turn off to save the panel. Battery power is left alone, so a laptop
// still sleeps when it is unplugged.
procedure KeepAwake();
begin
  Sh('powercfg /change standby-timeout-ac 0');
  Sh('powercfg /change hibernate-timeout-ac 0');
end;

// CreateHelperTask registers the logon task that runs the agent (mode=helper)
// elevated and in the user's session. Windows handles the elevation and the
// session placement; that is what makes screen capture and input injection work.
procedure CreateHelperTask();
var
  exe, params: String;
  rc: Integer;
begin
  exe := ExpandConstant('{app}\ctrlfreak-host.exe');
  // /tr value is quoted; the exe path is inner-quoted (escaped \") because
  // "Program Files" has a space, then the -mode helper argument follows.
  params := '/create /f /tn "CtrlFreak Helper" /tr "\"' + exe +
            '\" -mode helper" /sc onlogon /rl highest';
  Exec('schtasks.exe', params, '', SW_HIDE, ewWaitUntilTerminated, rc);
end;

// InstallSupervisor registers and starts the SYSTEM service that keeps the
// helper alive. The exe's own -service verb handles the SCM registration and
// sets crash-recovery.
procedure InstallSupervisor();
var
  exe: String;
  rc: Integer;
begin
  exe := ExpandConstant('{app}\ctrlfreak-host.exe');
  Exec(exe, '-service install', '', SW_HIDE, ewWaitUntilTerminated, rc);
  Exec(exe, '-service start', '', SW_HIDE, ewWaitUntilTerminated, rc);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  rc: Integer;
begin
  if CurStep <> ssPostInstall then
    Exit;

  WriteHostConfig();
  AddDefenderExclusion();
  DisableSecureDesktop();
  KeepAwake();
  CreateHelperTask();
  InstallSupervisor();
  // Start the helper now so it works without waiting for a reboot/logon. The
  // supervisor would do this within seconds anyway.
  Exec('schtasks.exe', '/run /tn "CtrlFreak Helper"', '', SW_HIDE,
    ewWaitUntilTerminated, rc);

  if WizardIsComponentSelected('client') then
    WriteClientShortcut();
end;

// Remove the service, the task, and the running agent on uninstall.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Sh('sc stop CtrlFreakSvc');
  Sh('sc delete CtrlFreakSvc');
  Sh('schtasks /delete /tn "CtrlFreak Helper" /f');
  Sh('taskkill /f /im ctrlfreak-host.exe');
end;
