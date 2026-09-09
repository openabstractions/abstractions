import sys

import abstraction_download as download
import abstraction_job as job

store = job.FileStore("store")
job_id = store.submit(job.Record(id="", kind="stranger", spec={}))
back = store.load(job_id)
print("state=%s kind=%s sink=%s digest=%s" % (
    back.state, back.kind, download.portable(r"models\x.gguf"), download.normal_digest("a" * 64)))
sys.exit(0 if back.id == job_id else 1)
