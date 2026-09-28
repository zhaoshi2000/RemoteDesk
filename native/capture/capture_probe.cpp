// Real DXGI acquisition diagnostic. This is not a remote desktop renderer or encoder.
#include "dxgi_capture.hpp"
#include <chrono>
#include <iostream>
#include <memory>
#include <thread>

int main(int argc,char** argv){try{
    int seconds=10;unsigned adapter=0,output=0;
    for(int i=1;i<argc;++i){const std::string arg=argv[i];if(i+1>=argc)throw std::runtime_error("usage: capture-probe --seconds 10 --adapter 0 --output 0");if(arg=="--seconds")seconds=std::stoi(argv[++i]);else if(arg=="--adapter")adapter=std::stoul(argv[++i]);else if(arg=="--output")output=std::stoul(argv[++i]);else throw std::runtime_error("unknown option");}
    if(seconds<1||seconds>3600)throw std::runtime_error("seconds must be 1..3600");
    SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
    auto capture=std::make_unique<rd::DxgiCapture>(adapter,output);
    std::wcout<<L"Adapter: "<<capture->adapter().Description<<L"\nOutput: "<<capture->output().DeviceName<<L"\n";
    const auto start=std::chrono::steady_clock::now();std::uint64_t frames=0,dirty=0,moves=0,cursor_changes=0,resets=0;
    UINT width=0,height=0;
    while(std::chrono::steady_clock::now()-start<std::chrono::seconds(seconds)){
        try{auto frame=capture->next(16);if(!frame)continue;++frames;dirty+=frame->dirty.size();moves+=frame->moves.size();if(!frame->pointer_shape.empty())++cursor_changes;D3D11_TEXTURE2D_DESC desc{};frame->texture->GetDesc(&desc);width=desc.Width;height=desc.Height;}
        catch(const rd::HResultError& e){if(e.code!=DXGI_ERROR_ACCESS_LOST)throw;capture.reset();std::this_thread::sleep_for(std::chrono::milliseconds(200));capture=std::make_unique<rd::DxgiCapture>(adapter,output);++resets;}
    }
    const auto elapsed=std::chrono::duration<double>(std::chrono::steady_clock::now()-start).count();
    std::cout<<"AcquiredFrames="<<frames<<" AcquiredFPS="<<(frames/elapsed)<<" Width="<<width<<" Height="<<height<<" DirtyRects="<<dirty<<" MoveRects="<<moves<<" CursorChanges="<<cursor_changes<<" Resets="<<resets<<"\n";
    std::cout<<"Static desktops may produce few updates. This is not a GPU encode/network latency benchmark.\n";return 0;
}catch(const rd::HResultError& e){std::cerr<<e.what()<<" HRESULT=0x"<<std::hex<<static_cast<unsigned long>(e.code)<<'\n';return 2;}catch(const std::exception& e){std::cerr<<e.what()<<'\n';return 1;}}
