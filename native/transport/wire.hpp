#pragma once
#include <array>
#include <chrono>
#include <cstdint>
#include <cstring>
#include <deque>
#include <span>
#include <stdexcept>
#include <string>
#include <vector>

namespace rd::wire {
using Bytes = std::vector<std::uint8_t>;
constexpr std::size_t header_size = 32;
constexpr std::size_t max_video = 4 * 1024 * 1024;
constexpr std::size_t max_control = 128 * 1024;
enum Channel : std::uint8_t { control=0, input=1, video=3, audio=4, clipboard=5, file=7 };
enum Kind : std::uint8_t {
    hello=1, ping=2, pong=3, request_keyframe=4, stats=5, release_all=6,
    video_frame=16, video_ack=17, pointer_shape=18, pointer_position=19,
    mouse_move=32, mouse_button=33, mouse_wheel=34, key=35, text=36,
    clipboard_text=48, audio_frame=49, capabilities=50, configure_video=51, close=63
};
enum Flags : std::uint16_t { keyframe=1, codec_config=2 };
inline std::uint64_t now_us() {
    return static_cast<std::uint64_t>(std::chrono::duration_cast<std::chrono::microseconds>(
        std::chrono::steady_clock::now().time_since_epoch()).count());
}
inline void put16(std::uint8_t* p,std::uint16_t n){p[0]=std::uint8_t(n>>8);p[1]=std::uint8_t(n);}
inline void put32(std::uint8_t* p,std::uint32_t n){for(int i=3;i>=0;--i){p[i]=std::uint8_t(n);n>>=8;}}
inline void put64(std::uint8_t* p,std::uint64_t n){for(int i=7;i>=0;--i){p[i]=std::uint8_t(n);n>>=8;}}
inline std::uint16_t get16(const std::uint8_t* p){return std::uint16_t((std::uint16_t(p[0])<<8)|p[1]);}
inline std::uint32_t get32(const std::uint8_t* p){std::uint32_t n=0;for(int i=0;i<4;++i)n=(n<<8)|p[i];return n;}
inline std::uint64_t get64(const std::uint8_t* p){std::uint64_t n=0;for(int i=0;i<8;++i)n=(n<<8)|p[i];return n;}
inline bool valid_channel(std::uint8_t c){return c==control||c==input||c==video||c==audio||c==clipboard||c==file;}
struct Message {
    std::uint8_t channel=control, kind=hello;
    std::uint16_t flags=0;
    std::uint64_t id=0, timestamp=0;
    Bytes payload;
};
inline Bytes encode(const Message& m){
    const auto max=m.channel==video?max_video:max_control;
    if(!valid_channel(m.channel)||m.flags>3||m.payload.size()>max)
        throw std::runtime_error("invalid message bounds");
    Bytes b(header_size+m.payload.size());
    std::memcpy(b.data(),"RDV2",4);b[4]=m.channel;b[5]=m.kind;
    put16(b.data()+6,m.flags);put64(b.data()+8,m.id);put64(b.data()+16,m.timestamp);
    put32(b.data()+24,static_cast<std::uint32_t>(m.payload.size()));
    std::copy(m.payload.begin(),m.payload.end(),b.begin()+header_size);return b;
}
class Parser {
    Bytes buffer_;
    int channel_=-1;
public:
    std::size_t buffered()const{return buffer_.size();}
    int channel()const{return channel_;}
    std::vector<Message> feed(std::span<const std::uint8_t> bytes){
        if(buffer_.size()+bytes.size()>max_video+header_size+65536)
            throw std::runtime_error("stream receive buffer limit");
        buffer_.insert(buffer_.end(),bytes.begin(),bytes.end());
        std::vector<Message> out;std::size_t off=0;
        while(buffer_.size()-off>=header_size){
            auto* p=buffer_.data()+off;
            if(std::memcmp(p,"RDV2",4)||!valid_channel(p[4])||get16(p+6)>3||get32(p+28)!=0)
                throw std::runtime_error("invalid RDV2 header");
            if(channel_==-1)channel_=p[4];
            if(channel_!=p[4])throw std::runtime_error("channel changed within stream");
            const auto n=get32(p+24);
            if(n>(p[4]==video?max_video:max_control))throw std::runtime_error("message too large");
            if(buffer_.size()-off<header_size+n)break;
            Message m{p[4],p[5],get16(p+6),get64(p+8),get64(p+16),{}};
            m.payload.assign(p+header_size,p+header_size+n);out.push_back(std::move(m));
            off+=header_size+n;
        }
        if(off)buffer_.erase(buffer_.begin(),buffer_.begin()+static_cast<std::ptrdiff_t>(off));
        return out;
    }
    void finish()const{if(!buffer_.empty())throw std::runtime_error("truncated message at FIN");}
};
// Video payload: width/height u16, fps u16, reserved u16, then one Annex-B H.264 access unit.
inline Bytes video_payload(std::uint16_t w,std::uint16_t h,std::uint16_t fps,std::span<const std::uint8_t> au){
    if(w<16||h<16||w>8192||h>8192||(w&1)||(h&1)||fps==0||fps>120||au.empty()||au.size()>max_video-8)
        throw std::runtime_error("invalid video configuration");
    Bytes b(8+au.size());put16(b.data(),w);put16(b.data()+2,h);put16(b.data()+4,fps);
    std::copy(au.begin(),au.end(),b.begin()+8);return b;
}
struct VideoView{std::uint16_t width,height,fps;std::span<const std::uint8_t> au;};
inline VideoView parse_video(std::span<const std::uint8_t> p){
    if(p.size()<9)throw std::runtime_error("short video access unit");
    VideoView v{get16(p.data()),get16(p.data()+2),get16(p.data()+4),p.subspan(8)};
    if(v.width<16||v.height<16||v.width>8192||v.height>8192||(v.width&1)||(v.height&1)||!v.fps||v.fps>120||get16(p.data()+6))
        throw std::runtime_error("invalid video dimensions");
    return v;
}
} // namespace rd::wire
