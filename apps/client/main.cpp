#include "mainwindow.hpp"
#include <QApplication>
#include <QDir>
#include <QMessageBox>
#include <QStandardPaths>
#include <QLockFile>
#include <QTimer>
#include <windows.h>
int main(int argc,char** argv){
 SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
 QApplication app(argc,argv);app.setApplicationName("RemoteDesk");app.setOrganizationName("RemoteDesk");app.setApplicationVersion("0.4.0-preview");
 const auto args=app.arguments();const int test=args.indexOf("--ui-self-test");
 if(test>=0){if(test+1>=args.size())return 2;app.setOrganizationName("RemoteDesk-CI-Ephemeral");MainWindow window;window.show();QTimer::singleShot(700,&window,[&app,&window,args,test]{app.exit(window.uiSelfTest(args[test+1])?0:3);});return app.exec();}
 const auto base=QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);QDir().mkpath(base);
 QLockFile lock(base+"/client.lock");lock.setStaleLockTime(0);
 if(!lock.tryLock()){QMessageBox::information(nullptr,QStringLiteral("RemoteDesk"),QStringLiteral("当前用户的 RemoteDesk 客户端已经打开。"));return 0;}
 MainWindow window;window.show();return app.exec();
}
