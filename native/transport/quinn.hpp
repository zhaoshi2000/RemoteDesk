#pragma once
#include "wire.hpp"
#include "config.hpp"
#include "remote_quic.h"
#include <algorithm>
#include <memory>
namespace rd {
class QuicSession {
 struct Delete {void operator()(RdqSession* p)const{rdq_destroy(p);}};
 std::unique_ptr<RdqSession,Delete> handle_;
 wire::Bytes buffer_=wire::Bytes(wire::max_video+wire::header_size);
 void check_state()const{if(rdq_state(handle_.get())<0){char error[1024]{};rdq_error(handle_.get(),error,sizeof(error));throw std::runtime_error(std::string("Quinn: ")+error);}if(rdq_state(handle_.get())==2)throw std::runtime_error("Quinn session closed");}
public:
 explicit QuicSession(QuicConfig c){
  RdqConfig cfg{};cfg.server=c.server?1:0;cfg.peer_port=c.peer_port;
  cfg.certificate=reinterpret_cast<const uint8_t*>(c.certificate_pem.data());cfg.certificate_len=c.certificate_pem.size();
  cfg.private_key=reinterpret_cast<const uint8_t*>(c.private_key_pem.data());cfg.private_key_len=c.private_key_pem.size();
  std::copy(c.peer_key.begin(),c.peer_key.end(),cfg.peer_key);char error[1024]{};handle_.reset(rdq_create(&cfg,error,sizeof(error)));
  volatile char* erase=c.private_key_pem.data();for(std::size_t i=0;i<c.private_key_pem.size();++i)erase[i]=0;
  if(!handle_)throw std::runtime_error(std::string("Quinn initialization: ")+error);
 }
 QuicSession(const QuicSession&)=delete;QuicSession& operator=(const QuicSession&)=delete;
 std::uint16_t port()const{return rdq_port(handle_.get());}
 bool ready()const{return rdq_state(handle_.get())==1;}
 std::uint64_t dropped()const{return rdq_dropped(handle_.get());}
 std::size_t queued_video()const{return rdq_queued_video(handle_.get());}
 bool send(wire::Message m){auto b=wire::encode(m);auto result=rdq_send(handle_.get(),b.data(),b.size());if(result<0)check_state();return result==1;}
 void acknowledge(std::uint64_t id){rdq_acknowledge(handle_.get(),id);}
 std::vector<wire::Message> pump(){check_state();std::vector<wire::Message> out;for(int i=0;i<64;++i){auto n=rdq_receive(handle_.get(),buffer_.data(),buffer_.size());if(!n)break;if(n<0)throw std::runtime_error("Quinn receive bounds error");wire::Parser parser;auto messages=parser.feed(std::span(buffer_.data(),static_cast<std::size_t>(n)));parser.finish();for(auto& m:messages)out.push_back(std::move(m));}return out;}
};
}
