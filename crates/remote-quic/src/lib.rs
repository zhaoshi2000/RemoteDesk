//! Bounded, mutually authenticated Quinn transport behind a narrow C ABI.
//! The only UDP listener is loopback; the Go broker supplies signed P2P/relay routing.
//! All remote input is treated as hostile.  No resumption, 0-RTT or insecure TLS mode.
mod pinning;
mod wire;
use std::{collections::{HashMap, VecDeque}, ffi::c_char, io::Cursor, net::{Ipv4Addr, SocketAddr, UdpSocket},
    ptr, slice, sync::{Arc, Mutex, atomic::{AtomicI32, AtomicU64, AtomicUsize, Ordering}}, thread, time::Duration};
use quinn::{Connection, Endpoint, TransportConfig, VarInt};
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use tokio::{sync::{mpsc, oneshot, Semaphore}, task::JoinSet, time::{timeout, interval}};
use zeroize::Zeroize;
use wire::{HEADER, VIDEO, MAX_QUEUED};
const ALPN: &[u8] = b"remotedesk-media/2";
const FRAME_TTL: Duration = Duration::from_millis(250);
type Result<T> = std::result::Result<T, String>;

pub struct Config { pub server: bool, pub certificate_pem: Vec<u8>, pub private_key_pem: Vec<u8>, pub peer_key: [u8;32], pub peer_port: u16 }
impl Drop for Config { fn drop(&mut self) { self.private_key_pem.zeroize(); } }
struct Tls { server: Option<quinn::ServerConfig>, client: quinn::ClientConfig }
fn tls(c: &Config) -> Result<Tls> {
    let certs = rustls_pemfile::certs(&mut Cursor::new(&c.certificate_pem)).collect::<std::result::Result<Vec<CertificateDer<'static>>, _>>().map_err(|e|e.to_string())?;
    if certs.len() != 1 { return Err("one local device certificate is required".into()); }
    let key: PrivateKeyDer<'static> = rustls_pemfile::private_key(&mut Cursor::new(&c.private_key_pem)).map_err(|e|e.to_string())?.ok_or("missing device private key")?;
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let mut client = rustls::ClientConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13]).map_err(|e|e.to_string())?
        .dangerous().with_custom_certificate_verifier(Arc::new(pinning::Pin::new(c.peer_key)))
        .with_client_auth_cert(certs.clone(), key.clone_key()).map_err(|e|e.to_string())?;
    client.alpn_protocols = vec![ALPN.to_vec()]; client.enable_early_data = false;
    client.resumption = rustls::client::Resumption::disabled();
    let mut server = rustls::ServerConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13]).map_err(|e|e.to_string())?
        .with_client_cert_verifier(Arc::new(pinning::Pin::new(c.peer_key)))
        .with_single_cert(certs, key).map_err(|e|e.to_string())?;
    server.alpn_protocols = vec![ALPN.to_vec()]; server.max_early_data_size = 0;
    server.send_tls13_tickets = 0;
    let mut transport = TransportConfig::default();
    transport.max_concurrent_uni_streams(VarInt::from_u32(24));
    transport.max_concurrent_bidi_streams(VarInt::from_u32(0));
    transport.stream_receive_window(VarInt::from_u32((wire::MAX_VIDEO+HEADER) as u32));
    transport.receive_window(VarInt::from_u32(MAX_QUEUED as u32));
    transport.send_window(MAX_QUEUED as u64);
    transport.keep_alive_interval(Some(Duration::from_secs(2)));
    transport.max_idle_timeout(Some(Duration::from_secs(10).try_into().map_err(|_|"idle timeout")?));
    // Outer authenticated relay adds bytes. Keep inner UDP packets at 1200 bytes.
    transport.initial_mtu(1200).min_mtu(1200).mtu_discovery_config(None);
    transport.datagram_receive_buffer_size(None);
    let transport = Arc::new(transport);
    let mut qclient = quinn::ClientConfig::new(Arc::new(quinn::crypto::rustls::QuicClientConfig::try_from(client).map_err(|e|e.to_string())?));
    qclient.transport_config(transport.clone());
    let qserver = if c.server {
        let mut s = quinn::ServerConfig::with_crypto(Arc::new(quinn::crypto::rustls::QuicServerConfig::try_from(server).map_err(|e|e.to_string())?));
        s.transport_config(transport); s.migration(false); Some(s)
    } else { None };
    Ok(Tls { server:qserver, client:qclient })
}
#[derive(Default)]
struct Incoming { messages: VecDeque<Vec<u8>>, bytes: usize }
struct Shared { state: AtomicI32, error: Mutex<String>, incoming: Mutex<Incoming>, queued: AtomicUsize, video: AtomicUsize, dropped: AtomicU64, rtt: AtomicU64, assembly:Arc<Semaphore> }
impl Default for Shared {
    fn default() -> Self { Self { state:AtomicI32::new(0), error:Mutex::new(String::new()), incoming:Mutex::new(Incoming::default()), queued:AtomicUsize::new(0), video:AtomicUsize::new(0), dropped:AtomicU64::new(0), rtt:AtomicU64::new(0),assembly:Arc::new(Semaphore::new(MAX_QUEUED)) } }
}
impl Shared {
    fn error(&self, e: String) { *self.error.lock().unwrap_or_else(|p|p.into_inner()) = e; self.state.store(-1, Ordering::Release); }
    fn reserve(&self,n:usize) -> bool { self.queued.fetch_update(Ordering::AcqRel, Ordering::Acquire, |v| v.checked_add(n).filter(|&v|v<=MAX_QUEUED)).is_ok() }
    fn incoming(&self, data:Vec<u8>) -> Result<()> {
        let is_video = data[4] == VIDEO;
        let mut q = self.incoming.lock().map_err(|_|"receive queue poisoned")?;
        if q.bytes + data.len() > MAX_QUEUED || q.messages.len() >= 256 {
            if is_video { self.dropped.fetch_add(1,Ordering::Relaxed); return Ok(()); }
            return Err("reliable receive queue overflow; disconnect to avoid losing key releases".into());
        }
        q.bytes += data.len(); q.messages.push_back(data); Ok(())
    }
}
struct Packet { data: Vec<u8>, shared: Arc<Shared>, video: bool }
impl Drop for Packet {
    fn drop(&mut self) { self.shared.queued.fetch_sub(self.data.len(),Ordering::AcqRel); if self.video { self.shared.video.fetch_sub(self.data.len(),Ordering::AcqRel); } }
}
enum Command { Send(Packet), Ack(u64) }
pub struct Session { shared: Arc<Shared>, tx: mpsc::Sender<Command>, close: Mutex<Option<oneshot::Sender<()>>>, thread: Option<thread::JoinHandle<()>>, port:u16 }
impl Session {
    pub fn new(config:Config) -> Result<Self> {
        let t = tls(&config)?;
        if !config.server && config.peer_port == 0 { return Err("missing loopback broker port".into()); }
        let socket = UdpSocket::bind((Ipv4Addr::LOCALHOST,0)).map_err(|e|e.to_string())?;
        socket.set_nonblocking(true).map_err(|e|e.to_string())?;
        let port = socket.local_addr().map_err(|e|e.to_string())?.port();
        let (tx, rx) = mpsc::channel(128); let (close_tx, close_rx)=oneshot::channel();
        let shared=Arc::new(Shared::default()); let s=shared.clone();
        let th=thread::Builder::new().name("rd-quinn".into()).spawn(move || {
            let rt = match tokio::runtime::Builder::new_current_thread().enable_all().build() { Ok(v)=>v, Err(e)=>{s.error(e.to_string());return;} };
            let result = rt.block_on(run(socket,t,config.server,config.peer_port,rx,close_rx,s.clone()));
            if let Err(e)=result { s.error(e); } else if s.state.load(Ordering::Acquire) != -1 { s.state.store(2,Ordering::Release); }
        }).map_err(|e|e.to_string())?;
        Ok(Self { shared,tx,close:Mutex::new(Some(close_tx)),thread:Some(th),port })
    }
    pub fn port(&self)->u16 { self.port }
    pub fn state(&self)->i32 { self.shared.state.load(Ordering::Acquire) }
    pub fn send(&self,data:Vec<u8>)->Result<bool> {
        let h=wire::frame(&data).map_err(str::to_owned)?;
        if self.state()!=1 {return Ok(false);}
        if !self.shared.reserve(data.len()) {
            if h.channel==VIDEO {self.shared.dropped.fetch_add(1,Ordering::Relaxed);return Ok(false);}
            self.shared.error("outgoing reliable queue overflow".into());self.stop();return Err("outgoing reliable queue overflow".into());
        }
        if h.channel==VIDEO {self.shared.video.fetch_add(data.len(),Ordering::AcqRel);}
        let packet=Packet { data,shared:self.shared.clone(),video:h.channel==VIDEO };
        match self.tx.try_send(Command::Send(packet)) {
            Ok(())=>Ok(true),
            Err(_)=>{
                if h.channel==VIDEO {self.shared.dropped.fetch_add(1,Ordering::Relaxed);Ok(false)}
                else {self.shared.error("outgoing command queue overflow".into());self.stop();Err("outgoing command queue overflow".into())}
            }
        }
    }
    pub fn receive(&self)->Option<Vec<u8>> {
        let mut q=self.shared.incoming.lock().unwrap_or_else(|p|p.into_inner());
        let b=q.messages.pop_front()?;q.bytes-=b.len();Some(b)
    }
    fn stop(&self) { if let Some(tx)=self.close.lock().unwrap_or_else(|p|p.into_inner()).take(){let _=tx.send(());} }
}
impl Drop for Session { fn drop(&mut self) { self.stop(); if let Some(t)=self.thread.take(){let _=t.join();} } }
async fn reliable_writer(connection:Connection, channel:u8, mut rx:mpsc::Receiver<Packet>) -> Result<()> {
    let mut stream=connection.open_uni().await.map_err(|e|e.to_string())?;
    stream.set_priority(wire::priority(channel)).map_err(|e|e.to_string())?;
    while let Some(packet)=rx.recv().await {
        timeout(Duration::from_secs(5),stream.write_all(&packet.data)).await.map_err(|_|"reliable send deadline")?.map_err(|e|e.to_string())?;
    }
    let _=stream.finish();Ok(())
}
async fn video_writer(connection:Connection, packet:Packet) {
    let s=packet.shared.clone();
    let sent=async {
        let mut stream=connection.open_uni().await.map_err(|_|())?;
        let _=stream.set_priority(wire::priority(VIDEO));
        let transfer=async { stream.write_all(&packet.data).await.map_err(|_|())?;stream.finish().map_err(|_|())?;stream.stopped().await.map_err(|_|())?;Ok::<(),()>(()) };
        if !matches!(timeout(FRAME_TTL,transfer).await, Ok(Ok(()))) {let _=stream.reset(VarInt::from_u32(0x100));return Err(());}
        Ok(())
    }.await;
    if sent.is_err(){s.dropped.fetch_add(1,Ordering::Relaxed);}
}
async fn receiver(connection:Connection, shared:Arc<Shared>) -> Result<()> {
    let mut workers=JoinSet::new();
    let reliable=Arc::new(Semaphore::new(5));
    let seen=Arc::new(Mutex::new([false;8]));
    loop {
        tokio::select! {
            result=workers.join_next(), if !workers.is_empty()=>{
                match result {Some(Ok(Ok(())))=>{},Some(Ok(Err(e)))=>return Err(e),Some(Err(e))=>return Err(e.to_string()),None=>{}}
            }
            accepted=connection.accept_uni(), if workers.len()<24=>{
                let stream=accepted.map_err(|e|e.to_string())?;
                workers.spawn(receive_stream(stream,shared.clone(),seen.clone(),reliable.clone()));
            }
        }
    }
}
async fn receive_stream(mut stream:quinn::RecvStream,shared:Arc<Shared>,seen:Arc<Mutex<[bool;8]>>,reliable:Arc<Semaphore>) -> Result<()> {
    let mut first=true;let mut channel=None;let mut _permit=None;
    loop {
        let mut header=[0u8;HEADER];
        // A reset can intentionally cancel a stale video, including its partial header.
        let result=if first {match timeout(Duration::from_secs(5),stream.read_exact(&mut header)).await {Ok(r)=>r,Err(_)=>{let _=stream.stop(VarInt::from_u32(0x100));return Ok(());}}} else {stream.read_exact(&mut header).await};
        if result.is_err(){return Ok(());}
        let h=wire::header(&header).map_err(str::to_owned)?;
        if first {
            channel=Some(h.channel);first=false;
            if h.channel!=VIDEO {
                let mut used=seen.lock().map_err(|_|"channel lock poisoned")?;
                if used[h.channel as usize] {return Err("duplicate reliable stream".into());}
                used[h.channel as usize]=true;
                _permit=Some(reliable.clone().try_acquire_owned().map_err(|_|"too many reliable streams")?);
            }
        }
        if channel!=Some(h.channel){return Err("channel changed in a reliable stream".into());}
        let _assembly=match shared.assembly.clone().try_acquire_many_owned((HEADER+h.size) as u32) {Ok(v)=>v,Err(_)=>{let _=stream.stop(VarInt::from_u32(0x100));if h.channel==VIDEO {shared.dropped.fetch_add(1,Ordering::Relaxed);return Ok(());}return Err("reliable assembly budget exceeded".into());}};
        let mut data=Vec::with_capacity(HEADER+h.size);data.extend_from_slice(&header);data.resize(HEADER+h.size,0);
        let budget=if h.channel==VIDEO{Duration::from_millis(350)}else{Duration::from_secs(5)};
        match timeout(budget,stream.read_exact(&mut data[HEADER..])).await {
            Ok(Ok(()))=>{},
            _ if h.channel==VIDEO=>{let _=stream.stop(VarInt::from_u32(0x100));return Ok(());},
            _=>return Err("truncated or stalled reliable payload".into()),
        }
        shared.incoming(data)?;
        if h.channel==VIDEO {
            // Exactly one complete frame per uni-stream. Reject trailing data.
            let mut extra=[0u8;1];
            match timeout(Duration::from_millis(100),stream.read(&mut extra)).await {
                Ok(Ok(None))=>return Ok(()),
                Ok(Ok(Some(_)))=>return Err("multiple frames in a video stream".into()),
                _=>{let _=stream.stop(VarInt::from_u32(0x100));return Ok(());}
            }
        }
    }
}
async fn run(socket:UdpSocket,t:Tls,server:bool,peer_port:u16,mut rx:mpsc::Receiver<Command>,mut close:oneshot::Receiver<()>,shared:Arc<Shared>)->Result<()> {
    let mut endpoint=Endpoint::new(quinn::EndpointConfig::default(),t.server,socket,Arc::new(quinn::TokioRuntime)).map_err(|e|e.to_string())?;
    endpoint.set_default_client_config(t.client);
    let connect=async {
        if server {
            endpoint.accept().await.ok_or("listener closed")?.await.map_err(|e|e.to_string())
        } else {
            endpoint.connect(SocketAddr::from((Ipv4Addr::LOCALHOST,peer_port)),"remotedesk.invalid").map_err(|e|e.to_string())?.await.map_err(|e|e.to_string())
        }
    };
    let connection=tokio::select!{_=&mut close=>return Ok(()),r=timeout(Duration::from_secs(15),connect)=>r.map_err(|_|"QUIC handshake timeout")??};
    shared.state.store(1,Ordering::Release);
    let mut channels=HashMap::new();let mut reliable_tasks=JoinSet::new();
    for channel in [0u8,1,4,5,7] {let (tx,rx)=mpsc::channel(64);channels.insert(channel,tx);reliable_tasks.spawn(reliable_writer(connection.clone(),channel,rx));}
    let mut receive=tokio::spawn(receiver(connection.clone(),shared.clone()));
    let mut videos:VecDeque<(u64,tokio::task::JoinHandle<()>)>=VecDeque::new();let mut tick=interval(Duration::from_millis(10));
    let result=loop {
        tokio::select! {
            _=&mut close=>break Ok(()),
            reason=connection.closed()=>break Err(reason.to_string()),
            r=&mut receive=>break match r {Ok(r)=>r,Err(e)=>Err(e.to_string())},
            r=reliable_tasks.join_next()=>break match r {Some(Ok(Err(e)))=>Err(e),Some(Err(e))=>Err(e.to_string()),_=>Err("reliable channel closed".into())},
            _=tick.tick()=>{
                shared.rtt.store(connection.rtt().as_micros() as u64,Ordering::Relaxed);
                videos.retain(|(_,task)|!task.is_finished());
            }
            cmd=rx.recv()=>match cmd {
                Some(Command::Ack(id))=>{if let Some(i)=videos.iter().position(|(n,_)|*n==id){if let Some((_,task))=videos.remove(i){task.abort();}}},
                Some(Command::Send(packet))=>{
                    let h=wire::frame(&packet.data).map_err(str::to_owned)?;
                    if h.channel==VIDEO {
                        videos.retain(|(_,task)|!task.is_finished());
                        if videos.len()>=3 {if let Some((_,task))=videos.pop_front(){task.abort();shared.dropped.fetch_add(1,Ordering::Relaxed);}}
                        videos.push_back((h.id,tokio::spawn(video_writer(connection.clone(),packet))));
                    } else if channels.get(&h.channel).ok_or("unknown channel")?.try_send(packet).is_err(){break Err("reliable stream backlog exceeded".into());}
                },
                None=>break Ok(()),
            }
        }
    };
    for (_,task) in videos {task.abort();}receive.abort();reliable_tasks.abort_all();
    connection.close(VarInt::from_u32(0),b"session closed");endpoint.close(VarInt::from_u32(0),b"session closed");
    let _=timeout(Duration::from_millis(300),endpoint.wait_idle()).await;
    result
}

/// All pointers passed by the trusted C++ host must be valid for their indicated
/// lengths. Calls on a handle may be serialized or concurrent, except destroy:
/// destruction must not overlap any other call. No callback crosses the ABI.
#[repr(C)]
pub struct RdqConfig { pub server:u8,pub peer_port:u16,pub certificate:*const u8,pub certificate_len:usize,pub private_key:*const u8,pub private_key_len:usize,pub peer_key:[u8;32] }
unsafe fn error_copy(error:*mut c_char,cap:usize,message:&str) {if !error.is_null()&&cap>0{let n=message.len().min(cap-1);ptr::copy_nonoverlapping(message.as_ptr(),error.cast(),n);*error.add(n)=0;}}
#[no_mangle]
pub unsafe extern "C" fn rdq_create(c:*const RdqConfig,error:*mut c_char,cap:usize)->*mut Session {
    if c.is_null(){error_copy(error,cap,"null QUIC configuration");return ptr::null_mut();}
    let c=&*c;
    if c.certificate.is_null()||c.private_key.is_null()||!(32..=16384).contains(&c.certificate_len)||!(32..=4096).contains(&c.private_key_len)||c.server>1 {error_copy(error,cap,"invalid QUIC configuration bounds");return ptr::null_mut();}
    let config=Config {server:c.server==1,certificate_pem:slice::from_raw_parts(c.certificate,c.certificate_len).to_vec(),private_key_pem:slice::from_raw_parts(c.private_key,c.private_key_len).to_vec(),peer_key:c.peer_key,peer_port:c.peer_port};
    match Session::new(config){Ok(s)=>Box::into_raw(Box::new(s)),Err(e)=>{error_copy(error,cap,&e);ptr::null_mut()}}
}
#[no_mangle] pub unsafe extern "C" fn rdq_destroy(s:*mut Session){if !s.is_null(){drop(Box::from_raw(s));}}
#[no_mangle] pub unsafe extern "C" fn rdq_port(s:*const Session)->u16{if s.is_null(){0}else{(*s).port()}}
#[no_mangle] pub unsafe extern "C" fn rdq_state(s:*const Session)->i32{if s.is_null(){-1}else{(*s).state()}}
#[no_mangle] pub unsafe extern "C" fn rdq_error(s:*const Session,out:*mut c_char,cap:usize){if !s.is_null(){error_copy(out,cap,&(*s).shared.error.lock().unwrap_or_else(|p|p.into_inner()));}}
#[no_mangle] pub unsafe extern "C" fn rdq_send(s:*const Session,data:*const u8,len:usize)->i32 {
    if s.is_null()||data.is_null()||!(HEADER..=wire::MAX_VIDEO+HEADER).contains(&len){return -1;}
    match (*s).send(slice::from_raw_parts(data,len).to_vec()){Ok(true)=>1,Ok(false)=>0,Err(e)=>{(*s).shared.error(e);-1}}
}
#[no_mangle] pub unsafe extern "C" fn rdq_receive(s:*const Session,out:*mut u8,cap:usize)->isize {
    if s.is_null()||out.is_null(){return -1;}
    let mut q=(*s).shared.incoming.lock().unwrap_or_else(|p|p.into_inner());
    let n=match q.messages.front(){None=>return 0,Some(b)=>b.len()};if cap<n{return -2;}
    let b=q.messages.pop_front().unwrap();q.bytes-=n;ptr::copy_nonoverlapping(b.as_ptr(),out,n);n as isize
}
#[no_mangle] pub unsafe extern "C" fn rdq_acknowledge(s:*const Session,id:u64){if !s.is_null(){let _=(*s).tx.try_send(Command::Ack(id));}}
#[no_mangle] pub unsafe extern "C" fn rdq_queued_video(s:*const Session)->usize{if s.is_null(){0}else{(*s).shared.video.load(Ordering::Relaxed)}}
#[no_mangle] pub unsafe extern "C" fn rdq_dropped(s:*const Session)->u64{if s.is_null(){0}else{(*s).shared.dropped.load(Ordering::Relaxed)}}
#[no_mangle] pub unsafe extern "C" fn rdq_rtt_us(s:*const Session)->u64{if s.is_null(){0}else{(*s).shared.rtt.load(Ordering::Relaxed)}}

#[cfg(test)]
mod tests {
    use super::*;
    fn identity()->(Vec<u8>,Vec<u8>,[u8;32]) {
        let key=rcgen::KeyPair::generate_for(&rcgen::PKCS_ED25519).unwrap();
        let cert=rcgen::CertificateParams::new(vec!["remotedesk.invalid".into()]).unwrap().self_signed(&key).unwrap();
        (cert.pem().into_bytes(),key.serialize_pem().into_bytes(),key.public_key_raw().try_into().unwrap())
    }
    fn cfg(i:&(Vec<u8>,Vec<u8>,[u8;32]),peer:[u8;32],server:bool,port:u16)->Config { Config{server,certificate_pem:i.0.clone(),private_key_pem:i.1.clone(),peer_key:peer,peer_port:port} }
    fn wait(f:impl Fn()->bool){let start=std::time::Instant::now();while !f(){assert!(start.elapsed()<Duration::from_secs(8),"QUIC test timed out");thread::sleep(Duration::from_millis(5));}}
    fn packet(channel:u8,id:u64,n:usize)->Vec<u8>{let mut b=vec![0;HEADER+n];b[..4].copy_from_slice(b"RDV2");b[4]=channel;b[5]=16;b[8..16].copy_from_slice(&id.to_be_bytes());b[24..28].copy_from_slice(&(n as u32).to_be_bytes());for (i,x) in b[HEADER..].iter_mut().enumerate(){*x=(i%251) as u8;}b}
    #[test] fn pinned_udp_video_and_reliable_input(){
        let a=identity();let b=identity();let server=Session::new(cfg(&a,b.2,true,0)).unwrap();let client=Session::new(cfg(&b,a.2,false,server.port())).unwrap();
        wait(||server.state()==1&&client.state()==1);
        let frame=packet(3,8,256*1024);assert!(server.send(frame.clone()).unwrap());
        let input=packet(1,9,32);assert!(client.send(input.clone()).unwrap());
        wait(||!client.shared.incoming.lock().unwrap().messages.is_empty());assert_eq!(client.receive(),Some(frame));
        wait(||!server.shared.incoming.lock().unwrap().messages.is_empty());assert_eq!(server.receive(),Some(input));
    }
    #[test] fn rejects_untrusted_device(){let a=identity();let b=identity();let bad=identity();let server=Session::new(cfg(&a,bad.2,true,0)).unwrap();let client=Session::new(cfg(&b,a.2,false,server.port())).unwrap();wait(||server.state()<0||client.state()<0);}
}
