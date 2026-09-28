import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import './style.css';
type Bridge = {
  write(data:string):void; resize(cols:number,rows:number):void; ready():void; acknowledged():void;
  output:{connect(fn:(base64:string)=>void):void};
  error:{connect(fn:(message:string)=>void):void};
  finished:{connect(fn:(code:number)=>void):void};
};
declare global { interface Window { qt?:{webChannelTransport:unknown}; QWebChannel:new(t:unknown,cb:(c:{objects:{terminal:Bridge}})=>void)=>unknown; } }
const term = new Terminal({cursorBlink:true,convertEol:false,scrollback:5000,fontSize:14,fontFamily:'Cascadia Mono, Consolas, monospace',allowProposedApi:false});
const fit = new FitAddon();term.loadAddon(fit);term.open(document.getElementById('terminal')!);
fit.fit();term.focus();
if (!window.qt || !window.QWebChannel) {
  term.write('This terminal must be opened inside the RemoteDesk client.\r\n');
} else {
  new window.QWebChannel(window.qt.webChannelTransport, channel => {
    const b=channel.objects.terminal;
    b.output.connect(data=>{const raw=atob(data);const bytes=new Uint8Array(raw.length);for(let i=0;i<raw.length;i++)bytes[i]=raw.charCodeAt(i);term.write(bytes,()=>b.acknowledged());});
    b.error.connect(message=>term.write('\r\n[RemoteDesk] '+message.replace(/[\x00-\x1f\x7f]/g,' ')+'\r\n'));
    b.finished.connect(code=>term.write(`\r\n[Session closed: ${code}]\r\n`));
    term.onData(data=>b.write(data));
    const resize=()=>{fit.fit();b.resize(term.cols,term.rows);};
    new ResizeObserver(resize).observe(document.body);resize();b.ready();
  });
}
