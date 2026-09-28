#pragma once
#include "render/d3d.hpp"
#include "transport/wire.hpp"
extern "C" {
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_d3d11va.h>
#include <libavutil/opt.h>
#include <libswscale/swscale.h>
}
#include <functional>
#include <iostream>
#include <memory>
namespace rd {
inline void avcheck(int rc,const char* where){if(rc<0){char text[AV_ERROR_MAX_STRING_SIZE]{};av_strerror(rc,text,sizeof(text));throw std::runtime_error(std::string(where)+": "+text);}}
struct FrameDelete{void operator()(AVFrame* f)const{av_frame_free(&f);}};using AvFrame=std::unique_ptr<AVFrame,FrameDelete>;
struct PacketDelete{void operator()(AVPacket* p)const{av_packet_free(&p);}};using AvPacket=std::unique_ptr<AVPacket,PacketDelete>;
struct CodecDelete{void operator()(AVCodecContext* c)const{avcodec_free_context(&c);}};using CodecContext=std::unique_ptr<AVCodecContext,CodecDelete>;
struct BufferDelete{void operator()(AVBufferRef* b)const{av_buffer_unref(&b);}};using AvBuffer=std::unique_ptr<AVBufferRef,BufferDelete>;
inline AvFrame make_frame(){AvFrame f(av_frame_alloc());if(!f)throw std::bad_alloc();return f;}
inline AvPacket make_packet(){AvPacket p(av_packet_alloc());if(!p)throw std::bad_alloc();return p;}
inline AvBuffer d3d_device(ID3D11Device* device){
 AvBuffer ref(av_hwdevice_ctx_alloc(AV_HWDEVICE_TYPE_D3D11VA));if(!ref)throw std::bad_alloc();auto* ctx=reinterpret_cast<AVHWDeviceContext*>(ref->data);auto* d=reinterpret_cast<AVD3D11VADeviceContext*>(ctx->hwctx);d->device=device;device->AddRef();avcheck(av_hwdevice_ctx_init(ref.get()),"initialize FFmpeg D3D11 device");return ref;
}
struct EncodedFrame{wire::Bytes data;std::int64_t pts=0;bool key=false;};
class H264Encoder {
 AvBuffer device_,frames_,qsvDevice_,qsvFrames_;CodecContext codec_;VideoBlitter blitter_;UINT w_,h_,fps_,bitrate_;std::string name_;bool software_=false;SwsContext* sws_=nullptr;
 void open(const char* name){
  const auto* impl=avcodec_find_encoder_by_name(name);if(!impl)throw std::runtime_error(std::string("encoder not included: ")+name);
  CodecContext c(avcodec_alloc_context3(impl));if(!c)throw std::bad_alloc();c->width=int(w_);c->height=int(h_);c->time_base={1,int(fps_)};c->framerate={int(fps_),1};c->bit_rate=bitrate_;c->rc_max_rate=bitrate_;c->rc_buffer_size=int(bitrate_/fps_*2);c->gop_size=int(fps_);c->max_b_frames=0;c->flags|=AV_CODEC_FLAG_LOW_DELAY;c->color_range=AVCOL_RANGE_MPEG;c->colorspace=AVCOL_SPC_BT709;c->color_primaries=AVCOL_PRI_BT709;c->color_trc=AVCOL_TRC_BT709;
  software_=std::string(name)=="libopenh264";
  if(software_){c->pix_fmt=AV_PIX_FMT_YUV420P;c->thread_count=2;}
  else if(std::string(name)=="h264_qsv"){
   AVBufferRef* q=nullptr;avcheck(av_hwdevice_ctx_create_derived(&q,AV_HWDEVICE_TYPE_QSV,device_.get(),0),"derive QSV device");qsvDevice_.reset(q);q=nullptr;
   avcheck(av_hwframe_ctx_create_derived(&q,AV_PIX_FMT_QSV,qsvDevice_.get(),frames_.get(),AV_HWFRAME_MAP_READ),"derive QSV frames");qsvFrames_.reset(q);c->pix_fmt=AV_PIX_FMT_QSV;c->hw_frames_ctx=av_buffer_ref(qsvFrames_.get());
  }else{c->pix_fmt=AV_PIX_FMT_D3D11;c->hw_frames_ctx=av_buffer_ref(frames_.get());}
  AVDictionary* opts=nullptr;
  if(std::string(name)=="h264_nvenc"){av_dict_set(&opts,"preset","p1",0);av_dict_set(&opts,"tune","ull",0);av_dict_set(&opts,"rc","cbr",0);av_dict_set(&opts,"zerolatency","1",0);av_dict_set(&opts,"rc-lookahead","0",0);av_dict_set(&opts,"forced-idr","1",0);}
  if(std::string(name)=="h264_qsv"){av_dict_set(&opts,"preset","veryfast",0);av_dict_set(&opts,"async_depth","1",0);av_dict_set(&opts,"look_ahead","0",0);}
  if(std::string(name)=="h264_amf"){av_dict_set(&opts,"usage","ultralowlatency",0);av_dict_set(&opts,"quality","speed",0);av_dict_set(&opts,"rc","cbr",0);av_dict_set(&opts,"header_insertion_mode","idr",0);}
  const auto rc=avcodec_open2(c.get(),impl,&opts);av_dict_free(&opts);avcheck(rc,"open H.264 encoder");codec_=std::move(c);name_=name;
 }
public:
 H264Encoder(ID3D11Device* d,UINT vendor,UINT w,UINT h,UINT fps,UINT bitrate,bool requireHardware):device_(d3d_device(d)),blitter_(d),w_(w),h_(h),fps_(fps),bitrate_(bitrate){
  frames_.reset(av_hwframe_ctx_alloc(device_.get()));if(!frames_)throw std::bad_alloc();auto* f=reinterpret_cast<AVHWFramesContext*>(frames_->data);f->format=AV_PIX_FMT_D3D11;f->sw_format=AV_PIX_FMT_NV12;f->width=int(w);f->height=int(h);f->initial_pool_size=12;auto* hw=reinterpret_cast<AVD3D11VAFramesContext*>(f->hwctx);hw->BindFlags=D3D11_BIND_RENDER_TARGET|D3D11_BIND_SHADER_RESOURCE;avcheck(av_hwframe_ctx_init(frames_.get()),"NV12 GPU texture pool");
  std::vector<const char*> choices;if(vendor==0x10de)choices={"h264_nvenc"};else if(vendor==0x8086)choices={"h264_qsv"};else if(vendor==0x1002)choices={"h264_amf"};else choices={"h264_nvenc","h264_qsv","h264_amf"};if(!requireHardware)choices.push_back("libopenh264");
  std::string errors;for(auto name:choices){try{open(name);return;}catch(const std::exception& e){errors+=std::string(name)+": "+e.what()+"; ";qsvFrames_.reset();qsvDevice_.reset();}}
  throw std::runtime_error("no usable H.264 encoder; "+errors);
 }
 ~H264Encoder(){if(sws_)sws_freeContext(sws_);}
 const std::string& name()const{return name_;}UINT width()const{return w_;}UINT height()const{return h_;}UINT fps()const{return fps_;}UINT bitrate()const{return bitrate_;}
 std::vector<EncodedFrame> receive(){std::vector<EncodedFrame> out;auto p=make_packet();for(;;){int rc=avcodec_receive_packet(codec_.get(),p.get());if(rc==AVERROR(EAGAIN)||rc==AVERROR_EOF)break;avcheck(rc,"receive encoded packet");out.push_back({wire::Bytes(p->data,p->data+p->size),p->pts,(p->flags&AV_PKT_FLAG_KEY)!=0});av_packet_unref(p.get());}return out;}
 bool submit(ID3D11Texture2D* source,DXGI_MODE_ROTATION rotation,std::int64_t pts,bool idr){
  auto gpu=make_frame();avcheck(av_hwframe_get_buffer(frames_.get(),gpu.get(),0),"acquire NV12 surface");auto* texture=reinterpret_cast<ID3D11Texture2D*>(gpu->data[0]);auto slice=UINT(reinterpret_cast<std::uintptr_t>(gpu->data[1]));blitter_.blit(source,0,texture,slice,rotation);
  gpu->pts=pts;gpu->pict_type=idr?AV_PICTURE_TYPE_I:AV_PICTURE_TYPE_NONE;gpu->color_range=AVCOL_RANGE_MPEG;gpu->colorspace=AVCOL_SPC_BT709;
  AvFrame mapped;AVFrame* input=gpu.get();
  if(qsvFrames_){mapped=make_frame();mapped->format=AV_PIX_FMT_QSV;mapped->hw_frames_ctx=av_buffer_ref(qsvFrames_.get());avcheck(av_hwframe_map(mapped.get(),gpu.get(),AV_HWFRAME_MAP_READ),"D3D11 to QSV map");avcheck(av_frame_copy_props(mapped.get(),gpu.get()),"QSV frame properties");input=mapped.get();}
  if(software_){auto cpu=make_frame();avcheck(av_hwframe_transfer_data(cpu.get(),gpu.get(),0),"software fallback GPU download");mapped=make_frame();mapped->format=AV_PIX_FMT_YUV420P;mapped->width=int(w_);mapped->height=int(h_);avcheck(av_frame_get_buffer(mapped.get(),32),"software frame allocation");sws_=sws_getCachedContext(sws_,int(w_),int(h_),AV_PIX_FMT_NV12,int(w_),int(h_),AV_PIX_FMT_YUV420P,SWS_FAST_BILINEAR,nullptr,nullptr,nullptr);if(!sws_)throw std::runtime_error("NV12 software conversion context");sws_scale(sws_,cpu->data,cpu->linesize,0,int(h_),mapped->data,mapped->linesize);avcheck(av_frame_copy_props(mapped.get(),gpu.get()),"software frame properties");input=mapped.get();}
  // AVFrame refs keep the conversion texture alive until the asynchronous encoder releases it.
  const int rc=avcodec_send_frame(codec_.get(),input);if(rc==AVERROR(EAGAIN))return false;avcheck(rc,"submit H.264 GPU frame");return true;
 }
};
class H264Decoder {
 AvBuffer device_;CodecContext codec_;
 static AVPixelFormat format(AVCodecContext*,const AVPixelFormat* formats){for(auto p=formats;*p!=AV_PIX_FMT_NONE;++p)if(*p==AV_PIX_FMT_D3D11)return *p;return AV_PIX_FMT_NONE;}
public:
 explicit H264Decoder(ID3D11Device* d):device_(d3d_device(d)){
  const AVCodec* impl=avcodec_find_decoder(AV_CODEC_ID_H264);if(!impl)throw std::runtime_error("H.264 decoder unavailable");codec_.reset(avcodec_alloc_context3(impl));if(!codec_)throw std::bad_alloc();codec_->hw_device_ctx=av_buffer_ref(device_.get());codec_->get_format=format;codec_->flags|=AV_CODEC_FLAG_LOW_DELAY;codec_->thread_count=1;codec_->extra_hw_frames=4;avcheck(avcodec_open2(codec_.get(),impl,nullptr),"open D3D11 H.264 decoder");
 }
 void flush(){avcodec_flush_buffers(codec_.get());}
 std::vector<AvFrame> receive(){std::vector<AvFrame> out;for(;;){auto frame=make_frame();const int rc=avcodec_receive_frame(codec_.get(),frame.get());if(rc==AVERROR(EAGAIN)||rc==AVERROR_EOF)break;avcheck(rc,"decode H.264 access unit");if(frame->format!=AV_PIX_FMT_D3D11)throw std::runtime_error("decoder did not return a GPU surface");out.push_back(std::move(frame));}return out;}
 bool submit(std::span<const std::uint8_t> au,std::int64_t pts){auto p=make_packet();avcheck(av_new_packet(p.get(),int(au.size())),"H.264 packet allocation");std::memcpy(p->data,au.data(),au.size());p->pts=p->dts=pts;const int rc=avcodec_send_packet(codec_.get(),p.get());if(rc==AVERROR(EAGAIN))return false;avcheck(rc,"submit H.264 access unit");return true;}
};
}
