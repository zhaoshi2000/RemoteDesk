#pragma once
#include "wire.hpp"
#include "config.hpp"
#include <openssl/ssl.h>
#include <openssl/quic.h>
#include <openssl/pem.h>
#include <openssl/err.h>
#include <openssl/crypto.h>
#include <algorithm>
#include <array>
#include <map>
#include <memory>
#include <string_view>
#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
#else
#include <arpa/inet.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/socket.h>
#endif

namespace rd {
inline std::runtime_error ssl_error(const char* where){
    char b[256]{};const auto e=ERR_get_error();ERR_error_string_n(e,b,sizeof(b));
    return std::runtime_error(std::string(where)+": "+b);
}
inline void ssl_check(int rc,const char* where){if(rc!=1)throw ssl_error(where);}
template<class T,auto Free> struct SslDelete{void operator()(T* p)const{if(p)Free(p);}};
using SSLPtr=std::unique_ptr<SSL,SslDelete<SSL,SSL_free>>;
using CtxPtr=std::unique_ptr<SSL_CTX,SslDelete<SSL_CTX,SSL_CTX_free>>;
using BioPtr=std::unique_ptr<BIO,SslDelete<BIO,BIO_free>>;
using CertPtr=std::unique_ptr<X509,SslDelete<X509,X509_free>>;
using KeyPtr=std::unique_ptr<EVP_PKEY,SslDelete<EVP_PKEY,EVP_PKEY_free>>;
class UdpSocket {
#ifdef _WIN32
    SOCKET fd_=INVALID_SOCKET;
#else
    int fd_=-1;
#endif
    std::uint16_t port_=0;
public:
    UdpSocket(){
#ifdef _WIN32
        static const int started=[](){WSADATA w{};return WSAStartup(MAKEWORD(2,2),&w);}();
        if(started)throw std::runtime_error("WSAStartup failed");
#endif
        fd_=::socket(AF_INET,SOCK_DGRAM,0);
#ifdef _WIN32
        if(fd_==INVALID_SOCKET)throw std::runtime_error("UDP socket failed");
        u_long nonblock=1;if(ioctlsocket(fd_,FIONBIO,&nonblock))throw std::runtime_error("UDP nonblocking failed");
#else
        if(fd_<0)throw std::runtime_error("UDP socket failed");
        if(fcntl(fd_,F_SETFL,fcntl(fd_,F_GETFL,0)|O_NONBLOCK)<0)throw std::runtime_error("UDP nonblocking failed");
#endif
        sockaddr_in a{};a.sin_family=AF_INET;a.sin_addr.s_addr=htonl(INADDR_LOOPBACK);
        if(::bind(fd_,reinterpret_cast<sockaddr*>(&a),sizeof(a)))throw std::runtime_error("UDP bind failed");
#ifdef _WIN32
        int n=sizeof(a);
#else
        socklen_t n=sizeof(a);
#endif
        if(getsockname(fd_,reinterpret_cast<sockaddr*>(&a),&n))throw std::runtime_error("getsockname failed");
        port_=ntohs(a.sin_port);
        int size=2*1024*1024;
        setsockopt(fd_,SOL_SOCKET,SO_RCVBUF,reinterpret_cast<const char*>(&size),sizeof(size));
        setsockopt(fd_,SOL_SOCKET,SO_SNDBUF,reinterpret_cast<const char*>(&size),sizeof(size));
    }
    ~UdpSocket(){
#ifdef _WIN32
        if(fd_!=INVALID_SOCKET)closesocket(fd_);
#else
        if(fd_>=0)::close(fd_);
#endif
    }
    UdpSocket(const UdpSocket&)=delete;UdpSocket& operator=(const UdpSocket&)=delete;
    int fd()const{return static_cast<int>(fd_);} // BIO_new_dgram ABI uses int, including Windows.
    std::uint16_t port()const{return port_;}
};

class QuicSession {
    struct Out {
        SSLPtr stream;std::deque<wire::Bytes> messages;std::size_t offset=0,queued=0;
        std::uint64_t frame_id=0,born=0;bool video=false,concluded=false;
    };
    struct In {SSLPtr stream;wire::Parser parser;std::uint64_t born=0;};
    UdpSocket socket_;
    CtxPtr context_;
    SSLPtr listener_,connection_;
    std::array<unsigned char,32> expected_{};
    std::map<int,Out> ordered_;
    std::deque<Out> videos_;
    std::vector<In> incoming_;
    bool server_=false,ready_=false;
    std::uint64_t born_=wire::now_us(),dropped_=0;
    static constexpr std::string_view alpn_="remotedesk-media/2";
    static int verify(X509_STORE_CTX* store,void* self){
        auto* s=static_cast<QuicSession*>(self);X509* cert=X509_STORE_CTX_get0_cert(store);
        if(!cert)return 0;
        KeyPtr key(X509_get_pubkey(cert));std::array<unsigned char,32> raw{};size_t n=raw.size();
        const bool ok=key&&EVP_PKEY_is_a(key.get(),"ED25519")&&
            EVP_PKEY_get_raw_public_key(key.get(),raw.data(),&n)==1&&n==raw.size()&&
            CRYPTO_memcmp(raw.data(),s->expected_.data(),raw.size())==0&&
            X509_cmp_current_time(X509_get0_notBefore(cert))<0&&X509_cmp_current_time(X509_get0_notAfter(cert))>0&&
            X509_verify(cert,key.get())==1;
        X509_STORE_CTX_set_error(store,ok?X509_V_OK:X509_V_ERR_APPLICATION_VERIFICATION);
        return ok?1:0;
    }
    static int select_alpn(SSL*,const unsigned char** out,unsigned char* outlen,const unsigned char* in,unsigned int inlen,void*){
        for(unsigned int off=0;off<inlen;){const unsigned n=in[off++];if(n>inlen-off)break;
            if(n==alpn_.size()&&std::memcmp(in+off,alpn_.data(),n)==0){*out=in+off;*outlen=static_cast<unsigned char>(n);return SSL_TLSEXT_ERR_OK;}off+=n;}
        return SSL_TLSEXT_ERR_ALERT_FATAL;
    }
    void configure_connection(){
        ssl_check(SSL_set_blocking_mode(connection_.get(),0),"nonblocking QUIC");
        ssl_check(SSL_set_default_stream_mode(connection_.get(),SSL_DEFAULT_STREAM_MODE_NONE),"disable default stream");
        ssl_check(SSL_set_incoming_stream_policy(connection_.get(),SSL_INCOMING_STREAM_POLICY_ACCEPT,0),"accept streams");
    }
    Out new_out(bool video,std::uint64_t frame){
        SSL* s=SSL_new_stream(connection_.get(),SSL_STREAM_FLAG_UNI|SSL_STREAM_FLAG_NO_BLOCK);
        if(!s)throw ssl_error("QUIC stream limit/open");
        Out o;o.stream.reset(s);o.video=video;o.frame_id=frame;o.born=wire::now_us();
        ssl_check(SSL_set_blocking_mode(s,0),"stream nonblocking");
        // OpenSSL 3.5 exposes this identifier but some builds cannot set it.
        // Application queues and per-pump write budgets enforce our own bounds.
        (void)SSL_set_generic_value_uint(s,SSL_VALUE_STREAM_WRITE_BUF_SIZE,video?65536:16384);ERR_clear_error();
        return o;
    }
    static void reset(Out& o){SSL_STREAM_RESET_ARGS a{0x100};(void)SSL_stream_reset(o.stream.get(),&a,sizeof(a));}
    bool flush(Out& o,std::size_t budget){
        while(!o.messages.empty()&&budget){
            auto& b=o.messages.front();const auto n=std::min({b.size()-o.offset,budget,std::size_t(16384)});size_t used=0;
            ERR_clear_error();int rc=SSL_write_ex(o.stream.get(),b.data()+o.offset,n,&used);
            if(rc!=1){int e=SSL_get_error(o.stream.get(),rc);if(e==SSL_ERROR_WANT_READ||e==SSL_ERROR_WANT_WRITE)return false;throw ssl_error("QUIC write");}
            o.offset+=used;o.queued-=used;budget-=used;
            if(o.offset==b.size()){o.messages.pop_front();o.offset=0;}
            if(used==0)return false;
        }
        if(o.video&&o.messages.empty()&&!o.concluded){ssl_check(SSL_stream_conclude(o.stream.get(),0),"video FIN");o.concluded=true;}
        return true;
    }
public:
    explicit QuicSession(QuicConfig c):expected_(c.peer_key),server_(c.server){
        context_.reset(SSL_CTX_new(server_?OSSL_QUIC_server_method():OSSL_QUIC_client_method()));
        if(!context_)throw ssl_error("QUIC context (OpenSSL >= 3.5 required)");
        SSL_CTX_set_verify(context_.get(),SSL_VERIFY_PEER|(server_?SSL_VERIFY_FAIL_IF_NO_PEER_CERT:0),nullptr);
        SSL_CTX_set_cert_verify_callback(context_.get(),verify,this);
        SSL_CTX_set_session_cache_mode(context_.get(),SSL_SESS_CACHE_OFF);
        SSL_CTX_set_num_tickets(context_.get(),0);
        SSL_CTX_set_alpn_select_cb(context_.get(),select_alpn,nullptr);
        BioPtr cb(BIO_new_mem_buf(c.certificate_pem.data(),static_cast<int>(c.certificate_pem.size())));
        BioPtr kb(BIO_new_mem_buf(c.private_key_pem.data(),static_cast<int>(c.private_key_pem.size())));
        CertPtr cert(PEM_read_bio_X509(cb.get(),nullptr,nullptr,nullptr));
        KeyPtr key(PEM_read_bio_PrivateKey(kb.get(),nullptr,nullptr,nullptr));
        if(!cert||!key)throw ssl_error("parse in-memory identity");
        ssl_check(SSL_CTX_use_certificate(context_.get(),cert.get()),"use certificate");
        ssl_check(SSL_CTX_use_PrivateKey(context_.get(),key.get()),"use device key");
        ssl_check(SSL_CTX_check_private_key(context_.get()),"matching device key");
        OPENSSL_cleanse(c.private_key_pem.data(),c.private_key_pem.size());
        BioPtr bio(BIO_new_dgram(socket_.fd(),BIO_NOCLOSE));if(!bio)throw ssl_error("datagram BIO");
        if(server_){
            // Loopback broker has already authenticated the signed session. TLS still pins both devices.
            listener_.reset(SSL_new_listener(context_.get(),0));if(!listener_)throw ssl_error("QUIC listener");
            SSL_set_bio(listener_.get(),bio.get(),bio.get());bio.release();
            ssl_check(SSL_set_blocking_mode(listener_.get(),0),"listener nonblocking");
            ssl_check(SSL_listen(listener_.get()),"QUIC listen");
        }else{
            if(!c.peer_port)throw std::runtime_error("missing loopback QUIC broker port");
            connection_.reset(SSL_new(context_.get()));if(!connection_)throw ssl_error("QUIC client");
            SSL_set_bio(connection_.get(),bio.get(),bio.get());bio.release();configure_connection();
            std::string a(1,static_cast<char>(alpn_.size()));a.append(alpn_);
            if(SSL_set_alpn_protos(connection_.get(),reinterpret_cast<const unsigned char*>(a.data()),static_cast<unsigned>(a.size()))!=0)throw ssl_error("client ALPN");
            BIO_ADDR* address=BIO_ADDR_new();in_addr ip{};ip.s_addr=htonl(INADDR_LOOPBACK);
            if(!address)throw ssl_error("peer BIO_ADDR");
            const auto made=BIO_ADDR_rawmake(address,AF_INET,&ip,sizeof(ip),htons(c.peer_port));
            const auto set=made?SSL_set1_initial_peer_addr(connection_.get(),address):0;BIO_ADDR_free(address);ssl_check(set,"initial QUIC peer");
        }
    }
    ~QuicSession(){
        if(connection_){SSL_SHUTDOWN_EX_ARGS a{0,"session closed"};(void)SSL_shutdown_ex(connection_.get(),SSL_SHUTDOWN_FLAG_RAPID|SSL_SHUTDOWN_FLAG_NO_BLOCK,&a,sizeof(a));}
    }
    QuicSession(const QuicSession&)=delete;QuicSession& operator=(const QuicSession&)=delete;
    std::uint16_t port()const{return socket_.port();}
    bool ready()const{return ready_;}
    std::uint64_t dropped()const{return dropped_;}
    std::size_t queued_video()const{std::size_t n=0;for(const auto& v:videos_)n+=v.queued;return n;}
    bool send(wire::Message m){
        if(!ready_)return false;
        if(m.channel==wire::video){
            if(videos_.size()>=3){reset(videos_.front());videos_.pop_front();++dropped_;}
            auto o=new_out(true,m.id);o.messages.push_back(wire::encode(m));o.queued=o.messages.front().size();videos_.push_back(std::move(o));
        }else{
            auto it=ordered_.find(m.channel);if(it==ordered_.end())it=ordered_.emplace(m.channel,new_out(false,0)).first;
            auto b=wire::encode(m);if(m.channel==wire::audio&&it->second.queued+b.size()>16*1024)return false;
            if(it->second.queued+b.size()>256*1024)throw std::runtime_error("reliable channel congestion: session closed to release input");
            it->second.queued+=b.size();it->second.messages.push_back(std::move(b));
        }return true;
    }
    void acknowledge(std::uint64_t frame){
        for(auto i=videos_.begin();i!=videos_.end();++i)if(i->frame_id==frame){videos_.erase(i);break;}
    }
    std::vector<wire::Message> pump(){
        const auto now=wire::now_us();std::vector<wire::Message> result;
        if(listener_){ssl_check(SSL_handle_events(listener_.get()),"listener events");
            if(!connection_){connection_.reset(SSL_accept_connection(listener_.get(),SSL_ACCEPT_CONNECTION_NO_BLOCK));if(connection_)configure_connection();}}
        if(!connection_){if(now-born_>15000000)throw std::runtime_error("QUIC accept timeout");return result;}
        ERR_clear_error();ssl_check(SSL_handle_events(connection_.get()),"QUIC events");
        if(!ready_){
            int rc=SSL_do_handshake(connection_.get());
            if(rc==1){
                const unsigned char* a=nullptr;unsigned n=0;SSL_get0_alpn_selected(connection_.get(),&a,&n);
                if(n!=alpn_.size()||std::memcmp(a,alpn_.data(),n)||SSL_get_verify_result(connection_.get())!=X509_V_OK||!SSL_get0_peer_certificate(connection_.get()))throw std::runtime_error("QUIC peer authentication failed");
                ready_=true;
            }else{int e=SSL_get_error(connection_.get(),rc);if(e!=SSL_ERROR_WANT_READ&&e!=SSL_ERROR_WANT_WRITE)throw ssl_error("QUIC handshake");}
            if(!ready_){if(now-born_>15000000)throw std::runtime_error("QUIC handshake timeout");return result;}
        }
        SSL_CONN_CLOSE_INFO ci{};if(SSL_get_conn_close_info(connection_.get(),&ci,sizeof(ci))==1)throw std::runtime_error("QUIC connection closed");
        // Control and input are submitted before media. The library owns transport congestion control.
        for(auto& [channel,o]:ordered_){(void)channel;flush(o,32768);}
        for(auto it=videos_.begin();it!=videos_.end();){
            if(now-it->born>250000){reset(*it);it=videos_.erase(it);++dropped_;}else{flush(*it,16384);++it;}}
        for(unsigned count=0;count<16;++count){SSL* s=SSL_accept_stream(connection_.get(),SSL_ACCEPT_STREAM_NO_BLOCK);if(!s)break;
            if(incoming_.size()>=24){SSL_free(s);throw std::runtime_error("too many incoming streams");}
            ssl_check(SSL_set_blocking_mode(s,0),"incoming nonblocking");incoming_.push_back(In{SSLPtr(s),{},now});}
        std::array<unsigned char,16384> bytes{};std::size_t total_buffer=0;
        for(auto it=incoming_.begin();it!=incoming_.end();){
            bool erase=false;
            for(int reads=0;reads<8;++reads){size_t n=0;ERR_clear_error();int rc=SSL_read_ex(it->stream.get(),bytes.data(),bytes.size(),&n);
                if(rc==1){auto messages=it->parser.feed({bytes.data(),n});for(auto& m:messages)result.push_back(std::move(m));continue;}
                int e=SSL_get_error(it->stream.get(),rc);
                if(e==SSL_ERROR_WANT_READ||e==SSL_ERROR_WANT_WRITE)break;
                if(e==SSL_ERROR_ZERO_RETURN){it->parser.finish();erase=true;break;}
                const int state=SSL_get_stream_read_state(it->stream.get());
                if(state==SSL_STREAM_STATE_RESET_REMOTE||state==SSL_STREAM_STATE_RESET_LOCAL){
                    if(it->parser.channel()==wire::video||it->parser.channel()==-1){++dropped_;erase=true;break;}
                    throw std::runtime_error("reliable control stream reset");
                }
                throw ssl_error("QUIC stream read");
            }
            if(!erase&&it->parser.channel()==wire::video&&now-it->born>500000){SSL_STREAM_RESET_ARGS a{0x100};(void)SSL_stream_reset(it->stream.get(),&a,sizeof(a));erase=true;++dropped_;}
            if(erase)it=incoming_.erase(it);else{total_buffer+=it->parser.buffered();++it;}
        }
        if(total_buffer>8*1024*1024)throw std::runtime_error("aggregate media receive limit");
        return result;
    }
};
} // namespace rd
