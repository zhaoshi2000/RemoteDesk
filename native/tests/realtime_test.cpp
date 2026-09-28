#include "core/realtime.hpp"
#include <iostream>
#include <limits>
#include <string>
#include <thread>

static int checks=0;
static void require(bool ok,const char* name){++checks;if(!ok)throw std::runtime_error(name);}
int main(){try{
    rd::Scheduler q(1024);
    require(q.push({rd::Channel::file,{1,2,3},0}),"file enqueue");
    require(q.push({rd::Channel::keyboard,{4},0}),"keyboard enqueue");
    require(q.push({rd::Channel::control,{5},0}),"control enqueue");
    require(q.pop(1)->channel==rd::Channel::control,"control priority");
    require(q.pop(1)->channel==rd::Channel::keyboard,"keyboard priority");
    require(q.pop(1)->channel==rd::Channel::file,"file after input");
    require(!q.pop(1),"empty queue");
    q.push({rd::Channel::mouse_position,{1},0});q.push({rd::Channel::mouse_position,{2},0});
    require(q.bytes()==1,"mouse coalescing size");require(q.pop(1)->data[0]==2,"latest mouse position");
    q.push({rd::Channel::video,{9},100});require(!q.pop(100),"expired video discarded");
    require(!q.push({rd::Channel::file,std::vector<std::uint8_t>(2048),0}),"bounded queue");
    require(!q.push({static_cast<rd::Channel>(255),{1},0}),"invalid channel rejected");
    std::thread a([&]{for(int i=0;i<1000;++i)q.push({rd::Channel::mouse_position,{1},0});});
    std::thread b([&]{for(int i=0;i<1000;++i)(void)q.pop(1);});a.join();b.join();require(q.bytes()<=1024,"concurrent queue bound");
    rd::AdaptiveRate rate;auto initial=rate.update({20,0,0,0});require(initial.fps==60,"initial target");
    auto early=rate.update({80,40,0.1,100});require(early.bitrate_bps==initial.bitrate_bps,"downshift hysteresis");
    auto lower=rate.update({80,40,0.1,500});require(lower.bitrate_bps<initial.bitrate_bps,"congestion reduction");
    for(std::uint64_t time=1000;time<=20'000;time+=500)rate.update({100,100,0.2,time});
    require(rate.target().bitrate_bps>=750'000&&rate.target().fps==30,"minimum operating point");
    const auto old=rate.target().bitrate_bps;rate.update({20,0,0,20'100});require(rate.target().bitrate_bps==old,"recovery hysteresis");
    rate.update({20,0,0,22'100});require(rate.target().bitrate_bps>old,"gradual recovery");
    bool rejected=false;try{rate.update({-1,0,0,23'000});}catch(const std::invalid_argument&){rejected=true;}require(rejected,"invalid RTT rejected");
    std::cout<<"PASS "<<checks<<" native checks (queue + adaptive controller; no GPU/QUIC benchmark)\n";return 0;
}catch(const std::exception& e){std::cerr<<"FAIL: "<<e.what()<<'\n';return 1;}}
