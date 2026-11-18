/* Owned inert child only: no installer, profile, registration or OA process. */
#include <windows.h>
#include <msiquery.h>
#include <stdio.h>
#include <aclapi.h>
#include <stdint.h>
#include <user_token.h>
static TOKEN_FACTS expectedReceiving;
static BOOL crossSessionObserved;
static BOOL WINAPI fixture_create_as_user(HANDLE token,LPCWSTR program,LPWSTR command,LPSECURITY_ATTRIBUTES processSecurity,LPSECURITY_ATTRIBUTES threadSecurity,BOOL inherit,DWORD flags,LPVOID environment,LPCWSTR cwd,LPSTARTUPINFOW start,LPPROCESS_INFORMATION process) {
    (void)program;(void)command;(void)processSecurity;(void)threadSecurity;(void)environment;(void)cwd;(void)process;
    TOKEN_FACTS received;
    crossSessionObserved=!inherit&&!(flags&EXTENDED_STARTUPINFO_PRESENT)&&!(start->dwFlags&STARTF_USESTDHANDLES)
        &&token_facts(token,&received)&&EqualSid(received.sid,expectedReceiving.sid)&&received.session==expectedReceiving.session
        &&received.authentication.HighPart==expectedReceiving.authentication.HighPart&&received.authentication.LowPart==expectedReceiving.authentication.LowPart;
    SetLastError(ERROR_CANCELLED);return FALSE;
}
static DWORD observedError,observedWait,observedExit;
static wchar_t observedStage[64],observedCategory[64];
static BOOL timeoutOnce,timeoutTriggered;
static ULONGLONG fixture_tick(void) { return GetTickCount64()+(timeoutTriggered?120001:0); }
static DWORD WINAPI fixture_wait(HANDLE process,DWORD milliseconds) {
    if(timeoutOnce&&milliseconds==100){timeoutOnce=FALSE;timeoutTriggered=TRUE;return WAIT_TIMEOUT;}
    return WaitForSingleObject(process,milliseconds);
}
static wchar_t observedFailure[1024];
static int WINAPI fixture_message(MSIHANDLE session,INSTALLMESSAGE kind,MSIHANDLE record) {
    (void)session;DWORD size=64;
    if(kind==INSTALLMESSAGE_ERROR){
        wchar_t format[8];DWORD capacity=8,text=1024;
        if(MsiRecordGetFieldCount(record)==1&&MsiRecordGetStringW(record,0,format,&capacity)==ERROR_SUCCESS&&!wcscmp(format,L"[1]"))
            MsiRecordGetStringW(record,1,observedFailure,&text);
        return 0;
    }
    if(MsiRecordGetFieldCount(record)==4){
        MsiRecordGetStringW(record,1,observedStage,&size);
        observedError=(DWORD)MsiRecordGetInteger(record,2);
        observedWait=(DWORD)MsiRecordGetInteger(record,3);
        observedExit=(DWORD)MsiRecordGetInteger(record,4);
    }
    if(MsiRecordGetFieldCount(record)==1){
        wchar_t format[128];DWORD capacity=128;
        if(MsiRecordGetStringW(record,0,format,&capacity)==ERROR_SUCCESS
           &&!wcscmp(format,L"OA OpenCode preparation failure category=[1]"))
            MsiRecordGetStringW(record,1,observedCategory,&size);
    }
    return 0;
}
#define WaitForSingleObject fixture_wait
#define GetTickCount64 fixture_tick
#define CreateProcessAsUserW fixture_create_as_user
#define MsiProcessMessage fixture_message
#include "installer_host.c"
#define CHECK(value) do{if(!(value)){fprintf(stderr,"child diagnostic failed line %d\n",__LINE__);return 1;}}while(0)
int wmain(int argc,wchar_t **argv) {
    if(argc==3&&!wcscmp(argv[1],L"--handles")){SetEvent((HANDLE)(uintptr_t)_wcstoui64(argv[2],NULL,10));return 0;}
    if(argc==2){DWORD written;WriteFile(GetStdHandle(STD_ERROR_HANDLE),"inert stderr diagnostic",23,&written,NULL);if(!wcscmp(argv[1],L"--zero"))return 0;if(!wcscmp(argv[1],L"--seven"))return 7;if(!wcscmp(argv[1],L"--image-refused"))return 72;if(!wcscmp(argv[1],L"--flood")){static char chunk[65536];for(int i=0;i<33;i++)WriteFile(GetStdHandle(STD_ERROR_HANDLE),chunk,sizeof(chunk),&written,NULL);return 0;}Sleep(30000);return 9;}
    wchar_t program[32768],command[32768],work[32768],path[32768];HANDLE token=NULL;
    CHECK(GetTempPathW(32768,work));CHECK(GetTempFileNameW(work,L"ocd",0,path));CHECK(DeleteFileW(path));CHECK(CreateDirectoryW(path,NULL));CHECK(SUCCEEDED(StringCchCopyW(work,32768,path)));
    CHECK(GetModuleFileNameW(NULL,program,32768));
    CHECK(OpenProcessToken(GetCurrentProcess(),TOKEN_QUERY|TOKEN_DUPLICATE,&token));
    CHECK(token_facts(token,&expectedReceiving));TOKEN_FACTS service=expectedReceiving;DWORD sidBytes=sizeof(service.sid);
    CHECK(CreateWellKnownSid(WinLocalSystemSid,NULL,service.sid,&sidBytes));service.session=expectedReceiving.session+1;
    STARTUPINFOW crossing={sizeof(crossing)};crossing.dwFlags=STARTF_USESHOWWINDOW;PROCESS_INFORMATION refused={0};HANDLE primary=NULL;
    CHECK(!start_captured_user_child(&service,&expectedReceiving,token,program,command,NULL,NULL,&crossing,&refused,&primary));
    CHECK(GetLastError()==ERROR_CANCELLED&&crossSessionObserved);if(primary)CloseHandle(primary);
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --zero",program)));
    CHECK(run_user_child(0,L"prepare_integration",program,command,NULL,NULL,token,work));
    CHECK(!wcscmp(observedStage,L"prepare_integration")&&observedError==0&&observedWait==WAIT_OBJECT_0&&observedExit==0);
    CHECK(combine(path,work,L"prepare_integration.stderr"));
    HANDLE errorFile=CreateFileW(path,GENERIC_READ|READ_CONTROL,FILE_SHARE_READ,NULL,OPEN_EXISTING,0,NULL);CHECK(errorFile!=INVALID_HANDLE_VALUE);
    char body[64]={0};DWORD got;CHECK(ReadFile(errorFile,body,sizeof(body)-1,&got,NULL)&&got==23&&!memcmp(body,"inert stderr diagnostic",23));
    PSECURITY_DESCRIPTOR security=NULL;PACL dacl=NULL;CHECK(GetSecurityInfo(errorFile,SE_FILE_OBJECT,DACL_SECURITY_INFORMATION,NULL,NULL,&dacl,NULL,&security)==ERROR_SUCCESS);
    SECURITY_DESCRIPTOR_CONTROL control;DWORD revision;CHECK(GetSecurityDescriptorControl(security,&control,&revision)&&(control&SE_DACL_PROTECTED)&&dacl&&dacl->AceCount==2);LocalFree(security);CloseHandle(errorFile);CHECK(DeleteFileW(path));
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --image-refused",program)));
    CHECK(!run_user_child(0,L"prepare_integration",program,command,NULL,NULL,token,work));
    CHECK(observedExit==72&&!wcscmp(observedCategory,L"integration-image")&&DeleteFileW(path));
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --seven",program)));
    CHECK(!run_user_child(0,L"prepare_integration",program,command,NULL,NULL,token,work));
    CHECK(observedExit==7&&!wcscmp(observedCategory,L"unspecified")&&DeleteFileW(path));
    observedCategory[0]=0;
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --image-refused",program)));
    CHECK(!run_user_child(0,L"configure",program,command,NULL,NULL,token,work));
    CHECK(!wcscmp(observedStage,L"configure")&&observedError==0&&observedWait==WAIT_OBJECT_0&&observedExit==72&&!observedCategory[0]);
    CHECK(combine(path,work,L"configure.stderr"));CHECK(DeleteFileW(path));
    SECURITY_ATTRIBUTES inheritable={sizeof(inheritable),NULL,TRUE};HANDLE unrelated=CreateEventW(&inheritable,TRUE,FALSE,NULL);CHECK(unrelated);
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --handles %llu",program,(unsigned long long)(uintptr_t)unrelated)));
    CHECK(run_user_child(0,L"configure",program,command,NULL,NULL,token,work));CHECK(WaitForSingleObject(unrelated,0)==WAIT_TIMEOUT);CHECK(CloseHandle(unrelated));CHECK(DeleteFileW(path));
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --flood",program)));
    CHECK(!run_user_child(0,L"configure",program,command,NULL,NULL,token,work));CHECK(observedError==ERROR_BUFFER_OVERFLOW);
    errorFile=CreateFileW(path,GENERIC_READ,FILE_SHARE_READ,NULL,OPEN_EXISTING,0,NULL);CHECK(errorFile!=INVALID_HANDLE_VALUE);LARGE_INTEGER bytes;CHECK(GetFileSizeEx(errorFile,&bytes)&&bytes.QuadPart==2*1024*1024);CloseHandle(errorFile);CHECK(DeleteFileW(path));
    CHECK(SUCCEEDED(StringCchPrintfW(command,32768,L"\"%s\" --sleep",program)));
    timeoutOnce=TRUE;
    CHECK(!run_user_child(0,L"prepare_integration",program,command,NULL,NULL,token,work));
    CHECK(observedError==ERROR_TIMEOUT&&observedWait==WAIT_TIMEOUT&&observedExit==STILL_ACTIVE);
    timeoutTriggered=FALSE;CHECK(combine(path,work,L"prepare_integration.stderr"));CHECK(DeleteFileW(path));
    wchar_t absent[32768];CHECK(SUCCEEDED(StringCchPrintfW(absent,32768,L"%s.missing",program)));
    CHECK(!run_user_child(0,L"configure",absent,command,NULL,NULL,token,work));
    CHECK(observedError==ERROR_FILE_NOT_FOUND&&observedWait==WAIT_FAILED&&observedExit==STILL_ACTIVE);
    CHECK(!run_user_child(0,L"configure",program,command,NULL,NULL,token,work));
    CHECK(observedError==ERROR_FILE_EXISTS&&observedWait==WAIT_FAILED&&observedExit==STILL_ACTIVE);
    CHECK(combine(path,work,L"configure.stderr"));CHECK(DeleteFileW(path));CHECK(RemoveDirectoryW(work));
    /* The installer's error box names the failed step and the Panel path. */
    failure_message(0,L"prepare_integration",L"C:\\Program Files\\OpenAbstractions [old]\\Abstraction Panel.exe",L"C:\\state");
    CHECK(!wcscmp(observedFailure,L"OpenCode with OA could not start the OpenAbstractions Panel at C:\\Program Files\\OpenAbstractions [old]\\Abstraction Panel.exe. Repair or reinstall OpenAbstractions, then run this installer again."));
    failure_message(0,L"configure",L"C:\\panel.exe",L"C:\\state");
    CHECK(!wcscmp(observedFailure,L"OpenCode with OA could not update OpenCode's settings. Your own settings are preserved. Close OpenCode, then run this installer again."));
    failure_message(0,L"workspace",L"",L"C:\\state");
    CHECK(wcsstr(observedFailure,L"could not create its work folder in C:\\state.")!=NULL);
    CloseHandle(token);puts("failure message PASS: prepare_integration names the Panel path");
    puts("child diagnostic PASS: success, nonzero, timeout, launch refusal; protected retained stderr; no OA/config effects");return 0;
}
