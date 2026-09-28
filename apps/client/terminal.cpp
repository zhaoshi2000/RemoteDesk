#include "terminal.hpp"
#include <QCoreApplication>
#include <QDir>
#include <QFileInfo>
#include <QLabel>
#include <QPushButton>
#include <QVBoxLayout>
#include <QWebChannel>
#include <QWebEnginePage>
#include <QWebEngineProfile>
#include <QWebEngineSettings>
#include <QWebEngineUrlRequestInterceptor>
#include <QWebEngineUrlRequestInfo>
#include <QWebEngineView>
#include <algorithm>
#include <vector>
namespace {
QString winError(const char* action){return QString::fromLatin1(action)+QStringLiteral(" (Windows error %1)").arg(GetLastError());}
std::wstring quote(const QString& text){
 const auto s=text.toStdWString();std::wstring r=L"\"";std::size_t slashes=0;
 for(auto c:s){if(c==L'\\'){++slashes;continue;}if(c==L'\"'){r.append(slashes*2+1,L'\\');r+=c;slashes=0;continue;}r.append(slashes,L'\\');slashes=0;r+=c;}r.append(slashes*2,L'\\');r+=L'\"';return r;
}
class LocalOnly final:public QWebEngineUrlRequestInterceptor {
 QString root_;
public:
 LocalOnly(QString root,QObject* parent):QWebEngineUrlRequestInterceptor(parent),root_(QDir(root).canonicalPath()+"/"){}
 void interceptRequest(QWebEngineUrlRequestInfo& i)override{
  const auto u=i.requestUrl();
  if(u.scheme()=="qrc"&&u.path()=="/qtwebchannel/qwebchannel.js")return;
  if(u==QUrl("about:blank")||u.scheme()=="data")return;
  if(u.isLocalFile()){const auto path=QFileInfo(u.toLocalFile()).canonicalFilePath();if(path.startsWith(root_,Qt::CaseInsensitive))return;}
  i.block(true);
 }
};
class TerminalPage final:public QWebEnginePage {
 QString index_;
public:
 TerminalPage(QWebEngineProfile* p,const QString& index,QObject* parent):QWebEnginePage(p,parent),index_(QFileInfo(index).canonicalFilePath()){}
 bool acceptNavigationRequest(const QUrl& url,NavigationType,bool)override{return url.isLocalFile()&&QFileInfo(url.toLocalFile()).canonicalFilePath()==index_;}
 QWebEnginePage* createWindow(WebWindowType)override{return nullptr;}
};
}
TerminalBridge::TerminalBridge(QObject* parent):QObject(parent){
 flush_.setInterval(16);connect(&flush_,&QTimer::timeout,this,[this]{QByteArray bytes;{std::lock_guard lock(ioMutex_);if(outputPending_||pendingOutput_.isEmpty())return;bytes=pendingOutput_.left(32768);pendingOutput_.remove(0,bytes.size());outputPending_=true;}emit output(QString::fromLatin1(bytes.toBase64()));});
 poll_.setInterval(100);connect(&poll_,&QTimer::timeout,this,[this]{if(!process_)return;DWORD code=0;if(GetExitCodeProcess(process_,&code)&&code!=STILL_ACTIVE){exitCode_=int(code);stop();emit finished(exitCode_);}});
}
TerminalBridge::~TerminalBridge(){stop();}
bool TerminalBridge::start(const QString& exe,const QStringList& args){
 if(process_)return false;
 if(!QFileInfo(exe).isFile()){emit error(QStringLiteral("remote-agent.exe 不存在。"));return false;}
 HANDLE inputRead=nullptr,outputWrite=nullptr;
 if(!CreatePipe(&inputRead,&input_,nullptr,0)||!CreatePipe(&output_,&outputWrite,nullptr,0)){if(inputRead)CloseHandle(inputRead);if(outputWrite)CloseHandle(outputWrite);emit error(winError("CreatePipe"));stop();return false;}
 const auto hr=CreatePseudoConsole(COORD{120,32},inputRead,outputWrite,0,&console_);
 CloseHandle(inputRead);CloseHandle(outputWrite);
 if(FAILED(hr)){emit error(QStringLiteral("CreatePseudoConsole failed: 0x%1; requires Windows 10 1809+").arg(quint32(hr),8,16,QChar('0')));stop();return false;}
 SIZE_T size=0;InitializeProcThreadAttributeList(nullptr,1,0,&size);std::vector<unsigned char> storage(size);
 auto* attrs=reinterpret_cast<LPPROC_THREAD_ATTRIBUTE_LIST>(storage.data());
 if(!InitializeProcThreadAttributeList(attrs,1,0,&size)){emit error(winError("InitializeProcThreadAttributeList"));stop();return false;}
 if(!UpdateProcThreadAttribute(attrs,0,PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,console_,sizeof(console_),nullptr,nullptr)){DeleteProcThreadAttributeList(attrs);emit error(winError("Pseudoconsole attribute"));stop();return false;}
 STARTUPINFOEXW si{};si.StartupInfo.cb=sizeof(si);si.lpAttributeList=attrs;PROCESS_INFORMATION pi{};
 std::wstring command=quote(exe);for(const auto& a:args){command+=L' ';command+=quote(a);}std::vector<wchar_t> mutableCommand(command.begin(),command.end());mutableCommand.push_back(0);
 const auto path=QDir::toNativeSeparators(QFileInfo(exe).absoluteFilePath()).toStdWString();
 const BOOL ok=CreateProcessW(path.c_str(),mutableCommand.data(),nullptr,nullptr,FALSE,EXTENDED_STARTUPINFO_PRESENT|CREATE_UNICODE_ENVIRONMENT|CREATE_SUSPENDED,nullptr,nullptr,&si.StartupInfo,&pi);
 DeleteProcThreadAttributeList(attrs);
 if(!ok){emit error(winError("CreateProcess"));stop();return false;}
 process_=pi.hProcess;job_=CreateJobObjectW(nullptr,nullptr);JOBOBJECT_EXTENDED_LIMIT_INFORMATION info{};info.BasicLimitInformation.LimitFlags=JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
 if(!job_||!SetInformationJobObject(job_,JobObjectExtendedLimitInformation,&info,sizeof(info))||!AssignProcessToJobObject(job_,process_)){TerminateProcess(process_,1);CloseHandle(pi.hThread);emit error(winError("SSH process containment"));stop();return false;}
 stopping_.store(false);
 reader_=std::thread([this]{char buffer[8192];DWORD n=0;for(;;){if(!ReadFile(output_,buffer,sizeof(buffer),&n,nullptr)||n==0)break;if(stopping_.load())continue;bool overflow=false;{std::lock_guard lock(ioMutex_);overflow=pendingOutput_.size()+int(n)>2*1024*1024;if(!overflow)pendingOutput_.append(buffer,int(n));}if(overflow){QMetaObject::invokeMethod(this,[this]{emit error(QStringLiteral("Terminal output backlog exceeded 2 MiB; connection closed."));stop();},Qt::QueuedConnection);stopping_.store(true);continue;}}});
 writer_=std::thread([this]{for(;;){QByteArray bytes;{std::unique_lock lock(ioMutex_);wake_.wait(lock,[this]{return stopping_.load()||!pendingInput_.empty();});if(stopping_.load())return;bytes=std::move(pendingInput_.front());pendingInput_.pop_front();inputBytes_-=std::size_t(bytes.size());}DWORD n=0;if(!WriteFile(input_,bytes.constData(),DWORD(bytes.size()),&n,nullptr)||n!=DWORD(bytes.size())){if(!stopping_.load())QMetaObject::invokeMethod(this,[this]{emit error(QStringLiteral("SSH terminal input pipe failed."));stop();},Qt::QueuedConnection);return;}}});
 ResumeThread(pi.hThread);CloseHandle(pi.hThread);poll_.start();flush_.start();return true;
}
void TerminalBridge::write(const QString& data){
 if(!input_||stopping_.load()||data.size()>65536)return;const auto bytes=data.toUtf8();bool overflow=false;{std::lock_guard lock(ioMutex_);overflow=inputBytes_+std::size_t(bytes.size())>256*1024;if(!overflow){inputBytes_+=std::size_t(bytes.size());pendingInput_.push_back(bytes);}}if(overflow){emit error(QStringLiteral("Terminal input backlog exceeded; connection closed."));stop();return;}wake_.notify_one();
}
void TerminalBridge::acknowledged(){std::lock_guard lock(ioMutex_);outputPending_=false;}

void TerminalBridge::resize(int columns,int rows){if(console_&&columns>=10&&columns<=500&&rows>=2&&rows<=200)ResizePseudoConsole(console_,COORD{SHORT(columns),SHORT(rows)});}
void TerminalBridge::ready(){emit webReady();}
void TerminalBridge::stop(){
 poll_.stop();flush_.stop();stopping_.store(true);wake_.notify_all();
 if(job_){TerminateJobObject(job_,0);CloseHandle(job_);job_=nullptr;}
 if(writer_.joinable()){CancelSynchronousIo(static_cast<HANDLE>(writer_.native_handle()));writer_.join();}
 if(input_){CloseHandle(input_);input_=nullptr;}
 // Do not stop the drain thread before ClosePseudoConsole emits its final frame.
 // It intentionally discards teardown output rather than blocking on the GUI.
 if(console_){if(!reader_.joinable()&&output_)reader_=std::thread([this]{char b[8192];DWORD n;while(ReadFile(output_,b,sizeof(b),&n,nullptr)&&n){};});ClosePseudoConsole(console_);console_=nullptr;}
 if(reader_.joinable()){CancelSynchronousIo(static_cast<HANDLE>(reader_.native_handle()));reader_.join();}
 if(output_){CloseHandle(output_);output_=nullptr;}
 if(process_){CloseHandle(process_);process_=nullptr;}
 {std::lock_guard lock(ioMutex_);pendingInput_.clear();inputBytes_=0;pendingOutput_.clear();outputPending_=false;}
}

QWidget* createTerminal(const QString& state,const QString& peer,const QString& user,bool relay,QWidget* parent){
 auto* pane=new QWidget(parent);auto* layout=new QVBoxLayout(pane);auto* note=new QLabel(QStringLiteral("SSH：%1 · %2。首次连接请核对 OpenSSH 主机指纹；设备授权不替代 SSH 用户认证。").arg(peer,user));note->setWordWrap(true);layout->addWidget(note);
 auto* view=new QWebEngineView(pane);view->setAcceptDrops(false);layout->addWidget(view,1);
 auto* stop=new QPushButton(QStringLiteral("断开 SSH"),pane);layout->addWidget(stop);
 const auto root=QCoreApplication::applicationDirPath()+"/terminal",index=root+"/index.html";
 if(!QFileInfo::exists(index)){note->setText(QStringLiteral("终端资源缺失：请先构建 apps/terminal，并把 dist 放入程序旁 terminal 目录。"));stop->setEnabled(false);return pane;}
 auto* profile=new QWebEngineProfile(pane); // off-the-record, no persisted cookies/cache
 profile->setUrlRequestInterceptor(new LocalOnly(root,profile));
 auto* page=new TerminalPage(profile,index,view);view->setPage(page);
 page->settings()->setAttribute(QWebEngineSettings::LocalContentCanAccessRemoteUrls,false);
 page->settings()->setAttribute(QWebEngineSettings::JavascriptCanAccessClipboard,false);
 page->settings()->setAttribute(QWebEngineSettings::JavascriptCanOpenWindows,false);
 auto* bridge=new TerminalBridge(pane);auto* channel=new QWebChannel(page);channel->registerObject("terminal",bridge);page->setWebChannel(channel);
 QObject::connect(bridge,&TerminalBridge::webReady,bridge,[bridge,state,peer,user,relay]{QStringList args{"ssh","--state",state,"--peer",peer,"--user",user};if(relay)args<<"--relay-only";bridge->start(QCoreApplication::applicationDirPath()+"/remote-agent.exe",args);},Qt::SingleShotConnection);
 QObject::connect(stop,&QPushButton::clicked,bridge,&TerminalBridge::stop);
 QObject::connect(bridge,&TerminalBridge::finished,stop,[stop](int){stop->setEnabled(false);});
 view->load(QUrl::fromLocalFile(index));return pane;
}
