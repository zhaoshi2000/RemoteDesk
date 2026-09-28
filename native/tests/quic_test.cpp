#include "transport/quic.hpp"
#include <iostream>
#include <thread>
#include <functional>
using namespace rd;
struct Identity {std::string cert,key;std::array<unsigned char,32> pub{};};
std::string pem(BIO* b){char* p=nullptr;const auto n=BIO_get_mem_data(b,&p);return {p,static_cast<std::size_t>(n)};}
Identity identity(){
    Identity i;KeyPtr key(EVP_PKEY_Q_keygen(nullptr,nullptr,"ED25519"));if(!key)throw ssl_error("keygen");
    size_t n=32;ssl_check(EVP_PKEY_get_raw_public_key(key.get(),i.pub.data(),&n),"public key");
    CertPtr c(X509_new());ssl_check(X509_set_version(c.get(),2),"version");ASN1_INTEGER_set(X509_get_serialNumber(c.get()),1);
    X509_gmtime_adj(X509_getm_notBefore(c.get()),-60);X509_gmtime_adj(X509_getm_notAfter(c.get()),3600);
    auto* name=X509_get_subject_name(c.get());X509_NAME_add_entry_by_txt(name,"CN",MBSTRING_ASC,reinterpret_cast<const unsigned char*>("test"),-1,-1,0);
    X509_set_issuer_name(c.get(),name);ssl_check(X509_set_pubkey(c.get(),key.get()),"cert key");if(X509_sign(c.get(),key.get(),nullptr)<=0)throw ssl_error("sign");
    BioPtr cb(BIO_new(BIO_s_mem())),kb(BIO_new(BIO_s_mem()));ssl_check(PEM_write_bio_X509(cb.get(),c.get()),"cert pem");ssl_check(PEM_write_bio_PrivateKey(kb.get(),key.get(),nullptr,nullptr,0,nullptr,nullptr),"key pem");i.cert=pem(cb.get());i.key=pem(kb.get());return i;
}
void require(bool x,const char* msg){if(!x)throw std::runtime_error(msg);}
int main(){try{
    auto a=identity(),b=identity();QuicSession server({true,b.cert,b.key,a.pub,0});QuicSession client({false,a.cert,a.key,b.pub,server.port()});
    auto until=wire::now_us()+5000000;
    while((!server.ready()||!client.ready())&&wire::now_us()<until){server.pump();client.pump();std::this_thread::sleep_for(std::chrono::milliseconds(1));}
    require(server.ready()&&client.ready(),"mTLS QUIC handshake did not complete");std::cout<<"PASS mutual Ed25519-pinned QUIC handshake over real UDP\n";
    wire::Message m;m.channel=wire::video;m.kind=wire::video_frame;m.id=1;m.payload.resize(256*1024);for(std::size_t i=0;i<m.payload.size();++i)m.payload[i]=std::uint8_t(i*37);
    server.send(m);wire::Message k;k.channel=wire::input;k.kind=wire::key;k.payload={1,2,3,4};client.send(k);
    bool video=false,input=false;until=wire::now_us()+5000000;
    while((!video||!input)&&wire::now_us()<until){for(auto& x:client.pump())if(x.channel==wire::video){require(x.payload==m.payload,"video corruption");server.acknowledge(x.id);video=true;}
        for(auto& x:server.pump()){if(x.channel==wire::input){require(x.payload==k.payload,"input corruption");input=true;}}
        std::this_thread::sleep_for(std::chrono::milliseconds(1));}
    require(video&&input,"multiplex transfer timeout");std::cout<<"PASS 256 KiB frame and independent reliable input stream\n";
    bool rejected=false;auto wrong=identity();try{QuicSession s({true,b.cert,b.key,a.pub,0});QuicSession c({false,wrong.cert,wrong.key,b.pub,s.port()});until=wire::now_us()+2000000;while(wire::now_us()<until){s.pump();c.pump();std::this_thread::sleep_for(std::chrono::milliseconds(1));}}catch(const std::exception&){rejected=true;}
    require(rejected,"untrusted client was not rejected");std::cout<<"PASS unauthorized client certificate rejected\n";
    wire::Parser parser;auto encoded=wire::encode(k);std::size_t got=0;for(auto v:encoded){auto out=parser.feed({&v,1});got+=out.size();}parser.finish();require(got==1,"incremental parser");
    std::cout<<"PASS single-byte fragmented reliable framing\n";
    std::cout<<"ALL QUIC CHECKS PASSED\n";return 0;
}catch(const std::exception& e){std::cerr<<"FAIL "<<e.what()<<'\n';return 1;}}
