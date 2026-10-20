/* The Desktop crash fix's hash gate on real files in an owned temporary
 * folder: no MSI session, Desktop, registry or profile is touched. */
#include "installer_host.c"
#include <stdio.h>
#define CHECK(value) do {if(!(value)){fprintf(stderr,"desktop fix failed at line %d (win32 %lu)\n",__LINE__,GetLastError());return 1;}} while(0)
static wchar_t live[MAX_PATH],backup[MAX_PATH],staged[MAX_PATH],source[MAX_PATH],marker[MAX_PATH];
static char stockHex[65],patchedHex[65],otherHex[65],earlierHex[65];
static BOOL put(const wchar_t *path,const char *text) {
    HANDLE file=CreateFileW(path,GENERIC_WRITE,0,NULL,CREATE_ALWAYS,FILE_ATTRIBUTE_NORMAL,NULL);DWORD wrote=0;
    if(file==INVALID_HANDLE_VALUE)return FALSE;
    BOOL ok=WriteFile(file,text,(DWORD)strlen(text),&wrote,NULL)&&wrote==strlen(text);CloseHandle(file);return ok;
}
static BOOL digest_of(const wchar_t *path,char *hex){return file_sha256(path,hex);}
static BOOL is(const wchar_t *path,const char *hex){char actual[65];return file_sha256(path,actual)&&lstrcmpA(actual,hex)==0;}
static BOOL absent(const wchar_t *path){return GetFileAttributesW(path)==INVALID_FILE_ATTRIBUTES&&GetLastError()==ERROR_FILE_NOT_FOUND;}
static void clean(void){DeleteFileW(live);DeleteFileW(backup);DeleteFileW(staged);DeleteFileW(marker);}
int wmain(int argc,wchar_t **argv) {
    CHECK(argc==2);
    const wchar_t *root=argv[1];wchar_t stockFile[MAX_PATH],otherFile[MAX_PATH],earlierFile[MAX_PATH];
    CHECK(SUCCEEDED(StringCchPrintfW(live,MAX_PATH,L"%s\\watcher.node",root)));
    CHECK(SUCCEEDED(StringCchPrintfW(backup,MAX_PATH,L"%s.oa-stock",live)));
    CHECK(SUCCEEDED(StringCchPrintfW(staged,MAX_PATH,L"%s.oa-new",live)));
    CHECK(SUCCEEDED(StringCchPrintfW(source,MAX_PATH,L"%s\\payload-watcher.node",root)));
    CHECK(SUCCEEDED(StringCchPrintfW(marker,MAX_PATH,L"%s\\desktop-fix.pending",root)));
    CHECK(SUCCEEDED(StringCchPrintfW(stockFile,MAX_PATH,L"%s\\stock.bin",root)));
    CHECK(SUCCEEDED(StringCchPrintfW(otherFile,MAX_PATH,L"%s\\other.bin",root)));
    CHECK(SUCCEEDED(StringCchPrintfW(earlierFile,MAX_PATH,L"%s\\earlier.bin",root)));
    CHECK(put(earlierFile,"MZ earlier OA build of the fix"));CHECK(digest_of(earlierFile,earlierHex));
    CHECK(put(stockFile,"MZ stock watcher 2.5.1"));CHECK(put(source,"MZ patched watcher 2.5.1 + #277"));CHECK(put(otherFile,"MZ watcher from another release"));
    CHECK(digest_of(stockFile,stockHex)&&digest_of(source,patchedHex)&&digest_of(otherFile,otherHex));
    const char *stock[]={stockHex,stockHex};const char *earlier[]={earlierHex};
    OA_DESKTOP_FIX fix={live,backup,staged,source,marker,patchedHex,stock,2,earlier,1};
    /* Production wiring: every listed release names a stock hash, all differ from the patched pin. */
    CHECK(OA_DESKTOP_RELEASE_COUNT==2);
    for(unsigned i=0;i<OA_DESKTOP_RELEASE_COUNT;i++)CHECK(lstrlenA(OA_DESKTOP_RELEASES[i].watcher_stock_sha256)==64&&lstrcmpA(OA_DESKTOP_RELEASES[i].watcher_stock_sha256,OA_DESKTOP_WATCHER_PATCHED_SHA256)!=0);
    CHECK(OA_DESKTOP_WATCHER_EARLIER_COUNT==1&&lstrcmpA(OA_DESKTOP_WATCHER_EARLIER[0],OA_DESKTOP_WATCHER_PATCHED_SHA256)!=0);

    /* Stock -> patched, the stock bytes kept beside it, the transaction marked. */
    CHECK(CopyFileW(stockFile,live,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED);
    CHECK(is(live,patchedHex)&&is(backup,stockHex)&&fix_marked(&fix)==OA_FIX_MARK_INSTALL&&absent(staged));
    CHECK(desktop_fix_commit(&fix,FALSE));
    CHECK(fix_marked(&fix)==OA_FIX_MARK_NONE&&is(backup,stockHex));
    /* Already patched: a no-op, and no marker for Rollback to act on. */
    CHECK(desktop_fix_install(&fix)==OA_FIX_ALREADY);
    CHECK(is(live,patchedHex)&&fix_marked(&fix)==OA_FIX_MARK_NONE);
    CHECK(desktop_fix_rollback(&fix)&&is(live,patchedHex)&&is(backup,stockHex));

    /* Remove restores the stock bytes; its commit drops the kept copy. */
    CHECK(desktop_fix_remove(&fix));
    CHECK(is(live,stockHex)&&is(backup,stockHex)&&fix_marked(&fix)==OA_FIX_MARK_REMOVE);
    CHECK(desktop_fix_commit(&fix,TRUE));
    CHECK(is(live,stockHex)&&absent(backup)&&absent(marker));
    /* Remove with nothing kept leaves the Desktop as it is. */
    CHECK(desktop_fix_remove(&fix)&&is(live,stockHex)&&absent(marker));

    /* Rollback of Install puts the stock bytes back and forgets the copy. */
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED);
    CHECK(desktop_fix_rollback(&fix));
    CHECK(is(live,stockHex)&&absent(backup)&&absent(marker));

    /* Rollback of Remove re-applies the fix and keeps the stock copy. */
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED&&desktop_fix_commit(&fix,FALSE));
    CHECK(desktop_fix_remove(&fix)&&is(live,stockHex));
    CHECK(desktop_fix_rollback(&fix));
    CHECK(is(live,patchedHex)&&is(backup,stockHex)&&absent(marker));
    /* Remove over a live file OA did not write (an OpenCode update) leaves it. */
    CHECK(CopyFileW(otherFile,live,FALSE));
    CHECK(desktop_fix_remove(&fix)&&is(live,otherHex)&&absent(marker));
    clean();

    /* Repair after OpenCode's updater restored the stock file: the step runs
       again, with or without the earlier kept copy, and re-applies the fix. */
    CHECK(CopyFileW(stockFile,live,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED&&desktop_fix_commit(&fix,FALSE));
    CHECK(CopyFileW(stockFile,live,FALSE)&&DeleteFileW(backup));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED);
    CHECK(is(live,patchedHex)&&is(backup,stockHex)&&fix_marked(&fix)==OA_FIX_MARK_INSTALL&&desktop_fix_commit(&fix,FALSE));
    CHECK(CopyFileW(stockFile,live,FALSE)&&is(backup,stockHex));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED&&is(live,patchedHex)&&desktop_fix_commit(&fix,FALSE));
    /* Repair after an update to an unlisted release: that watcher stays. */
    CHECK(CopyFileW(otherFile,live,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_UNKNOWN&&is(live,otherHex)&&absent(marker)&&is(backup,stockHex));
    clean();

    /* A stale marker from an interrupted transaction: Install over the patched
       file clears it, and a later Rollback leaves the patched file. */
    CHECK(CopyFileW(stockFile,live,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED&&desktop_fix_commit(&fix,FALSE));
    CHECK(fix_mark(&fix,OA_FIX_MARK_INSTALL));
    CHECK(desktop_fix_install(&fix)==OA_FIX_ALREADY&&absent(marker));
    CHECK(desktop_fix_rollback(&fix)&&is(live,patchedHex)&&is(backup,stockHex));
    /* Remove clears a stale marker too before it restores. */
    CHECK(fix_mark(&fix,OA_FIX_MARK_INSTALL));
    CHECK(desktop_fix_remove(&fix)&&is(live,stockHex)&&fix_marked(&fix)==OA_FIX_MARK_REMOVE&&desktop_fix_commit(&fix,TRUE));
    clean();

    /* An earlier OA build: Install replaces it and keeps the stock copy it was
       applied over; Remove restores the stock bytes over an earlier build. */
    CHECK(CopyFileW(earlierFile,live,FALSE)&&CopyFileW(stockFile,backup,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED);
    CHECK(is(live,patchedHex)&&is(backup,stockHex)&&fix_marked(&fix)==OA_FIX_MARK_INSTALL&&desktop_fix_commit(&fix,FALSE));
    CHECK(CopyFileW(earlierFile,live,FALSE));
    CHECK(desktop_fix_remove(&fix)&&is(live,stockHex)&&desktop_fix_commit(&fix,TRUE)&&absent(backup));
    clean();

    /* Install rollback over a live file that is neither stock nor OA's: the
       verified stock copy goes back and stays kept. */
    CHECK(CopyFileW(stockFile,live,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED);
    CHECK(CopyFileW(otherFile,live,FALSE));
    CHECK(desktop_fix_rollback(&fix)&&is(live,stockHex)&&is(backup,stockHex)&&absent(marker));
    /* Without a verified stock copy, Rollback writes nothing and keeps the file. */
    CHECK(desktop_fix_install(&fix)==OA_FIX_APPLIED&&CopyFileW(otherFile,backup,FALSE));
    CHECK(desktop_fix_rollback(&fix)&&is(live,patchedHex)&&is(backup,otherHex)&&absent(marker));
    clean();

    /* Unknown hash: nothing written. */
    CHECK(CopyFileW(otherFile,live,FALSE));
    CHECK(desktop_fix_install(&fix)==OA_FIX_UNKNOWN);
    CHECK(is(live,otherHex)&&absent(backup)&&absent(marker)&&absent(staged));
    /* A missing watcher is unknown too. */
    CHECK(DeleteFileW(live));
    CHECK(desktop_fix_install(&fix)==OA_FIX_UNKNOWN&&absent(live)&&absent(backup));

    /* A payload copy that differs from the patched pin never reaches live. */
    CHECK(CopyFileW(stockFile,live,FALSE));
    CHECK(put(source,"MZ tampered payload"));
    CHECK(desktop_fix_install(&fix)==OA_FIX_FAILED);
    CHECK(is(live,stockHex)&&absent(marker)&&absent(staged));
    clean();
    printf("desktop fix PASS: stock->patched, already patched, unknown left unchanged, repair re-applies, earlier OA builds replaced, stale markers cleared, rollback and remove restore, rollback of remove re-applies\n");
    return 0;
}
