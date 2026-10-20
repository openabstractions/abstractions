#ifndef OA_PANEL_NATIVE_HOST_H
#define OA_PANEL_NATIVE_HOST_H
#include "NativePresence.h"
#include <abstraction/facade/appmodel.h>
#ifdef __cplusplus
extern "C" {
#endif
/* Private generated-record projection for this signed host. The common core
 * owns all schema checks, callback state and MCP interpretation. */
oa_appmodel_status oa_panel_appmodel_configure(oa_appmodel_session *, const char *, size_t);
oa_appmodel_status oa_panel_appmodel_window_closed(oa_appmodel *, uint64_t);
#ifdef __cplusplus
}
#endif
#endif
