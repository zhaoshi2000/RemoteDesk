#include "ipc/config.hpp"
#include "app/worker.hpp"
#include "core/realtime.hpp"
#include "input/input.hpp"
#include "input/clipboard.hpp"
#include <windowsx.h>
#include <tlhelp32.h>
#include <fcntl.h>
#include <io.h>
#include <iostream>
#include <thread>
#include <sstream>
using namespace rd;
namespace {
struct WindowState {bool host=false,closed=false,control=false;std::uint64_t seq=0,lastMove=0;HCURSOR cursor=nullptr;std::deque<wire::Message> input;std::uint16_t x=0,y=0;};
void enqueue(WindowState* s,wire::Kind kind,wire::Bytes bytes={}){if(!s->control&&kind!=wire::release_all)return;wire::Message m;m.channel=wire::input;m.kind=kind;m.id=++s->seq;m.timestamp=wire::now_us();m.payload=std::move(bytes);if(kind==wire::mouse_move&&!s->input.empty()&&s->input.back().kind==kind)s->input.back()=std::move(m);else s->input.push_back(std::move(m));if(s->input.size()>256){s->input.clear();wire::Message release;release.channel=wire::input;release.kind=wire::release_all;s->input.push_back(release);}}
LRESULT CALLBACK window_proc(HWND hwnd,UINT message,WPARAM w,LPARAM l){
 auto* s=reinterpret_cast<WindowState*>(GetWindowLongPtrW(hwnd,GWLP_USERDATA));if(message==WM_NCCREATE){auto* create=reinterpret_cast<CREATESTRUCTW*>(l);s=static_cast<WindowState*>(create->lpCreateParams);SetWindowLongPtrW(hwnd,GWLP_USERDATA,reinterpret_cast<LONG_PTR>(s));}
 if(!s)return DefWindowProcW(hwnd,message,w,l);
 if(message==WM_CLOSE){enqueue(s,wire::release_all);s->closed=true;return 0;}
 if(message==WM_KILLFOCUS){enqueue(s,wire::release_all);return 0;}
 if(message==WM_ERASEBKGND&&!s->host)return 1;
 if(message==WM_PAINT){PAINTSTRUCT p;BeginPaint(hwnd,&p);if(s->host){RECT r;GetClientRect(hwnd,&r);DrawTextW(p.hdc,L"RemoteDesk：已授权的远程会话正在进行\n关闭此窗口立即断开",-1,&r,DT_CENTER|DT_VCENTER|DT_WORDBREAK);}EndPaint(hwnd,&p);return 0;}
 if(message==WM_SETCURSOR&&!s->host){SetCursor(s->cursor?s->cursor:LoadCursorW(nullptr,IDC_ARROW));return TRUE;}
 if(s->host)return DefWindowProcW(hwnd,message,w,l);
 if(message==WM_MOUSEMOVE){RECT r;GetClientRect(hwnd,&r);if(r.right>0&&r.bottom>0){s->x=std::uint16_t(std::clamp<LONG>(GET_X_LPARAM(l),0,r.right)*65535/r.right);s->y=std::uint16_t(std::clamp<LONG>(GET_Y_LPARAM(l),0,r.bottom)*65535/r.bottom);wire::Bytes p(4);wire::put16(p.data(),s->x);wire::put16(p.data()+2,s->y);s->lastMove=wire::now_us();enqueue(s,wire::mouse_move,std::move(p));}return 0;}
 if(message==WM_LBUTTONDOWN||message==WM_LBUTTONUP||message==WM_RBUTTONDOWN||message==WM_RBUTTONUP||message==WM_MBUTTONDOWN||message==WM_MBUTTONUP){const bool down=message==WM_LBUTTONDOWN||message==WM_RBUTTONDOWN||message==WM_MBUTTONDOWN;const auto b=(message==WM_LBUTTONDOWN||message==WM_LBUTTONUP)?0:(message==WM_RBUTTONDOWN||message==WM_RBUTTONUP)?1:2;if(down){SetFocus(hwnd);SetCapture(hwnd);}else ReleaseCapture();enqueue(s,wire::mouse_button,{std::uint8_t(b),std::uint8_t(down)});return 0;}
 if(message==WM_MOUSEWHEEL||message==WM_MOUSEHWHEEL){wire::Bytes p(3);p[0]=message==WM_MOUSEHWHEEL;wire::put16(p.data()+1,std::uint16_t(GET_WHEEL_DELTA_WPARAM(w)));enqueue(s,wire::mouse_wheel,std::move(p));return 0;}
 if(message==WM_KEYDOWN||message==WM_KEYUP||message==WM_SYSKEYDOWN||message==WM_SYSKEYUP){if(w==VK_F12&&(GetKeyState(VK_CONTROL)&0x8000)&&(GetKeyState(VK_SHIFT)&0x8000)){s->closed=true;enqueue(s,wire::release_all);return 0;}wire::Bytes p(3);wire::put16(p.data(),std::uint16_t((l>>16)&0x1ff));p[2]=message==WM_KEYDOWN||message==WM_SYSKEYDOWN;enqueue(s,wire::key,std::move(p));return 0;}
 return DefWindowProcW(hwnd,message,w,l);
}
HWND create_window(const MediaConfig& c,WindowState* s){WNDCLASSW cls{};cls.lpfnWndProc=window_proc;cls.hInstance=GetModuleHandleW(nullptr);cls.lpszClassName=L"RemoteDeskNativeWindow";cls.hCursor=LoadCursorW(nullptr,IDC_ARROW);cls.hbrBackground=reinterpret_cast<HBRUSH>(COLOR_WINDOW+1);if(!RegisterClassW(&cls)&&GetLastError()!=ERROR_CLASS_ALREADY_EXISTS)throw std::runtime_error("register media window");
 HWND parent=reinterpret_cast<HWND>(std::uintptr_t(c.parentWindow));if(parent&&!IsWindow(parent))throw std::runtime_error("invalid Qt canvas window");DWORD style=parent?(WS_CHILD|WS_VISIBLE):(WS_OVERLAPPEDWINDOW|WS_VISIBLE);int w=c.host?430:1280,h=c.host?110:720;HWND hwnd=CreateWindowExW(c.host?WS_EX_TOPMOST:0,cls.lpszClassName,c.host?L"RemoteDesk 远程会话 — 关闭可断开":L"RemoteDesk 远程桌面",style,CW_USEDEFAULT,CW_USEDEFAULT,w,h,parent,nullptr,cls.hInstance,s);if(!hwnd)throw std::runtime_error("create media window");return hwnd;
}
wire::Bytes settings(const MediaConfig& c){wire::Bytes p(16);wire::put16(p.data(),c.width);wire::put16(p.data()+2,c.height);wire::put16(p.data()+4,c.fps);wire::put16(p.data()+6,c.monitor);wire::put32(p.data()+8,c.bitrate);wire::put32(p.data()+12,(c.control?2u:0u)|(c.clipboard?4u:0u)|(c.audio?8u:0u));return p;}
void apply_settings(MediaConfig& c,const wire::Bytes& p){if(p.size()!=16)throw std::runtime_error("bad video settings");const auto w=wire::get16(p.data()),h=wire::get16(p.data()+2),fps=wire::get16(p.data()+4),monitor=wire::get16(p.data()+6);const auto b=wire::get32(p.data()+8),flags=wire::get32(p.data()+12);if(w<16||h<16||w>8192||h>8192||(w&1)||(h&1)||fps==0||fps>120||monitor>31||b<250000||b>100000000||flags&~14u)throw std::runtime_error("video settings bounds");c.width=w;c.height=h;c.fps=fps;c.monitor=monitor;c.bitrate=b;c.control=c.control&&(flags&2);c.clipboard=c.clipboard&&(flags&4);c.audio=c.audio&&(flags&8);}
void update_cursor(WindowState& s,const wire::Bytes& p){if(p.size()<24)return;const auto type=wire::get32(p.data()),w=wire::get32(p.data()+4),h=wire::get32(p.data()+8),pitch=wire::get32(p.data()+12),hx=wire::get32(p.data()+16),hy=wire::get32(p.data()+20);if(w==0||h==0||w>256||h>512||hx>=w||hy>=h||std::uint64_t(pitch)*h!=p.size()-24)return;
 HBITMAP color=nullptr,mask=nullptr;ICONINFO info{};info.fIcon=FALSE;info.xHotspot=hx;info.yHotspot=hy;
 if(type==DXGI_OUTDUPL_POINTER_SHAPE_TYPE_COLOR||type==DXGI_OUTDUPL_POINTER_SHAPE_TYPE_MASKED_COLOR){if(pitch<w*4)return;BITMAPINFO bi{};bi.bmiHeader.biSize=sizeof(BITMAPINFOHEADER);bi.bmiHeader.biWidth=LONG(w);bi.bmiHeader.biHeight=-LONG(h);bi.bmiHeader.biPlanes=1;bi.bmiHeader.biBitCount=32;bi.bmiHeader.biCompression=BI_RGB;void* pixels=nullptr;color=CreateDIBSection(nullptr,&bi,DIB_RGB_COLORS,&pixels,nullptr,0);if(!color)return;for(UINT y=0;y<h;++y)std::memcpy(static_cast<std::uint8_t*>(pixels)+y*w*4,p.data()+24+y*pitch,w*4);std::vector<std::uint8_t> zeros(((w+15)/16)*2*h);mask=CreateBitmap(int(w),int(h),1,1,zeros.data());info.hbmColor=color;info.hbmMask=mask;
 }else if(type==DXGI_OUTDUPL_POINTER_SHAPE_TYPE_MONOCHROME){if(h%2||pitch<(w+7)/8)return;const auto stride=((w+15)/16)*2;std::vector<std::uint8_t> packed(stride*h);for(UINT y=0;y<h;++y)std::memcpy(packed.data()+y*stride,p.data()+24+y*pitch,std::min(stride,pitch));mask=CreateBitmap(int(w),int(h),1,1,packed.data());info.hbmMask=mask;}else return;
 auto cursor=static_cast<HCURSOR>(CreateIconIndirect(&info));if(color)DeleteObject(color);if(mask)DeleteObject(mask);if(cursor){if(s.cursor)DestroyCursor(s.cursor);s.cursor=cursor;SetCursor(cursor);}
}
class ParentGuard {
 HANDLE parent_=nullptr;
public:
 ParentGuard(){HANDLE snap=CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS,0);if(snap==INVALID_HANDLE_VALUE)throw std::runtime_error("cannot identify parent agent");PROCESSENTRY32W e{};e.dwSize=sizeof(e);DWORD pid=0;if(Process32FirstW(snap,&e)){do{if(e.th32ProcessID==GetCurrentProcessId()){pid=e.th32ParentProcessID;break;}}while(Process32NextW(snap,&e));}CloseHandle(snap);if(pid)parent_=OpenProcess(SYNCHRONIZE,FALSE,pid);if(!parent_)throw std::runtime_error("parent agent is unavailable");}
 ~ParentGuard(){if(parent_)CloseHandle(parent_);}
 bool alive()const{return WaitForSingleObject(parent_,0)==WAIT_TIMEOUT;}
};
int run(MediaConfig cfg){
 ParentGuard parentGuard;
 SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);check(CoInitializeEx(nullptr,COINIT_MULTITHREADED),"main COM init");struct COM{~COM(){CoUninitialize();}} com;
 DWORD session=0;ProcessIdToSessionId(GetCurrentProcessId(),&session);if(cfg.host&&session==0)throw std::runtime_error("capture must run in an interactive user session, not Session 0; use the user-session host task");
 auto qcfg=std::move(cfg.quic);QuicSession network(std::move(qcfg));std::cout<<"READY "<<network.port()<<std::endl;
 WindowState state;state.host=cfg.host;HWND window=create_window(cfg,&state);struct WindowGuard{HWND h;WindowState& s;~WindowGuard(){DestroyWindow(h);if(s.cursor)DestroyCursor(s.cursor);}} guard{window,state};
 std::unique_ptr<D3DRenderer> renderer;std::unique_ptr<H264Decoder> decoder;std::unique_ptr<CaptureWorker> capture;std::unique_ptr<InputController> input;std::unique_ptr<TextClipboard> clipboard;std::unique_ptr<AudioPlayback> audio;AdaptiveRate rate;
 if(!cfg.host){renderer=std::make_unique<D3DRenderer>(window,cfg.adapter);decoder=std::make_unique<H264Decoder>(renderer->device());}
 bool helloSent=false,authenticated=false,waitingKey=true;std::uint64_t lastVideo=0,lastPing=0,lastLog=0,lastContact=wire::now_us(),lastKeyRequest=0,previousDrops=0,frames=0,lastFrames=0,decodeTime=0,renderTime=0;double rtt=0;UINT vw=0,vh=0;
 auto requestIDR=[&](){auto now=wire::now_us();if(now-lastKeyRequest<200000)return;lastKeyRequest=now;wire::Message m;m.kind=wire::request_keyframe;network.send(std::move(m));};
 while(!state.closed&&parentGuard.alive()){
  MSG msg;while(PeekMessageW(&msg,nullptr,0,0,PM_REMOVE)){if(msg.message==WM_QUIT){state.closed=true;break;}TranslateMessage(&msg);DispatchMessageW(&msg);}
  if(cfg.parentWindow){HWND parent=reinterpret_cast<HWND>(std::uintptr_t(cfg.parentWindow));if(!IsWindow(parent))break;RECT rect{};GetClientRect(parent,&rect);SetWindowPos(window,nullptr,0,0,rect.right,rect.bottom,SWP_NOZORDER|SWP_NOACTIVATE);}
  auto messages=network.pump();auto now=wire::now_us();if(network.ready()&&!helloSent){helloSent=true;wire::Message m;m.kind=wire::hello;m.payload=settings(cfg);network.send(std::move(m));}
  for(auto& m:messages){lastContact=now;
   if(m.channel==wire::control&&m.kind==wire::hello){if(authenticated)continue;apply_settings(cfg,m.payload);rate=AdaptiveRate({cfg.bitrate,cfg.fps,cfg.width,cfg.height});authenticated=true;state.control=!cfg.host&&cfg.control;if(cfg.clipboard)clipboard=std::make_unique<TextClipboard>(window);
    if(cfg.host){capture=std::make_unique<CaptureWorker>(cfg);input=std::make_unique<InputController>(RECT{});}else if(cfg.audio){try{audio=std::make_unique<AudioPlayback>();}catch(const std::exception& e){std::cerr<<e.what()<<'\n';}}continue;}
   if(!authenticated)continue;
   if(m.channel==wire::control&&m.kind==wire::ping){m.kind=wire::pong;network.send(std::move(m));continue;}
   if(m.channel==wire::control&&m.kind==wire::pong){if(m.timestamp<=now)rtt=double(now-m.timestamp)/1000.0;continue;}
   if(m.channel==wire::control&&m.kind==wire::video_ack){network.acknowledge(m.id);continue;}
   if(m.channel==wire::control&&m.kind==wire::request_keyframe){if(capture)capture->request_keyframe();continue;}
   if(m.kind==wire::close){state.closed=true;break;}
   if(m.channel==wire::input&&cfg.host&&cfg.control&&input){auto bounds=capture->bounds();if(bounds.right>bounds.left){input->set_monitor(bounds);input->handle(m);}continue;}
   if(m.channel==wire::clipboard&&clipboard){clipboard->receive(std::move(m));continue;}
   if(m.channel==wire::audio&&m.kind==wire::audio_frame&&audio){audio->receive(m);continue;}
   if(!cfg.host&&m.kind==wire::pointer_shape){update_cursor(state,m.payload);continue;}
   if(!cfg.host&&m.channel==wire::video&&m.kind==wire::video_frame){
    wire::Message ack;ack.kind=wire::video_ack;ack.id=m.id;ack.timestamp=m.timestamp;network.send(ack);
    if(m.id<=lastVideo)continue;const bool key=(m.flags&wire::keyframe)!=0;if((lastVideo&&m.id!=lastVideo+1)||network.dropped()!=previousDrops){waitingKey=true;decoder->flush();requestIDR();}previousDrops=network.dropped();lastVideo=m.id;if(waitingKey&&!key){requestIDR();continue;}
    auto v=wire::parse_video(m.payload);if(v.width!=vw||v.height!=vh){decoder->flush();vw=v.width;vh=v.height;if(!key){waitingKey=true;requestIDR();continue;}}if(key){waitingKey=false;}
    auto render=[&](){for(auto& frame:decoder->receive()){auto* tex=reinterpret_cast<ID3D11Texture2D*>(frame->data[0]);auto slice=UINT(reinterpret_cast<std::uintptr_t>(frame->data[1]));auto before=wire::now_us();if(renderer->present(tex,slice))++frames;renderTime=wire::now_us()-before;}};
    try{auto start=wire::now_us();render();if(!decoder->submit(v.au,std::int64_t(m.id))){render();if(!decoder->submit(v.au,std::int64_t(m.id)))throw std::runtime_error("decoder backpressure");}decodeTime=wire::now_us()-start;render();}catch(const std::exception& e){std::cerr<<"decoder recovery: "<<e.what()<<'\n';decoder->flush();waitingKey=true;requestIDR();}
   }
  }
  if(authenticated){
   if(capture){for(auto& m:capture->drain())network.send(std::move(m));if(network.dropped()!=previousDrops){previousDrops=network.dropped();capture->request_keyframe();}}
   while(!state.input.empty()){network.send(std::move(state.input.front()));state.input.pop_front();}
   if(clipboard){if(auto m=clipboard->poll())network.send(std::move(*m));}
   if(audio)audio->pump();
   if(now-lastPing>=1000000){lastPing=now;wire::Message ping;ping.kind=wire::ping;ping.timestamp=now;network.send(ping);
    if(capture){auto target=rate.update({rtt,double(network.queued_video())*8.0/std::max(1u,cfg.bitrate)*1000.0,0,now/1000});const auto b=std::min(cfg.bitrate,target.bitrate_bps);capture->target(b,std::min<unsigned>(cfg.fps,target.fps),std::min<unsigned>(cfg.width,target.width),std::min<unsigned>(cfg.height,target.height));}
   }
   if(now-lastLog>=1000000){auto elapsed=lastLog?double(now-lastLog)/1000000:1.0;lastLog=now;std::ostringstream out;out<<"{\"event\":\"media-stats\",\"role\":\""<<(cfg.host?"host":"viewer")<<"\",\"render_fps\":"<<double(frames-lastFrames)/elapsed<<",\"rtt_ms\":"<<rtt<<",\"decode_submit_us\":"<<decodeTime<<",\"render_submit_us\":"<<renderTime<<",\"send_queue_bytes\":"<<network.queued_video()<<",\"dropped\":"<<network.dropped();if(capture){auto s=capture->stats();out<<",\"captured\":"<<s.captured<<",\"encoded\":"<<s.encoded<<",\"capture_submit_us\":"<<s.capture_us<<",\"encode_output_us\":"<<s.encode_us;}out<<"}";std::cout<<out.str()<<std::endl;lastFrames=frames;}
   if(now-lastContact>15000000)throw std::runtime_error("media peer heartbeat timed out");
  }
  std::this_thread::sleep_for(std::chrono::milliseconds(1));
 }
 if(input)input->release();if(network.ready()){wire::Message m;m.channel=wire::input;m.kind=wire::release_all;network.send(m);m.channel=wire::control;m.kind=wire::close;network.send(m);for(int i=0;i<10;++i){network.pump();std::this_thread::sleep_for(std::chrono::milliseconds(1));}}
 return 0;
}
}
int main(int argc,char** argv){try{if(argc!=2||std::string(argv[1])!="--ipc")throw std::runtime_error("launch through remote-agent desktop or the authenticated host; standalone key arguments are prohibited");_setmode(_fileno(stdin),_O_BINARY);return run(MediaConfig::read(std::cin));}catch(const std::exception& e){std::cerr<<"native media error: "<<e.what()<<std::endl;return 1;}}
