/* Inert preflight argument and message receiving: no registry, config,
 * install or window effect. */
#include <windows.h>
#include <wchar.h>
static unsigned prompts,busy_prompts,identity_prompts,refusals;static int answer=IDCANCEL;
static int WINAPI fixture_message(HWND window,LPCWSTR text,LPCWSTR title,UINT flags) {
    prompts++;
    if(window!=NULL)return 0;
    if(wcsstr(text,L"Close OpenCode to finish installing")&&wcsstr(title,L"Close OpenCode")
            &&flags==(MB_RETRYCANCEL|MB_ICONWARNING|MB_SETFOREGROUND)){busy_prompts++;return answer;}
    if(wcsstr(text,L"could not verify whether OpenCode is closed")&&lstrcmpW(title,L"Check that OpenCode is closed")==0
            &&flags==(MB_RETRYCANCEL|MB_ICONWARNING|MB_SETFOREGROUND)){identity_prompts++;return answer;}
    if(wcsstr(text,L"official Desktop 1.18.34 or 1.19.5")&&lstrcmpW(title,L"OpenCode with OA compatibility check")==0
            &&flags==(MB_OK|MB_ICONERROR|MB_SETFOREGROUND)){refusals++;return IDOK;}
    return 0;
}
/* Force an early compatibility refusal without examining a real profile. */
static BOOL WINAPI fixture_open_token(HANDLE process,DWORD access,PHANDLE token) {
    (void)process;(void)access;(void)token;
    SetLastError(ERROR_ACCESS_DENIED);
    return FALSE;
}
#define MessageBoxW fixture_message
#define OpenProcessToken fixture_open_token
#ifdef OA_PREFLIGHT_ARGUMENT_FIXTURE
#define wWinMain oa_preflight_entry
#endif
#include "desktop_preflight.c"
#ifdef OA_PREFLIGHT_ARGUMENT_FIXTURE
#undef wWinMain
#endif
#undef MessageBoxW
#undef OpenProcessToken
#include <stdio.h>
#define CHECK(value) do {if(!(value)){fprintf(stderr,"preflight quiet failed at line %d\n",__LINE__);return 1;}} while(0)
#ifdef OA_PREFLIGHT_ARGUMENT_FIXTURE
/* The real Windows GUI entrypoint and argument parser, with no receiving,
   registry or desktop calls. PowerShell's actual Start-Process owns its tail. */
int WINAPI wWinMain(HINSTANCE instance,HINSTANCE previous,LPWSTR arguments,int show) {
    (void)instance;(void)previous;(void)arguments;(void)show;
    BOOL quiet=FALSE;BOOL accepted=preflight_arguments(GetCommandLineW(),&quiet);
    DESKTOP_DIAGNOSTIC diagnostic={accepted&&quiet?L"arguments_accepted":L"arguments_refused",accepted&&quiet?0:ERROR_INVALID_PARAMETER,accepted&&quiet?S_OK:E_INVALIDARG,0};
    preflight_diagnostic(&diagnostic);return accepted&&quiet?0:ERROR_INSTALL_FAILURE;
}
#else
static BOOL captured_diagnostic(const DESKTOP_DIAGNOSTIC *diagnostic,BOOL entrypoint,const char *expected) {
    HANDLE read=NULL,write=NULL,original=GetStdHandle(STD_ERROR_HANDLE);
    if(!CreatePipe(&read,&write,NULL,0))return FALSE;
    if(!SetStdHandle(STD_ERROR_HANDLE,write)){CloseHandle(read);CloseHandle(write);return FALSE;}
    BOOL status=TRUE;
    if(entrypoint)status=preflight_main(L"preflight.exe --unknown")==ERROR_INSTALL_FAILURE;
    else preflight_diagnostic(diagnostic);
    SetStdHandle(STD_ERROR_HANDLE,original);CloseHandle(write);
    char received[256];DWORD size=0;
    BOOL ok=ReadFile(read,received,sizeof(received)-1,&size,NULL);CloseHandle(read);
    if(!ok||size>=sizeof(received))return FALSE;
    received[size]=0;return status&&strcmp(received,expected)==0;
}
int wmain(void) {
    CHECK(captured_diagnostic(NULL,TRUE,"OpenCode preflight stage=arguments win32=87 hresult=0x80070057\r\n")&&prompts==0);
    DESKTOP_DIAGNOSTIC output={L"primary_token",ERROR_ACCESS_DENIED,E_ACCESSDENIED,0};
    CHECK(captured_diagnostic(&output,FALSE,"OpenCode preflight stage=primary_token win32=5 hresult=0x80070005\r\n"));
    output.stage=L"desktop_process_running";output.win32=ERROR_BUSY;output.hresult=HRESULT_FROM_WIN32(ERROR_BUSY);
    CHECK(captured_diagnostic(&output,FALSE,"OpenCode preflight stage=desktop_process_running win32=170 hresult=0x800700aa\r\n"));
    desktop_diagnostic(&output,L"desktop_process_identity_unread",ERROR_NOT_SUPPORTED,HRESULT_FROM_WIN32(ERROR_NOT_SUPPORTED));
    output.processId=1234;
    CHECK(captured_diagnostic(&output,FALSE,"OpenCode preflight stage=desktop_process_identity_unread win32=50 hresult=0x80070032 pid=1234\r\n"));
    desktop_diagnostic(&output,L"preflight_complete",0,S_OK);
    CHECK(captured_diagnostic(&output,FALSE,"OpenCode preflight stage=preflight_complete win32=0 hresult=0x00000000\r\n"));
    HANDLE originalError=GetStdHandle(STD_ERROR_HANDLE);
    CHECK(SetStdHandle(STD_ERROR_HANDLE,INVALID_HANDLE_VALUE));preflight_diagnostic(&output);
    CHECK(SetStdHandle(STD_ERROR_HANDLE,originalError));
    BOOL quiet=FALSE;
    CHECK(preflight_arguments(NULL,&quiet)&&!quiet);
    CHECK(preflight_arguments(L"",&quiet)&&!quiet);
    CHECK(preflight_arguments(L"preflight.exe",&quiet)&&!quiet);
    CHECK(preflight_arguments(L"preflight.exe --quiet",&quiet)&&quiet);
    CHECK(preflight_arguments(L"preflight.exe --quiet ",&quiet)&&quiet);
    CHECK(preflight_arguments(L"\"C:\\owned fixture\\preflight.exe\" \t\"--quiet\" \t",&quiet)&&quiet);
    CHECK(preflight_arguments(L"preflight.exe --passive",&quiet)&&!quiet);
    CHECK(preflight_arguments(L"preflight.exe \t\"--passive\" \t",&quiet)&&!quiet);
    DESKTOP_DIAGNOSTIC diagnostic={L"desktop_process_running",ERROR_BUSY,HRESULT_FROM_WIN32(ERROR_BUSY),0};
    CHECK(!preflight_retry(TRUE,&diagnostic)&&prompts==0);
    answer=IDRETRY;CHECK(preflight_retry(FALSE,&diagnostic)&&prompts==1&&busy_prompts==1);
    answer=IDCANCEL;CHECK(!preflight_retry(FALSE,&diagnostic)&&prompts==2&&busy_prompts==2);
    diagnostic.win32=ERROR_ACCESS_DENIED;CHECK(!preflight_retry(FALSE,&diagnostic)&&prompts==2);
    CHECK(preflight_main(L"preflight.exe --quiet")==ERROR_INSTALL_FAILURE&&prompts==2);
    CHECK(preflight_main(L"preflight.exe")==ERROR_INSTALL_FAILURE&&prompts==3&&refusals==1);
    CHECK(preflight_main(L"preflight.exe --passive")==ERROR_INSTALL_FAILURE&&prompts==4&&refusals==2);
    desktop_diagnostic(&diagnostic,L"desktop_process_identity_unread",ERROR_NOT_SUPPORTED,HRESULT_FROM_WIN32(ERROR_NOT_SUPPORTED));
    CHECK(!preflight_retry(FALSE,&diagnostic)&&prompts==4); /* no candidate PID */
    diagnostic.processId=1234;
    CHECK(!preflight_retry(TRUE,&diagnostic)&&prompts==4);
    answer=IDRETRY;CHECK(preflight_retry(FALSE,&diagnostic)&&prompts==5&&identity_prompts==1);
    answer=IDCANCEL;CHECK(!preflight_retry(FALSE,&diagnostic)&&prompts==6&&identity_prompts==2);
    wchar_t program[32768];CHECK(GetModuleFileNameW(NULL,program,32768)>0);
    CHECK(!preflight_closed(program,&diagnostic));CHECK(diagnostic.win32==ERROR_BUSY);
    const wchar_t *refused[]={L"preflight.exe --quiet --other",L"preflight.exe -quiet",L"preflight.exe --unknown",L"preflight.exe \"--quiet --other\"",L"preflight.exe --quiet \"\"",L"preflight.exe --passive --quiet",L"preflight.exe --quietx"};
    for(unsigned i=0;i<sizeof(refused)/sizeof(refused[0]);i++){quiet=FALSE;CHECK(!preflight_arguments(refused[i],&quiet)&&quiet);}
    /* Run as administrator with UAC on: a split token, elevated half. */
    CHECK(preflight_elevation(TRUE,TokenElevationTypeFull)==PREFLIGHT_ELEVATED);
    /* UAC off or the built-in Administrator: elevated with no split. */
    CHECK(preflight_elevation(TRUE,TokenElevationTypeDefault)==PREFLIGHT_ALWAYS_ELEVATED);
    CHECK(preflight_elevation(FALSE,TokenElevationTypeLimited)==PREFLIGHT_ORDINARY);
    CHECK(preflight_elevation(FALSE,TokenElevationTypeDefault)==PREFLIGHT_ORDINARY);
    CHECK(wcsstr(preflight_refusal(PREFLIGHT_ELEVATED),L"without Run as administrator")!=NULL);
    CHECK(lstrcmpW(preflight_refusal(PREFLIGHT_ALWAYS_ELEVATED),L"OpenCode with OA installs only for an account with User Account Control on: turn it on or use a standard account.")==0);
    CHECK(wcsstr(preflight_refusal(PREFLIGHT_ORDINARY),L"official Desktop 1.18.34 or 1.19.5")!=NULL);
    printf("preflight quiet PASS: --quiet and unknown arguments never show a box; elevated Setup and UAC off are named\n");
    return 0;
}
#endif
