#pragma once
#include <QMainWindow>
#include <QProcess>
#include <QJsonDocument>
#include <functional>
class QLineEdit;class QLabel;class QPushButton;class QTableWidget;class QPlainTextEdit;class QTabWidget;class QCheckBox;class QComboBox;class QSpinBox;class QProgressBar;
class MainWindow final:public QMainWindow {
 Q_OBJECT
 QLineEdit *state_,*peer_,*remoteDir_;
 QLabel *identity_,*hostStatus_,*videoStats_,*fileStats_;
 QTableWidget *devices_,*files_;
 QPlainTextEdit* log_;
 QTabWidget* tabs_;
 QWidget* video_;
 QComboBox *resolution_,*fps_;
 QSpinBox *monitor_,*bitrate_;
 QCheckBox *control_,*clipboard_,*audio_,*relay_;
 QProgressBar* fileProgress_;
 QProcess *host_=nullptr,*desktop_=nullptr,*transfer_=nullptr;
 QByteArray desktopData_,transferData_,transferLines_;
 bool refreshing_=false;
 QString agent()const;
 QString state()const;
 QString target()const;
 void appendLog(const QString& text);
 void command(QStringList args,std::function<void(bool,QByteArray)> done={});
 void identity();void onboarding();void refreshPeers();void trustPeer();void grantPeer();void configureHost();void toggleHost();
 void connectDesktop();void stopDesktop();void ssh();void listFiles();void upload(bool folder);void download();void fileOperation(const QString& op,const QString& local,const QString& remote,bool overwrite);
 void parseMedia(const QByteArray& data);void cancelFile();
protected:
 void closeEvent(QCloseEvent*)override;
public:
 MainWindow();~MainWindow()override;
};
