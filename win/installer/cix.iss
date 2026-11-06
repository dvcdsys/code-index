; cix.iss — Inno Setup script for the Windows app.
;
; Builds cix-<version>-x64-setup.exe: a per-user installer (no administrator
; prompt, ever) that drops the tray launcher into %LOCALAPPDATA%\Programs\cix.
; The server is NOT in here — like the macOS app, the launcher downloads the
; runtime (cix-server, cix, llama-server with CUDA) on first run into
; ~\.cix\runtime. See win/README.md.
;
; Compile (from the repo root, after win/scripts/build-app.sh has staged files):
;   ISCC /DAppVersion=0.1.0 win\installer\cix.iss
;
; Inputs, relative to this file's directory's ..\dist\stage:
;   cix-launcher.exe    the tray app
;   cix.ico             application icon

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#define AppName "cix"
#define Stage "..\dist\stage"

[Setup]
; Never change AppId: it is how a newer setup finds the install it replaces.
AppId={{6B0B1F3C-6C0E-4E1E-9E0B-2C7B1A5C1D01}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher=cix
AppPublisherURL=https://github.com/dvcdsys/code-index
DefaultDirName={localappdata}\Programs\cix
DefaultGroupName=cix
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\dist
OutputBaseFilename=cix-{#AppVersion}-x64-setup
SetupIconFile=..\Resources\cix.ico
UninstallDisplayIcon={app}\cix.ico
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
; The running launcher holds cix-launcher.exe open. Restart Manager closes it
; (the hidden tray window answers WM_CLOSE by quitting) and the [Run] entry
; below starts the new one.
CloseApplications=yes
RestartApplications=no
; This is what lets the launcher update itself: it starts this setup with
; /VERYSILENT and exits, and setup needs no confirmation from anyone.
UsePreviousTasks=yes
ChangesEnvironment=yes

[Tasks]
Name: "autostart"; Description: "Start the cix tray icon when I sign in"; GroupDescription: "Startup:"
Name: "addpath"; Description: "Add the cix command to my PATH"; GroupDescription: "Command line:"

[Files]
Source: "{#Stage}\cix-launcher.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#Stage}\cix.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\cix"; Filename: "{app}\cix-launcher.exe"; IconFilename: "{app}\cix.ico"

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "cix"; \
  ValueData: """{app}\cix-launcher.exe"""; Flags: uninsdeletevalue; Tasks: autostart
; The CLI lives in the runtime, behind the `current` junction, so the entry
; keeps working across server updates. Appended, never replacing.
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; \
  ValueData: "{olddata};%USERPROFILE%\.cix\runtime\current"; Check: NeedsPathEntry; Tasks: addpath

[Run]
; Interactive install: the usual "launch" checkbox on the last page.
Filename: "{app}\cix-launcher.exe"; Description: "Launch cix"; Flags: nowait postinstall skipifsilent
; Self-update: the launcher passes /RELAUNCH=1 so a silent install puts the user
; back where they were. An unattended silent install does not pop the app up.
Filename: "{app}\cix-launcher.exe"; Flags: nowait; Check: ShouldRelaunch

[UninstallRun]
; The server is a scheduled task this app created; leaving it would keep a
; server running from a runtime nothing manages any more.
Filename: "{sys}\schtasks.exe"; Parameters: "/End /TN cix-server"; Flags: runhidden; RunOnceId: "EndTask"
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM cix-server.exe /IM llama-server.exe"; Flags: runhidden; RunOnceId: "KillServer"
Filename: "{sys}\schtasks.exe"; Parameters: "/Delete /TN cix-server /F"; Flags: runhidden; RunOnceId: "DeleteTask"

[Code]
function ShouldRelaunch: Boolean;
begin
  Result := ExpandConstant('{param:RELAUNCH|0}') = '1';
end;

function NeedsPathEntry: Boolean;
var
  Existing: string;
begin
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', Existing) then
    Existing := '';
  Result := Pos('.cix\runtime\current', Existing) = 0;
end;

procedure RemovePathEntry;
var
  Existing, Entry: string;
  P: Integer;
begin
  Entry := ';%USERPROFILE%\.cix\runtime\current';
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', Existing) then
    Exit;
  P := Pos(Entry, Existing);
  if P > 0 then
  begin
    Delete(Existing, P, Length(Entry));
    RegWriteExpandStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', Existing);
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
    RemovePathEntry;
  if CurUninstallStep = usPostUninstall then
    if not UninstallSilent then
      if MsgBox('Also delete cix data (configuration, the runtime, your index and database) in ' +
                ExpandConstant('{%USERPROFILE}') + '\.cix ?', mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
        DelTree(ExpandConstant('{%USERPROFILE}') + '\.cix', True, True, True);
end;
