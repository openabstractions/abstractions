#include <abstraction/download/digest.h>
#include <abstraction/download/sink.h>
#include <abstraction/job/store.h>

#include <cstdio>
#include <string>

int main() {
    abstraction::job::FileStore store("store");
    abstraction::job::Record r;
    r.kind = "stranger";
    r.spec = abstraction::job::Json::object();
    const std::string id = store.submit(r);
    const abstraction::job::Record back = store.load(id);
    std::printf("state=%s kind=%s sink=%s digest=%s\n", back.state.c_str(), back.kind.c_str(),
                abstraction::download::portable("models\\x.gguf").c_str(),
                abstraction::download::normal_digest(std::string(64, 'a')).c_str());
    return back.id == id ? 0 : 1;
}
