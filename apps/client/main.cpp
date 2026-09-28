#include "mainwindow.hpp"
#include <QApplication>
#include <QDir>
#include <QFileInfo>
#include <QMessageBox>
#include <QStandardPaths>
#include <QLockFile>
#include <windows.h>
int main(int argc,char** argv){
 SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
 QApplication app(argc,argv);app.setApplicationName("RemoteDesk");app.setOrganizationName("RemoteDesk");app.setApplicationVersion("0.2.0-engineering");
 const auto base=QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);QDir().mkpath(base);
 QLockFile lock(base+"/client.lock");lock.setStaleLockTime(0);
 if(!lock.tryLock()){QMessageBox::information(nullptr,QStringLiteral("RemoteDesk"),QStringLiteral("当前用户的 RemoteDesk 客户端已经打开。"));return 0;}
 MainWindow window;window.show();return app.exec();
}
