#ifndef OA_PANEL_NATIVE_PRESENCE_H
#define OA_PANEL_NATIVE_PRESENCE_H
#include <stddef.h>
#ifdef __cplusplus
extern "C" {
#endif
/* Private launcher bridge. No endpoint, application, method or RPC input. */
typedef struct oa_panel_presence oa_panel_presence;
oa_panel_presence *oa_panel_presence_create(void);
/* Initial empty-instance Announce or renewal of the one returned handle.
   0 means applied; any other result retires this handle without retry. */
int oa_panel_presence_announce(oa_panel_presence *, char *instance, size_t capacity);
void oa_panel_presence_close(oa_panel_presence *);
#ifdef __cplusplus
}
#endif
#endif
