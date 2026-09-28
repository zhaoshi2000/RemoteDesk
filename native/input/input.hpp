#pragma once
#include "capture/dxgi_capture.hpp"
#include "transport/wire.hpp"
#include <set>
namespace rd {
// Input is accepted only after device authentication AND the host's Control capability grant.
class InputController {
 RECT monitor_{};std::set<unsigned> keys_;std::set<unsigned> buttons_;
 static void send(INPUT in){if(SendInput(1,&in,sizeof(in))!=1)throw std::runtime_error("SendInput rejected (desktop/UIPI boundary)");}
public:
 explicit InputController(RECT monitor):monitor_(monitor){}
 ~InputController(){release();}
 void set_monitor(RECT monitor){if(std::memcmp(&monitor_,&monitor,sizeof(RECT))){release();monitor_=monitor;}}
 void release()noexcept{
  for(auto key:keys_){INPUT in{};in.type=INPUT_KEYBOARD;in.ki.wScan=WORD(key&255);in.ki.dwFlags=KEYEVENTF_SCANCODE|KEYEVENTF_KEYUP|((key&256)?KEYEVENTF_EXTENDEDKEY:0);SendInput(1,&in,sizeof(in));}keys_.clear();
  for(auto b:buttons_){INPUT in{};in.type=INPUT_MOUSE;const DWORD up[]={MOUSEEVENTF_LEFTUP,MOUSEEVENTF_RIGHTUP,MOUSEEVENTF_MIDDLEUP};if(b<3){in.mi.dwFlags=up[b];SendInput(1,&in,sizeof(in));}}buttons_.clear();
 }
 void handle(const wire::Message& m){
  auto& p=m.payload;
  if(m.kind==wire::release_all){release();return;}
  if(m.kind==wire::mouse_move){if(p.size()!=4)throw std::runtime_error("bad pointer packet");const auto x=wire::get16(p.data()),y=wire::get16(p.data()+2);const LONG px=monitor_.left+LONG((std::int64_t(x)*(monitor_.right-monitor_.left-1))/65535),py=monitor_.top+LONG((std::int64_t(y)*(monitor_.bottom-monitor_.top-1))/65535);
   const auto vx=GetSystemMetrics(SM_XVIRTUALSCREEN),vy=GetSystemMetrics(SM_YVIRTUALSCREEN),vw=GetSystemMetrics(SM_CXVIRTUALSCREEN),vh=GetSystemMetrics(SM_CYVIRTUALSCREEN);if(vw<2||vh<2)return;INPUT in{};in.type=INPUT_MOUSE;in.mi.dx=LONG(std::clamp<std::int64_t>((std::int64_t(px)-vx)*65535/(vw-1),0,65535));in.mi.dy=LONG(std::clamp<std::int64_t>((std::int64_t(py)-vy)*65535/(vh-1),0,65535));in.mi.dwFlags=MOUSEEVENTF_MOVE|MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK;send(in);
  }else if(m.kind==wire::mouse_button){if(p.size()!=2||p[0]>2||p[1]>1)throw std::runtime_error("bad pointer button");const DWORD down[]={MOUSEEVENTF_LEFTDOWN,MOUSEEVENTF_RIGHTDOWN,MOUSEEVENTF_MIDDLEDOWN},up[]={MOUSEEVENTF_LEFTUP,MOUSEEVENTF_RIGHTUP,MOUSEEVENTF_MIDDLEUP};INPUT in{};in.type=INPUT_MOUSE;in.mi.dwFlags=p[1]?down[p[0]]:up[p[0]];send(in);if(p[1])buttons_.insert(p[0]);else buttons_.erase(p[0]);
  }else if(m.kind==wire::mouse_wheel){if(p.size()!=3||p[0]>1)throw std::runtime_error("bad wheel");INPUT in{};in.type=INPUT_MOUSE;in.mi.dwFlags=p[0]?MOUSEEVENTF_HWHEEL:MOUSEEVENTF_WHEEL;in.mi.mouseData=DWORD(std::int16_t(wire::get16(p.data()+1)));send(in);
  }else if(m.kind==wire::key){if(p.size()!=3||wire::get16(p.data())>511||p[2]>1)throw std::runtime_error("bad key");const unsigned k=wire::get16(p.data());if((k&255)==0)return;INPUT in{};in.type=INPUT_KEYBOARD;in.ki.wScan=WORD(k&255);in.ki.dwFlags=KEYEVENTF_SCANCODE|((k&256)?KEYEVENTF_EXTENDEDKEY:0)|(p[2]?0:KEYEVENTF_KEYUP);send(in);if(p[2])keys_.insert(k);else keys_.erase(k);
  }
 }
};
}
