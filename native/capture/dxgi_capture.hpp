#pragma once
#ifndef _WIN32
#error DXGI capture is Windows-only
#endif
#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <wrl/client.h>
#include <cstdint>
#include <optional>
#include <stdexcept>
#include <string>
#include <utility>
#include <vector>

namespace rd {
using Microsoft::WRL::ComPtr;
struct HResultError:std::runtime_error{HRESULT code;HResultError(HRESULT h,const char* where):std::runtime_error(where),code(h){}};
inline void check(HRESULT h,const char* where){if(FAILED(h))throw HResultError(h,where);}
// A lease must not outlive GPU use of the duplicated resource. An asynchronous encoder must
// retain this lease through completion, or GPU-copy into its own texture pool before releasing it.
struct Frame {
    ComPtr<IDXGIOutputDuplication> owner;
    ComPtr<ID3D11Texture2D> texture;
    DXGI_OUTDUPL_FRAME_INFO info{};
    std::vector<RECT> dirty;
    std::vector<DXGI_OUTDUPL_MOVE_RECT> moves;
    std::vector<std::uint8_t> pointer_shape;
    DXGI_OUTDUPL_POINTER_SHAPE_INFO pointer_info{};
    Frame()=default;Frame(const Frame&)=delete;Frame& operator=(const Frame&)=delete;
    Frame(Frame&& other)noexcept:owner(std::move(other.owner)),texture(std::move(other.texture)),info(other.info),dirty(std::move(other.dirty)),moves(std::move(other.moves)),pointer_shape(std::move(other.pointer_shape)),pointer_info(other.pointer_info){}
    Frame& operator=(Frame&&)=delete;
    ~Frame(){if(owner)owner->ReleaseFrame();}
};
class DxgiCapture {
    ComPtr<ID3D11Device> device_;
    ComPtr<ID3D11DeviceContext> context_;
    ComPtr<IDXGIOutputDuplication> duplication_;
    DXGI_OUTPUT_DESC output_{};
    DXGI_ADAPTER_DESC1 adapter_{};
public:
    explicit DxgiCapture(UINT adapter_index=0,UINT output_index=0){
        ComPtr<IDXGIFactory1> factory;check(CreateDXGIFactory1(IID_PPV_ARGS(factory.GetAddressOf())),"CreateDXGIFactory1");
        ComPtr<IDXGIAdapter1> adapter;check(factory->EnumAdapters1(adapter_index,adapter.GetAddressOf()),"EnumAdapters1");check(adapter->GetDesc1(&adapter_),"GetDesc1");
        D3D_FEATURE_LEVEL level{};check(D3D11CreateDevice(adapter.Get(),D3D_DRIVER_TYPE_UNKNOWN,nullptr,D3D11_CREATE_DEVICE_BGRA_SUPPORT|D3D11_CREATE_DEVICE_VIDEO_SUPPORT,nullptr,0,D3D11_SDK_VERSION,device_.GetAddressOf(),&level,context_.GetAddressOf()),"D3D11CreateDevice");
        ComPtr<IDXGIOutput> output;check(adapter->EnumOutputs(output_index,output.GetAddressOf()),"EnumOutputs");check(output->GetDesc(&output_),"GetOutputDesc");
        if(!output_.AttachedToDesktop)throw std::runtime_error("output is not attached to desktop");
        ComPtr<IDXGIOutput1> output1;check(output.As(&output1),"IDXGIOutput1");check(output1->DuplicateOutput(device_.Get(),duplication_.GetAddressOf()),"DuplicateOutput");
    }
    const DXGI_OUTPUT_DESC& output()const{return output_;}
    const DXGI_ADAPTER_DESC1& adapter()const{return adapter_;}
    ID3D11Device* device()const{return device_.Get();}
    std::optional<Frame> next(UINT timeout_ms=16){
        DXGI_OUTDUPL_FRAME_INFO info{};ComPtr<IDXGIResource> resource;
        const auto hr=duplication_->AcquireNextFrame(timeout_ms,&info,resource.GetAddressOf());
        if(hr==DXGI_ERROR_WAIT_TIMEOUT)return std::nullopt;
        check(hr,"AcquireNextFrame");Frame f;f.owner=duplication_;f.info=info;
        check(resource.As(&f.texture),"ID3D11Texture2D");
        if(info.TotalMetadataBufferSize>8*1024*1024)throw std::runtime_error("unexpected DXGI metadata size");
        if(info.TotalMetadataBufferSize){
            UINT needed=0;auto rc=duplication_->GetFrameDirtyRects(0,nullptr,&needed);
            if(rc!=DXGI_ERROR_MORE_DATA)check(rc,"GetFrameDirtyRects size");
            if(needed>8*1024*1024||needed%sizeof(RECT)!=0)throw std::runtime_error("invalid dirty rect size");
            if(needed){f.dirty.resize(needed/sizeof(RECT));check(duplication_->GetFrameDirtyRects(needed,f.dirty.data(),&needed),"GetFrameDirtyRects");}
            needed=0;rc=duplication_->GetFrameMoveRects(0,nullptr,&needed);
            if(rc!=DXGI_ERROR_MORE_DATA)check(rc,"GetFrameMoveRects size");
            if(needed>8*1024*1024||needed%sizeof(DXGI_OUTDUPL_MOVE_RECT)!=0)throw std::runtime_error("invalid move rect size");
            if(needed){f.moves.resize(needed/sizeof(DXGI_OUTDUPL_MOVE_RECT));check(duplication_->GetFrameMoveRects(needed,f.moves.data(),&needed),"GetFrameMoveRects");}
        }
        if(info.PointerShapeBufferSize){if(info.PointerShapeBufferSize>1024*1024)throw std::runtime_error("unexpected pointer shape size");f.pointer_shape.resize(info.PointerShapeBufferSize);UINT needed=0;check(duplication_->GetFramePointerShape(static_cast<UINT>(f.pointer_shape.size()),f.pointer_shape.data(),&needed,&f.pointer_info),"GetFramePointerShape");f.pointer_shape.resize(needed);}
        return f;
    }
};
} // namespace rd
