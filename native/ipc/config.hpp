#pragma once
#include "transport/session.hpp"
#include <istream>
namespace rd {
struct MediaConfig {
 bool host=false,control=false,clipboard=false,audio=false,requireHardware=true;
 std::uint16_t broker=0,adapter=0,monitor=0,width=1920,height=1080,fps=60;
 std::uint32_t bitrate=12000000;std::uint64_t parentWindow=0;QuicConfig quic;
 static MediaConfig read(std::istream& in){
  std::array<std::uint8_t,72> h{};if(!in.read(reinterpret_cast<char*>(h.data()),h.size())||std::memcmp(h.data(),"RDC2",4))throw std::runtime_error("invalid inherited media configuration");
  MediaConfig c;auto flags=wire::get32(h.data()+4);if(flags&~31u)throw std::runtime_error("unknown media flags");c.host=flags&1;c.control=flags&2;c.clipboard=flags&4;c.audio=flags&8;c.requireHardware=flags&16;c.broker=wire::get16(h.data()+8);c.adapter=wire::get16(h.data()+10);c.monitor=wire::get16(h.data()+12);c.width=wire::get16(h.data()+14);c.height=wire::get16(h.data()+16);c.fps=wire::get16(h.data()+18);c.bitrate=wire::get32(h.data()+20);c.parentWindow=wire::get64(h.data()+24);
  auto cn=wire::get32(h.data()+32),kn=wire::get32(h.data()+36);if(cn<32||cn>16384||kn<32||kn>4096||c.broker==0||c.adapter>31||c.monitor>31||c.width<16||c.width>8192||c.height<16||c.height>8192||(c.width&1)||(c.height&1)||c.fps==0||c.fps>120||c.bitrate<250000||c.bitrate>100000000)throw std::runtime_error("invalid media configuration bounds");
  c.quic.server=c.host;c.quic.peer_port=c.broker;std::copy(h.begin()+40,h.end(),c.quic.peer_key.begin());c.quic.certificate_pem.resize(cn);c.quic.private_key_pem.resize(kn);if(!in.read(c.quic.certificate_pem.data(),cn)||!in.read(c.quic.private_key_pem.data(),kn))throw std::runtime_error("truncated inherited identity");return c;
 }
};
}
