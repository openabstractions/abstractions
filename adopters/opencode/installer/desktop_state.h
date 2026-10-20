/* The pinned official host's fixed, read-only registration/identity boundary. */
#pragma once
/* The official Desktop releases this OA release has tested (ruling
   OPENCODE-WATCHER-PIN 2026-11-10): exact versions, never a range. Each names
   its signed installer, its OpenCode.exe and the stock @parcel/watcher binary
   the installer's crash fix replaces. build_installer.py DESKTOP_RELEASES
   carries the same pins; the bundle ships the first entry's installer. */
typedef struct {const wchar_t *version;const char *installer_sha256;ULONGLONG installer_bytes;const char *exe_sha256;const char *watcher_stock_sha256;} OA_DESKTOP_RELEASE;
static const OA_DESKTOP_RELEASE OA_DESKTOP_RELEASES[]={
    {L"1.18.34","403d446d6ca9a9f2edb694d0ecf64c2be7468facade100b9d6e9e65430672bd2",126348744,
     "b82ea814eb265c07c97df0e112f673c8be4718e00c311c154db85e06d3c1f9ac","a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315"},
    {L"1.19.5","a14e77b5aa94a71de493d0fd4ac7a70031246ebb5cceb8ab847fc71d4a2fe62b",126390832,
     "64f43dc4e75cee06c85f03361d12a7791a60873114a2420cbdd94ae7f0c5da3f","a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315"},
};
#define OA_DESKTOP_RELEASE_COUNT (sizeof(OA_DESKTOP_RELEASES)/sizeof(OA_DESKTOP_RELEASES[0]))
#define OA_DESKTOP_VERSIONS_TEXT L"1.18.34 or 1.19.5"
/* @parcel/watcher 2.5.1 plus parcel-bundler/watcher#277, built by OA. */
#define OA_DESKTOP_WATCHER_RELATIVE L"resources\\app.asar.unpacked\\node_modules\\@parcel\\watcher-win32-x64\\watcher.node"
#define OA_DESKTOP_WATCHER_PATCHED_SHA256 "0251ee3ba7027af84a0cb8e0c2ffa1c5989eacf21bc091f95a7c5c00cc486e12"
/* Earlier OA builds of the same fix: Install replaces them, Remove restores
   the kept stock bytes over them. build_installer.py DESKTOP_FIX['earlier']. */
static const char *const OA_DESKTOP_WATCHER_EARLIER[]={"9ec8ac8abf41c8ff2a7cc2298961f2165e14939e8e3a6d9d2aa49757697e4fed"};
#define OA_DESKTOP_WATCHER_EARLIER_COUNT (sizeof(OA_DESKTOP_WATCHER_EARLIER)/sizeof(OA_DESKTOP_WATCHER_EARLIER[0]))
#define OA_DESKTOP_WATCHER_PAYLOAD L"desktop-fix\\watcher.node"
static const OA_DESKTOP_RELEASE *desktop_release(const wchar_t *version) {
    for(unsigned i=0;i<OA_DESKTOP_RELEASE_COUNT;i++)if(lstrcmpW(version,OA_DESKTOP_RELEASES[i].version)==0)return &OA_DESKTOP_RELEASES[i];
    return NULL;
}
#define OA_DESKTOP_ARP L"Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\d074f30d-5f88-5885-b075-be1348cc7676"
#define OA_DESKTOP_REGISTRATION L"Software\\d074f30d-5f88-5885-b075-be1348cc7676"
typedef struct {BOOL arp,location,machine,standard,program,identity,destinationAbsent;wchar_t version[64],directory[32768];} DESKTOP_STATE;
typedef struct {const wchar_t *stage;DWORD win32;HRESULT hresult;DWORD processId;} DESKTOP_DIAGNOSTIC;
static void desktop_diagnostic(DESKTOP_DIAGNOSTIC *out,const wchar_t *stage,DWORD error,HRESULT hr) {
    if(out){out->stage=stage;out->win32=error;out->hresult=hr;out->processId=0;}
}
typedef LSTATUS (WINAPI *OA_REGISTRY_READ)(HKEY,LPCWSTR,LPCWSTR,DWORD,LPDWORD,PVOID,LPDWORD);
static BOOL desktop_text(OA_REGISTRY_READ read,HKEY currentUser,const wchar_t *key,const wchar_t *name,wchar_t *value,DWORD capacity,BOOL *present) {
    DWORD size=capacity*sizeof(wchar_t),type=0;
    LSTATUS status=read(currentUser,key,name,RRF_RT_REG_SZ,&type,value,&size);
    *present=FALSE;
    if(status==ERROR_FILE_NOT_FOUND||status==ERROR_PATH_NOT_FOUND)return TRUE;
    if(status!=ERROR_SUCCESS){SetLastError(status);return FALSE;}
    if(type!=REG_SZ||size<sizeof(wchar_t)||size>capacity*sizeof(wchar_t)||size%sizeof(wchar_t)||value[size/sizeof(wchar_t)-1]){SetLastError(ERROR_INVALID_DATA);return FALSE;}
    *present=TRUE;return TRUE;
}
static BOOL desktop_registration(OA_REGISTRY_READ read,HKEY currentUser,DESKTOP_STATE *state) {
    return desktop_text(read,currentUser,OA_DESKTOP_ARP,L"DisplayVersion",state->version,64,&state->arp)
       &&desktop_text(read,currentUser,OA_DESKTOP_REGISTRATION,L"InstallLocation",state->directory,32768,&state->location);
}
static BOOL desktop_supported(const DESKTOP_STATE *state,const wchar_t *expected,BOOL requireHost) {
    if(state->machine||!state->standard)return FALSE;
    if(!state->arp&&!state->location)return !requireHost&&!state->program&&state->destinationAbsent;
    return state->arp&&state->location&&state->program&&state->identity
       &&desktop_release(state->version)!=NULL&&oa_native_same_paths(state->directory,expected,TRUE,FALSE);
}
/* Diagnostic projection only: desktop_supported remains the admission owner. */
static const wchar_t *desktop_policy_refusal(const DESKTOP_STATE *state,const wchar_t *expected,BOOL requireHost) {
    if(state->machine)return L"desktop_machine_registration";
    if(!state->standard)return L"desktop_nonstandard_programs";
    if(!state->arp&&!state->location) {
        if(requireHost)return L"desktop_host_required";
        if(state->program)return L"desktop_unregistered_program";
        if(!state->destinationAbsent)return L"desktop_unregistered_destination";
    }
    if(!state->arp)return L"desktop_arp_missing";
    if(!state->location)return L"desktop_location_missing";
    if(!state->program)return L"desktop_program_missing";
    if(!desktop_release(state->version))return L"desktop_version_mismatch";
    if(!state->identity)return L"desktop_identity_mismatch";
    if(!oa_native_same_paths(state->directory,expected,TRUE,FALSE))return L"desktop_location_mismatch";
    return L"desktop_registration_policy";
}
static BOOL desktop_canonical_directory(const wchar_t *path) {
    OA_NATIVE_FILE held={0};
    BOOL ok=oa_native_open(path,FILE_READ_ATTRIBUTES,FILE_SHARE_READ|FILE_SHARE_WRITE,TRUE,TRUE,&held);
    oa_native_close(&held);return ok;
}
typedef DWORD (WINAPI *OA_FILE_ATTRIBUTES_READ)(LPCWSTR);
/* Absence permits a new host install. Every existing unregistered root is
   preserved, including empty directories and roots without OpenCode.exe. */
static BOOL desktop_destination_absent(OA_FILE_ATTRIBUTES_READ read,const wchar_t *expected) {
    SetLastError(ERROR_SUCCESS);
    DWORD attributes=read(expected);
    if(attributes!=INVALID_FILE_ATTRIBUTES){SetLastError(ERROR_ALREADY_EXISTS);return FALSE;}
    DWORD error=GetLastError();
    if(error==ERROR_FILE_NOT_FOUND||error==ERROR_PATH_NOT_FOUND)return TRUE;
    if(error==ERROR_SUCCESS)SetLastError(ERROR_INVALID_DATA);
    return FALSE;
}
static BOOL desktop_payload_path_supported(const wchar_t *appRoot) {
    /* The qualified MSI receiver uses ordinary paths, including the NUL. */
    return wcslen(appRoot)+1+OA_PAYLOAD_MAX_RELATIVE<MAX_PATH;
}
static BOOL desktop_check_diagnostic(HANDLE token,BOOL requireHost,wchar_t *program,DESKTOP_DIAGNOSTIC *diagnostic) {
    DESKTOP_STATE *state=HeapAlloc(GetProcessHeap(),HEAP_ZERO_MEMORY,sizeof(DESKTOP_STATE));
    wchar_t (*scratch)[32768]=state?HeapAlloc(GetProcessHeap(),0,3*sizeof(*scratch)):NULL;
    PWSTR local=NULL,userPrograms=NULL;HKEY currentUser=NULL;BOOL ok=FALSE;
    wchar_t *standard=scratch?scratch[0]:NULL,*expected=scratch?scratch[1]:NULL,*appRoot=scratch?scratch[2]:NULL;
    HRESULT hr=S_OK;DWORD error=ERROR_SUCCESS;
#define DESKTOP_REFUSE(stage,code,result) do {desktop_diagnostic(diagnostic,stage,code,result);goto done;} while(0)
    desktop_diagnostic(diagnostic,L"allocation",ERROR_NOT_ENOUGH_MEMORY,E_OUTOFMEMORY);
    if(!state||!scratch)goto done;
    hr=SHGetKnownFolderPath(&FOLDERID_LocalAppData,KF_FLAG_DONT_VERIFY,token,&local);
    if(FAILED(hr))DESKTOP_REFUSE(L"known_local",ERROR_SUCCESS,hr);
    hr=SHGetKnownFolderPath(&FOLDERID_UserProgramFiles,KF_FLAG_DONT_VERIFY,token,&userPrograms);
    if(FAILED(hr))DESKTOP_REFUSE(L"known_user_programs",ERROR_SUCCESS,hr);
    if(!combine(standard,local,L"Programs")||!combine(expected,standard,L"@opencode-aidesktop")||!combine(program,expected,L"OpenCode.exe")
       ||!combine(appRoot,standard,L"OpenAbstractions\\apps\\opencode"))DESKTOP_REFUSE(L"path_composition",ERROR_BUFFER_OVERFLOW,STRSAFE_E_INSUFFICIENT_BUFFER);
    if(!desktop_payload_path_supported(appRoot))DESKTOP_REFUSE(L"payload_target_length",ERROR_FILENAME_EXCED_RANGE,HRESULT_FROM_WIN32(ERROR_FILENAME_EXCED_RANGE));
    state->standard=oa_native_same_paths(standard,userPrograms,TRUE,TRUE);
    if(!state->standard)DESKTOP_REFUSE(L"known_programs_identity",ERROR_NOT_SUPPORTED,HRESULT_FROM_WIN32(ERROR_NOT_SUPPORTED));
    SetLastError(ERROR_SUCCESS);if(!desktop_canonical_directory(standard)){error=GetLastError();DESKTOP_REFUSE(L"canonical_programs",error,error?HRESULT_FROM_WIN32(error):E_FAIL);}
    SetLastError(ERROR_SUCCESS);if(!desktop_canonical_directory(expected)){error=GetLastError();DESKTOP_REFUSE(L"canonical_desktop",error,error?HRESULT_FROM_WIN32(error):E_FAIL);}
    SetLastError(ERROR_SUCCESS);if(!desktop_canonical_directory(appRoot)){error=GetLastError();DESKTOP_REFUSE(L"canonical_integration",error,error?HRESULT_FROM_WIN32(error):E_FAIL);}
    for(unsigned i=0;i<2;i++) {
        HKEY machine=NULL;LSTATUS status=RegOpenKeyExW(HKEY_LOCAL_MACHINE,OA_DESKTOP_ARP,0,KEY_QUERY_VALUE|(i?KEY_WOW64_32KEY:KEY_WOW64_64KEY),&machine);
        if(status==ERROR_SUCCESS){state->machine=TRUE;RegCloseKey(machine);}
        else if(status!=ERROR_FILE_NOT_FOUND&&status!=ERROR_PATH_NOT_FOUND)DESKTOP_REFUSE(L"machine_registration_read",status,HRESULT_FROM_WIN32(status));
    }
    /* RegOpenCurrentUser honors the installing thread's impersonation token;
       the predefined HKCU handle can be cached for an MSI service identity. */
    LSTATUS status=RegOpenCurrentUser(KEY_READ,&currentUser);
    if(status!=ERROR_SUCCESS)DESKTOP_REFUSE(L"current_user_registry",status,HRESULT_FROM_WIN32(status));
    SetLastError(ERROR_SUCCESS);if(!desktop_registration(RegGetValueW,currentUser,state)){error=GetLastError();DESKTOP_REFUSE(L"registration_fields",error,error?HRESULT_FROM_WIN32(error):E_FAIL);}
    if(!state->arp&&!state->location) {
        state->destinationAbsent=desktop_destination_absent(GetFileAttributesW,expected);
        if(!state->destinationAbsent){error=GetLastError();DESKTOP_REFUSE(L"unregistered_desktop_destination",error,HRESULT_FROM_WIN32(error));}
    }
    DWORD attributes=GetFileAttributesW(program);
    if(attributes==INVALID_FILE_ATTRIBUTES&&GetLastError()!=ERROR_FILE_NOT_FOUND&&GetLastError()!=ERROR_PATH_NOT_FOUND){error=GetLastError();DESKTOP_REFUSE(L"desktop_attributes",error,HRESULT_FROM_WIN32(error));}
    state->program=attributes!=INVALID_FILE_ATTRIBUTES;
    /* The registered version selects its release's OpenCode.exe hash; an
       unlisted version is refused below without hashing its program. */
    const OA_DESKTOP_RELEASE *release=desktop_release(state->version);
    if(state->arp&&state->location&&state->program&&release){SetLastError(ERROR_SUCCESS);state->identity=hash_file(program,release->exe_sha256);if(!state->identity){error=GetLastError();DESKTOP_REFUSE(L"desktop_file_identity",error,error?HRESULT_FROM_WIN32(error):E_FAIL);}}
    ok=desktop_supported(state,expected,requireHost);
    desktop_diagnostic(diagnostic,ok?L"desktop_supported":desktop_policy_refusal(state,expected,requireHost),ok?ERROR_SUCCESS:ERROR_NOT_SUPPORTED,ok?S_OK:HRESULT_FROM_WIN32(ERROR_NOT_SUPPORTED));
done:if(currentUser)RegCloseKey(currentUser);if(local)CoTaskMemFree(local);if(userPrograms)CoTaskMemFree(userPrograms);if(state)HeapFree(GetProcessHeap(),0,state);if(scratch)HeapFree(GetProcessHeap(),0,scratch);return ok;
#undef DESKTOP_REFUSE
}
static BOOL desktop_check(HANDLE token,BOOL requireHost,wchar_t *program) {
    return desktop_check_diagnostic(token,requireHost,program,NULL);
}

/* Shared read-only closure facts for MSI and the pre-core bundle preflight. */
/* desktop is the identity of the Desktop file this context already opened. A
   process image this context cannot open (access denied) cannot be that same
   openable file, and an image gone since the snapshot runs no Desktop, so
   both are skipped. Every other open or identity failure refuses. */
static BOOL desktop_process_path_closed_fact(const wchar_t *reported,const OA_NATIVE_FILE_ID *desktop,HANDLE desktopFile,DWORD processId,DESKTOP_DIAGNOSTIC *diagnostic) {
    OA_NATIVE_FILE image={0};OA_NATIVE_FILE_ID id;
    if(!oa_native_open(reported,FILE_READ_ATTRIBUTES,FILE_SHARE_READ|FILE_SHARE_WRITE,FALSE,FALSE,&image)) {
        DWORD error=GetLastError();
        if(error==ERROR_ACCESS_DENIED||error==ERROR_FILE_NOT_FOUND||error==ERROR_PATH_NOT_FOUND)return TRUE;
        desktop_diagnostic(diagnostic,L"desktop_process_identity_refused",error,error?HRESULT_FROM_WIN32(error):E_FAIL);return FALSE;
    }
    BOOL read=oa_native_id(image.file,&id);DWORD error=read?ERROR_SUCCESS:GetLastError();
    BOOL same=read&&oa_native_id_equal(&id,desktop);
    if(!read&&error==ERROR_NOT_SUPPORTED) {
        BY_HANDLE_FILE_INFORMATION left,right;
        if(oa_native_local_ntfs_file_id(desktopFile,&left)&&oa_native_local_ntfs_file_id(image.file,&right)) {
            read=TRUE;
            same=left.dwVolumeSerialNumber==right.dwVolumeSerialNumber&&
                left.nFileIndexHigh==right.nFileIndexHigh&&left.nFileIndexLow==right.nFileIndexLow;
        }
    }
    oa_native_close(&image);
    if(!read){
        desktop_diagnostic(diagnostic,L"desktop_process_identity_unread",error,error?HRESULT_FROM_WIN32(error):E_FAIL);
        if(diagnostic)diagnostic->processId=processId;
        return FALSE;
    }
    if(same){desktop_diagnostic(diagnostic,L"desktop_process_running",ERROR_BUSY,HRESULT_FROM_WIN32(ERROR_BUSY));return FALSE;}
    return TRUE;
}
static BOOL desktop_identity_fact(const wchar_t *desktop,OA_NATIVE_FILE_ID *id,OA_NATIVE_FILE *held,DESKTOP_DIAGNOSTIC *diagnostic) {
    BOOL ok=oa_native_open(desktop,FILE_READ_ATTRIBUTES,FILE_SHARE_READ|FILE_SHARE_WRITE,FALSE,FALSE,held)&&oa_native_id(held->file,id);
    DWORD error=ok?ERROR_SUCCESS:GetLastError();if(!ok)oa_native_close(held);
    if(!ok)desktop_diagnostic(diagnostic,L"desktop_identity_refused",error,error?HRESULT_FROM_WIN32(error):E_FAIL);
    return ok;
}
/* Every process whose image this context reads is compared with the Desktop's
   identity; path is 32768-character scratch for the reported image name. */
static BOOL desktop_processes_closed_fact(const OA_NATIVE_FILE_ID *desktop,HANDLE desktopFile,wchar_t *path,DESKTOP_DIAGNOSTIC *diagnostic) {
    desktop_diagnostic(diagnostic,L"process_snapshot_enter",0,S_OK);
    HANDLE processes=CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS,0);if(processes==INVALID_HANDLE_VALUE){DWORD error=GetLastError();desktop_diagnostic(diagnostic,L"process_snapshot_refused",error,HRESULT_FROM_WIN32(error));return FALSE;}
    BOOL closed=FALSE;PROCESSENTRY32W entry={sizeof(entry)};
    if(!Process32FirstW(processes,&entry)){DWORD error=GetLastError();desktop_diagnostic(diagnostic,L"process_first_refused",error,HRESULT_FROM_WIN32(error));goto done;}
    do {
        HANDLE process=OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION,FALSE,entry.th32ProcessID);
        if(!process)continue;DWORD length=32768;
        BOOL read=QueryFullProcessImageNameW(process,0,path,&length);
        BOOL other=!read||desktop_process_path_closed_fact(path,desktop,desktopFile,entry.th32ProcessID,diagnostic);CloseHandle(process);
        /* Filesystem identity of the reported name; no loaded-image proof inferred. */
        if(!other)goto done;
    }while(Process32NextW(processes,&entry));
    closed=TRUE;
done:CloseHandle(processes);return closed;
}
