/* Real Win32 receiving regression: 64 KiB threads, read-only paths/hash and
 * invalid MSI session error paths. No install/config/registry/child actions. */
#include <windows.h>
#include <stdio.h>
#include <malloc.h>
#include <msiquery.h>
static BOOL contextFixture;
static wchar_t *fixturePanel,*fixtureContext;
static unsigned contextWrites;
static UINT WINAPI fixture_get_property(MSIHANDLE session,LPCWSTR name,LPWSTR value,LPDWORD count) {
    if(!contextFixture)return MsiGetPropertyW(session,name,value,count);
    const wchar_t *source=lstrcmpW(name,L"OaPanel")==0?fixturePanel:fixtureContext;
    size_t length=wcslen(source);if(*count<=length){*count=(DWORD)length;return ERROR_MORE_DATA;}
    wcscpy_s(value,*count,source);*count=(DWORD)length;return ERROR_SUCCESS;
}
static UINT WINAPI fixture_set_property(MSIHANDLE session,LPCWSTR name,LPCWSTR value) {
    if(!contextFixture)return MsiSetPropertyW(session,name,value);
    (void)name;if(contextWrites&&wcscmp(fixtureContext,value))return ERROR_INVALID_DATA;
    wcscpy_s(fixtureContext,196608,value);contextWrites++;return ERROR_SUCCESS;
}
#define MsiGetPropertyW fixture_get_property
#define MsiSetPropertyW fixture_set_property
static volatile LONG attempts,live,failAt,failedAllocation;
static volatile LONG failIdentity,identityReads;
static BOOL WINAPI tracked_information(HANDLE file,FILE_INFO_BY_HANDLE_CLASS kind,LPVOID data,DWORD size) {
    if((int)kind==18){InterlockedIncrement(&identityReads);if(failIdentity){SetLastError(ERROR_NOT_SUPPORTED);return FALSE;}}
    return GetFileInformationByHandleEx(file,kind,data,size);
}
static LPVOID WINAPI tracked_alloc(HANDLE heap,DWORD flags,SIZE_T size) {
    LONG number=InterlockedIncrement(&attempts);
    if(number==failAt){InterlockedExchange(&failedAllocation,TRUE);SetLastError(ERROR_NOT_ENOUGH_MEMORY);return NULL;}
    LPVOID result=HeapAlloc(heap,flags,size);if(result)InterlockedIncrement(&live);return result;
}
static BOOL WINAPI tracked_free(HANDLE heap,DWORD flags,LPVOID memory) {
    BOOL result=HeapFree(heap,flags,memory);if(result)InterlockedDecrement(&live);return result;
}
static LPVOID WINAPI tracked_realloc(HANDLE heap,DWORD flags,LPVOID memory,SIZE_T size) {
    LONG number=InterlockedIncrement(&attempts);
    if(number==failAt){InterlockedExchange(&failedAllocation,TRUE);SetLastError(ERROR_NOT_ENOUGH_MEMORY);return NULL;}
    return HeapReAlloc(heap,flags,memory,size);
}
/* This source fixture never reads the account's actual registration. */
static LSTATUS WINAPI fixture_registry_open(HKEY root,LPCWSTR name,DWORD options,REGSAM access,PHKEY key) {
    (void)root;(void)name;(void)options;(void)access;(void)key;return ERROR_ACCESS_DENIED;
}
static LSTATUS WINAPI fixture_current_user(REGSAM access,PHKEY key) {(void)access;(void)key;return ERROR_ACCESS_DENIED;}
#define RegOpenKeyExW fixture_registry_open
#define RegOpenCurrentUser fixture_current_user
#define HeapAlloc tracked_alloc
#define HeapFree tracked_free
#define HeapReAlloc tracked_realloc
#define GetFileInformationByHandleEx tracked_information
#ifdef OA_STACK_LEGACY_SOURCE
#include OA_STACK_LEGACY_SOURCE
#else
#include "installer_host.c"
#endif
#undef HeapAlloc
#undef HeapFree
#undef HeapReAlloc
#undef GetFileInformationByHandleEx
#undef MsiGetPropertyW
#undef MsiSetPropertyW

#ifndef OA_STACK_LEGACY_SOURCE
static BOOL launcher_context(const wchar_t *directory) {
    wchar_t (*b)[32768]=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,12*sizeof(*b));
    fixtureContext=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,196608*sizeof(wchar_t));
    if(!b||!fixtureContext)return FALSE;
    BOOL ok=FALSE;OA_NATIVE_FILE held={0};HANDLE file=INVALID_HANDLE_VALUE;
    if(!combine(b[0],directory,L"selected OA tools")||!CreateDirectoryW(b[0],NULL)
       ||!combine(b[1],b[0],L"panel.exe")||!combine(b[2],b[0],L"oa-opencode-integration.exe"))goto done;
	file=CreateFileW(b[1],GENERIC_WRITE,0,NULL,CREATE_NEW,FILE_ATTRIBUTE_NORMAL,NULL);if(file==INVALID_HANDLE_VALUE)goto done;
	CloseHandle(file);file=INVALID_HANDLE_VALUE;
    file=CreateFileW(b[2],GENERIC_WRITE,0,NULL,CREATE_NEW,FILE_ATTRIBUTE_NORMAL,NULL);if(file==INVALID_HANDLE_VALUE)goto done;
    CloseHandle(file);file=INVALID_HANDLE_VALUE;fixturePanel=b[1];contextFixture=TRUE;contextWrites=0;
    if(!prepare_context(0,b[3])||contextWrites!=6||!paths(0,b[4],b[5],b[6],b[7],b[8],b[9])||wcscmp(b[1],b[9]))goto done;
    if(!combine(b[11],b[0],L"prepared.txt")||FAILED(StringCchPrintfW(b[10],32768,L"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n%s",b[2]))
       ||!write_utf8(b[11],b[10])||!prepared_launcher(b[11],b[9],&held)||wcscmp(b[2],b[9])||!DeleteFileW(b[11]))goto done;
    oa_native_close(&held);
    if(!hold_launcher(L"install",b[9],&held))goto done;
    /* Native held custody prevents replacement while JS receives the request. */
    if(DeleteFileW(b[2]))goto done;
    if(!json_string(b[10],32768,b[9]))goto done;
    wchar_t *values[9]={L"\"install\"",L"\"directory\"",L"\"stage\"",L"\"plugin\"",L"\"cli\"",L"\"state\"",L"\"work\"",L"\"cache\"",b[10]};
    wchar_t *json=HeapAlloc(GetProcessHeap(),0,65536*sizeof(wchar_t));
    if(!json)goto done;
    BOOL encoded=request_json(json,values)&&wcsstr(json,L"\"mcpLauncher\":")&&wcsstr(json,b[10]);
    if(encoded){
        int size=WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,json,-1,NULL,0,NULL,NULL);
        char *utf8=size>0?HeapAlloc(GetProcessHeap(),0,(SIZE_T)size):NULL;
        encoded=utf8&&WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,json,-1,utf8,size,NULL,NULL)>0;
        if(encoded)printf("launcher_request=%s\n",utf8);
        if(utf8)HeapFree(GetProcessHeap(),0,utf8);
    }
    HeapFree(GetProcessHeap(),0,json);if(!encoded)goto done;
    /* Windows cannot name a quote-bearing file, but request escaping covers it. */
    if(!json_string(b[10],32768,L"C:\\tools with spaces\\a\"b.exe")||wcscmp(b[10],L"\"C:\\\\tools with spaces\\\\a\\\"b.exe\""))goto done;
    oa_native_close(&held);if(!DeleteFileW(b[2])||hold_launcher(L"install",b[9],&held)||hold_launcher(L"install",L"",&held))goto done;
    if(!hold_launcher(L"remove",b[9],&held)||!hold_launcher(L"rollback",L"",&held))goto done;
    fixturePanel=L"";contextWrites=0;
    if(!prepare_context(0,b[3])||!paths(0,b[4],b[5],b[6],NULL,NULL,b[9])||*b[9])goto done;
    ok=TRUE;printf("launcher context PASS: selected Panel, exact prepared integration, escaped request, held file, missing install refusal, absent-core remove/rollback\n");
done:
    contextFixture=FALSE;oa_native_close(&held);if(file!=INVALID_HANDLE_VALUE)CloseHandle(file);
    if(b){DeleteFileW(b[1]);DeleteFileW(b[2]);DeleteFileW(b[11]);RemoveDirectoryW(b[0]);HeapFree(GetProcessHeap(),0,b);}
    if(fixtureContext)HeapFree(GetProcessHeap(),0,fixtureContext);fixtureContext=NULL;return ok;
}
#endif
typedef struct {unsigned subject,variant;const wchar_t *file,*directory;BOOL result;DWORD exception;wchar_t output[32768];} CASE;
/* An image this account cannot open: a protected empty DACL denies even
   SYNCHRONIZE; the owner keeps WRITE_DAC to give the file back. */
static BOOL deny_file(const wchar_t *path,BOOL deny) {
    SECURITY_DESCRIPTOR sd;ACL empty;
    if(!InitializeSecurityDescriptor(&sd,SECURITY_DESCRIPTOR_REVISION)||!InitializeAcl(&empty,sizeof(empty),ACL_REVISION)
       ||!SetSecurityDescriptorDacl(&sd,TRUE,deny?&empty:NULL,FALSE))return FALSE;
    return SetFileSecurityW(path,DACL_SECURITY_INFORMATION|(deny?PROTECTED_DACL_SECURITY_INFORMATION:UNPROTECTED_DACL_SECURITY_INFORMATION),&sd);
}
static BOOL subject(CASE *item) {
    switch(item->subject) {
    case 0:return hash_file(item->file,"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    case 1:return desktop_canonical_directory(item->directory);
    case 2:{HANDLE token=user_token();BOOL result=token&&desktop_check(token,TRUE,item->output);if(token)CloseHandle(token);return result;}
    case 3:return prepare_context(0,item->output);
    case 4:return helper(0,L"install")==ERROR_SUCCESS;
    case 5:return commit(0,FALSE)==ERROR_SUCCESS;
#ifdef OA_STACK_LEGACY_SOURCE
    case 6:return CheckDesktopClosed(0)==ERROR_SUCCESS;
#else
    case 6:return check_desktop_closed(0)==ERROR_SUCCESS;
#endif
#ifndef OA_STACK_LEGACY_SOURCE
    case 7:{
        const wchar_t *reported=item->variant==0?item->file:item->output;
        /* 7 treats this running test program as the Desktop: the real walk
           finds it and refuses. */
        if(item->variant==7&&!GetModuleFileNameW(NULL,item->output,32768))return FALSE;
        OA_NATIVE_FILE_ID desktop;OA_NATIVE_FILE held={0};if(!desktop_identity(0,item->variant==7?item->output:item->file,&desktop,&held))return FALSE;
        BOOL result;
        if(item->variant==7||item->variant==6)result=desktop_processes_closed(0,&desktop,held.file,item->output);
        else if(item->variant!=4&&item->variant!=8&&item->variant!=9)result=desktop_process_path_closed(0,reported,&desktop,held.file);
        else {
            DESKTOP_DIAGNOSTIC diagnostic={0};
            failIdentity=TRUE;
            result=desktop_process_path_closed_fact(reported,&desktop,item->variant==9?INVALID_HANDLE_VALUE:held.file,GetCurrentProcessId(),&diagnostic);
            failIdentity=FALSE;
            if(item->variant==8)
                result=result||!diagnostic.stage||lstrcmpW(diagnostic.stage,L"desktop_process_running")||
                    diagnostic.win32!=ERROR_BUSY;
            if(item->variant==9)
                result=result||!diagnostic.stage||lstrcmpW(diagnostic.stage,L"desktop_process_identity_unread")||
                    diagnostic.processId!=GetCurrentProcessId()||diagnostic.win32!=ERROR_NOT_SUPPORTED;
        }
        oa_native_close(&held);
        return result;
    }
#endif
    }
    return FALSE;
}
static DWORD WINAPI receive(LPVOID value) {
    CASE *item=value;
    __try {item->result=subject(item);}
    __except(EXCEPTION_EXECUTE_HANDLER) {
        item->exception=GetExceptionCode();
        if(item->exception==EXCEPTION_STACK_OVERFLOW)_resetstkoflw();
    }
    return 0;
}
static BOOL run(CASE *item,LONG allocation) {
    attempts=live=failedAllocation=0;failAt=allocation;item->result=FALSE;item->exception=0;
    HANDLE thread=CreateThread(NULL,64*1024,receive,item,STACK_SIZE_PARAM_IS_A_RESERVATION,NULL);
    if(!thread)return FALSE;
    BOOL completed=WaitForSingleObject(thread,30000)==WAIT_OBJECT_0;CloseHandle(thread);
    return completed&&!item->exception&&!live;
}
int wmain(int argc,wchar_t **argv) {
    if(argc!=3)return 2;
    CASE *item=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,sizeof(*item));if(!item)return 2;
    item->file=argv[1];item->directory=argv[2];unsigned failures=0,allocationCases=0;DWORD handlesBefore=0,handlesAfter=0;
#ifndef OA_STACK_LEGACY_SOURCE
    if(!launcher_context(item->directory))failures++;
    wchar_t expectedRoot[32768];
    if(FAILED(StringCchPrintfW(expectedRoot,32768,L"%s\\Programs\\OpenAbstractions\\apps\\opencode",item->directory))
       ||!integration_root(item->output,item->directory)||lstrcmpW(item->output,expectedRoot)!=0)failures++;
#endif
    /* Warm the read-only Windows/MSI APIs before measuring handle stability. */
    for(item->subject=0;item->subject<7;item->subject++)run(item,0);
    GetProcessHandleCount(GetCurrentProcess(),&handlesBefore);
    for(item->subject=0;item->subject<7;item->subject++) {
        BOOL clean=run(item,0);LONG count=attempts;
        BOOL expected=item->subject<2?item->result:(item->subject==2?TRUE:!item->result);
        printf("subject=%u stack=65536 clean=%d result=%d expected=%d exception=0x%08lx allocations=%ld live=%ld\n",item->subject,clean,item->result,expected,item->exception,count,live);
        if(!clean||!expected){failures++;continue;}
        for(LONG allocation=1;allocation<=count;allocation++) {
            clean=run(item,allocation);allocationCases++;
            if(!clean||item->result||!failedAllocation){printf("allocation failure subject=%u nth=%ld exception=0x%08lx live=%ld\n",item->subject,allocation,item->exception,live);failures++;}
        }
    }
#ifndef OA_STACK_LEGACY_SOURCE
    item->subject=7;
    /* 0 and 1: the Desktop itself runs (refuse). 2: another file. 3: an image
       gone since the snapshot. 4: an unreadable identity (refuse). 5: an image
       this account cannot open, which cannot be the openable Desktop file.
       6: every process on this machine, none of which runs the fixture.
       7: the same walk with this running test program as the Desktop (refuse). */
    for(item->variant=0;item->variant<10;item->variant++) {
        wcscpy_s(item->output,32768,item->file);
        if(item->variant==1)_wcsupr_s(item->output,32768);
        else if(item->variant==3)wcscat_s(item->output,32768,L".missing");
        else if(item->variant==8) {
            wcscat_s(item->output,32768,L".link");
            if(!CreateHardLinkW(item->output,item->file,NULL))return 3;
        }
        else if(item->variant==2||item->variant==4||item->variant==9)wcscat_s(item->output,32768,L".different");
        else if(item->variant==5)wcscat_s(item->output,32768,L".denied");
        if(item->variant==2||item->variant==5){HANDLE other=CreateFileW(item->output,GENERIC_WRITE,0,NULL,CREATE_NEW,FILE_ATTRIBUTE_NORMAL,NULL);if(other==INVALID_HANDLE_VALUE)return 3;CloseHandle(other);}
        if(item->variant==5&&!deny_file(item->output,TRUE))return 3;
        identityReads=0;BOOL clean=run(item,0);LONG count=attempts;
        BOOL expected=item->variant==2||item->variant==3||item->variant==4||item->variant==5||item->variant==6;
        printf("process guard variant=%u clean=%d result=%d expected=%d identity_reads=%ld allocations=%ld\n",item->variant,clean,item->result,expected,identityReads,count);
        if(!clean||item->result!=expected||(item->variant==4&&!identityReads))failures++;
        if(item->variant==2)for(LONG allocation=1;allocation<=count;allocation++) {
            clean=run(item,allocation);allocationCases++;
            if(!clean||item->result||!failedAllocation)failures++;
        }
    }
    swprintf_s(item->output,32768,L"%s.link",item->file);if(!DeleteFileW(item->output))failures++;
    swprintf_s(item->output,32768,L"%s.different",item->file);if(!DeleteFileW(item->output))failures++;
    swprintf_s(item->output,32768,L"%s.denied",item->file);if(!deny_file(item->output,FALSE)||!DeleteFileW(item->output))failures++;
#endif
    GetProcessHandleCount(GetCurrentProcess(),&handlesAfter);
    if(handlesAfter!=handlesBefore){printf("handle count differs before=%lu after=%lu\n",handlesBefore,handlesAfter);failures++;}
    HeapFree(GetProcessHeap(),0,item);
    printf("stack boundary: subjects=7 allocation_failures=%u failures=%u; no install/config/registry/child actions\n",allocationCases,failures);
    return failures?1:0;
}
