#include "generated/cpp/rec.h"
#include <fstream>
#include <iostream>

std::string read(const std::string& path) {
    std::ifstream f(path, std::ios::binary);
    if(!f) throw std::runtime_error("missing input");
    return std::string(std::istreambuf_iterator<char>(f), {});
}
int main(int argc,char**argv) {
    if(argc<4)return 2;
    try {
        auto r=rec::decode(read(argv[1]));const std::string scope=argv[3];
        if(scope=="mutate") {r.id="edited";if(r.child)r.child->name="edited-child";for(auto& child:r.children)child.name="edited-child";}
        else if(scope!="read") {
            auto* target=&r.extras;
            if(scope=="child")target=&r.child->extras;else if(scope=="repeated")target=&r.children[0].extras;
            const std::string key=scope=="bad-key"?std::string(1,char(255)):argv[4];
            (*target)[key]=read(argv[5]);
        }
        auto output=rec::encode(r);rec::decode(output);
        std::ofstream f(argv[2],std::ios::binary);f<<output;
        if(!f)throw std::runtime_error("output failed");
        std::cout<<"ok";
    }catch(const rec::Refusal&e){std::cout<<e.word;}
}
