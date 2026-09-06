cmake_minimum_required(VERSION 3.16)
project(stranger LANGUAGES CXX)

find_package(abstraction 0.1 CONFIG REQUIRED)

add_executable(stranger main.cpp)
target_link_libraries(stranger PRIVATE abstraction::job abstraction::download)
