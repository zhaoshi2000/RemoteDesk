#pragma once
#include "capture/dxgi_capture.hpp"
#include <d3d11_1.h>
#include <d3d10.h>
#include <dxgi1_3.h>
#include <algorithm>
#include <memory>
namespace rd {
inline void protect_context(ID3D11Device* device){ComPtr<ID3D10Multithread> mt;ComPtr<ID3D11DeviceContext> context;device->GetImmediateContext(context.GetAddressOf());check(context.As(&mt),"D3D multithread interface");mt->SetMultithreadProtected(TRUE);}
class VideoBlitter {
 ComPtr<ID3D11VideoDevice> device_;ComPtr<ID3D11VideoContext> context_;
 ComPtr<ID3D11VideoProcessorEnumerator> enumerator_;ComPtr<ID3D11VideoProcessor> processor_;
 UINT iw_=0,ih_=0,ow_=0,oh_=0;
public:
 explicit VideoBlitter(ID3D11Device* d){check(d->QueryInterface(IID_PPV_ARGS(device_.GetAddressOf())),"video device");ComPtr<ID3D11DeviceContext> c;d->GetImmediateContext(c.GetAddressOf());check(c.As(&context_),"video context");}
 void blit(ID3D11Texture2D* in,UINT slice,ID3D11Texture2D* out,UINT outSlice=0,DXGI_MODE_ROTATION rotation=DXGI_MODE_ROTATION_IDENTITY){
  D3D11_TEXTURE2D_DESC a{},b{};in->GetDesc(&a);out->GetDesc(&b);
  if(!processor_||iw_!=a.Width||ih_!=a.Height||ow_!=b.Width||oh_!=b.Height){
   processor_.Reset();enumerator_.Reset();iw_=a.Width;ih_=a.Height;ow_=b.Width;oh_=b.Height;
   D3D11_VIDEO_PROCESSOR_CONTENT_DESC desc{};desc.InputFrameFormat=D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE;desc.InputWidth=iw_;desc.InputHeight=ih_;desc.OutputWidth=ow_;desc.OutputHeight=oh_;desc.InputFrameRate={60,1};desc.OutputFrameRate={60,1};desc.Usage=D3D11_VIDEO_USAGE_PLAYBACK_NORMAL;
   check(device_->CreateVideoProcessorEnumerator(&desc,enumerator_.GetAddressOf()),"video processor enumerate");check(device_->CreateVideoProcessor(enumerator_.Get(),0,processor_.GetAddressOf()),"video processor create");
  }
  D3D11_VIDEO_PROCESSOR_INPUT_VIEW_DESC iv{};iv.ViewDimension=D3D11_VPIV_DIMENSION_TEXTURE2D;iv.Texture2D.ArraySlice=slice;
  ComPtr<ID3D11VideoProcessorInputView> input;check(device_->CreateVideoProcessorInputView(in,enumerator_.Get(),&iv,input.GetAddressOf()),"video input view");
  D3D11_VIDEO_PROCESSOR_OUTPUT_VIEW_DESC ov{};
  if(b.ArraySize>1){ov.ViewDimension=D3D11_VPOV_DIMENSION_TEXTURE2DARRAY;ov.Texture2DArray.MipSlice=0;ov.Texture2DArray.FirstArraySlice=outSlice;ov.Texture2DArray.ArraySize=1;}else{ov.ViewDimension=D3D11_VPOV_DIMENSION_TEXTURE2D;ov.Texture2D.MipSlice=0;}
  ComPtr<ID3D11VideoProcessorOutputView> output;check(device_->CreateVideoProcessorOutputView(out,enumerator_.Get(),&ov,output.GetAddressOf()),"video output view");
  const RECT source{0,0,LONG(iw_),LONG(ih_)},target{0,0,LONG(ow_),LONG(oh_)};
  context_->VideoProcessorSetStreamFrameFormat(processor_.Get(),0,D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE);
  context_->VideoProcessorSetStreamAutoProcessingMode(processor_.Get(),0,FALSE);
  context_->VideoProcessorSetStreamSourceRect(processor_.Get(),0,TRUE,&source);context_->VideoProcessorSetStreamDestRect(processor_.Get(),0,TRUE,&target);context_->VideoProcessorSetOutputTargetRect(processor_.Get(),TRUE,&target);
  D3D11_VIDEO_PROCESSOR_COLOR_SPACE rgb{};rgb.RGB_Range=0;rgb.YCbCr_Matrix=1;rgb.Nominal_Range=2;
  D3D11_VIDEO_PROCESSOR_COLOR_SPACE yuv{};yuv.YCbCr_Matrix=1;yuv.Nominal_Range=1;
  context_->VideoProcessorSetStreamColorSpace(processor_.Get(),0,a.Format==DXGI_FORMAT_NV12?&yuv:&rgb);
  context_->VideoProcessorSetOutputColorSpace(processor_.Get(),b.Format==DXGI_FORMAT_NV12?&yuv:&rgb);
  ComPtr<ID3D11VideoContext1> c1;if(SUCCEEDED(context_.As(&c1))){auto rot=D3D11_VIDEO_PROCESSOR_ROTATION_IDENTITY;
   if(rotation==DXGI_MODE_ROTATION_ROTATE90)rot=D3D11_VIDEO_PROCESSOR_ROTATION_90;
   if(rotation==DXGI_MODE_ROTATION_ROTATE180)rot=D3D11_VIDEO_PROCESSOR_ROTATION_180;
   if(rotation==DXGI_MODE_ROTATION_ROTATE270)rot=D3D11_VIDEO_PROCESSOR_ROTATION_270;
   c1->VideoProcessorSetStreamRotation(processor_.Get(),0,rot!=D3D11_VIDEO_PROCESSOR_ROTATION_IDENTITY,rot);
  }else if(rotation>DXGI_MODE_ROTATION_IDENTITY){throw std::runtime_error("rotated output requires D3D11.1 video context");}
  D3D11_VIDEO_PROCESSOR_STREAM stream{};stream.Enable=TRUE;stream.pInputSurface=input.Get();
  check(context_->VideoProcessorBlt(processor_.Get(),output.Get(),0,1,&stream),"GPU video conversion");
 }
};
class D3DRenderer {
 ComPtr<ID3D11Device> device_;ComPtr<ID3D11DeviceContext> context_;ComPtr<IDXGISwapChain1> swap_;std::unique_ptr<VideoBlitter> blitter_;HWND window_;UINT width_=0,height_=0;
public:
 explicit D3DRenderer(HWND hwnd,UINT adapterIndex=0):window_(hwnd){
  ComPtr<IDXGIFactory1> f;check(CreateDXGIFactory1(IID_PPV_ARGS(f.GetAddressOf())),"DXGI factory");ComPtr<IDXGIAdapter1> a;check(f->EnumAdapters1(adapterIndex,a.GetAddressOf()),"render adapter");D3D_FEATURE_LEVEL level{};
  check(D3D11CreateDevice(a.Get(),D3D_DRIVER_TYPE_UNKNOWN,nullptr,D3D11_CREATE_DEVICE_BGRA_SUPPORT|D3D11_CREATE_DEVICE_VIDEO_SUPPORT,nullptr,0,D3D11_SDK_VERSION,device_.GetAddressOf(),&level,context_.GetAddressOf()),"renderer D3D11 device");protect_context(device_.Get());
  ComPtr<IDXGIFactory2> factory;check(f.As(&factory),"DXGI factory2");DXGI_SWAP_CHAIN_DESC1 desc{};desc.Format=DXGI_FORMAT_B8G8R8A8_UNORM;desc.SampleDesc.Count=1;desc.BufferUsage=DXGI_USAGE_RENDER_TARGET_OUTPUT;desc.BufferCount=2;desc.SwapEffect=DXGI_SWAP_EFFECT_FLIP_DISCARD;desc.Scaling=DXGI_SCALING_STRETCH;
  check(factory->CreateSwapChainForHwnd(device_.Get(),window_,&desc,nullptr,nullptr,swap_.GetAddressOf()),"flip-model swap chain");factory->MakeWindowAssociation(window_,DXGI_MWA_NO_ALT_ENTER);ComPtr<IDXGISwapChain2> swap2;if(SUCCEEDED(swap_.As(&swap2)))swap2->SetMaximumFrameLatency(1);
  blitter_=std::make_unique<VideoBlitter>(device_.Get());
 }
 ID3D11Device* device()const{return device_.Get();}
 bool present(ID3D11Texture2D* texture,UINT slice){
  RECT r{};GetClientRect(window_,&r);const UINT w=UINT(std::max<LONG>(0,r.right)),h=UINT(std::max<LONG>(0,r.bottom));if(!w||!h||IsIconic(window_))return false;
  if(w!=width_||h!=height_){context_->OMSetRenderTargets(0,nullptr,nullptr);context_->Flush();check(swap_->ResizeBuffers(0,w,h,DXGI_FORMAT_UNKNOWN,0),"resize swap chain");width_=w;height_=h;}
  ComPtr<ID3D11Texture2D> back;check(swap_->GetBuffer(0,IID_PPV_ARGS(back.GetAddressOf())),"swap back buffer");blitter_->blit(texture,slice,back.Get());
  auto hr=swap_->Present(0,DXGI_PRESENT_DO_NOT_WAIT);if(hr==DXGI_ERROR_WAS_STILL_DRAWING||hr==DXGI_STATUS_OCCLUDED)return false;check(hr,"present");return true;
 }
};
}
