/* Inert Win32 receiving test: queries only this test process, launches nothing. */
#include "installer_host.c"
#include <stdio.h>
#define CHECK(value) do {if(!(value)){fprintf(stderr,"token boundary failed at line %d\n",__LINE__);return 1;}} while(0)
int main(void) {
    HANDLE current=NULL;TOKEN_FACTS live,user,host;
    CHECK(OpenProcessToken(GetCurrentProcess(),TOKEN_QUERY,&current));
    CHECK(token_facts(current,&live));CloseHandle(current);
    CHECK(same_execution_user(&live,&live)==ordinary_user(&live));
    SID_IDENTIFIER_AUTHORITY authority=SECURITY_NT_AUTHORITY;PSID sid=NULL,other=NULL;
    CHECK(AllocateAndInitializeSid(&authority,2,21,1,0,0,0,0,0,0,&sid));
    CHECK(AllocateAndInitializeSid(&authority,2,21,2,0,0,0,0,0,0,&other));
    ZeroMemory(&user,sizeof(user));CHECK(CopySid(sizeof(user.sid),user.sid,sid));
    user.authentication.LowPart=7;user.authentication.HighPart=9;user.session=2;user.integrity=SECURITY_MANDATORY_MEDIUM_RID;
    host=user;CHECK(same_execution_user(&host,&user));
    host.elevated=TRUE;CHECK(!same_execution_user(&host,&user));
    host=user;host.integrity=SECURITY_MANDATORY_HIGH_RID;CHECK(!same_execution_user(&host,&user));
    host=user;host.service=TRUE;CHECK(!same_execution_user(&host,&user));
    host=user;host.authentication.LowPart++;CHECK(!same_execution_user(&host,&user));
    host=user;host.authentication.HighPart++;CHECK(!same_execution_user(&host,&user));
    host=user;host.session++;CHECK(!same_execution_user(&host,&user));
    host=user;CHECK(CopySid(sizeof(host.sid),host.sid,other));CHECK(!same_execution_user(&host,&user));
    host=user;user.integrity=SECURITY_MANDATORY_LOW_RID;CHECK(!same_execution_user(&host,&user));
    user=host;user.elevated=TRUE;CHECK(!same_execution_user(&host,&user));
    FreeSid(sid);FreeSid(other);
    printf("token boundary PASS; current process ordinary=%d; no child process or config mutation\n",ordinary_user(&live));
    return 0;
}
