#pragma once
#include <QObject>
#include <QWidget>
#include <QStringList>
#include <QTimer>
#include <atomic>
#include <condition_variable>
#include <mutex>
#include <deque>
#include <QByteArray>
#include <thread>
#include <windows.h>
class TerminalBridge final:public QObject {
 Q_OBJECT
 HANDLE input_=nullptr,output_=nullptr,process_=nullptr,job_=nullptr;
 HPCON console_=nullptr;
 std::thread reader_,writer_;
 std::mutex ioMutex_;std::condition_variable wake_;std::deque<QByteArray> pendingInput_;QByteArray pendingOutput_;std::size_t inputBytes_=0;bool outputPending_=false;
 QTimer flush_;
 std::atomic<bool> stopping_{true};
 QTimer poll_;
 int exitCode_=0;
public:
 explicit TerminalBridge(QObject* parent=nullptr);
 ~TerminalBridge() override;
 bool start(const QString& executable,const QStringList& args);
 void stop();
public slots:
 void write(const QString& data);
 void resize(int columns,int rows);
 void ready();
 void acknowledged();
signals:
 void output(const QString& base64);
 void error(const QString& message);
 void finished(int code);
 void webReady();
};
QWidget* createTerminal(const QString& state,const QString& peer,const QString& user,bool relay,QWidget* parent=nullptr);
