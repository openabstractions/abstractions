/* Real MSI database receiving test for the registration manifest guard:
 * the earlier and current packages are real, uninstalled WiX databases read
 * through Windows Installer's own SQL and MsiGetFileHashW. Only the installed
 * state (related products, local package, component paths) is a fixture.
 * argv: current.msi earlier.msi root mode current-product current-component
 *       earlier-product earlier-component
 * mode: absent | upgrade | edited | unowned | current-edited
 * Nothing is installed, registered or launched. */
#define UNICODE
#define _UNICODE
#include <windows.h>
#include <msiquery.h>
#include <stdio.h>
static const wchar_t *currentMsi,*earlierMsi,*currentProduct,*currentComponent,*earlierProduct,*earlierComponent;
static wchar_t ownedPath[32768];
static wchar_t installingSid[256];
static const wchar_t *mode;
static DWORD clientCalls;
static wchar_t loggedStage[256],loggedMessage[65536],loggedNullMessage[65536];
static BOOL messageFormatted;
static int loggedError;
static MSIHANDLE formatSession;
int WINAPI fixture_message(MSIHANDLE session,INSTALLMESSAGE kind,MSIHANDLE record) {
    DWORD count;
    if(kind==INSTALLMESSAGE_INFO){
        count=256;MsiRecordGetStringW(record,1,loggedStage,&count);loggedError=MsiRecordGetInteger(record,2);
    }else if(kind==INSTALLMESSAGE_ERROR){
        count=65536;messageFormatted=MsiFormatRecordW(session,record,loggedMessage,&count)==ERROR_SUCCESS;
        count=65536;messageFormatted=(MsiFormatRecordW(0,record,loggedNullMessage,&count)==ERROR_SUCCESS)&&messageFormatted;
    }
    return IDOK;
}
static BOOL retained(void){return wcsncmp(mode,L"retained-",9)==0;}
static BOOL missing_package(void){return !lstrcmpW(mode,L"related-package-missing")||!lstrcmpW(mode,L"related-product-missing");}
static int owner; /* 0 nobody, 1 the current product, 2 the earlier product */
static MSIHANDLE WINAPI fixture_active(MSIHANDLE session) {
    (void)session;MSIHANDLE database=0;
    return MsiOpenDatabaseW(currentMsi,MSIDBOPEN_READONLY,&database)==ERROR_SUCCESS?database:0;
}
static UINT WINAPI fixture_related(LPCWSTR upgrade,DWORD reserved,DWORD index,LPWSTR product) {
    (void)upgrade;(void)reserved;
    if(retained())return ERROR_NO_MORE_ITEMS;
    if(!lstrcmpW(mode,L"related-error"))return ERROR_ACCESS_DENIED;
    if(index)return ERROR_NO_MORE_ITEMS;
    lstrcpyW(product,earlierProduct);return ERROR_SUCCESS;
}
UINT WINAPI fixture_clients(LPCWSTR component,LPCWSTR sid,DWORD context,DWORD index,LPWSTR product,
                            MSIINSTALLCONTEXT *installed,LPWSTR returnedSid,LPDWORD count) {
    clientCalls++;
    if(lstrcmpW(component,currentComponent)||lstrcmpW(sid,installingSid)||context!=MSIINSTALLCONTEXT_USERUNMANAGED)return ERROR_INVALID_PARAMETER;
    if(!lstrcmpW(mode,L"retained-error"))return ERROR_ACCESS_DENIED;
    if((!retained()&&lstrcmpW(mode,L"related-broken")&&!missing_package())||!lstrcmpW(mode,L"retained-unowned")||(index&&!lstrcmpW(mode,L"retained-local")))return ERROR_NO_MORE_ITEMS;
    if(!lstrcmpW(mode,L"retained-client-broken")&&index>1)return ERROR_NO_MORE_ITEMS;
    if(index&&!lstrcmpW(mode,L"retained-edited"))return ERROR_NO_MORE_ITEMS;
    if(*count<256)return ERROR_MORE_DATA;
    lstrcpyW(product,!lstrcmpW(mode,L"retained-client-broken")&&index?currentProduct:earlierProduct);
    lstrcpyW(returnedSid,!lstrcmpW(mode,L"retained-wrong-sid")?L"S-1-5-18":sid);
    *count=lstrlenW(returnedSid);*installed=!lstrcmpW(mode,L"retained-wrong-context")?MSIINSTALLCONTEXT_MACHINE:MSIINSTALLCONTEXT_USERUNMANAGED;
    return ERROR_SUCCESS;
}
static UINT WINAPI fixture_product_info(LPCWSTR product,LPCWSTR sid,MSIINSTALLCONTEXT context,LPCWSTR property,LPWSTR value,LPDWORD count) {
    (void)sid;
    if(!lstrcmpW(mode,L"related-package-missing"))return ERROR_UNKNOWN_PROPERTY;
    if(!lstrcmpW(mode,L"related-product-missing"))return ERROR_UNKNOWN_PRODUCT;
    if(!lstrcmpW(mode,L"related-package-error"))return ERROR_ACCESS_DENIED;
    if(context!=MSIINSTALLCONTEXT_USERUNMANAGED||lstrcmpW(property,L"LocalPackage")||lstrcmpW(product,earlierProduct))return ERROR_UNKNOWN_PRODUCT;
    if(*count<=(DWORD)lstrlenW(earlierMsi))return ERROR_MORE_DATA;
    lstrcpyW(value,earlierMsi);*count=lstrlenW(earlierMsi);return ERROR_SUCCESS;
}
static INSTALLSTATE WINAPI fixture_component_path(LPCWSTR product,LPCWSTR component,LPCWSTR sid,MSIINSTALLCONTEXT context,LPWSTR path,LPDWORD count) {
    if(!installingSid[0])lstrcpyW(installingSid,sid);
    SetLastError(ERROR_ACCESS_DENIED); /* An INSTALLSTATE must not borrow this. */
    if(!lstrcmpW(mode,L"current-error"))return INSTALLSTATE_INVALIDARG;
    if(!lstrcmpW(mode,L"retained-current-broken")&&!lstrcmpW(product,currentProduct))return INSTALLSTATE_BROKEN;
    if(!lstrcmpW(mode,L"related-component-error")&&!lstrcmpW(product,earlierProduct))return INSTALLSTATE_INVALIDARG;
    if(!lstrcmpW(mode,L"related-broken")&&!lstrcmpW(product,earlierProduct)&&!lstrcmpW(component,earlierComponent))return INSTALLSTATE_BROKEN;
    if(!lstrcmpW(mode,L"retained-client-broken")&&clientCalls==1)return INSTALLSTATE_BROKEN;
    BOOL current=owner==1&&!lstrcmpW(product,currentProduct)&&!lstrcmpW(component,currentComponent);
    BOOL earlier=owner==2&&!lstrcmpW(product,earlierProduct)&&!lstrcmpW(component,earlierComponent);
    if(retained()&&!lstrcmpW(product,earlierProduct)&&!lstrcmpW(component,currentComponent)
       &&lstrcmpW(mode,L"retained-wrong-component")&&lstrcmpW(mode,L"retained-limit"))earlier=TRUE;
    if((((!lstrcmpW(mode,L"related-broken")||missing_package())&&!lstrcmpW(product,earlierProduct))||(!lstrcmpW(mode,L"retained-client-broken")&&clientCalls>1))
       &&!lstrcmpW(component,currentComponent))earlier=TRUE;
    if(context!=MSIINSTALLCONTEXT_USERUNMANAGED||!(current||earlier))return INSTALLSTATE_UNKNOWN;
    if(*count<=(DWORD)lstrlenW(ownedPath))return INSTALLSTATE_MOREDATA;
    lstrcpyW(path,ownedPath);if(!lstrcmpW(mode,L"retained-wrong-path"))lstrcatW(path,L".other");
    *count=lstrlenW(path);return INSTALLSTATE_LOCAL;
}
static DWORD WINAPI fixture_attributes(LPCWSTR path) {
    DWORD result=GetFileAttributesW(path);
    return result!=INVALID_FILE_ATTRIBUTES&&!lstrcmpW(mode,L"retained-reparse")&&!lstrcmpW(path,ownedPath)?result|FILE_ATTRIBUTE_REPARSE_POINT:result;
}
#define GetFileAttributesW fixture_attributes
#define MsiProcessMessage fixture_message
#define MsiGetActiveDatabase fixture_active
#define MsiEnumRelatedProductsW fixture_related
#define MsiEnumClientsExW fixture_clients
#define MsiGetProductInfoExW fixture_product_info
#define MsiGetComponentPathExW fixture_component_path
#include "installer_host.c"
#undef MsiGetActiveDatabase
#undef MsiEnumRelatedProductsW
#undef MsiEnumClientsExW
#undef MsiGetProductInfoExW
#undef MsiGetComponentPathExW
#undef GetFileAttributesW
#undef MsiProcessMessage

static BOOL recovery_copy_matches(void) {
    if(!messageFormatted||lstrcmpW(loggedMessage,loggedNullMessage))return FALSE;
    if(wcspbrk(ownedPath,L"[]{}")){
        return !lstrcmpW(loggedMessage,L"Setup could not verify openabstractions-application.json in the OpenAbstractions apps\\opencode folder. Move that file to another folder, then run Setup again.")
            &&!wcsstr(loggedMessage,L"[\\")&&!wcsstr(loggedMessage,L"substituted-property");
    }
    return wcsstr(loggedMessage,ownedPath)&&wcsstr(loggedMessage,L"run Setup again");
}

int wmain(int argc,wchar_t **argv) {
    if(argc!=9&&argc!=11)return 2;
    currentMsi=argv[1];earlierMsi=argv[2];currentProduct=argv[5];currentComponent=argv[6];earlierProduct=argv[7];earlierComponent=argv[8];
    mode=argv[4];
    if(!combine(ownedPath,argv[3],OA_REGISTRATION_MANIFEST_NAME))return 2;
    if(!lstrcmpW(mode,L"recovery-literal")||!lstrcmpW(mode,L"recovery-plain")){
        HRESULT initialized=CoInitializeEx(NULL,COINIT_APARTMENTTHREADED);if(FAILED(initialized))return 7;
        INSTALLUILEVEL priorUi=MsiSetInternalUI(INSTALLUILEVEL_NONE,NULL);BOOL valid=FALSE;
        /* A restricted fixture handle is independent of, and cannot change,
           machine state. No installer action or custom action is executed. */
        if(MsiOpenPackageExW(currentMsi,MSIOPENPACKAGEFLAGS_IGNOREMACHINESTATE,&formatSession)==ERROR_SUCCESS
           &&MsiSetPropertyW(formatSession,L"N2_FORMAT_TEST",L"substituted-property")==ERROR_SUCCESS){
            manifest_recovery_message(formatSession,argv[3]);
            valid=recovery_copy_matches();
        }
        if(formatSession)MsiCloseHandle(formatSession);formatSession=0;
        MsiSetInternalUI(priorUi,NULL);CoUninitialize();
        if(!valid){fwprintf(stderr,L"session message: %ls\nNULL message: %ls\n",loggedMessage,loggedNullMessage);return 6;}
        wprintf(L"manifest recovery dual-format PASS\n");return 0;
    }
    owner=!lstrcmpW(mode,L"upgrade")||!lstrcmpW(mode,L"edited")?2:!lstrcmpW(mode,L"current-edited")?1:0;
    BOOL allowed=registration_manifest_allowed(0,argv[3]);
    if(argc==11&&(lstrcmpW(loggedStage,argv[9])||loggedError!=_wtoi(argv[10]))){
        fwprintf(stderr,L"diagnostic stage=%ls error=%d expected=%ls/%ls\n",loggedStage,loggedError,argv[9],argv[10]);return 5;
    }
    if(!allowed){
        manifest_recovery_message(0,argv[3]);
        if(!recovery_copy_matches())return 6;
    }
    if((!lstrcmpW(mode,L"edited")||!lstrcmpW(mode,L"current-edited")||!lstrcmpW(mode,L"related-error")||!lstrcmpW(mode,L"related-package-error")||!lstrcmpW(mode,L"related-component-error")||!lstrcmpW(mode,L"current-error")||!lstrcmpW(mode,L"retained-reparse"))&&clientCalls)return 3;
    if((!lstrcmpW(mode,L"retained-current-broken")||!lstrcmpW(mode,L"related-broken"))&&clientCalls)return 3;
    if(!lstrcmpW(mode,L"retained-client-broken")&&clientCalls!=1)return 3;
    if(!lstrcmpW(mode,L"retained-limit")&&clientCalls!=128)return 4;
    wprintf(L"manifest guard mode=%ls allowed=%d\n",mode,allowed);
    return 0;
}
