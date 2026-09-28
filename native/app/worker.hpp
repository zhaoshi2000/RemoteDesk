#pragma once
#include "codec/ffmpeg_codec.hpp"
#include "audio/wasapi_opus.hpp"
#include "ipc/config.hpp"
#include <atomic>
#include <map>
#include <mutex>
#include <thread>
namespace rd {
struct CaptureStats {std::uint64_t captured=0,encoded=0,dropped=0,capture_us=0,encode_us=0;};
class CaptureWorker {
 MediaConfig config_;std::jthread thread_;std::mutex mutex_;std::deque<wire::Message> output_;std::string error_;CaptureStats stats_;RECT bounds_{};
 std::atomic<bool> keyframe_{true};std::atomic<unsigned> bitrate_;std::atomic<unsigned> fps_;std::atomic<unsigned> width_,height_;
 void enqueue(wire::Message m){std::lock_guard lock(mutex_);if(m.channel==wire::video){unsigned videos=0;for(auto& x:output_)if(x.channel==wire::video)++videos;if(videos>=3){for(auto it=output_.begin();it!=output_.end();)if(it->channel==wire::video){it=output_.erase(it);++stats_.dropped;}else ++it;keyframe_=true;}}
  if(output_.size()>=128){output_.pop_front();keyframe_=true;++stats_.dropped;}output_.push_back(std::move(m));}
 void run(std::stop_token stop){
  auto hr=CoInitializeEx(nullptr,COINIT_MULTITHREADED);if(FAILED(hr)){std::lock_guard lock(mutex_);error_="worker COM initialization failed";return;}struct COM{~COM(){CoUninitialize();}} com;
  try{
   std::unique_ptr<DxgiCapture> capture;std::unique_ptr<H264Encoder> encoder;std::unique_ptr<LoopbackAudio> audio;ComPtr<ID3D11Texture2D> snapshot;std::uint64_t frameId=0,videoSeq=0,pointerSeq=0,lastSubmit=0,staticSince=0,nextRecovery=0,lastPointer=0;bool dirty=false;
   std::map<std::int64_t,std::uint64_t> times;
   if(config_.audio){try{audio=std::make_unique<LoopbackAudio>();}catch(const std::exception& e){std::cerr<<"audio unavailable: "<<e.what()<<'\n';}}
   while(!stop.stop_requested()){
    auto now=wire::now_us();if(audio){try{for(auto& m:audio->poll())enqueue(std::move(m));}catch(const std::exception& e){std::cerr<<"audio stopped: "<<e.what()<<'\n';audio.reset();}}
    if(!capture){if(now<nextRecovery){std::this_thread::sleep_for(std::chrono::milliseconds(5));continue;}try{capture=std::make_unique<DxgiCapture>(config_.adapter,config_.monitor);protect_context(capture->device());{std::lock_guard lock(mutex_);bounds_=capture->output().DesktopCoordinates;}keyframe_=true;}catch(const HResultError&){nextRecovery=now+500000;continue;}}
    const auto captureStart=wire::now_us();
    try{
     auto frame=capture->next(0);if(frame){D3D11_TEXTURE2D_DESC desc{};frame->texture->GetDesc(&desc);D3D11_TEXTURE2D_DESC old{};if(snapshot)snapshot->GetDesc(&old);
      if(!snapshot||old.Width!=desc.Width||old.Height!=desc.Height){snapshot.Reset();desc.BindFlags=D3D11_BIND_SHADER_RESOURCE|D3D11_BIND_RENDER_TARGET;desc.MiscFlags=0;desc.CPUAccessFlags=0;desc.Usage=D3D11_USAGE_DEFAULT;check(capture->device()->CreateTexture2D(&desc,nullptr,snapshot.GetAddressOf()),"capture snapshot texture");encoder.reset();keyframe_=true;}
      ComPtr<ID3D11DeviceContext> dc;capture->device()->GetImmediateContext(dc.GetAddressOf());dc->CopyResource(snapshot.Get(),frame->texture.Get());dirty=true;
      {std::lock_guard lock(mutex_);stats_.captured++;stats_.capture_us=wire::now_us()-captureStart;}
      if(frame->info.LastMouseUpdateTime.QuadPart&&now-lastPointer>=8000){lastPointer=now;auto bounds=capture->output().DesktopCoordinates;const LONG w=std::max<LONG>(1,bounds.right-bounds.left),h=std::max<LONG>(1,bounds.bottom-bounds.top);wire::Message m;m.channel=wire::control;m.kind=wire::pointer_position;m.id=++pointerSeq;m.payload.resize(5);wire::put16(m.payload.data(),std::uint16_t(std::clamp<std::int64_t>(std::int64_t(frame->info.PointerPosition.Position.x)*65535/w,0,65535)));wire::put16(m.payload.data()+2,std::uint16_t(std::clamp<std::int64_t>(std::int64_t(frame->info.PointerPosition.Position.y)*65535/h,0,65535)));m.payload[4]=frame->info.PointerPosition.Visible?1:0;enqueue(std::move(m));}
      if(!frame->pointer_shape.empty()&&frame->pointer_shape.size()<120000){wire::Message m;m.channel=wire::control;m.kind=wire::pointer_shape;m.id=++pointerSeq;m.payload.resize(24+frame->pointer_shape.size());auto& p=frame->pointer_info;UINT fields[]={p.Type,p.Width,p.Height,p.Pitch,UINT(p.HotSpot.x),UINT(p.HotSpot.y)};for(int i=0;i<6;++i)wire::put32(m.payload.data()+i*4,fields[i]);std::copy(frame->pointer_shape.begin(),frame->pointer_shape.end(),m.payload.begin()+24);enqueue(std::move(m));}
     }
    }catch(const HResultError& e){if(e.code==DXGI_ERROR_ACCESS_LOST||e.code==DXGI_ERROR_DEVICE_REMOVED||e.code==DXGI_ERROR_DEVICE_RESET||e.code==E_ACCESSDENIED){capture.reset();encoder.reset();snapshot.Reset();nextRecovery=now+500000;continue;}throw;}
    if(!snapshot){std::this_thread::sleep_for(std::chrono::milliseconds(1));continue;}
    const unsigned targetFPS=fps_.load();if(now-lastSubmit<1000000/targetFPS){std::this_thread::sleep_for(std::chrono::milliseconds(1));continue;}
    const bool key=keyframe_.exchange(false);if(!dirty&&!key&&now-staticSince<1000000){std::this_thread::sleep_for(std::chrono::milliseconds(1));continue;}
    auto bounds=capture->output().DesktopCoordinates;const auto sourceW=double(bounds.right-bounds.left),sourceH=double(bounds.bottom-bounds.top);const double scale=std::min(double(width_)/sourceW,double(height_)/sourceH);const UINT w=std::max<UINT>(16,UINT(sourceW*scale)&~1u),h=std::max<UINT>(16,UINT(sourceH*scale)&~1u);bool restart=!encoder||encoder->width()!=w||encoder->height()!=h||encoder->fps()!=targetFPS||encoder->bitrate()!=bitrate_.load();
    if(restart){encoder=std::make_unique<H264Encoder>(capture->device(),capture->adapter().VendorId,w,h,targetFPS,bitrate_.load(),config_.requireHardware);times.clear();std::cerr<<"encoder="<<encoder->name()<<" resolution="<<w<<'x'<<h<<" fps="<<targetFPS<<'\n';}
    auto collect=[&](){for(auto& packet:encoder->receive()){auto it=times.find(packet.pts);const auto timestamp=it==times.end()?wire::now_us():it->second;if(it!=times.end())times.erase(it);wire::Message m;m.channel=wire::video;m.kind=wire::video_frame;m.flags=packet.key?wire::keyframe:0;m.id=++videoSeq;m.timestamp=timestamp;m.payload=wire::video_payload(std::uint16_t(w),std::uint16_t(h),std::uint16_t(targetFPS),packet.data);{std::lock_guard lock(mutex_);stats_.encoded++;stats_.encode_us=wire::now_us()-timestamp;}enqueue(std::move(m));}};
    collect();const auto pts=std::int64_t(++frameId);const auto began=wire::now_us();if(encoder->submit(snapshot.Get(),capture->output().Rotation,pts,key||restart)){times[pts]=began;lastSubmit=now;dirty=false;staticSince=now;}else keyframe_=key||restart;
    collect();if(times.size()>32){times.clear();keyframe_=true;}
   }
  }catch(const std::exception& e){std::lock_guard lock(mutex_);error_=e.what();}
 }
public:
 explicit CaptureWorker(MediaConfig config):config_(std::move(config)),bitrate_(config_.bitrate),fps_(config_.fps),width_(config_.width),height_(config_.height){thread_=std::jthread([this](std::stop_token s){run(s);});}
 ~CaptureWorker(){thread_.request_stop();if(thread_.joinable())thread_.join();}
 void request_keyframe(){keyframe_=true;}
 void target(unsigned bitrate,unsigned fps,unsigned w,unsigned h){if(bitrate_.load()==bitrate&&fps_.load()==fps&&width_.load()==w&&height_.load()==h)return;bitrate_=std::clamp(bitrate,250000u,100000000u);fps_=std::clamp(fps,1u,120u);width_=std::clamp(w,16u,8192u)&~1u;height_=std::clamp(h,16u,8192u)&~1u;keyframe_=true;}
 std::vector<wire::Message> drain(){std::lock_guard lock(mutex_);if(!error_.empty())throw std::runtime_error(error_);std::vector<wire::Message> out;while(!output_.empty()){out.push_back(std::move(output_.front()));output_.pop_front();}return out;}
 RECT bounds(){std::lock_guard lock(mutex_);return bounds_;}
 CaptureStats stats(){std::lock_guard lock(mutex_);return stats_;}
};
}
