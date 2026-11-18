/* App-owned cached MSI custom action. Fixed operations and fixed reviewed
 * payload paths; no script/executable argument is exposed to the product. */
#define UNICODE
#define _UNICODE
#include <windows.h>
#include <msiquery.h>
#include <shlobj.h>
#include <shlwapi.h>
#include <bcrypt.h>
#include <strsafe.h>
#include <tlhelp32.h>
#include <stdlib.h>
#include <string.h>
#include <sddl.h>
#include <userenv.h>
#include <malloc.h>
#include "host_inputs.h"
#include <native_windows.h>

#ifndef OA_PREFLIGHT_ONLY
static void message(MSIHANDLE session, const wchar_t *text) {
    MSIHANDLE record=MsiCreateRecord(1);
    if(record){MsiRecordSetStringW(record,0,text);MsiProcessMessage(session,INSTALLMESSAGE_ERROR,record);MsiCloseHandle(record);}
}
static void guard_info_process(MSIHANDLE session,const wchar_t *stage,DWORD error,HRESULT hr,DWORD exception,DWORD processId) {
    MSIHANDLE record=MsiCreateRecord(processId?5:4);
    if(record){
        MsiRecordSetStringW(record,0,processId?L"OA OpenCode guard stage=[1] win32=[2] hresult=[3] exception=[4] pid=[5]":L"OA OpenCode guard stage=[1] win32=[2] hresult=[3] exception=[4]");
        MsiRecordSetStringW(record,1,stage);MsiRecordSetInteger(record,2,(int)error);
        MsiRecordSetInteger(record,3,(int)hr);MsiRecordSetInteger(record,4,(int)exception);
        if(processId)MsiRecordSetInteger(record,5,(int)processId);
        MsiProcessMessage(session,INSTALLMESSAGE_INFO,record);MsiCloseHandle(record);
    }
}
static void guard_info(MSIHANDLE session,const wchar_t *stage,DWORD error,HRESULT hr,DWORD exception) {
    guard_info_process(session,stage,error,hr,exception,0);
}
#endif
static BOOL combine(wchar_t *out, const wchar_t *root, const wchar_t *leaf) {
    return SUCCEEDED(StringCchPrintfW(out,32768,L"%s\\%s",root,leaf));
}
static BOOL file_sha256(const wchar_t *path, char hex[65]) {
    struct HASH_SCRATCH {wchar_t extended[32768],final[32768];BYTE buffer[65536];};
    struct HASH_SCRATCH *scratch=HeapAlloc(GetProcessHeap(),0,sizeof(*scratch));
    HANDLE file=INVALID_HANDLE_VALUE;OA_NATIVE_FILE held={0};
    BCRYPT_ALG_HANDLE algorithm=NULL; BCRYPT_HASH_HANDLE hash=NULL;
    BYTE digest[32];DWORD got;BOOL ok=FALSE;
    if(!scratch){SetLastError(ERROR_NOT_ENOUGH_MEMORY);goto done;}
    if(!oa_native_open(path,GENERIC_READ,FILE_SHARE_READ,FALSE,FALSE,&held))goto done;
    file=held.file;
    if(BCryptOpenAlgorithmProvider(&algorithm,BCRYPT_SHA256_ALGORITHM,NULL,0)<0)goto done;
    if(BCryptCreateHash(algorithm,&hash,NULL,0,NULL,0,0)<0)goto done;
    do {if(!ReadFile(file,scratch->buffer,sizeof(scratch->buffer),&got,NULL))goto done;if(got&&BCryptHashData(hash,scratch->buffer,got,0)<0)goto done;}while(got);
    if(BCryptFinishHash(hash,digest,sizeof(digest),0)<0)goto done;
    for(unsigned i=0;i<32;i++)StringCchPrintfA(hex+i*2,3,"%02x",digest[i]);
    ok=TRUE;
done:
    {DWORD savedError=GetLastError();
    if(hash)BCryptDestroyHash(hash);if(algorithm)BCryptCloseAlgorithmProvider(algorithm,0);
    oa_native_close(&held);if(scratch)HeapFree(GetProcessHeap(),0,scratch);SetLastError(savedError);return ok;}
}
static BOOL hash_file(const wchar_t *path, const char *expected) {
    char hex[65];
    return file_sha256(path,hex)&&lstrcmpA(hex,expected)==0;
}
#ifndef OA_PREFLIGHT_ONLY
static BOOL json_string(wchar_t *out, size_t capacity, const wchar_t *value) {
    size_t n=0;out[n++]=L'"';
    for(;*value;value++) {if(*value<32)return FALSE;if(n+3>=capacity)return FALSE;if(*value==L'\\'||*value==L'"')out[n++]=L'\\';out[n++]=*value;}
    out[n++]=L'"';out[n]=0;return TRUE;
}
static BOOL request_json(wchar_t *json,wchar_t *const *values) {
    return SUCCEEDED(StringCchPrintfW(json,65536,L"{%s:%s,%s:%s,%s:%s,%s:%s,%s:%s,%s:%s,%s:%s}",
       L"\"operation\"",values[0],L"\"directory\"",values[1],L"\"plugin\"",values[2],L"\"state\"",values[3],L"\"work\"",values[4],L"\"cache\"",values[5],L"\"mcpLauncher\"",values[6]));
}
static BOOL hold_launcher(const wchar_t *operation,const wchar_t *launcher,OA_NATIVE_FILE *held) {
    return lstrcmpW(operation,L"install")!=0||(*launcher&&oa_native_open(launcher,GENERIC_READ,FILE_SHARE_READ,FALSE,FALSE,held));
}
static BOOL write_utf8(const wchar_t *path, const wchar_t *text) {
    int size=WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,text,-1,NULL,0,NULL,NULL);DWORD wrote;
    if(size<1)return FALSE;char *data=HeapAlloc(GetProcessHeap(),0,size);if(!data)return FALSE;
    BOOL ok=FALSE;HANDLE file=INVALID_HANDLE_VALUE;
    if(!WideCharToMultiByte(CP_UTF8,WC_ERR_INVALID_CHARS,text,-1,data,size,NULL,NULL))goto done;
    file=CreateFileW(path,GENERIC_WRITE,0,NULL,CREATE_NEW,FILE_ATTRIBUTE_NORMAL,NULL);if(file==INVALID_HANDLE_VALUE)goto done;
    ok=WriteFile(file,data,(DWORD)size-1,&wrote,NULL)&&wrote==(DWORD)size-1&&FlushFileBuffers(file);
done:if(file!=INVALID_HANDLE_VALUE)CloseHandle(file);HeapFree(GetProcessHeap(),0,data);return ok;
}
static BOOL env_value(wchar_t *block, size_t *used, const wchar_t *key, const wchar_t *value) {
    size_t length=wcslen(key)+wcslen(value)+2;if(*used+length+1>=65536)return FALSE;
    if(FAILED(StringCchPrintfW(block+*used,65536-*used,L"%s=%s",key,value)))return FALSE;
    *used+=length;block[*used]=0;return TRUE;
}
static int compare_env(const void *left,const void *right){return _wcsicmp(*(const wchar_t*const*)left,*(const wchar_t*const*)right);}
static BOOL sort_env(wchar_t *block) {
    wchar_t *entries[32];unsigned count=0;wchar_t *p=block;
    while(*p){if(count>=32)return FALSE;entries[count++]=p;p+=wcslen(p)+1;}
    qsort(entries,count,sizeof(entries[0]),compare_env);
    wchar_t *copy=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,65536*sizeof(wchar_t));if(!copy)return FALSE;size_t used=0;
    for(unsigned i=0;i<count;i++){size_t size=wcslen(entries[i])+1;CopyMemory(copy+used,entries[i],size*sizeof(wchar_t));used+=size;}
    CopyMemory(block,copy,(used+1)*sizeof(wchar_t));HeapFree(GetProcessHeap(),0,copy);return TRUE;
}
#endif
#include <user_token.h>
#include "desktop_state.h"
#ifndef OA_PREFLIGHT_ONLY
#include "desktop_fix.h"
/* FindRecords: app.wxs reads the Panel's program into OaPanel with it. */
#include <oa_records.h>
/* The installing user's Desktop watcher, its kept stock copy, the verified
   payload copy and OA's marker; paths is six 32768-character buffers. */
typedef struct {wchar_t paths[6][32768];const char *stock[OA_DESKTOP_RELEASE_COUNT];OA_DESKTOP_FIX fix;} OA_DESKTOP_FIX_CONTEXT;
static BOOL desktop_fix_context(const wchar_t *root,const wchar_t *state,OA_DESKTOP_FIX_CONTEXT *context) {
    HANDLE token=user_token();PWSTR local=NULL;BOOL ok=FALSE;wchar_t (*paths)[32768]=context->paths;
    if(!token||FAILED(SHGetKnownFolderPath(&FOLDERID_LocalAppData,0,token,&local)))goto done;
    if(!combine(paths[0],local,L"Programs\\@opencode-aidesktop")||!combine(paths[1],paths[0],OA_DESKTOP_WATCHER_RELATIVE)
       ||FAILED(StringCchPrintfW(paths[2],32768,L"%s.oa-stock",paths[1]))||FAILED(StringCchPrintfW(paths[3],32768,L"%s.oa-new",paths[1]))
       ||!combine(paths[4],root,OA_DESKTOP_WATCHER_PAYLOAD)||!combine(paths[5],state,L"desktop-fix.pending"))goto done;
    for(unsigned i=0;i<OA_DESKTOP_RELEASE_COUNT;i++)context->stock[i]=OA_DESKTOP_RELEASES[i].watcher_stock_sha256;
    OA_DESKTOP_FIX fix={paths[1],paths[2],paths[3],paths[4],paths[5],OA_DESKTOP_WATCHER_PATCHED_SHA256,context->stock,OA_DESKTOP_RELEASE_COUNT,
                       OA_DESKTOP_WATCHER_EARLIER,OA_DESKTOP_WATCHER_EARLIER_COUNT};
    context->fix=fix;ok=TRUE;
done:if(local)CoTaskMemFree(local);if(token)CloseHandle(token);return ok;
}
static BOOL token_sid(HANDLE token,wchar_t *out) {
    BYTE bytes[4096];DWORD size;LPWSTR sid=NULL;BOOL ok=FALSE;
    if(GetTokenInformation(token,TokenUser,bytes,sizeof(bytes),&size)&&ConvertSidToStringSidW(((TOKEN_USER*)bytes)->User.Sid,&sid))ok=SUCCEEDED(StringCchCopyW(out,256,sid));
    if(sid)LocalFree(sid);return ok;
}
static BOOL manifest_state_allowed(BOOL present,BOOL plain,BOOL owned,BOOL samePath,BOOL sameBytes) {
    return !present||(plain&&owned&&samePath&&sameBytes);
}
static BOOL manifest_refusal(MSIHANDLE session,const wchar_t *site,DWORD error) {
    guard_info(session,site,error,error?HRESULT_FROM_WIN32(error):E_ACCESSDENIED,0);return FALSE;
}
static BOOL manifest_state_refusal(MSIHANDLE session,const wchar_t *site,INSTALLSTATE state) {
    wchar_t stage[160];
    if(FAILED(StringCchPrintfW(stage,160,L"%s installstate=%d",site,(int)state)))return manifest_refusal(session,site,ERROR_BUFFER_OVERFLOW);
    guard_info(session,stage,ERROR_SUCCESS,E_FAIL,0);return FALSE;
}
static BOOL manifest_database_text(MSIHANDLE session,MSIHANDLE database,const wchar_t *query,wchar_t *value,DWORD capacity,const wchar_t *site) {
    MSIHANDLE view=0,record=0;BOOL ok=FALSE;UINT status;
    if((status=MsiDatabaseOpenViewW(database,query,&view))!=ERROR_SUCCESS
       ||(status=MsiViewExecute(view,0))!=ERROR_SUCCESS
       ||(status=MsiViewFetch(view,&record))!=ERROR_SUCCESS){manifest_refusal(session,site,status);goto done;}
    status=MsiRecordGetStringW(record,1,value,&capacity);ok=status==ERROR_SUCCESS&&value[0];
    if(!ok)manifest_refusal(session,site,status);
done:if(record)MsiCloseHandle(record);if(view)MsiCloseHandle(view);return ok;
}
/* An earlier OpenCode with OA package (a related product of this upgrade code)
   records its registration manifest in its own cached package: the component
   that installs it, its size and its MsiFileHash. An upgrade replaces that
   file when the earlier product owns it at this path with exactly those bytes
   (preview-1031m: every upgrade that changed the manifest was refused, since
   only this build's bytes were accepted). Read by file name, so a changed
   component id or key between builds is found too. */
static BOOL related_manifest_recorded(MSIHANDLE session,const wchar_t *product,const wchar_t *sid,const wchar_t *expected,wchar_t *registered,BOOL *owned,BOOL *failed) {
    wchar_t package[MAX_PATH*2],component[40],name[512];DWORD capacity=MAX_PATH*2;
    MSIHANDLE database=0,view=0,record=0;BOOL matched=FALSE;UINT status=ERROR_NO_MORE_ITEMS;*failed=TRUE;
    status=MsiGetProductInfoExW(product,sid,MSIINSTALLCONTEXT_USERUNMANAGED,L"LocalPackage",package,&capacity);
    if(status==ERROR_UNKNOWN_PRODUCT||status==ERROR_UNKNOWN_PROPERTY){
        *failed=FALSE;guard_info(session,L"manifest_related_package_missing",status,S_OK,0);goto done;
    }
    if(status!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_package",status);goto done;}
    if((status=MsiOpenDatabaseW(package,(LPCWSTR)MSIDBOPEN_READONLY,&database))!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_database",status);goto done;}
    if((status=MsiDatabaseOpenViewW(database,L"SELECT `Component`.`ComponentId`,`File`.`FileName`,`File`.`FileSize`,`MsiFileHash`.`HashPart1`,"
          L"`MsiFileHash`.`HashPart2`,`MsiFileHash`.`HashPart3`,`MsiFileHash`.`HashPart4` FROM `File`,`Component`,`MsiFileHash` "
          L"WHERE `File`.`Component_`=`Component`.`Component` AND `MsiFileHash`.`File_`=`File`.`File`",&view))!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_query",status);goto done;}
    if((status=MsiViewExecute(view,0))!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_execute",status);goto done;}
    while(!*owned&&(status=MsiViewFetch(view,&record))==ERROR_SUCCESS){
        DWORD size=40,nameSize=512;
        if((status=MsiRecordGetStringW(record,1,component,&size))!=ERROR_SUCCESS
           ||(status=MsiRecordGetStringW(record,2,name,&nameSize))!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_record",status);goto done;}
        const wchar_t *longName=wcschr(name,L'|');longName=longName?longName+1:name;
        if(lstrcmpW(longName,OA_REGISTRATION_MANIFEST_NAME)==0){
            capacity=32768;
            INSTALLSTATE state=MsiGetComponentPathExW(product,component,sid,MSIINSTALLCONTEXT_USERUNMANAGED,registered,&capacity);
            if(state==INSTALLSTATE_BROKEN||(state<0&&state!=INSTALLSTATE_UNKNOWN)){manifest_state_refusal(session,L"manifest_related_component",state);goto done;}
            BOOL same=FALSE;
            if(state==INSTALLSTATE_LOCAL&&!oa_native_compare_paths(registered,expected,FALSE,FALSE,&same)){manifest_refusal(session,L"manifest_related_identity",GetLastError());goto done;}
            if(state==INSTALLSTATE_LOCAL&&same){
                *owned=TRUE;
                MSIFILEHASHINFO hash={sizeof(hash)};WIN32_FILE_ATTRIBUTE_DATA facts;
                if(!GetFileAttributesExW(expected,GetFileExInfoStandard,&facts)){manifest_refusal(session,L"manifest_related_attributes",GetLastError());goto done;}
                if(facts.nFileSizeHigh||(int)facts.nFileSizeLow!=MsiRecordGetInteger(record,3)){manifest_refusal(session,L"manifest_related_size",0);goto done;}
                if((status=MsiGetFileHashW(expected,0,&hash))!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_file_hash",status);goto done;}
                matched=TRUE;
                for(unsigned part=0;matched&&part<4;part++)matched=(int)hash.dwData[part]==MsiRecordGetInteger(record,4+part);
                if(!matched)manifest_refusal(session,L"manifest_related_bytes",0);
            }
        }
        MsiCloseHandle(record);record=0;
    }
    *failed=!*owned&&status!=ERROR_NO_MORE_ITEMS;
    if(*failed)manifest_refusal(session,L"manifest_related_fetch",status);
done:if(record)MsiCloseHandle(record);if(view)MsiCloseHandle(view);if(database)MsiCloseHandle(database);return matched;
}
/* ComponentId identifies one shared component across product clients. Recovery
   can find its LOCAL registration when related-product lookup has no match.
   This path admits only this build's exact manifest bytes, never an
   earlier manifest or a source-cache package as authority. */
static BOOL component_manifest_current(MSIHANDLE session,const wchar_t *component,const wchar_t *sid,const wchar_t *expected,wchar_t *registered) {
    wchar_t client[40],clientSid[256];OA_NATIVE_FILE held={0};BOOL ok=FALSE,hadClient=FALSE,pathMismatch=FALSE;
    INSTALLSTATE lastState=INSTALLSTATE_UNKNOWN;
    if(!oa_native_open(expected,GENERIC_READ,FILE_SHARE_READ,FALSE,FALSE,&held)){manifest_refusal(session,L"manifest_component_target",GetLastError());goto done;}
    for(DWORD index=0;index<128;index++){
        DWORD count=256;MSIINSTALLCONTEXT context=MSIINSTALLCONTEXT_NONE;
        UINT status=MsiEnumClientsExW(component,sid,MSIINSTALLCONTEXT_USERUNMANAGED,index,client,&context,clientSid,&count);
        if(status==ERROR_NO_MORE_ITEMS){
            if(hadClient&&!pathMismatch)manifest_state_refusal(session,L"manifest_client_not_local",lastState);
            else manifest_refusal(session,pathMismatch?L"manifest_client_path_mismatch":L"manifest_client_missing",0);
            goto done;
        }
        if(status!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_client_enumeration",status);goto done;}
        if(context!=MSIINSTALLCONTEXT_USERUNMANAGED){manifest_refusal(session,L"manifest_client_context",0);goto done;}
        if(lstrcmpW(clientSid,sid)){manifest_refusal(session,L"manifest_client_sid",0);goto done;}
        count=32768;
        INSTALLSTATE state=MsiGetComponentPathExW(client,component,sid,MSIINSTALLCONTEXT_USERUNMANAGED,registered,&count);
        hadClient=TRUE;lastState=state;
        if(state==INSTALLSTATE_BROKEN||(state<0&&state!=INSTALLSTATE_UNKNOWN)){manifest_state_refusal(session,L"manifest_client_component",state);goto done;}
        if(state!=INSTALLSTATE_LOCAL)continue;
        BOOL same=FALSE;
        if(!oa_native_compare_paths(registered,expected,FALSE,FALSE,&same)){manifest_refusal(session,L"manifest_client_identity",GetLastError());goto done;}
        if(!same){pathMismatch=TRUE;continue;}
        ok=hash_file(expected,OA_REGISTRATION_MANIFEST_SHA256);
        if(!ok)manifest_refusal(session,L"manifest_component_bytes",0);goto done;
    }
    manifest_refusal(session,L"manifest_client_limit",0);
done:oa_native_close(&held);return ok;
}
static BOOL registration_manifest_allowed(MSIHANDLE session,const wchar_t *integrationRoot) {
    wchar_t (*scratch)[32768]=HeapAlloc(GetProcessHeap(),0,2*sizeof(*scratch));
    HANDLE token=NULL;MSIHANDLE database=0;BOOL ok=FALSE;wchar_t product[40],component[40],upgrade[40],sid[256];
    if(!scratch){manifest_refusal(session,L"manifest_scratch",ERROR_NOT_ENOUGH_MEMORY);goto done;}
    wchar_t *expected=scratch[0],*registered=scratch[1];
    if(!combine(expected,integrationRoot,OA_REGISTRATION_MANIFEST_NAME)){manifest_refusal(session,L"manifest_path",ERROR_BUFFER_OVERFLOW);goto done;}
    DWORD attributes=GetFileAttributesW(expected);
    if(attributes==INVALID_FILE_ATTRIBUTES){DWORD error=GetLastError();ok=error==ERROR_FILE_NOT_FOUND||error==ERROR_PATH_NOT_FOUND;if(!ok)manifest_refusal(session,L"manifest_attributes",error);goto done;}
    if(attributes&(FILE_ATTRIBUTE_DIRECTORY|FILE_ATTRIBUTE_REPARSE_POINT)){manifest_refusal(session,L"manifest_not_plain",0);goto done;}
    SetLastError(ERROR_SUCCESS);token=user_token();
    if(!token){manifest_refusal(session,L"manifest_user_token",GetLastError());goto done;}
    SetLastError(ERROR_SUCCESS);if(!token_sid(token,sid)){manifest_refusal(session,L"manifest_user_sid",GetLastError());goto done;}
    database=MsiGetActiveDatabase(session);if(!database){manifest_refusal(session,L"manifest_active_database",0);goto done;}
    if(!manifest_database_text(session,database,L"SELECT `Value` FROM `Property` WHERE `Property`='ProductCode'",product,40,L"manifest_product_property")
       ||!manifest_database_text(session,database,L"SELECT `ComponentId` FROM `Component` WHERE `Component`='ApplicationManifest'",component,40,L"manifest_component_property"))goto done;
    DWORD capacity=32768;
    INSTALLSTATE installed=MsiGetComponentPathExW(product,component,sid,MSIINSTALLCONTEXT_USERUNMANAGED,registered,&capacity);
    if(installed==INSTALLSTATE_BROKEN||(installed<0&&installed!=INSTALLSTATE_UNKNOWN)){manifest_state_refusal(session,L"manifest_current_component",installed);goto done;}
    BOOL owned=FALSE;
    if(installed==INSTALLSTATE_LOCAL&&!oa_native_compare_paths(registered,expected,FALSE,FALSE,&owned)){manifest_refusal(session,L"manifest_current_identity",GetLastError());goto done;}
    if(owned){
        /* This product's own manifest (repair): this build's bytes. */
        ok=manifest_state_allowed(TRUE,TRUE,TRUE,TRUE,hash_file(expected,OA_REGISTRATION_MANIFEST_SHA256));
        if(!ok)manifest_refusal(session,L"manifest_current_bytes",0);
        goto done;
    }
    /* An earlier product's manifest (upgrade): the bytes that product
       recorded for it. A file nobody owns, or one the person edited, stays
       refused. */
    if(!manifest_database_text(session,database,L"SELECT `Value` FROM `Property` WHERE `Property`='UpgradeCode'",upgrade,40,L"manifest_upgrade_property"))goto done;
    BOOL recorded=FALSE;
    for(DWORD index=0;index<128&&!owned;index++){
        UINT status=MsiEnumRelatedProductsW(upgrade,0,index,product);
        if(status==ERROR_NO_MORE_ITEMS)break;
        if(status!=ERROR_SUCCESS){manifest_refusal(session,L"manifest_related_enumeration",status);goto done;}
        BOOL failed=FALSE;
        recorded=related_manifest_recorded(session,product,sid,expected,registered,&owned,&failed);
        if(failed)goto done;
        if(index==127&&!owned){manifest_refusal(session,L"manifest_related_limit",0);goto done;}
    }
    ok=owned?manifest_state_allowed(TRUE,TRUE,owned,owned,recorded):component_manifest_current(session,component,sid,expected,registered);
done:if(database)MsiCloseHandle(database);if(token)CloseHandle(token);if(scratch)HeapFree(GetProcessHeap(),0,scratch);return ok;
}
static void manifest_recovery_message(MSIHANDLE session,const wchar_t *integrationRoot) {
    wchar_t *path=HeapAlloc(GetProcessHeap(),0,32768*sizeof(wchar_t));MSIHANDLE record=0;
    /* WiX 5's external record callback formats with NULL; MSI uses a session.
       Display only paths that both consumers leave unchanged. */
    if(path&&combine(path,integrationRoot,OA_REGISTRATION_MANIFEST_NAME)&&!wcspbrk(path,L"[]{}"))record=MsiCreateRecord(1);
    if(record&&MsiRecordSetStringW(record,0,L"Setup could not verify this OpenCode registration file: [1]. Move it to another folder, then run Setup again.")==ERROR_SUCCESS
       &&MsiRecordSetStringW(record,1,path)==ERROR_SUCCESS)MsiProcessMessage(session,INSTALLMESSAGE_ERROR,record);
    else message(session,L"Setup could not verify openabstractions-application.json in the OpenAbstractions apps\\opencode folder. Move that file to another folder, then run Setup again.");
    if(record)MsiCloseHandle(record);if(path)HeapFree(GetProcessHeap(),0,path);
}
static BOOL prepare_context(MSIHANDLE session,wchar_t *localOut) {
    HANDLE token=user_token();PWSTR local=NULL,profile=NULL;BOOL ok=FALSE;wchar_t sid[256];
    wchar_t (*scratch)[32768]=HeapAlloc(GetProcessHeap(),0,4*sizeof(*scratch));
    wchar_t *config=scratch?scratch[0]:NULL,*directory=scratch?scratch[1]:NULL,*temp=scratch?scratch[2]:NULL;
    wchar_t *cache=scratch?scratch[3]:NULL;
    wchar_t *context=HeapAlloc(GetProcessHeap(),0,196608*sizeof(wchar_t));
    wchar_t *launcher=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,32768*sizeof(wchar_t));if(!token||!context||!scratch||!launcher){SetLastError(ERROR_NOT_ENOUGH_MEMORY);goto done;}
    if(!token_sid(token,sid)||FAILED(SHGetKnownFolderPath(&FOLDERID_LocalAppData,0,token,&local)))goto done;
    DWORD count=GetEnvironmentVariableW(L"XDG_CONFIG_HOME",config,32768);if(count>=32768)goto done;
    if(!count){if(FAILED(SHGetKnownFolderPath(&FOLDERID_Profile,0,token,&profile))||!combine(config,profile,L".config"))goto done;}
    count=GetTempPathW(32768,temp);if(!count||count>=32768||PathIsRelativeW(config)||PathIsRelativeW(temp)||!combine(directory,config,L"opencode"))goto done;
    count=GetEnvironmentVariableW(L"XDG_CACHE_HOME",config,32768);if(count>=32768)goto done;
    if(!count){if(!profile&&FAILED(SHGetKnownFolderPath(&FOLDERID_Profile,0,token,&profile)))goto done;if(!combine(config,profile,L".cache"))goto done;}
    if(PathIsRelativeW(config)||!combine(cache,config,L"opencode"))goto done;
    if(wcschr(local,L'\n')||wcschr(directory,L'\n')||wcschr(temp,L'\n')||wcschr(cache,L'\n'))goto done;
    /* OaPanel is the private FindRecords result for the selected core.
       Its existing Setup owner prepares the dedicated MCP image. */
    count=32768;
    UINT panelResult=MsiGetPropertyW(session,L"OaPanel",launcher,&count);
    if(panelResult!=ERROR_SUCCESS)goto done;
    if(count){
        if(wcschr(launcher,L'\n')||PathIsRelativeW(launcher))goto done;
    }
    if(FAILED(StringCchPrintfW(context,196608,L"%s\n%s\n%s\n%s\n%s\n%s",sid,local,directory,temp,cache,launcher)))goto done;
    const wchar_t *actions[]={L"InstallOpenCodeIntegration",L"RemoveOpenCodeIntegration",L"RollbackOpenCodeInstall",L"RollbackOpenCodeRemove",L"CommitOpenCodeInstall",L"CommitOpenCodeRemove"};
    for(unsigned i=0;i<6;i++)if(MsiSetPropertyW(session,actions[i],context)!=ERROR_SUCCESS)goto done;
    ok=SUCCEEDED(StringCchCopyW(localOut,32768,local));
done:if(launcher)HeapFree(GetProcessHeap(),0,launcher);if(context)HeapFree(GetProcessHeap(),0,context);if(scratch)HeapFree(GetProcessHeap(),0,scratch);if(local)CoTaskMemFree(local);if(profile)CoTaskMemFree(profile);if(token)CloseHandle(token);return ok;
}
static BOOL integration_root(wchar_t *root,const wchar_t *local) {
    return combine(root,local,L"Programs\\OpenAbstractions\\apps\\opencode");
}
static BOOL paths(MSIHANDLE session,wchar_t *root,wchar_t *state,wchar_t *directory,wchar_t *tempOut,wchar_t *cacheOut,wchar_t *launcherOut) {
    HANDLE token=user_token();PWSTR local=NULL;BOOL ok=FALSE;wchar_t sid[256];DWORD size=196608;
    wchar_t *context=HeapAlloc(GetProcessHeap(),0,size*sizeof(wchar_t));if(!token||!context)goto done;
    if(MsiGetPropertyW(session,L"CustomActionData",context,&size)!=ERROR_SUCCESS||!size||!token_sid(token,sid))goto done;
    wchar_t *parts[6];parts[0]=context;
    for(unsigned i=1;i<6;i++){wchar_t *end=wcschr(parts[i-1],L'\n');if(!end)goto done;*end=0;parts[i]=end+1;}
    if(wcschr(parts[5],L'\n')||lstrcmpW(parts[0],sid)!=0||FAILED(SHGetKnownFolderPath(&FOLDERID_LocalAppData,0,token,&local))
       ||!oa_native_same_paths(parts[1],local,TRUE,FALSE)||PathIsRelativeW(parts[2])||PathIsRelativeW(parts[3])||PathIsRelativeW(parts[4]))goto done;
    if(!integration_root(root,local)||!combine(state,local,L"OpenAbstractionsApps\\opencode-installer")
       ||FAILED(StringCchCopyW(directory,32768,parts[2]))||tempOut&&FAILED(StringCchCopyW(tempOut,32768,parts[3]))||cacheOut&&FAILED(StringCchCopyW(cacheOut,32768,parts[4]))||launcherOut&&FAILED(StringCchCopyW(launcherOut,32768,parts[5])))goto done;
    ok=TRUE;
done:if(context)HeapFree(GetProcessHeap(),0,context);if(local)CoTaskMemFree(local);if(token)CloseHandle(token);return ok;
}
static BOOL start_captured_user_child(const TOKEN_FACTS *host,const TOKEN_FACTS *receiving,HANDLE user,const wchar_t *program,wchar_t *command,void *environment,const wchar_t *cwd,STARTUPINFOW *start,PROCESS_INFORMATION *child,HANDLE *primary) {
    BOOL inherit=host->session==receiving->session;
    if(!inherit&&(start->dwFlags&STARTF_USESTDHANDLES)){SetLastError(ERROR_INVALID_PARAMETER);return FALSE;}
    DWORD flags=CREATE_UNICODE_ENVIRONMENT|CREATE_NO_WINDOW|(inherit?EXTENDED_STARTUPINFO_PRESENT:0);
    if(same_execution_user(host,receiving))return CreateProcessW(program,command,NULL,NULL,inherit,flags,environment,cwd,start,child);
    if(!IsWellKnownSid((PSID)host->sid,WinLocalSystemSid)||!ordinary_user(receiving)){SetLastError(ERROR_ACCESS_DENIED);return FALSE;}
    if(!DuplicateTokenEx(user,TOKEN_ASSIGN_PRIMARY|TOKEN_DUPLICATE|TOKEN_QUERY,NULL,SecurityImpersonation,TokenPrimary,primary))return FALSE;
    return CreateProcessAsUserW(*primary,program,command,NULL,NULL,inherit,flags,environment,cwd,start,child);
}
/* Private Panel process statuses. Never copy a child diagnostic into the MSI log. */
static const wchar_t *preparation_failure_category(DWORD code) {
    switch(code){
    case 71:return L"diagnostic-file";
    case 72:return L"integration-image";
    case 73:return L"prepared-receipt";
    case 74:return L"preparation-deadline";
    default:return L"unspecified";
    }
}
static BOOL run_user_child(MSIHANDLE session,const wchar_t *stage,const wchar_t *program,wchar_t *command,void *environment,const wchar_t *cwd,HANDLE user,const wchar_t *work) {
    PROCESS_INFORMATION child={0};STARTUPINFOEXW start={0};start.StartupInfo.cb=sizeof(start);
    HANDLE hostToken=NULL,primary=NULL;TOKEN_FACTS hostFacts,userFacts;BOOL ok=FALSE,attributesReady=FALSE;
    HANDLE diagnostic=INVALID_HANDLE_VALUE;PSECURITY_DESCRIPTOR descriptor=NULL;PWSTR sid=NULL;
    wchar_t *path=HeapAlloc(GetProcessHeap(),0,32768*sizeof(wchar_t)),acl[512];SIZE_T attributeBytes=0;
    DWORD error=ERROR_SUCCESS,wait=WAIT_FAILED,code=STILL_ACTIVE;BOOL inherit=FALSE;
    if(!path){error=ERROR_NOT_ENOUGH_MEMORY;goto done;}
    if(!user||!token_facts(user,&userFacts)||!OpenProcessToken(GetCurrentProcess(),TOKEN_QUERY,&hostToken)||!token_facts(hostToken,&hostFacts)){error=GetLastError();goto done;}
    inherit=hostFacts.session==userFacts.session;
    if(!work||(wcscmp(stage,L"prepare_integration")&&wcscmp(stage,L"configure"))){error=ERROR_INVALID_PARAMETER;goto done;}
    if(!inherit){start.StartupInfo.cb=sizeof(STARTUPINFOW);start.StartupInfo.dwFlags=STARTF_USESHOWWINDOW;start.StartupInfo.wShowWindow=SW_HIDE;goto launch;}
    if(FAILED(StringCchPrintfW(path,32768,L"%s\\%s.stderr",work,stage))||!ConvertSidToStringSidW(userFacts.sid,&sid)
       ||FAILED(StringCchPrintfW(acl,512,L"D:P(A;;GA;;;SY)(A;;GA;;;%s)",sid))
       ||!ConvertStringSecurityDescriptorToSecurityDescriptorW(acl,SDDL_REVISION_1,&descriptor,NULL)){error=GetLastError();goto done;}
    SECURITY_ATTRIBUTES security={sizeof(security),descriptor,TRUE};
    diagnostic=CreateFileW(path,GENERIC_WRITE,FILE_SHARE_READ,&security,CREATE_NEW,FILE_ATTRIBUTE_NORMAL|FILE_FLAG_OPEN_REPARSE_POINT,NULL);
    if(diagnostic==INVALID_HANDLE_VALUE){error=GetLastError();goto done;}
    InitializeProcThreadAttributeList(NULL,1,0,&attributeBytes);
    start.lpAttributeList=HeapAlloc(GetProcessHeap(),0,attributeBytes);
    if(!start.lpAttributeList){error=ERROR_NOT_ENOUGH_MEMORY;goto done;}
    if(!InitializeProcThreadAttributeList(start.lpAttributeList,1,0,&attributeBytes)){error=GetLastError();goto done;}
    attributesReady=TRUE;
    if(!UpdateProcThreadAttribute(start.lpAttributeList,0,PROC_THREAD_ATTRIBUTE_HANDLE_LIST,&diagnostic,sizeof(diagnostic),NULL,NULL)){error=GetLastError();goto done;}
    start.StartupInfo.dwFlags=STARTF_USESHOWWINDOW|STARTF_USESTDHANDLES;start.StartupInfo.wShowWindow=SW_HIDE;
    start.StartupInfo.hStdInput=INVALID_HANDLE_VALUE;start.StartupInfo.hStdOutput=INVALID_HANDLE_VALUE;start.StartupInfo.hStdError=diagnostic;
launch:
    if(!start_captured_user_child(&hostFacts,&userFacts,user,program,command,environment,cwd,&start.StartupInfo,&child,&primary)){error=GetLastError();goto done;}
    ULONGLONG deadline=GetTickCount64()+120000;
    do {
        wait=WaitForSingleObject(child.hProcess,100);
        LARGE_INTEGER bytes;
        if(diagnostic!=INVALID_HANDLE_VALUE){if(!GetFileSizeEx(diagnostic,&bytes)){error=GetLastError();wait=WAIT_FAILED;break;}if(bytes.QuadPart>2*1024*1024){error=ERROR_BUFFER_OVERFLOW;wait=WAIT_FAILED;break;}}
    } while(wait==WAIT_TIMEOUT&&GetTickCount64()<deadline);
    if(wait!=WAIT_OBJECT_0){if(!error)error=wait==WAIT_TIMEOUT?ERROR_TIMEOUT:GetLastError();TerminateProcess(child.hProcess,ERROR_TIMEOUT);WaitForSingleObject(child.hProcess,5000);goto done;}
    if(!GetExitCodeProcess(child.hProcess,&code)){error=GetLastError();goto done;}
    ok=code==0;
done:
    if(!ok&&!error&&!child.hProcess)error=ERROR_ACCESS_DENIED;
    if(diagnostic!=INVALID_HANDLE_VALUE){MSIHANDLE retained=MsiCreateRecord(3);if(retained){MsiRecordSetStringW(retained,0,L"OA OpenCode stderr stage=[1] path=[2] bound_bytes=[3]");MsiRecordSetStringW(retained,1,stage);MsiRecordSetStringW(retained,2,path);MsiRecordSetInteger(retained,3,2*1024*1024);MsiProcessMessage(session,INSTALLMESSAGE_INFO,retained);MsiCloseHandle(retained);}}
    else if(!inherit){MSIHANDLE retained=MsiCreateRecord(1);if(retained){MsiRecordSetStringW(retained,0,L"OA OpenCode child diagnostics: cross-session inheritance disabled; preparation owns prepared-integration/prepare.stderr, configuration owns result.json; missing child-owned diagnostics indicate failure before that owner started");MsiProcessMessage(session,INSTALLMESSAGE_INFO,retained);MsiCloseHandle(retained);}}
    MSIHANDLE record=MsiCreateRecord(4);
    if(record){
        MsiRecordSetStringW(record,0,L"OA OpenCode child stage=[1] win32=[2] wait=[3] exit=[4]");
        MsiRecordSetStringW(record,1,stage);MsiRecordSetInteger(record,2,(int)error);
        MsiRecordSetInteger(record,3,(int)wait);MsiRecordSetInteger(record,4,(int)code);
        MsiProcessMessage(session,INSTALLMESSAGE_INFO,record);MsiCloseHandle(record);
    }
    if(!ok&&!error&&wait==WAIT_OBJECT_0&&!wcscmp(stage,L"prepare_integration")){
        MSIHANDLE category=MsiCreateRecord(1);
        if(category){
            MsiRecordSetStringW(category,0,L"OA OpenCode preparation failure category=[1]");
            MsiRecordSetStringW(category,1,preparation_failure_category(code));
            MsiProcessMessage(session,INSTALLMESSAGE_INFO,category);MsiCloseHandle(category);
        }
    }
    if(child.hThread)CloseHandle(child.hThread);if(child.hProcess)CloseHandle(child.hProcess);
    if(start.lpAttributeList){if(attributesReady)DeleteProcThreadAttributeList(start.lpAttributeList);HeapFree(GetProcessHeap(),0,start.lpAttributeList);}
    if(diagnostic!=INVALID_HANDLE_VALUE){LARGE_INTEGER bytes,limit;limit.QuadPart=2*1024*1024;if(GetFileSizeEx(diagnostic,&bytes)&&bytes.QuadPart>limit.QuadPart){SetFilePointerEx(diagnostic,limit,NULL,FILE_BEGIN);SetEndOfFile(diagnostic);}CloseHandle(diagnostic);}
    if(descriptor)LocalFree(descriptor);if(sid)LocalFree(sid);if(path)HeapFree(GetProcessHeap(),0,path);
    if(primary)CloseHandle(primary);if(hostToken)CloseHandle(hostToken);SetLastError(error);return ok;
}
/* The installer's error box for a failed Install, Remove or Rollback step,
   naming the step that failed in plain words. The text is a record field, so
   a bracket in a path is shown as written. panel is the Panel program the
   prepare_integration step starts, folder the integration's state folder. */
static void failure_message(MSIHANDLE session,const wchar_t *stage,const wchar_t *panel,const wchar_t *folder) {
    wchar_t text[1024];HRESULT written;
    if(!wcscmp(stage,L"prepare_integration")&&panel&&*panel)
        written=StringCchPrintfW(text,1024,L"OpenCode with OA could not start the OpenAbstractions Panel at %s. Repair or reinstall OpenAbstractions, then run this installer again.",panel);
    else if(!wcscmp(stage,L"prepare_integration"))
        written=StringCchCopyW(text,1024,L"OpenCode with OA could not find the OpenAbstractions Panel. Repair or reinstall OpenAbstractions, then run this installer again.");
    else if(!wcscmp(stage,L"prepared_image_receipt"))
        written=StringCchCopyW(text,1024,L"OpenCode with OA could not use the program the OpenAbstractions Panel prepared for OpenCode. Repair or reinstall OpenAbstractions, then run this installer again.");
    else if(!wcscmp(stage,L"desktop_fix")||!wcscmp(stage,L"desktop_fix_restore")||!wcscmp(stage,L"desktop_fix_paths"))
        written=StringCchCopyW(text,1024,L"OpenCode with OA could not update its file watcher fix in OpenCode's program folder. Close OpenCode, then run this installer again.");
    else if(!wcscmp(stage,L"trusted_payload"))
        written=StringCchCopyW(text,1024,L"OpenCode with OA found a changed file in its own program folder. Repair OpenCode with OA, then run this installer again.");
    else if(!wcscmp(stage,L"workspace")&&folder&&*folder)
        written=StringCchPrintfW(text,1024,L"OpenCode with OA could not create its work folder in %s. Check that this folder can be written, then run this installer again.",folder);
    else if(!wcscmp(stage,L"desktop_runtime"))
        written=StringCchCopyW(text,1024,L"OpenCode with OA updates OpenCode's settings with OpenCode Desktop " OA_DESKTOP_VERSIONS_TEXT L" at its standard per-user location, which it could not find. Install that OpenCode Desktop, then run this installer again.");
    else if(!wcscmp(stage,L"configuration_request")||!wcscmp(stage,L"configure")||!wcscmp(stage,L"configuration_result"))
        written=StringCchCopyW(text,1024,L"OpenCode with OA could not update OpenCode's settings. Your own settings are preserved. Close OpenCode, then run this installer again.");
    else
        written=StringCchCopyW(text,1024,L"OpenCode with OA could not read its installer settings. Run this installer again.");
    if(FAILED(written)&&written!=STRSAFE_E_INSUFFICIENT_BUFFER)return;
    MSIHANDLE record=MsiCreateRecord(1);
    if(record){MsiRecordSetStringW(record,0,L"[1]");MsiRecordSetStringW(record,1,text);MsiProcessMessage(session,INSTALLMESSAGE_ERROR,record);MsiCloseHandle(record);}
}
static BOOL prepared_launcher(const wchar_t *receipt,wchar_t *launcher,OA_NATIVE_FILE *image) {
    OA_NATIVE_FILE held={0};char *raw=NULL;BOOL ok=FALSE;DWORD size,got;
    if(!oa_native_open(receipt,GENERIC_READ,FILE_SHARE_READ,FALSE,FALSE,&held))goto done;
    size=GetFileSize(held.file,NULL);if(size<=65||size>131133)goto done;
    raw=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,size+1);if(!raw)goto done;
    if(!ReadFile(held.file,raw,size,&got,NULL)||got!=size||memchr(raw,0,size))goto done;
    if(raw[64]!='\n')goto done;
    for(unsigned i=0;i<64;i++)if(!((raw[i]>='0'&&raw[i]<='9')||(raw[i]>='a'&&raw[i]<='f')))goto done;
    raw[64]=0;
    int chars=MultiByteToWideChar(CP_UTF8,MB_ERR_INVALID_CHARS,raw+65,(int)size-65,launcher,32767);
    if(!chars)goto done;launcher[chars]=0;
    ok=!PathIsRelativeW(launcher)&&!wcschr(launcher,L'\n')&&!wcschr(launcher,L'\r')
       &&_wcsicmp(PathFindFileNameW(launcher),L"oa-opencode-integration.exe")==0
       &&oa_native_open(launcher,GENERIC_READ,FILE_SHARE_READ,FALSE,FALSE,image)&&hash_file(launcher,raw);
done:if(!ok)oa_native_close(image);if(raw)HeapFree(GetProcessHeap(),0,raw);oa_native_close(&held);return ok;
}
static UINT helper(MSIHANDLE session,const wchar_t *operation) {
    wchar_t *root=NULL,*state=NULL,*directory=NULL,*work=NULL,*home=NULL,*cwd=NULL,*runtime=NULL,*script=NULL,*request=NULL;
    wchar_t *json=NULL,*command=NULL,*environment=NULL;UINT result=ERROR_INSTALL_FAILURE;size_t used=0;
    GUID guid;wchar_t unique[64],*temp=NULL,*value=NULL,*originalTemp=NULL,*cache=NULL;
    HANDLE installingUser=NULL;void *userEnvironment=NULL;
    OA_NATIVE_FILE launcherHeld={0};wchar_t *launcher=NULL;
    const wchar_t *stage=L"allocation";DWORD failureError=ERROR_INVALID_DATA;
    wchar_t *values[7]={0};
    OA_DESKTOP_FIX_CONTEXT *fixContext=NULL;
#define ALLOC(name,count) name=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,(count)*sizeof(wchar_t));if(!name){SetLastError(ERROR_NOT_ENOUGH_MEMORY);goto done;}
    ALLOC(root,32768);ALLOC(state,32768);ALLOC(directory,32768);ALLOC(work,32768);ALLOC(home,32768);ALLOC(cwd,32768);ALLOC(runtime,32768);ALLOC(script,32768);ALLOC(request,32768);
    ALLOC(json,65536);ALLOC(command,32768);ALLOC(environment,65536);
    ALLOC(temp,32768);ALLOC(value,32768);ALLOC(originalTemp,32768);ALLOC(cache,32768);
    ALLOC(launcher,32768);
    fixContext=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,sizeof(*fixContext));if(!fixContext){SetLastError(ERROR_NOT_ENOUGH_MEMORY);goto done;}
    for(unsigned i=0;i<7;i++){values[i]=HeapAlloc(GetProcessHeap(),0,65536*sizeof(wchar_t));if(!values[i]){SetLastError(ERROR_NOT_ENOUGH_MEMORY);goto done;}}
    stage=L"captured_paths";
    if(!paths(session,root,state,directory,originalTemp,cache,launcher))goto done;
    stage=L"desktop_fix_paths";
    BOOL fixReady=desktop_fix_context(root,state,fixContext);
    if(lstrcmpW(operation,L"rollback")==0) {
        /* Rollback continues with the configuration whatever happens here; the
           kept stock copy and marker stay for the next Install or Remove. */
        if(!fixReady||!desktop_fix_rollback(&fixContext->fix)){
            DWORD error=GetLastError();guard_info(session,fixReady?L"desktop_fix_rollback":L"desktop_fix_paths",error,error?HRESULT_FROM_WIN32(error):E_FAIL,0);
        }
    }
    else if(!fixReady)goto done;
    stage=L"trusted_payload";
    for(unsigned i=0;i<OA_TRUSTED_COUNT;i++){if(!combine(temp,root,OA_TRUSTED[i].relative)||!hash_file(temp,OA_TRUSTED[i].sha256))goto done;}
    stage=L"workspace";
    if(SHCreateDirectoryExW(NULL,state,NULL)!=ERROR_SUCCESS&&GetLastError()!=ERROR_ALREADY_EXISTS&&GetFileAttributesW(state)==INVALID_FILE_ATTRIBUTES)goto done;
    if(FAILED(CoCreateGuid(&guid))||!StringFromGUID2(&guid,unique,64))goto done;
    if(FAILED(StringCchPrintfW(temp,32768,L"work-%s",unique))||!combine(work,state,temp)||!CreateDirectoryW(work,NULL))goto done;
    MSIHANDLE workRecord=MsiCreateRecord(1);
    if(workRecord){MsiRecordSetStringW(workRecord,0,L"OA OpenCode installer work=[1]");MsiRecordSetStringW(workRecord,1,work);MsiProcessMessage(session,INSTALLMESSAGE_INFO,workRecord);MsiCloseHandle(workRecord);}
    if(!combine(home,work,L"home")||!CreateDirectoryW(home,NULL)||!combine(cwd,root,L"installer")
       ||!combine(script,root,OA_HELPER_RELATIVE)||!combine(request,work,L"request.json"))goto done;
    /* The Desktop check (CheckOpenCodeClosed) admitted a listed Desktop with no
       process running it. Install and Repair (Setup's msiexec /fomus, README
       "Repair: the watcher fix after an OpenCode update") replace only a stock
       watcher with the patched copy in this package's own folder. A patched,
       different or missing watcher is left as it is and the step succeeds. */
    if(lstrcmpW(operation,L"install")==0) {
        stage=L"desktop_fix";
        OA_FIX_RESULT fixed=desktop_fix_install(&fixContext->fix);
        if(fixed==OA_FIX_FAILED){failureError=GetLastError();goto done;}
        guard_info(session,fixed==OA_FIX_APPLIED?L"desktop_fix_applied":fixed==OA_FIX_ALREADY?L"desktop_fix_current":L"desktop_fix_unknown_left",0,S_OK,0);
    }
    else if(lstrcmpW(operation,L"remove")==0) {
        stage=L"desktop_fix_restore";
        if(!desktop_fix_remove(&fixContext->fix)){failureError=GetLastError();goto done;}
    }
    installingUser=user_token();
    if(lstrcmpW(operation,L"install")==0) {
        stage=L"prepare_integration";
        /* The Panel runs as the captured ordinary installer user with that
           user's profile, before the isolated Bun config worker is started. */
        if(!*launcher||!oa_native_open(launcher,GENERIC_READ,FILE_SHARE_READ,FALSE,FALSE,&launcherHeld)
           ||!CreateEnvironmentBlock(&userEnvironment,installingUser,FALSE)
           ||!combine(temp,work,L"prepared-integration\\program.txt")
           ||FAILED(StringCchPrintfW(command,32768,L"\"%s\" -prepare-opencode-integration \"%s\"",launcher,temp)))goto done;
        if(!run_user_child(session,L"prepare_integration",launcher,command,userEnvironment,cwd,installingUser,work)){failureError=GetLastError();goto done;}
        oa_native_close(&launcherHeld);
        stage=L"prepared_image_receipt";
        if(!prepared_launcher(temp,launcher,&launcherHeld))goto done;
        /* The child owns retained prepare.stderr in this private directory. */
        if(!DeleteFileW(temp))goto done;
    }
    /* The configuration helper runs on the official OpenCode Desktop's own
       Node (ELECTRON_RUN_AS_NODE): the listed release at its standard
       per-user location, with its OpenCode.exe digest checked here. Remove
       and Rollback without that Desktop leave OpenCode's settings as they are
       and succeed; OpenCode is gone or unsupported then. */
    stage=L"desktop_runtime";
    if(!installingUser||!desktop_check(installingUser,TRUE,runtime)) {
        DWORD error=GetLastError();
        guard_info(session,L"desktop_runtime_unavailable",error,error?HRESULT_FROM_WIN32(error):E_FAIL,0);
        if(lstrcmpW(operation,L"install")){result=ERROR_SUCCESS;RemoveDirectoryW(home);RemoveDirectoryW(work);}
        goto done;
    }
    stage=L"configuration_request";
    if(!json_string(values[0],65536,operation)||!json_string(values[1],65536,directory)
       ||!combine(temp,root,OA_PLUGIN_RELATIVE)||!json_string(values[2],65536,temp)||!json_string(values[3],65536,state)||!json_string(values[4],65536,work)||!json_string(values[5],65536,cache))goto done;
    if(!json_string(values[6],65536,launcher))goto done;
    if(!request_json(json,values)||!write_utf8(request,json))goto done;
    const wchar_t *systemKeys[]={L"SystemRoot",L"WINDIR",L"COMSPEC",L"PATHEXT",L"PROCESSOR_ARCHITECTURE"};
    for(unsigned i=0;i<5;i++){DWORD n=GetEnvironmentVariableW(systemKeys[i],value,32768);if(n>=32768)goto done;if(n&&!env_value(environment,&used,systemKeys[i],value))goto done;}
    if(!env_value(environment,&used,L"ELECTRON_RUN_AS_NODE",L"1")||!env_value(environment,&used,L"HOME",home)||!env_value(environment,&used,L"USERPROFILE",home)
       ||!env_value(environment,&used,L"OA_PLUGIN_INVENTORY_SHA256",OA_PLUGIN_INVENTORY_SHA256))goto done;
    const wchar_t *privateKeys[]={L"APPDATA",L"LOCALAPPDATA",L"XDG_CONFIG_HOME",L"XDG_DATA_HOME",L"XDG_CACHE_HOME",L"XDG_STATE_HOME"};
    for(unsigned i=0;i<6;i++){if(!combine(temp,home,privateKeys[i])||!env_value(environment,&used,privateKeys[i],temp))goto done;}
    /* Keep the existing config owner's account-global lock directory. All
       helper working/config/cache directories above are explicitly private. */
    if(!env_value(environment,&used,L"TEMP",originalTemp)||!env_value(environment,&used,L"TMP",originalTemp))goto done;
    if(!sort_env(environment))goto done;
    if(FAILED(StringCchPrintfW(command,32768,L"\"%s\" \"%s\" --request \"%s\"",runtime,script,request)))goto done;
    stage=L"configure";
    if(!run_user_child(session,L"configure",runtime,command,environment,cwd,installingUser,work)){failureError=GetLastError();goto done;}
    stage=L"configuration_result";
    if(!combine(temp,work,L"result.json")||GetFileAttributesW(temp)==INVALID_FILE_ATTRIBUTES)goto done;
    result=ERROR_SUCCESS;
    DeleteFileW(request);DeleteFileW(temp);RemoveDirectoryW(home);RemoveDirectoryW(work);
done:
    guard_info(session,stage,result==ERROR_SUCCESS?ERROR_SUCCESS:failureError,result==ERROR_SUCCESS?S_OK:E_FAIL,0);
    if(result!=ERROR_SUCCESS)failure_message(session,stage,launcher,state);
    if(userEnvironment)DestroyEnvironmentBlock(userEnvironment);if(installingUser)CloseHandle(installingUser);
    oa_native_close(&launcherHeld);if(fixContext)HeapFree(GetProcessHeap(),0,fixContext);
    for(unsigned i=0;i<7;i++)if(values[i])HeapFree(GetProcessHeap(),0,values[i]);
    wchar_t *allocated[]={root,state,directory,work,home,cwd,runtime,script,request,json,command,environment,temp,value,originalTemp,cache,launcher};
    for(unsigned i=0;i<sizeof(allocated)/sizeof(allocated[0]);i++)if(allocated[i])HeapFree(GetProcessHeap(),0,allocated[i]);return result;
}
static UINT commit(MSIHANDLE session,BOOL removing) {
    wchar_t (*scratch)[32768]=HeapAlloc(GetProcessHeap(),0,4*sizeof(*scratch));UINT result=ERROR_INSTALL_FAILURE;
    if(!scratch){SetLastError(ERROR_NOT_ENOUGH_MEMORY);return result;}
    wchar_t *root=scratch[0],*state=scratch[1],*directory=scratch[2],*file=scratch[3];
    if(!paths(session,root,state,directory,NULL,NULL,NULL))goto done;
    if(!combine(file,state,L"pending.json")||(!DeleteFileW(file)&&GetLastError()!=ERROR_FILE_NOT_FOUND))goto done;
    /* The Desktop fix's marker goes; Remove's commit also drops the kept stock
       copy. A commit cannot roll back, so a failure here is reported only. */
    OA_DESKTOP_FIX_CONTEXT *fixContext=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,sizeof(*fixContext));
    if(!fixContext||!desktop_fix_context(root,state,fixContext)||!desktop_fix_commit(&fixContext->fix,removing)){
        DWORD error=GetLastError();guard_info(session,L"desktop_fix_commit",error,error?HRESULT_FROM_WIN32(error):E_FAIL,0);
    }
    if(fixContext)HeapFree(GetProcessHeap(),0,fixContext);
    if(removing){if(!combine(file,state,L"owned.json")||(!DeleteFileW(file)&&GetLastError()!=ERROR_FILE_NOT_FOUND))goto done;RemoveDirectoryW(state);}
    result=ERROR_SUCCESS;
done:HeapFree(GetProcessHeap(),0,scratch);return result;
}
__declspec(dllexport) UINT __stdcall Install(MSIHANDLE session){return helper(session,L"install");}
__declspec(dllexport) UINT __stdcall Remove(MSIHANDLE session){return helper(session,L"remove");}
__declspec(dllexport) UINT __stdcall Rollback(MSIHANDLE session){return helper(session,L"rollback");}
__declspec(dllexport) UINT __stdcall CommitInstall(MSIHANDLE session){return commit(session,FALSE);}
__declspec(dllexport) UINT __stdcall CommitRemove(MSIHANDLE session){return commit(session,TRUE);}
static void desktop_closure_diagnostic(MSIHANDLE session,const DESKTOP_DIAGNOSTIC *diagnostic) {
    guard_info_process(session,diagnostic->stage,diagnostic->win32,diagnostic->hresult,0,diagnostic->processId);
    if(diagnostic->win32==ERROR_BUSY)message(session,L"Close OpenCode completely, then run its OA integration installer again. The normal Desktop will load both configured integration entries after restart.");
}
static BOOL desktop_process_path_closed(MSIHANDLE session,const wchar_t *reported,const OA_NATIVE_FILE_ID *desktop,HANDLE desktopFile) {
    DESKTOP_DIAGNOSTIC diagnostic={L"desktop_process_check",0,S_OK,0};
    BOOL ok=desktop_process_path_closed_fact(reported,desktop,desktopFile,0,&diagnostic);if(!ok)desktop_closure_diagnostic(session,&diagnostic);return ok;
}
static BOOL desktop_identity(MSIHANDLE session,const wchar_t *desktop,OA_NATIVE_FILE_ID *id,OA_NATIVE_FILE *held) {
    DESKTOP_DIAGNOSTIC diagnostic={L"desktop_identity",0,S_OK,0};
    BOOL ok=desktop_identity_fact(desktop,id,held,&diagnostic);if(!ok)desktop_closure_diagnostic(session,&diagnostic);return ok;
}
static BOOL desktop_processes_closed(MSIHANDLE session,const OA_NATIVE_FILE_ID *desktop,HANDLE desktopFile,wchar_t *path) {
    DESKTOP_DIAGNOSTIC diagnostic={L"process_snapshot_enter",0,S_OK,0};
    guard_info(session,diagnostic.stage,0,S_OK,0);
    BOOL ok=desktop_processes_closed_fact(desktop,desktopFile,path,&diagnostic);if(!ok)desktop_closure_diagnostic(session,&diagnostic);return ok;
}
static UINT check_desktop_closed(MSIHANDLE session) {
    wchar_t (*scratch)[32768]=HeapAlloc(GetProcessHeap(),0,3*sizeof(*scratch));UINT result=ERROR_INSTALL_FAILURE;
    wchar_t *local=scratch?scratch[0]:NULL,*desktop=scratch?scratch[1]:NULL,*path=scratch?scratch[2]:NULL;
    if(!scratch){guard_info(session,L"guard_scratch_allocation",ERROR_NOT_ENOUGH_MEMORY,E_OUTOFMEMORY,0);goto done;}
    guard_info(session,L"prepare_context_enter",0,S_OK,0);
    if(!prepare_context(session,local)){DWORD error=GetLastError();guard_info(session,L"prepare_context_refused",error,error?HRESULT_FROM_WIN32(error):E_FAIL,0);goto done;}
    guard_info(session,L"prepare_context_complete",0,S_OK,0);
    if(!combine(desktop,local,L"Programs\\@opencode-aidesktop\\OpenCode.exe")){guard_info(session,L"desktop_path_composition",ERROR_BUFFER_OVERFLOW,STRSAFE_E_INSUFFICIENT_BUFFER,0);goto done;}
    if(!integration_root(path,local)){guard_info(session,L"manifest_root_path",ERROR_BUFFER_OVERFLOW,STRSAFE_E_INSUFFICIENT_BUFFER,0);message(session,L"Setup could not compose the OpenCode registration file path.");goto done;}
    if(!registration_manifest_allowed(session,path)){manifest_recovery_message(session,path);goto done;}
    wchar_t removing[16];DWORD removingSize=16;BOOL isRemove=MsiGetPropertyW(session,L"REMOVE",removing,&removingSize)==ERROR_SUCCESS&&lstrcmpW(removing,L"ALL")==0;
    if(!isRemove) {
        guard_info(session,L"desktop_check_enter",0,S_OK,0);
        HANDLE token=user_token();DESKTOP_DIAGNOSTIC diagnostic={L"user_token",GetLastError(),E_ACCESSDENIED,0};
        BOOL supported=token&&desktop_check_diagnostic(token,TRUE,desktop,&diagnostic);if(token)CloseHandle(token);
        guard_info(session,diagnostic.stage,diagnostic.win32,diagnostic.hresult,0);
        if(!supported){message(session,L"OpenCode with OA requires the exact official Desktop " OA_DESKTOP_VERSIONS_TEXT L" at its standard per-user location. The registration, version, file identity or location is unsupported; no integration settings were changed.");goto done;}
    }
    if(GetFileAttributesW(desktop)==INVALID_FILE_ATTRIBUTES){
        DWORD error=GetLastError();guard_info(session,L"desktop_attributes_after_check",error,HRESULT_FROM_WIN32(error),0);
        wchar_t remove[16];DWORD count=16;
        if(MsiGetPropertyW(session,L"REMOVE",remove,&count)==ERROR_SUCCESS&&lstrcmpW(remove,L"ALL")==0){result=ERROR_SUCCESS;goto done;}
        message(session,L"Install the pinned official OpenCode Desktop package before its OA integration.");goto done;
    }
    OA_NATIVE_FILE_ID desktopId;OA_NATIVE_FILE held={0};
    if(!desktop_identity(session,desktop,&desktopId,&held))goto done;
    if(desktop_processes_closed(session,&desktopId,held.file,path))result=ERROR_SUCCESS;
    oa_native_close(&held);
done:if(scratch)HeapFree(GetProcessHeap(),0,scratch);guard_info(session,result==ERROR_SUCCESS?L"guard_complete":L"guard_refused",result,result==ERROR_SUCCESS?S_OK:HRESULT_FROM_WIN32(result),0);return result;
}
__declspec(dllexport) UINT __stdcall CheckDesktopClosed(MSIHANDLE session) {
    guard_info(session,L"guard_enter",0,S_OK,0);
    __try {return check_desktop_closed(session);}
    __except(EXCEPTION_EXECUTE_HANDLER) {
        DWORD exception=GetExceptionCode();
        if(exception==EXCEPTION_STACK_OVERFLOW&&!_resetstkoflw())return ERROR_INSTALL_FAILURE;
        guard_info(session,L"guard_exception",ERROR_UNHANDLED_EXCEPTION,E_UNEXPECTED,exception);
        return ERROR_INSTALL_FAILURE;
    }
}
#endif
