'use strict';
let sshTerminal=null,sshTerminalFit=null,sshTerminalSocket=null,sshTerminalHost=null,sshTerminalObserver=null;
function fitSSHTerminal(){if(sshTerminalFit&&view==='ssh'&&$('sshTerminalPanel').open)sshTerminalFit.fit()}
function disposeSSHTerminal(){
	ipPrivacy.attachTerminal(null);
 const socket=sshTerminalSocket;sshTerminalSocket=null;if(socket)socket.close();
 sshTerminalObserver?.disconnect();sshTerminalObserver=null;
 sshTerminal?.dispose();sshTerminal=null;sshTerminalFit=null;
}
function closeSSHTerminal(){disposeSSHTerminal();if($('sshTerminalPanel').open)$('sshTerminalPanel').close()}
$('sshTerminalPanel').addEventListener('close',disposeSSHTerminal);
function openSSHTerminal(host){
 if(sshTerminalSocket&&sshTerminalSocket.readyState<2&&!confirm('打开此终端会断开当前终端，继续？'))return;
 disposeSSHTerminal();sshTerminalHost=host.id;
 showSSHDialog('sshTerminalPanel',host.id,'terminal');$('sshTerminalTitle').textContent=`远程终端 · ${host.name} · ${host.username}@${sshAddress(host)}`;
 $('sshTerminalStatus').textContent='正在连接…';$('sshTerminalReconnect').disabled=true;
 const term=new Terminal({cursorBlink:true,fontSize:14,fontFamily:'Consolas, "Courier New", monospace',scrollback:3000,theme:{background:'#111b2b',foreground:'#e2e8f0'}});
 const fit=new FitAddon.FitAddon();term.loadAddon(fit);term.open($('sshTerminalScreen'));fit.fit();
 term.attachCustomKeyEventHandler(event=>{
  if(!event.ctrlKey||event.altKey||event.metaKey)return true;
  const key=event.key.toLowerCase();
  if(key==='v')return false;
  if(key==='c'&&term.hasSelection()){
   if(event.type==='keydown'){
    event.preventDefault();
    navigator.clipboard.writeText(term.getSelection()).catch(()=>{$('sshTerminalStatus').textContent='复制失败，请检查浏览器剪贴板权限'});
   }
   return false;
  }
  return true;
 });
 sshTerminal=term;sshTerminalFit=fit;ipPrivacy.attachTerminal(term);
 term.onRender(ipPrivacy.renderTerminal);term.onScroll(ipPrivacy.renderTerminal);
 const ws=new WebSocket((location.protocol==='https:'?'wss://':'ws://')+location.host+'/api/ssh/terminal');ws.binaryType='arraybuffer';sshTerminalSocket=ws;
 let ready=false,error='';
 const send=m=>{if(ws.readyState===WebSocket.OPEN)ws.send(JSON.stringify(m))};
 ws.onopen=()=>send({type:'open',csrf,host_id:host.id,cols:term.cols,rows:term.rows});
 ws.onmessage=event=>{
  if(ws!==sshTerminalSocket)return;
  if(event.data instanceof ArrayBuffer){term.write(new Uint8Array(event.data));return}
  const m=JSON.parse(event.data);
  if(m.type==='ready'){ready=true;$('sshTerminalStatus').textContent='已连接 · Ctrl+C 中断命令';term.textarea?.focus({preventScroll:true});send({type:'resize',cols:term.cols,rows:term.rows})}
  if(m.type==='error'){error=m.message;$('sshTerminalStatus').textContent=error}
  if(m.type==='closed')$('sshTerminalStatus').textContent=m.message;
 };
 ws.onclose=()=>{if(ws!==sshTerminalSocket)return;ready=false;$('sshTerminalReconnect').disabled=false;$('sshTerminalStatus').textContent=error||'连接已断开';term.writeln('\r\n[终端连接已关闭]');loadSSHHosts().catch(()=>{})};
 ws.onerror=()=>{error='终端连接失败，请检查登录状态和 SSH 连接信息'};
 term.onData(data=>{if(!ready)return;while(data){let end=Math.min(4096,data.length);if(end<data.length&&/[\uD800-\uDBFF]/.test(data[end-1]))end--;send({type:'input',data:data.slice(0,end)});data=data.slice(end)}});
 term.onResize(size=>{if(ready)send({type:'resize',...size})});
 sshTerminalObserver=new ResizeObserver(fitSSHTerminal);sshTerminalObserver.observe($('sshTerminalScreen'));
}
$('sshTerminalClose').onclick=closeSSHTerminal;
$('sshTerminalPanel').oncancel=event=>event.preventDefault();
$('sshTerminalReconnect').onclick=()=>{const host=sshHosts.find(h=>h.id===sshTerminalHost);if(host?.has_secret)openSSHTerminal(host);else toast('请先补充主机凭据')};
window.addEventListener('pagehide',closeSSHTerminal);
