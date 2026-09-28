#pragma once
#include "capture/dxgi_capture.hpp"
#include "transport/wire.hpp"
#include <mmdeviceapi.h>
#include <audioclient.h>
#include <ksmedia.h>
#include <opus/opus.h>
extern "C" {
#include <libswresample/swresample.h>
#include <libavutil/channel_layout.h>
}
#include <deque>
#include <memory>
namespace rd {
struct OpusEncoderDelete{void operator()(OpusEncoder* p)const{opus_encoder_destroy(p);}};
struct OpusDecoderDelete{void operator()(OpusDecoder* p)const{opus_decoder_destroy(p);}};
class LoopbackAudio {
 ComPtr<IAudioClient> client_;ComPtr<IAudioCaptureClient> capture_;std::unique_ptr<OpusEncoder,OpusEncoderDelete> opus_;SwrContext* swr_=nullptr;std::vector<float> samples_;std::uint64_t id_=0,timestamp_=0;UINT channels_=0,bits_=0;
public:
 LoopbackAudio(){
  ComPtr<IMMDeviceEnumerator> enumerator;check(CoCreateInstance(__uuidof(MMDeviceEnumerator),nullptr,CLSCTX_ALL,IID_PPV_ARGS(enumerator.GetAddressOf())),"audio device enumerator");ComPtr<IMMDevice> dev;check(enumerator->GetDefaultAudioEndpoint(eRender,eConsole,dev.GetAddressOf()),"system audio endpoint");check(dev->Activate(__uuidof(IAudioClient),CLSCTX_ALL,nullptr,reinterpret_cast<void**>(client_.GetAddressOf())),"WASAPI client");
  WAVEFORMATEX* fmt=nullptr;check(client_->GetMixFormat(&fmt),"audio mix format");std::unique_ptr<WAVEFORMATEX,decltype(&CoTaskMemFree)> format(fmt,CoTaskMemFree);channels_=fmt->nChannels;bits_=fmt->wBitsPerSample;
  bool floating=fmt->wFormatTag==WAVE_FORMAT_IEEE_FLOAT;DWORD mask=0;if(fmt->wFormatTag==WAVE_FORMAT_EXTENSIBLE&&fmt->cbSize>=22){auto* ex=reinterpret_cast<WAVEFORMATEXTENSIBLE*>(fmt);floating=IsEqualGUID(ex->SubFormat,KSDATAFORMAT_SUBTYPE_IEEE_FLOAT);mask=ex->dwChannelMask;}
  AVSampleFormat sample=floating?AV_SAMPLE_FMT_FLT:(bits_==16?AV_SAMPLE_FMT_S16:(bits_==32?AV_SAMPLE_FMT_S32:AV_SAMPLE_FMT_NONE));if(sample==AV_SAMPLE_FMT_NONE||channels_>16)throw std::runtime_error("unsupported WASAPI mix format");
  AVChannelLayout input{},output=AV_CHANNEL_LAYOUT_STEREO;if(mask)av_channel_layout_from_mask(&input,mask);else av_channel_layout_default(&input,int(channels_));
  const auto rc=swr_alloc_set_opts2(&swr_,&output,AV_SAMPLE_FMT_FLT,48000,&input,sample,int(fmt->nSamplesPerSec),0,nullptr);av_channel_layout_uninit(&input);if(rc<0||swr_init(swr_)<0)throw std::runtime_error("initialize audio resampler");
  check(client_->Initialize(AUDCLNT_SHAREMODE_SHARED,AUDCLNT_STREAMFLAGS_LOOPBACK,500000,0,fmt,nullptr),"WASAPI loopback initialize");check(client_->GetService(IID_PPV_ARGS(capture_.GetAddressOf())),"WASAPI capture service");
  int error=0;opus_.reset(opus_encoder_create(48000,2,OPUS_APPLICATION_RESTRICTED_LOWDELAY,&error));if(error!=OPUS_OK)throw std::runtime_error("Opus encoder creation");opus_encoder_ctl(opus_.get(),OPUS_SET_BITRATE(128000));opus_encoder_ctl(opus_.get(),OPUS_SET_COMPLEXITY(5));check(client_->Start(),"start loopback capture");
 }
 ~LoopbackAudio(){if(client_)client_->Stop();if(swr_)swr_free(&swr_);}
 std::vector<wire::Message> poll(){
  std::vector<wire::Message> out;UINT next=0;check(capture_->GetNextPacketSize(&next),"loopback packet size");
  for(unsigned reads=0;next&&reads<16;++reads){BYTE* data=nullptr;UINT frames=0;DWORD flags=0;UINT64 pos=0,qpc=0;check(capture_->GetBuffer(&data,&frames,&flags,&pos,&qpc),"loopback buffer");
   struct Release{IAudioCaptureClient* c;UINT frames;~Release(){if(c)c->ReleaseBuffer(frames);}} release{capture_.Get(),frames};
   if(frames>48000)throw std::runtime_error("oversized audio capture buffer");std::vector<std::uint8_t> silence;if(flags&AUDCLNT_BUFFERFLAGS_SILENT){silence.resize(std::size_t(frames)*channels_*bits_/8);data=silence.data();}
   auto wanted=swr_get_out_samples(swr_,int(frames));if(wanted<0||wanted>96000)throw std::runtime_error("audio resampler bounds");std::vector<float> converted(std::size_t(wanted)*2);std::uint8_t* dst=reinterpret_cast<std::uint8_t*>(converted.data());const std::uint8_t* src=data;int n=swr_convert(swr_,&dst,wanted,&src,int(frames));if(n<0)throw std::runtime_error("audio resample failed");
   if(samples_.size()>4800*2){samples_.clear();timestamp_=0;}samples_.insert(samples_.end(),converted.begin(),converted.begin()+n*2);if(!timestamp_)timestamp_=wire::now_us();
   while(samples_.size()>=960){wire::Message m;m.channel=wire::audio;m.kind=wire::audio_frame;m.id=++id_;m.timestamp=timestamp_;timestamp_+=10000;m.payload.resize(1275);int bytes=opus_encode_float(opus_.get(),samples_.data(),480,m.payload.data(),int(m.payload.size()));if(bytes<0)throw std::runtime_error("Opus encode failed");m.payload.resize(bytes);out.push_back(std::move(m));samples_.erase(samples_.begin(),samples_.begin()+960);}
   // ReleaseBuffer must happen before asking for another packet.
   release.c->ReleaseBuffer(release.frames);release.frames=0;release.c=nullptr;
   check(capture_->GetNextPacketSize(&next),"next loopback packet");
  }return out;
 }
};
class AudioPlayback {
 ComPtr<IAudioClient> client_;ComPtr<IAudioRenderClient> render_;std::unique_ptr<OpusDecoder,OpusDecoderDelete> opus_;std::deque<float> pcm_;UINT buffer_=0;std::uint64_t last_=0,remoteBase_=0,localBase_=0;
public:
 AudioPlayback(){ComPtr<IMMDeviceEnumerator> e;check(CoCreateInstance(__uuidof(MMDeviceEnumerator),nullptr,CLSCTX_ALL,IID_PPV_ARGS(e.GetAddressOf())),"audio enumerate");ComPtr<IMMDevice> d;check(e->GetDefaultAudioEndpoint(eRender,eConsole,d.GetAddressOf()),"playback endpoint");check(d->Activate(__uuidof(IAudioClient),CLSCTX_ALL,nullptr,reinterpret_cast<void**>(client_.GetAddressOf())),"playback client");WAVEFORMATEX f{};f.wFormatTag=WAVE_FORMAT_IEEE_FLOAT;f.nChannels=2;f.nSamplesPerSec=48000;f.wBitsPerSample=32;f.nBlockAlign=8;f.nAvgBytesPerSec=384000;
  check(client_->Initialize(AUDCLNT_SHAREMODE_SHARED,AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM|AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY,500000,0,&f,nullptr),"playback initialize");check(client_->GetBufferSize(&buffer_),"playback buffer size");check(client_->GetService(IID_PPV_ARGS(render_.GetAddressOf())),"playback service");int err=0;opus_.reset(opus_decoder_create(48000,2,&err));if(err!=OPUS_OK)throw std::runtime_error("Opus decoder create");check(client_->Start(),"start playback");
 }
 ~AudioPlayback(){if(client_)client_->Stop();}
 void receive(const wire::Message& m){if(m.payload.empty()||m.payload.size()>1275||m.id<=last_)return;const auto now=wire::now_us();if(!remoteBase_){remoteBase_=m.timestamp;localBase_=now;}
  const auto expected=localBase_+(m.timestamp>=remoteBase_?m.timestamp-remoteBase_:0);if(now>expected+200000){last_=m.id;return;}if(expected>now+500000){remoteBase_=m.timestamp;localBase_=now;pcm_.clear();}
  std::array<float,1920> samples{};if(last_&&m.id==last_+2){int n=opus_decode_float(opus_.get(),nullptr,0,samples.data(),480,0);if(n>0)pcm_.insert(pcm_.end(),samples.begin(),samples.begin()+n*2);}int n=opus_decode_float(opus_.get(),m.payload.data(),int(m.payload.size()),samples.data(),960,0);last_=m.id;if(n<0)return;
  if(pcm_.size()>4800*2)pcm_.clear();pcm_.insert(pcm_.end(),samples.begin(),samples.begin()+n*2);
 }
 void pump(){UINT pad=0;check(client_->GetCurrentPadding(&pad),"audio padding");if(pad>=buffer_||pcm_.empty())return;UINT n=std::min<UINT>(buffer_-pad,UINT(pcm_.size()/2));BYTE* b=nullptr;check(render_->GetBuffer(n,&b),"render audio buffer");auto* f=reinterpret_cast<float*>(b);for(UINT i=0;i<n*2;++i){f[i]=pcm_.front();pcm_.pop_front();}check(render_->ReleaseBuffer(n,0),"release audio render buffer");}
};
}
