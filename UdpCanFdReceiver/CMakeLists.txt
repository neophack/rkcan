cmake_minimum_required(VERSION 3.10)
project(UdpCanFdReceiver LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)

add_executable(udp_canfd_recv main.cpp)
if(WIN32)
    target_link_libraries(udp_canfd_recv ws2_32)
endif()

# Optional statistics tool, built only when its source is present
if(EXISTS ${CMAKE_CURRENT_SOURCE_DIR}/stat_main.cpp)
    add_executable(udp_canfd_stat stat_main.cpp)
    if(WIN32)
        target_link_libraries(udp_canfd_stat ws2_32)
    endif()
endif()
