'use strict';
let sshHosts=[];
let sshSaving=false,sshEditorTrigger=null,sshEditorHostID='';
let sshCheckController=null;
const sshCollator=new Intl.Collator('zh-CN',{numeric:true,sensitivity:'base'});
const sshDialogOrigins=new Map();
function showSSHDialog(id,hostID='',action='speed'){
 const dialog=$(id);if(dialog.open)return;
 sshDialogOrigins.set(id,{trigger:document.activeElement,hostID,action});dialog.showModal();
}
for(const id of ['sshTestPanel','sshTerminalPanel','sshHistoryPanel','sshSamplesPanel'])$(id).addEventListener('close',()=>{
 const origin=sshDialogOrigins.get(id);if(!origin)return;
 const trigger=origin.trigger?.isConnected?origin.trigger:[...document.querySelectorAll(`[data-ssh-${origin.action}]`)].find(b=>b.getAttribute('data-ssh-'+origin.action)===origin.hostID)||($('sshTestPanel').open?$('sshResultSamples'):$('sshRefresh'));
 trigger.focus({preventScroll:true});
});
async function loadSSHHosts(){
 const selected=$('sshHost').value,source=$('sshSourceHost').value;
 const data=await api('/api/speed/ssh/hosts');sshHosts=(data.items||[]).sort((a,b)=>sshCollator.compare(a.name,b.name));
 $('sshHost').innerHTML='<option value="">请选择主机</option>'+sshHosts.map(h=>`<option value="${esc(h.id)}" ${h.has_secret?'':'disabled'}>${esc(h.name)} · ${esc(h.host)}:${h.port}${h.has_secret?'':' · 待补凭据'}</option>`).join('');
 $('sshHost').value=sshHosts.some(h=>h.id===selected&&h.has_secret)?selected:sshHosts.find(h=>h.has_secret)?.id||'';
 $('sshSourceHost').innerHTML='<option value="">请选择源主机</option>'+sshHosts.filter(h=>h.has_secret).map(h=>`<option value="${esc(h.id)}">${esc(h.name)} · ${esc(h.host)}:${h.port}</option>`).join('');
 $('sshSourceHost').value=sshHosts.some(h=>h.id===source&&h.has_secret)?source:'';
 renderSSHHistoryNodes();
 renderSSHHosts();renderSSHButtons();updateSSHTestTitle();
}
function renderSSHHosts(){
 const query=$('sshSearch').value.trim().toLowerCase(),status=$('sshCredentialFilter').value;
 const hosts=sshHosts.filter(h=>(!status||(status==='ready')===!!h.has_secret)&&`${h.name} ${h.host} ${h.port} ${h.username}`.toLowerCase().includes(query));
 $('sshHostCount').textContent=`${hosts.length} / ${sshHosts.length} 台主机 · ${sshHosts.filter(h=>!h.has_secret).length} 台待补凭据`;
 $('sshHostRows').innerHTML=hosts.length?hosts.map(h=>`<tr><td class="speed-name">${esc(h.name)}</td><td>${esc(h.host)}</td><td>${h.port}</td><td>${esc(h.username)}</td><td>${h.auth==='key'?'私钥':'密码'}</td><td><span class="badge ${h.has_secret?'':'muted'}">${h.has_secret?'已配置':h.auth==='key'?'待补私钥':'待补密码'}</span></td><td class="ssh-row-actions"><button class="secondary" data-ssh-terminal="${esc(h.id)}">终端</button><button class="secondary" data-ssh-speed="${esc(h.id)}">测试</button><button class="ghost" data-ssh-edit="${esc(h.id)}">${h.has_secret?'编辑':'补充凭据'}</button><select data-ssh-more="${esc(h.id)}" aria-label="${esc(h.name)} 更多操作"><option value="">更多…</option><option value="check" ${h.has_secret?'':'disabled'}>检查连接</option><option value="install" ${h.has_secret?'':'disabled'}>安装 iperf3</option><option value="history">测试记录</option><option value="delete">删除主机</option></select></td></tr>`).join(''):'<tr><td colspan="7" class="empty">没有匹配的 SSH 主机</td></tr>';
 renderSSHButtons();
}
function renderSSHButtons(){
 const selected=sshHosts.find(h=>h.id===$('sshHost').value),busy=!!speedCurrent.running,peer=$('sshLink').value==='peer';
 $('sshStart').disabled=!selected?.has_secret||busy||(peer&&(!$('sshSourceHost').value||$('sshSourceHost').value===selected?.id));
 $('sshTestForm').querySelectorAll('input,select').forEach(el=>el.disabled=busy);
 $('sshHostForm').querySelector('button[type="submit"]').disabled=busy||sshSaving;
 $('sshAdd').disabled=busy;
 document.querySelectorAll('[data-ssh-edit],[data-ssh-more]').forEach(b=>b.disabled=busy);
 for(const action of ['speed','terminal'])document.querySelectorAll(`[data-ssh-${action}]`).forEach(b=>b.disabled=(busy&&(action!=='speed'||speedCurrent.current?.node_id!=='ssh:'+b.getAttribute('data-ssh-'+action)))||!sshHosts.find(h=>h.id===b.getAttribute('data-ssh-'+action))?.has_secret);
 $('sshStart').textContent=busy&&speedCurrent.current?.node_id?.startsWith('ssh:')?'任务运行中…':'开始测速';
 const current=speedCurrent.current||{},isSSH=current.node_id?.startsWith('ssh:');
 $('sshTaskView').disabled=!isSSH;$('sshTaskView').textContent=isSSH&&busy?'查看测试进度':'查看测试任务';
 $('sshTask').hidden=!isSSH&&!busy;
 if(busy&&!isSSH)$('sshTaskDetail').textContent='代理测试正在运行，完成后可开始 SSH 测试。';
 if(isSSH)$('sshTaskDetail').textContent=`${current.source_host_name?current.source_host_name+' ↔ ':''}${current.node_name||''} · ${speedResultDirection(current)||'准备连接'} · ${speedCurrent.stopping?'正在停止':speedLabel[current.status]||'准备中'} · ${speedCurrent.done||0}/${speedCurrent.total||0}${current.message?' · '+current.message:''}`;
 $('sshTaskStop').hidden=!isSSH;
 $('sshTaskStop').disabled=!busy||speedCurrent.stopping;
 renderSSHResults();
}
function sshAuthFields(){const key=$('sshHostForm').elements.auth.value==='key';$('sshPasswordField').hidden=key;document.querySelectorAll('.ssh-key').forEach(e=>e.hidden=!key)}
function resetSSHForm(){ $('sshHostForm').reset();$('sshHostForm').elements.id.value='';sshAuthFields() }
function editSSHHost(id){
 sshEditorTrigger=document.activeElement;sshEditorHostID=id;
 resetSSHForm();const host=sshHosts.find(h=>h.id===id),form=$('sshHostForm');
 if(host)for(const [key,value]of Object.entries(host)){const el=form.elements.namedItem(key);if(el)el.value=value}
 $('sshEditorTitle').textContent=host?'编辑 SSH 主机 · '+host.name:'新增 SSH 主机';
 $('sshEditorError').hidden=true;clearSSHCheck();sshAuthFields();$('sshEditor').showModal();form.elements.name.focus({preventScroll:true});
}
function closeSSHEditor(){if(!sshSaving)$('sshEditor').close()}
$('sshEditor').oncancel=event=>{if(sshSaving)event.preventDefault()};
$('sshEditor').onclose=()=>{
 clearSSHCheck();
 resetSSHForm();
 const trigger=sshEditorTrigger?.isConnected?sshEditorTrigger:[...document.querySelectorAll('[data-ssh-edit]')].find(b=>b.dataset.sshEdit===sshEditorHostID)||$('sshAdd');
 trigger.focus({preventScroll:true});
};
$('sshHost').onchange=()=>{updateSSHLink();updateSSHTestTitle()};
$('sshTestClose').onclick=()=>{$('sshTestPanel').close()};
$('sshMode').onchange=updateSSHLink;
$('sshProtocol').onchange=updateSSHLink;
$('sshLink').onchange=updateSSHLink;
$('sshSourceHost').onchange=updateSSHLink;
function updateSSHLink(){
 const peer=$('sshLink').value==='peer';
 $('sshTestDescription').textContent=peer?'源主机与目标主机直接传输测试流量，支持 TCP / UDP 双向测试。':'从软路由直连远端主机；原生 SSH 测加密传输，iperf3 测 TCP / UDP 吞吐。';
 $('sshSourceField').hidden=!peer;$('sshModeField').hidden=peer;
 if(peer)$('sshMode').value='iperf3';
 document.querySelectorAll('.ssh-iperf').forEach(e=>e.hidden=$('sshMode').value!=='iperf3');
 const iperf=$('sshMode').value==='iperf3',protocol=$('sshProtocol').value;
 $('sshUDPBandwidthField').hidden=!iperf||protocol==='tcp';$('sshUDPLengthField').hidden=protocol==='tcp';
 document.querySelectorAll('.ssh-tcp-option').forEach(e=>e.hidden=protocol==='udp');
 const direction=$('sshDirection').value;
 $('sshDirection').innerHTML=peer?'<option value="both">双向依次测试</option><option value="upload">源 → 目标</option><option value="download">目标 → 源</option>':'<option value="both">下载 + 上传</option><option value="download">仅下载</option><option value="upload">仅上传</option>';
 $('sshDirection').value=direction;
 const source=peer?(sshHosts.find(h=>h.id===$('sshSourceHost').value)?.name||'源主机'):'软路由',target=sshHosts.find(h=>h.id===$('sshHost').value)?.name||'目标主机';
 $('sshLinkHint').innerHTML=`以 ${esc(source)} 为参照：<span class="speed-download">↓ 下载 ${esc(target)} → ${esc(source)}</span>；<span class="speed-upload">↑ 上传 ${esc(source)} → ${esc(target)}</span>。${peer?'测速数据直接在两台远程主机之间传输，软路由仅负责控制。':''}${iperf?' UDP 需同时放通目标测速端口的 TCP 和 UDP。':''}`;
 renderSSHButtons();
}
function openSSHTest(id){
 const current=speedCurrent.current||{};
 if(!speedCurrent.running){$('sshMode').value='iperf3';$('sshSeconds').value='20'}
 if(current.node_id==='ssh:'+id){$('sshLink').value=current.source_host_id?'peer':'router';$('sshSourceHost').value=current.source_host_id||'';if(speedCurrent.running&&['ssh','iperf3'].includes(current.mode)){$('sshMode').value=current.mode;const cfg=current.test_config;if(cfg)for(const [id,key]of Object.entries({sshSeconds:'seconds',sshTestPort:'port',sshParallel:'parallel',sshOmit:'omit',sshTCPBandwidth:'tcp_bandwidth',sshUDPLength:'udp_length',sshBandwidth:'bandwidth',sshProtocol:'protocol',sshDirection:'direction'}))$(id).value=cfg[key]??$(id).value}}
 $('sshHost').value=id;$('sshTestError').hidden=true;updateSSHTestTitle();updateSSHLink();showSSHDialog('sshTestPanel',id);
}
$('sshTaskView').onclick=()=>openSSHTest(speedCurrent.current.node_id.slice(4));
$('sshHistoryClose').onclick=()=>{$('sshHistoryPanel').close()};
async function openSSHHistory(id=''){
 $('sshHistoryNode').value=id?'ssh:'+id:'';$('sshHistoryError').hidden=true;showSSHDialog('sshHistoryPanel',id,'more');
 try{await loadSSHHistory()}catch(e){$('sshHistoryError').textContent=e.message;$('sshHistoryError').hidden=false}
}
$('sshHistoryOpen').onclick=()=>openSSHHistory();
$('sshHostForm').elements.auth.onchange=sshAuthFields;
$('sshAdd').onclick=()=>editSSHHost('');
$('sshEditorCancel').onclick=closeSSHEditor;
$('sshEditorClose').onclick=closeSSHEditor;
function clearSSHCheck(){sshCheckController?.abort();sshCheckController=null;$('sshEditorCheck').disabled=sshSaving;$('sshEditorCheck').textContent='测试连接';$('sshEditorCheckResult').hidden=true}
$('sshHostForm').oninput=clearSSHCheck;
$('sshHostForm').onchange=clearSSHCheck;
$('sshEditorCheck').onclick=async()=>{
 const host=Object.fromEntries(new FormData($('sshHostForm')));host.port=Number(host.port);
 clearSSHCheck();const controller=new AbortController();sshCheckController=controller;
 const result=$('sshEditorCheckResult');result.hidden=false;result.dataset.state='pending';result.textContent='正在连接并验证登录…';$('sshEditorCheck').disabled=true;$('sshEditorCheck').textContent='测试中…';
 try{const response=await api('/api/speed/ssh/check',{method:'POST',body:JSON.stringify(host),signal:controller.signal});if(sshCheckController===controller){result.dataset.state='success';result.textContent=response.message}}
 catch(e){if(sshCheckController===controller){result.dataset.state='error';result.textContent=e.message}}
 finally{if(sshCheckController===controller){sshCheckController=null;$('sshEditorCheck').disabled=sshSaving;$('sshEditorCheck').textContent='测试连接'}}
};
$('sshRefresh').onclick=()=>loadSSHHosts().catch(e=>toast(e.message));
$('sshSearch').oninput=renderSSHHosts;
$('sshCredentialFilter').onchange=renderSSHHosts;
 $('sshTaskStop').onclick=()=>stopSpeed().catch(showSSHTestError);
$('sshHostForm').onsubmit=async event=>{
 event.preventDefault();if(sshSaving)return;const form=event.target,host=Object.fromEntries(new FormData(form));host.port=Number(host.port);
 sshSaving=true;clearSSHCheck();$('sshEditorError').hidden=true;const submit=form.querySelector('button[type="submit"]');submit.textContent='保存中…';renderSSHButtons();
 $('sshEditorCancel').disabled=$('sshEditorClose').disabled=true;
 try{
  await api('/api/speed/ssh/hosts',{method:'POST',body:JSON.stringify(host)});
  try{await loadSSHHosts();toast('SSH 主机已保存')}catch{toast('SSH 主机已保存，列表刷新失败，请点击刷新列表')}
  $('sshEditor').close();
 }catch(e){$('sshEditorError').textContent=e.message;$('sshEditorError').hidden=false}
 finally{sshSaving=false;submit.textContent='保存主机';$('sshEditorCheck').disabled=$('sshEditorCancel').disabled=$('sshEditorClose').disabled=false;renderSSHButtons()}
};
$('sshHostRows').onclick=event=>{
 const button=event.target.closest('button');if(!button||button.disabled)return;
 const action=['edit','terminal','speed'].find(a=>button.hasAttribute('data-ssh-'+a));if(action)sshHostAction(action,button.getAttribute('data-ssh-'+action));
};
$('sshHostRows').onchange=event=>{const select=event.target.closest('[data-ssh-more]');if(!select)return;const action=select.value;select.value='';if(action)sshHostAction(action,select.dataset.sshMore)};
async function sshHostAction(action,id){
 const host=sshHosts.find(h=>h.id===id);if(!host)return;
 try{
  if(action==='edit'){editSSHHost(host.id);return}
  if(action==='delete'){
   if(!confirm(`删除 SSH 主机“${host.name}”及保存的凭据？`))return;
   await api('/api/speed/ssh/hosts',{method:'DELETE',body:JSON.stringify({id:host.id})});
   if($('sshHostForm').elements.id.value===host.id)closeSSHEditor();
   await loadSSHHosts();toast('SSH 主机已删除');return;
  }
  if(action==='terminal'){openSSHTerminal(host);return}
  if(action==='history'){await openSSHHistory(host.id);return}
  if(action==='speed'){openSSHTest(host.id);return}
  if(action==='install'&&!confirm(`在“${host.name}”及本软路由安装缺失的 iperf3 软件包？需要 root 或免密 sudo，并能访问系统软件源。安装会更新软件包索引。`))return;
  openSSHTest(host.id);await startSSH(action,host.id);
 }catch(e){if($('sshTestPanel').open)showSSHTestError(e);else toast(e.message)}
}
function updateSSHTestTitle(){const host=sshHosts.find(h=>h.id===$('sshHost').value);$('sshTestTitle').textContent=host?'SSH 测试 · '+host.name+' · '+host.host+':'+host.port:'SSH 主机直连测速'}
async function startSSH(mode,hostID=$('sshHost').value){
 $('sshTestError').hidden=true;
 const req={host_id:hostID,source_host_id:mode==='iperf3'&&$('sshLink').value==='peer'?$('sshSourceHost').value:'',mode,protocol:$('sshProtocol').value,direction:$('sshDirection').value,port:Number($('sshTestPort').value),seconds:Number($('sshSeconds').value),bandwidth:Number($('sshBandwidth').value),parallel:Number($('sshParallel').value),omit:Number($('sshOmit').value),tcp_bandwidth:Number($('sshTCPBandwidth').value),udp_length:Number($('sshUDPLength').value)};
 renderSpeed(await api('/api/speed/ssh/start',{method:'POST',body:JSON.stringify(req)}));
}
function showSSHTestError(e){$('sshTestError').textContent=e.message;$('sshTestError').hidden=false}
$('sshTestForm').onsubmit=event=>{event.preventDefault();startSSH($('sshMode').value).catch(showSSHTestError)};

function renderSSHResults(){
 const hostID='ssh:'+$('sshHost').value;
 const results=(speedCurrent.results||[]).filter(r=>r.node_id===hostID);
 const current=speedCurrent.current;
 if(current?.node_id===hostID&&current.direction&&!results.some(r=>r.id&&r.id===current.id))results.push(current);
 $('sshResultsPanel').hidden=!results.length;
 const groups=new Map();
 for(const r of results){const key=[r.source_host_id||'',r.node_id,r.mode,r.protocol].join('/');if(!groups.has(key))groups.set(key,{host:r});groups.get(key)[r.direction]=r}
 const peer=results.some(r=>r.source_host_id);
 const anchor=results[0];
 $('sshResultDown').innerHTML=(peer?'↓ 源主机下载':'↓ 下载')+(anchor?`<small>${esc(sshFlow({...anchor,direction:'download'}))}</small>`:'');$('sshResultUp').innerHTML=(peer?'↑ 源主机上传':'↑ 上传')+(anchor?`<small>${esc(sshFlow({...anchor,direction:'upload'}))}</small>`:'');
 const cell=r=>r?speedCell(r,false)+`<small>${esc(bytes(r.bytes))} · ${Number(r.seconds||0).toFixed(2)} 秒</small>`:'—';
 $('sshResultRows').innerHTML=[...groups.values()].map(g=>{const r=g.host,operation=g.check||g.install;return `<tr><td>${esc(r.source_host_name?r.source_host_name+' ↔ '+r.node_name:r.node_name)}<small>${esc(r.source_host_id?r.target.split(' · ').slice(1).join(' · '):r.target)}</small></td>${operation?`<td colspan="2">${esc(speedResultDirection(operation))} · ${cell(operation)}</td>`:`<td>${cell(g.download)}</td><td>${cell(g.upload)}</td>`}</tr>`}).join('');
}
function renderSSHHistoryNodes(){const selected=$('sshHistoryNode').value;$('sshHistoryNode').innerHTML='<option value="">全部主机</option>'+sshHosts.map(h=>`<option value="ssh:${esc(h.id)}">${esc(h.name)}</option>`).join('');$('sshHistoryNode').value=sshHosts.some(h=>'ssh:'+h.id===selected)?selected:''}
