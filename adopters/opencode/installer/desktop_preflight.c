/* Fixed read-only app preflight. No child launch, config/environment reads,
 * install, registry write or permission mutation. */
#define OA_PREFLIGHT_ONLY
#include "installer_host.c"
/* Burn runs this selected read-only package before the core. Headless/quiet
   Setup passes --quiet and never waits for input. Passive Setup passes
   --passive and receives the same named Retry/Cancel prompt as full UI.
   Unknown arguments refuse quietly. */
static BOOL preflight_arguments(LPCWSTR commandLine,BOOL *quiet) {
    *quiet=TRUE;
    if(!commandLine||!*commandLine){*quiet=FALSE;return TRUE;}
    int count=0;LPWSTR *arguments=CommandLineToArgvW(commandLine,&count);
    if(!arguments)return FALSE;
    BOOL known=FALSE;
    if(count==1){*quiet=FALSE;known=TRUE;}
    else if(count==2) {
        if(lstrcmpW(arguments[1],L"--passive")==0){*quiet=FALSE;known=TRUE;}
        else known=lstrcmpW(arguments[1],L"--quiet")==0;
    }
    LocalFree(arguments);return known;
}
static BOOL preflight_closed(const wchar_t *program,DESKTOP_DIAGNOSTIC *diagnostic) {
    DWORD attributes=GetFileAttributesW(program);
    if(attributes==INVALID_FILE_ATTRIBUTES) {
        DWORD error=GetLastError();
        if(error==ERROR_FILE_NOT_FOUND||error==ERROR_PATH_NOT_FOUND)return TRUE;
        desktop_diagnostic(diagnostic,L"desktop_attributes",error,HRESULT_FROM_WIN32(error));return FALSE;
    }
    wchar_t *path=HeapAlloc(GetProcessHeap(),0,32768*sizeof(wchar_t));OA_NATIVE_FILE_ID desktop;OA_NATIVE_FILE held={0};
    if(!path){desktop_diagnostic(diagnostic,L"guard_scratch_allocation",ERROR_NOT_ENOUGH_MEMORY,E_OUTOFMEMORY);return FALSE;}
    BOOL ok=desktop_identity_fact(program,&desktop,&held,diagnostic)&&desktop_processes_closed_fact(&desktop,held.file,path,diagnostic);
    oa_native_close(&held);HeapFree(GetProcessHeap(),0,path);return ok;
}
static BOOL preflight_identity_indeterminate(const DESKTOP_DIAGNOSTIC *diagnostic) {
    return diagnostic->processId&&diagnostic->stage&&
        lstrcmpW(diagnostic->stage,L"desktop_process_identity_unread")==0;
}
static BOOL preflight_retry(BOOL quiet,const DESKTOP_DIAGNOSTIC *diagnostic) {
    /* Burn supplies no owner HWND. Windows decides whether to grant foreground. */
    if(quiet)return FALSE;
    if(preflight_identity_indeterminate(diagnostic))return MessageBoxW(NULL,
        L"Setup could not verify whether OpenCode is closed. Close all OpenCode windows, then choose Retry to check again. Choose Cancel if the check still fails.",
        L"Check that OpenCode is closed",MB_RETRYCANCEL|MB_ICONWARNING|MB_SETFOREGROUND)==IDRETRY;
    return diagnostic->win32==ERROR_BUSY&&MessageBoxW(NULL,
        L"Close OpenCode to finish installing. Close all OpenCode windows, then choose Retry. Cancel stops installation before the core and application packages run.",
        L"Close OpenCode to finish installing",MB_RETRYCANCEL|MB_ICONWARNING|MB_SETFOREGROUND)==IDRETRY;
}
/* How this process holds administrator rights. An administrator with User
   Account Control on has a split token and is elevated only by Run as
   administrator (PREFLIGHT_ELEVATED). With UAC off, or as the built-in
   Administrator, the token is always elevated and has no ordinary half
   (PREFLIGHT_ALWAYS_ELEVATED): OpenCode with OA can never install there. */
enum {PREFLIGHT_ORDINARY,PREFLIGHT_ELEVATED,PREFLIGHT_ALWAYS_ELEVATED};
static int preflight_elevation(BOOL elevated,TOKEN_ELEVATION_TYPE type) {
    if(!elevated)return PREFLIGHT_ORDINARY;
    return type==TokenElevationTypeFull?PREFLIGHT_ELEVATED:PREFLIGHT_ALWAYS_ELEVATED;
}
/* The person's own account at ordinary rights installs OpenCode with OA;
   an elevated Setup is told why, not that OpenCode is unsupported. */
static const wchar_t *preflight_refusal(int elevation) {
    if(elevation==PREFLIGHT_ALWAYS_ELEVATED)
        return L"OpenCode with OA installs only for an account with User Account Control on: turn it on or use a standard account.";
    return elevation==PREFLIGHT_ELEVATED
        ? L"OpenCode with OA installs for your own Windows account without administrator rights. Close Setup and start it again normally, without Run as administrator. Nothing was installed for OpenCode."
        : L"OpenCode with OA supports the official Desktop " OA_DESKTOP_VERSIONS_TEXT L" in its standard per-user location. An unsupported version, registration, custom/redirected location, preexisting unregistered destination or process identity was found. Existing OpenCode files and settings were preserved; the host installer has not run.";
}
/* A parent may capture this inherited standard handle. No file is opened and
   unavailable output never changes the guard's admission or exit status. All
   stages are fixed ASCII identifiers owned by this native receiver. */
static void preflight_diagnostic(const DESKTOP_DIAGNOSTIC *diagnostic) {
    char line[256];
    HANDLE output=GetStdHandle(STD_ERROR_HANDLE);
    if(!output||output==INVALID_HANDLE_VALUE)return;
    HRESULT formatted=diagnostic->processId?
        StringCchPrintfA(line,sizeof(line),"OpenCode preflight stage=%ls win32=%lu hresult=0x%08lx pid=%lu\r\n",
            diagnostic->stage,diagnostic->win32,(unsigned long)diagnostic->hresult,diagnostic->processId):
        StringCchPrintfA(line,sizeof(line),"OpenCode preflight stage=%ls win32=%lu hresult=0x%08lx\r\n",
            diagnostic->stage,diagnostic->win32,(unsigned long)diagnostic->hresult);
    if(FAILED(formatted))return;
    DWORD length=(DWORD)strlen(line),offset=0;
    while(offset<length) {
        DWORD written=0;
        if(!WriteFile(output,line+offset,length-offset,&written,NULL)||!written)return;
        offset+=written;
    }
}
static int preflight_main(LPCWSTR commandLine) {
    HANDLE user=NULL,primary=NULL;TOKEN_FACTS userFacts,hostFacts;wchar_t program[32768];BOOL ok=FALSE,quiet=TRUE,busy=FALSE;int elevation=PREFLIGHT_ORDINARY;
    DESKTOP_DIAGNOSTIC diagnostic={L"arguments",ERROR_INVALID_PARAMETER,E_INVALIDARG,0};
    if(!preflight_arguments(commandLine,&quiet))goto done;
    if(!OpenProcessToken(GetCurrentProcess(),TOKEN_QUERY,&primary)) {
        DWORD error=GetLastError();desktop_diagnostic(&diagnostic,L"primary_token",error,HRESULT_FROM_WIN32(error));goto done;
    }
    if(!token_facts(primary,&hostFacts)) {
        DWORD error=GetLastError();desktop_diagnostic(&diagnostic,L"primary_token_facts",error,error?HRESULT_FROM_WIN32(error):E_FAIL);goto done;
    }
    {
        TOKEN_ELEVATION_TYPE type=TokenElevationTypeDefault;DWORD size=0;
        if(!GetTokenInformation(primary,TokenElevationType,&type,sizeof(type),&size))type=TokenElevationTypeDefault;
        elevation=preflight_elevation(hostFacts.elevated,type);
    }
    SetLastError(ERROR_SUCCESS);
    user=user_token();
    if(!user) {
        DWORD error=GetLastError();desktop_diagnostic(&diagnostic,L"user_token",error,error?HRESULT_FROM_WIN32(error):E_ACCESSDENIED);goto done;
    }
    if(!token_facts(user,&userFacts)) {
        DWORD error=GetLastError();desktop_diagnostic(&diagnostic,L"user_token_facts",error,error?HRESULT_FROM_WIN32(error):E_FAIL);goto done;
    }
    if(!same_execution_user(&hostFacts,&userFacts)) {
        desktop_diagnostic(&diagnostic,L"execution_user_policy",ERROR_ACCESS_DENIED,E_ACCESSDENIED);goto done;
    }
    {
        desktop_diagnostic(&diagnostic,L"desktop_closure",0,S_OK);
        do {
            if(!desktop_check_diagnostic(user,FALSE,program,&diagnostic)){ok=FALSE;busy=FALSE;break;}
            ok=preflight_closed(program,&diagnostic);busy=!ok&&diagnostic.win32==ERROR_BUSY;
        }while(!ok&&preflight_retry(quiet,&diagnostic));
    }
done:if(primary)CloseHandle(primary);if(user)CloseHandle(user);
    if(ok)desktop_diagnostic(&diagnostic,L"preflight_complete",ERROR_SUCCESS,S_OK);
    preflight_diagnostic(&diagnostic);
    if(!ok&&!quiet&&!busy&&!preflight_identity_indeterminate(&diagnostic))MessageBoxW(NULL,preflight_refusal(elevation),
        L"OpenCode with OA compatibility check",MB_OK|MB_ICONERROR|MB_SETFOREGROUND);
    return ok?0:busy?ERROR_BUSY:ERROR_INSTALL_FAILURE;
}
int WINAPI wWinMain(HINSTANCE instance,HINSTANCE previous,LPWSTR arguments,int show) {
    (void)instance;(void)previous;(void)arguments;(void)show;
    return preflight_main(GetCommandLineW());
}
