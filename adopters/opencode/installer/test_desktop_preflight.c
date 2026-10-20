/* Inert registration receiving fixtures: no registry/config writes or host launch. */
#include "desktop_preflight.c"
#include <stdio.h>
#define CHECK(value) do {if(!(value)){fprintf(stderr,"desktop preflight failed at line %d\n",__LINE__);return 1;}} while(0)
static DESKTOP_STATE fixture;
static unsigned calls;
static DWORD fixtureType=REG_SZ;
static LSTATUS WINAPI registry_fixture(HKEY root,LPCWSTR key,LPCWSTR name,DWORD flags,LPDWORD type,PVOID data,LPDWORD size) {
    if(root!=HKEY_CURRENT_USER||flags!=RRF_RT_REG_SZ)return ERROR_ACCESS_DENIED;
    const wchar_t *value=NULL;BOOL present=FALSE;
    if(lstrcmpW(key,OA_DESKTOP_ARP)==0&&lstrcmpW(name,L"DisplayVersion")==0){value=fixture.version;present=fixture.arp;}
    else if(lstrcmpW(key,OA_DESKTOP_REGISTRATION)==0&&lstrcmpW(name,L"InstallLocation")==0){value=fixture.directory;present=fixture.location;}
    else return ERROR_ACCESS_DENIED;
    calls++;if(!present)return ERROR_FILE_NOT_FOUND;
    DWORD needed=(DWORD)((wcslen(value)+1)*sizeof(wchar_t));if(*size<needed)return ERROR_MORE_DATA;
    CopyMemory(data,value,needed);*size=needed;*type=fixtureType;return ERROR_SUCCESS;
}
static DWORD destinationAttributes,destinationError;
static const wchar_t *destinationExpected;
static DWORD WINAPI destination_fixture(LPCWSTR path) {
    if(lstrcmpW(path,destinationExpected)!=0){SetLastError(ERROR_INVALID_NAME);return INVALID_FILE_ATTRIBUTES;}
    SetLastError(destinationError);return destinationAttributes;
}
int wmain(int argc,wchar_t **argv) {
    CHECK(argc==5);
    CHECK(desktop_canonical_directory(argv[1]));
    CHECK(!desktop_destination_absent(GetFileAttributesW,argv[1])); /* owned foreign/empty folder without an EXE */
    CHECK(!desktop_canonical_directory(argv[2]));
    CHECK(!desktop_canonical_directory(L"\\\\unrequested-host\\share"));
    wchar_t lengthRoot[MAX_PATH];
    for(unsigned i=0;i<MAX_PATH-1;i++)lengthRoot[i]=L'x';
    lengthRoot[MAX_PATH-2-OA_PAYLOAD_MAX_RELATIVE]=0;
    CHECK(desktop_payload_path_supported(lengthRoot));
    lengthRoot[MAX_PATH-2-OA_PAYLOAD_MAX_RELATIVE]=L'x';
    lengthRoot[MAX_PATH-1-OA_PAYLOAD_MAX_RELATIVE]=0;
    CHECK(!desktop_payload_path_supported(lengthRoot));
    HANDLE process=NULL;TOKEN_FACTS live;
    CHECK(OpenProcessToken(GetCurrentProcess(),TOKEN_QUERY,&process));CHECK(token_facts(process,&live));CloseHandle(process);
    CHECK(same_execution_user(&live,&live)==ordinary_user(&live));
    const wchar_t *expected=argv[1];
    destinationExpected=expected;
    /* Existing foreign/no-exe and empty directories have the same protected boundary. */
    const DWORD existing[]={FILE_ATTRIBUTE_DIRECTORY,FILE_ATTRIBUTE_DIRECTORY|FILE_ATTRIBUTE_REPARSE_POINT,FILE_ATTRIBUTE_NORMAL};
    for(unsigned i=0;i<sizeof(existing)/sizeof(existing[0]);i++) {
        destinationAttributes=existing[i];destinationError=ERROR_SUCCESS;
        CHECK(!desktop_destination_absent(destination_fixture,expected));CHECK(GetLastError()==ERROR_ALREADY_EXISTS);
    }
    destinationAttributes=INVALID_FILE_ATTRIBUTES;
    destinationError=ERROR_ACCESS_DENIED;CHECK(!desktop_destination_absent(destination_fixture,expected));
    destinationError=ERROR_SUCCESS;CHECK(!desktop_destination_absent(destination_fixture,expected));
    destinationError=ERROR_FILE_NOT_FOUND;CHECK(desktop_destination_absent(destination_fixture,expected));
    destinationError=ERROR_PATH_NOT_FOUND;CHECK(desktop_destination_absent(destination_fixture,expected));
    DESKTOP_STATE state={0};state.standard=TRUE;
    CHECK(!desktop_supported(&state,expected,FALSE)); /* foreign/no-exe or empty unregistered root */
    CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_unregistered_destination")==0);
    state.destinationAbsent=TRUE;
    CHECK(desktop_supported(&state,expected,FALSE));CHECK(!desktop_supported(&state,expected,TRUE));
    CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,TRUE),L"desktop_host_required")==0);
    state.program=TRUE;CHECK(!desktop_supported(&state,expected,FALSE));state.program=FALSE;
    state.program=TRUE;CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_unregistered_program")==0);state.program=FALSE;
    state.standard=FALSE;CHECK(!desktop_supported(&state,expected,FALSE));state.standard=TRUE;
    state.standard=FALSE;CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_nonstandard_programs")==0);state.standard=TRUE;
    state.destinationAbsent=FALSE; /* supported registered host already has its directory */
    state.arp=state.location=state.program=state.identity=TRUE;
    CHECK(SUCCEEDED(StringCchCopyW(state.version,64,OA_DESKTOP_RELEASES[0].version)));CHECK(SUCCEEDED(StringCchCopyW(state.directory,32768,expected)));
    CHECK(desktop_supported(&state,expected,FALSE));CHECK(desktop_supported(&state,expected,TRUE));
    CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,TRUE),L"desktop_registration_policy")==0); /* defensive unexpected-state fallback */
    /* Distinct case-sensitive twins must never pass the ownership predicate. */
    CHECK(SUCCEEDED(StringCchCopyW(state.directory,32768,argv[3])));
    CHECK(!desktop_supported(&state,argv[4],TRUE));
    CHECK(SUCCEEDED(StringCchCopyW(state.directory,32768,expected)));
    fixture=state;DESKTOP_STATE received={0};calls=0;CHECK(desktop_registration(registry_fixture,HKEY_CURRENT_USER,&received));CHECK(calls==2);
    CHECK(received.arp&&received.location&&lstrcmpW(received.version,OA_DESKTOP_RELEASES[0].version)==0&&lstrcmpW(received.directory,expected)==0);
    fixtureType=REG_EXPAND_SZ;CHECK(!desktop_registration(registry_fixture,HKEY_CURRENT_USER,&received));fixtureType=REG_SZ;
    for(unsigned i=0;i<3;i++){CHECK(SUCCEEDED(StringCchCopyW(state.version,64,i==2?L"1.20.0":i?L"1.18.35":L"1.18.33")));CHECK(!desktop_supported(&state,expected,FALSE));CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_version_mismatch")==0);}
    /* Every tested release is admitted; an unlisted version is refused by
       version even when no program identity was read for it. */
    CHECK(OA_DESKTOP_RELEASE_COUNT==2&&desktop_release(L"1.18.34")==&OA_DESKTOP_RELEASES[0]&&desktop_release(L"1.19.5")==&OA_DESKTOP_RELEASES[1]);
    CHECK(SUCCEEDED(StringCchCopyW(state.version,64,L"1.19.5")));CHECK(desktop_supported(&state,expected,TRUE));
    CHECK(SUCCEEDED(StringCchCopyW(state.version,64,L"1.20.0")));state.identity=FALSE;
    CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,TRUE),L"desktop_version_mismatch")==0);state.identity=TRUE;
    CHECK(SUCCEEDED(StringCchCopyW(state.version,64,OA_DESKTOP_RELEASES[0].version)));
    state.arp=FALSE;CHECK(!desktop_supported(&state,expected,FALSE));CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_arp_missing")==0);state.arp=TRUE;
    state.location=FALSE;CHECK(!desktop_supported(&state,expected,FALSE));CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_location_missing")==0);state.location=TRUE;
    state.program=FALSE;CHECK(!desktop_supported(&state,expected,FALSE));CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_program_missing")==0);state.program=TRUE;
    state.identity=FALSE;CHECK(!desktop_supported(&state,expected,FALSE));CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_identity_mismatch")==0);state.identity=TRUE;
    state.machine=TRUE;CHECK(!desktop_supported(&state,expected,FALSE));CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_machine_registration")==0);state.machine=FALSE;
    CHECK(SUCCEEDED(StringCchCopyW(state.directory,32768,L"D:\\custom\\OpenCode")));CHECK(!desktop_supported(&state,expected,FALSE));
    CHECK(lstrcmpW(desktop_policy_refusal(&state,expected,FALSE),L"desktop_location_mismatch")==0);
    fixture.arp=fixture.location=FALSE;calls=0;ZeroMemory(&received,sizeof(received));CHECK(desktop_registration(registry_fixture,HKEY_CURRENT_USER,&received));CHECK(calls==2&&!received.arp&&!received.location);
    printf("desktop preflight PASS: fixed ARP receiving keys, missing/old/new/custom/redirected/identity fixtures; no host or config effects\n");
    return 0;
}
