/* The Desktop crash fix: OA's build of @parcel/watcher 2.5.1 plus
 * parcel-bundler/watcher#277 replaces the official Desktop's stock
 * watcher.node. Every write is gated on a recorded hash: the stock file and
 * an earlier OA build are replaced; the patched file, any other file and a
 * missing file are left as they are, and the step succeeds (Setup reports an
 * unknown file itself).
 * The stock bytes are kept beside the live file for Rollback and Remove; a
 * marker in OA's installer state records which transaction changed the file,
 * so the matching rollback undoes exactly that change.
 * The includer supplies file_sha256 and hash_file (installer_host.c). */
#pragma once
typedef enum {OA_FIX_APPLIED,OA_FIX_ALREADY,OA_FIX_UNKNOWN,OA_FIX_FAILED} OA_FIX_RESULT;
typedef struct {
    const wchar_t *live;    /* the Desktop's watcher.node */
    const wchar_t *backup;  /* the stock bytes, kept beside it */
    const wchar_t *staged;  /* a new file beside it before the rename */
    const wchar_t *source;  /* the verified payload copy of the patched file */
    const wchar_t *marker;  /* OA installer state: which transaction changed live */
    const char *patched;
    const char *const *stock;unsigned stockCount;
    const char *const *earlier;unsigned earlierCount; /* earlier OA builds */
} OA_DESKTOP_FIX;
#define OA_FIX_MARK_NONE 0
#define OA_FIX_MARK_INSTALL 1
#define OA_FIX_MARK_REMOVE 2
static BOOL fix_stock(const OA_DESKTOP_FIX *fix,const char *hex) {
    for(unsigned i=0;i<fix->stockCount;i++)if(lstrcmpA(hex,fix->stock[i])==0)return TRUE;
    return FALSE;
}
static BOOL fix_earlier(const OA_DESKTOP_FIX *fix,const char *hex) {
    for(unsigned i=0;i<fix->earlierCount;i++)if(lstrcmpA(hex,fix->earlier[i])==0)return TRUE;
    return FALSE;
}
/* TRUE with the digest, TRUE with present=FALSE when the file is absent,
   FALSE when the file exists and cannot be read. */
static BOOL fix_digest(const wchar_t *path,char hex[65],BOOL *present) {
    *present=FALSE;hex[0]=0;
    if(file_sha256(path,hex)){*present=TRUE;return TRUE;}
    DWORD error=GetLastError();
    if(error==ERROR_FILE_NOT_FOUND||error==ERROR_PATH_NOT_FOUND){SetLastError(ERROR_SUCCESS);return TRUE;}
    return FALSE;
}
/* Copy beside the live file, verify the copy, then rename it over live. */
static BOOL fix_place(const OA_DESKTOP_FIX *fix,const wchar_t *from,const char *expected) {
    if(!CopyFileW(from,fix->staged,FALSE))return FALSE;
    if(!hash_file(fix->staged,expected)){DWORD error=GetLastError();DeleteFileW(fix->staged);SetLastError(error?error:ERROR_INVALID_DATA);return FALSE;}
    if(!MoveFileExW(fix->staged,fix->live,MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH)){DWORD error=GetLastError();DeleteFileW(fix->staged);SetLastError(error);return FALSE;}
    if(!hash_file(fix->live,expected)){SetLastError(ERROR_INVALID_DATA);return FALSE;}
    return TRUE;
}
static BOOL fix_mark(const OA_DESKTOP_FIX *fix,int mark) {
    const char *text=mark==OA_FIX_MARK_INSTALL?"install":"remove";DWORD wrote=0,length=(DWORD)lstrlenA(text);
    HANDLE file=CreateFileW(fix->marker,GENERIC_WRITE,0,NULL,CREATE_ALWAYS,FILE_ATTRIBUTE_NORMAL,NULL);
    if(file==INVALID_HANDLE_VALUE)return FALSE;
    BOOL ok=WriteFile(file,text,length,&wrote,NULL)&&wrote==length&&FlushFileBuffers(file);
    DWORD error=GetLastError();CloseHandle(file);if(!ok){DeleteFileW(fix->marker);SetLastError(error);}
    return ok;
}
/* OA_FIX_MARK_* or -1 when the marker exists and cannot be read. */
static int fix_marked(const OA_DESKTOP_FIX *fix) {
    char text[16]={0};DWORD got=0;
    HANDLE file=CreateFileW(fix->marker,GENERIC_READ,FILE_SHARE_READ,NULL,OPEN_EXISTING,FILE_ATTRIBUTE_NORMAL,NULL);
    if(file==INVALID_HANDLE_VALUE){DWORD error=GetLastError();return error==ERROR_FILE_NOT_FOUND||error==ERROR_PATH_NOT_FOUND?OA_FIX_MARK_NONE:-1;}
    BOOL read=ReadFile(file,text,sizeof(text)-1,&got,NULL);CloseHandle(file);
    if(!read)return -1;
    if(lstrcmpA(text,"install")==0)return OA_FIX_MARK_INSTALL;
    if(lstrcmpA(text,"remove")==0)return OA_FIX_MARK_REMOVE;
    SetLastError(ERROR_INVALID_DATA);return -1;
}
static BOOL fix_delete(const wchar_t *path) {
    return DeleteFileW(path)||GetLastError()==ERROR_FILE_NOT_FOUND||GetLastError()==ERROR_PATH_NOT_FOUND;
}
/* A marker or staged copy left by an interrupted transaction describes no
   change of this one; a rollback must never act on it. */
static BOOL fix_clear_stale(const OA_DESKTOP_FIX *fix) {
    return fix_delete(fix->marker)&&fix_delete(fix->staged);
}
/* Install and Repair: replace the stock file or an earlier OA build only.
   OA_FIX_UNKNOWN (another hash or no file) changes nothing; the caller treats
   it as success. */
static OA_FIX_RESULT desktop_fix_install(const OA_DESKTOP_FIX *fix) {
    char current[65],kept[65];BOOL present;
    if(!fix_clear_stale(fix))return OA_FIX_FAILED;
    if(!fix_digest(fix->live,current,&present))return OA_FIX_FAILED;
    if(present&&lstrcmpA(current,fix->patched)==0)return OA_FIX_ALREADY;
    if(!present||!(fix_stock(fix,current)||fix_earlier(fix,current))){SetLastError(ERROR_INVALID_DATA);return OA_FIX_UNKNOWN;}
    if(!hash_file(fix->source,fix->patched)){if(!GetLastError())SetLastError(ERROR_INVALID_DATA);return OA_FIX_FAILED;}
    /* An earlier OA build: the stock bytes kept when it was applied stay. */
    if(fix_earlier(fix,current))
        return fix_mark(fix,OA_FIX_MARK_INSTALL)&&fix_place(fix,fix->source,fix->patched)?OA_FIX_APPLIED:OA_FIX_FAILED;
    if(!CopyFileW(fix->live,fix->backup,FALSE))return OA_FIX_FAILED;
    if(!fix_digest(fix->backup,kept,&present)||!present||lstrcmpA(kept,current)!=0){SetLastError(ERROR_INVALID_DATA);return OA_FIX_FAILED;}
    if(!fix_mark(fix,OA_FIX_MARK_INSTALL))return OA_FIX_FAILED;
    return fix_place(fix,fix->source,fix->patched)?OA_FIX_APPLIED:OA_FIX_FAILED;
}
/* Remove: put the kept stock bytes back over OA's patched file or an earlier
   OA build. Any other live file (an OpenCode update, a person's own change)
   stays as it is. */
static BOOL desktop_fix_remove(const OA_DESKTOP_FIX *fix) {
    char current[65],kept[65];BOOL currentPresent,keptPresent;
    if(!fix_clear_stale(fix))return FALSE;
    if(!fix_digest(fix->backup,kept,&keptPresent))return FALSE;
    if(!keptPresent||!fix_stock(fix,kept))return TRUE;
    if(!fix_digest(fix->live,current,&currentPresent))return FALSE;
    if(!currentPresent||!(lstrcmpA(current,fix->patched)==0||fix_earlier(fix,current)))return TRUE;
    return fix_mark(fix,OA_FIX_MARK_REMOVE)&&fix_place(fix,fix->backup,kept);
}
/* Rollback undoes only what this transaction's Install or Remove did. */
static BOOL desktop_fix_rollback(const OA_DESKTOP_FIX *fix) {
    char current[65],kept[65];BOOL currentPresent,keptPresent;
    int mark=fix_marked(fix);
    if(mark<0)return FALSE;
    if(mark==OA_FIX_MARK_NONE)return TRUE;
    if(!fix_delete(fix->staged))return FALSE;
    if(!fix_digest(fix->live,current,&currentPresent))return FALSE;
    if(mark==OA_FIX_MARK_INSTALL) {
        /* Without a verified stock copy there is nothing to restore; the kept
           file stays for a later Remove and the live file is left as it is. */
        if(!fix_digest(fix->backup,kept,&keptPresent))return FALSE;
        if(!keptPresent||!fix_stock(fix,kept))return fix_delete(fix->marker);
        if(currentPresent&&fix_stock(fix,current))return fix_delete(fix->backup)&&fix_delete(fix->marker);
        if(!fix_place(fix,fix->backup,kept))return FALSE;
        /* Over OA's own file the copy has done its work; over anything else
           (the transaction's live file was not OA's) the stock copy stays. */
        if(currentPresent&&(lstrcmpA(current,fix->patched)==0||fix_earlier(fix,current))&&!fix_delete(fix->backup))return FALSE;
        return fix_delete(fix->marker);
    }
    if(currentPresent&&fix_stock(fix,current)&&!(hash_file(fix->source,fix->patched)&&fix_place(fix,fix->source,fix->patched)))return FALSE;
    return fix_delete(fix->marker);
}
/* Commit: the transaction stands. Remove's commit also drops the kept copy. */
static BOOL desktop_fix_commit(const OA_DESKTOP_FIX *fix,BOOL removing) {
    return fix_delete(fix->marker)&&(!removing||fix_delete(fix->backup))&&fix_delete(fix->staged);
}
