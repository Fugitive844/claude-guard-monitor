'use strict';
let speedTargetsList=[];
let speedVisibleGroups=[],speedCollapsed=new Set(),speedActiveGroup='';
let speedNodes=[], speedSelected=new Set(), speedResults=new Map(), speedMeasured=new Map(), speedCurrent={}, speedLoaded=false;
const speedToolbar=document.querySelector('.speed-toolbar');
new ResizeObserver(()=>{$('speed').style.setProperty('--speed-toolbar-height',speedToolbar.getBoundingClientRect().height+'px')}).observe(speedToolbar);
function hasSpeedMeasurement(r){return r&&['download','upload'].includes(r.direction)&&(['complete','limited'].includes(r.status)||r.status==='cancelled'&&r.bytes>0&&r.seconds>0)}
function speedMeasurementKey(r){return JSON.stringify([r.source_host_id||'',r.node_id,r.mode||'',r.protocol||'',r.test_config?.port||0,r.direction])}
function rememberMeasuredSpeed(r){if(!hasSpeedMeasurement(r))return;const key=speedMeasurementKey(r),old=speedMeasured.get(key);if(!old||r.time>old.time||(r.time===old.time&&(r.id||0)>=(old.id||0)))speedMeasured.set(key,r)}
function proxySpeedCell(nodeID,direction){const current=speedResults.get(nodeID+'/'+direction),previous=speedMeasured.get(speedMeasurementKey({node_id:nodeID,direction}));const thisJob=current?.job&&current.job===speedCurrent.job;return speedCell(thisJob?current:previous||current,true,!thisJob)}
function rememberSpeed(r){rememberMeasuredSpeed(r);const key=r.node_id+'/'+r.direction,old=speedResults.get(key);if(!old||r.time>old.time||(r.time===old.time&&(r.id||0)>=(old.id||0)))speedResults.set(key,r)}
const speedLabel={starting:'准备连接',running:'运行中',complete:'完成',limited:'时限采样',error:'失败',cancelled:'已停止'};
const speedDirectionLabel={download:'下载',upload:'上传',check:'连接检查',install:'安装 iperf3'};
const speedNodeCollator=new Intl.Collator('zh-CN',{numeric:true,sensitivity:'base'});
function compareSpeedNodes(a,b){
 const compare=(x,y)=>speedNodeCollator.compare(x||'',y||'');
 if($('speedSort').value==='host'){
  const hostOrder=Number(!a.host)-Number(!b.host)||compare(a.host,b.host);
  if(hostOrder)return hostOrder;
 }
 if($('speedSort').value==='protocol'){
  const protocolOrder=compare(a.protocol,b.protocol)||compare(a.transport,b.transport);
  if(protocolOrder)return protocolOrder;
 }
 return compare(a.name,b.name)||compare(a.id,b.id);
}
function groupedSpeedNodes(nodes){
 const groups=new Map();for(const n of nodes){if(!groups.has(n.group))groups.set(n.group,[]);groups.get(n.group).push(n)}
 return [...groups].map(([name,nodes])=>({name,nodes:nodes.sort(compareSpeedNodes)}));
}
function speedResultDirection(r){return r.source_host_id&&['download','upload'].includes(r.direction)?(r.direction==='upload'?'源主机上传':'源主机下载'):speedDirectionLabel[r.direction]||r.direction}
function speedDirectionClass(r){return ['download','upload'].includes(r?.direction)?'speed-'+r.direction:''}
function speedDirectionBadge(r){return `<span class="speed-direction ${speedDirectionClass(r)}">${esc(speedResultDirection(r))}</span>`}
function speedRate(r){if(r?.status==='running'&&r.interval_mbps!=null)return Number(r.interval_mbps).toFixed(2)+' Mbps';if(r?.status==='running'&&(r.mode==='iperf3'||r.mode==='ssh'&&r.direction==='upload'))return '等待统计';return r&&['download','upload'].includes(r.direction)&&(['complete','limited','running'].includes(r.status)||r.status==='cancelled'&&r.bytes>0&&r.seconds>0)?Number(r.mbps||0).toFixed(2)+' Mbps':'—'}
function speedCell(r,showTarget=true,historical=false){if(!r)return '—';return `${historical?'<small class="speed-previous">历史结果 · '+esc(when(r.time))+'</small>':''}<b class="${speedDirectionClass(r)}">${esc(speedRate(r))}</b><small>${esc(speedLabel[r.status]||r.status)}${r.message?' · '+esc(r.message):''}${r.mode==='iperf3'&&r.status==='complete'?' · 接收端平均':''}</small>${r.protocol==='udp'&&(r.status==='complete'||r.status==='cancelled'&&r.packets>0)?`<small>丢包 ${Number(r.lost_percent||0).toFixed(2)}% (${r.lost_packets||0}/${r.packets||0}) · ${r.status==='cancelled'?'末次抖动':'抖动'} ${Number(r.jitter_ms||0).toFixed(2)} ms</small>`:''}${iperfResultMetrics(r)}${showTarget?'<small>'+esc(r.target||'')+'</small>'+(!historical?'<small>'+esc(when(r.time))+'</small>':''):''}`}
function renderSpeedGroups(){
 const groups=new Map();for(const n of speedNodes)groups.set(n.group,(groups.get(n.group)||0)+1);
 if(speedActiveGroup&&!groups.has(speedActiveGroup))speedActiveGroup='';
 $('speedGroups').innerHTML=[['','全部',speedNodes.length],...[...groups].map(([name,count])=>[name,name,count])].map(([value,label,count])=>`<button type="button" class="speed-group-filter" data-speed-filter="${esc(value)}" aria-pressed="${speedActiveGroup===value}"><span>${esc(label)}</span><small>${count}</small></button>`).join('');
}
function visibleSpeedNodes(){const q=$('speedSearch').value.trim().toLowerCase();return speedNodes.filter(n=>(!speedActiveGroup||n.group===speedActiveGroup)&&(n.name+' '+(n.host||'')+' '+n.group+' '+n.core+' '+n.protocol+' '+n.transport).toLowerCase().includes(q))}
function updateSpeedSelection(){
 const visible=visibleSpeedNodes(),visibleIDs=new Set(visible.map(n=>n.id));
 const outside=[...speedSelected].filter(id=>!visibleIDs.has(id)).length;
 $('speedCount').textContent=`已选 ${speedSelected.size} 个${outside?' · 筛选外 '+outside+' 个':''}`;
 $('speedVisibleCount').textContent=`显示 ${visible.length} / ${speedNodes.length} 个节点`;
 $('speedStart').textContent=speedSelected.size?`测速所选 (${speedSelected.size})`:'测速所选节点';
 $('speedStart').disabled=!!speedCurrent.running||!speedSelected.size;
 $('speedClear').disabled=!speedSelected.size;
 $('speedSelectAll').disabled=!visible.some(n=>n.supported&&!speedSelected.has(n.id));
}
function updateSpeedNodeButtons(){
 document.querySelectorAll('[data-speed-run]').forEach(b=>{
  const active=!!speedCurrent.running&&b.dataset.speedRun===speedCurrent.current?.node_id;
  b.textContent=active?(speedCurrent.stopping?'正在停止…':'停止'):'测速';
  b.disabled=active?!!speedCurrent.stopping:!!speedCurrent.running||!speedNodes.find(n=>n.id===b.dataset.speedRun)?.supported;
  b.classList.toggle('danger',active);
  b.title=active?'停止当前测速任务（含剩余队列）':'';
 });
}
function renderSpeedNodes(){
 speedVisibleGroups=groupedSpeedNodes(visibleSpeedNodes());
 $('speedNodeRows').innerHTML=speedVisibleGroups.length?speedVisibleGroups.map((g,i)=>{
 const usable=g.nodes.filter(n=>n.supported),selected=usable.filter(n=>speedSelected.has(n.id)).length,collapsed=speedCollapsed.has(g.name);
 const header=`<tr class="speed-group"><td><input type="checkbox" data-speed-group-select="${i}" aria-label="选择分组 ${esc(g.name)}" ${usable.length&&selected===usable.length?'checked':''} ${usable.length?'':'disabled'}></td><td colspan="3"><button class="text-button" data-speed-fold="${i}" aria-expanded="${!collapsed}">${collapsed?'▸':'▾'} ${esc(g.name)}</button><span class="tiny">${g.nodes.length} 个节点 · 已选 ${selected} / ${usable.length} 个可测节点</span></td><td><button class="secondary" data-speed-group-run="${i}" ${!usable.length||speedCurrent.running?'disabled':''}>组内测速 (${usable.length})</button></td></tr>`;
 return header+(collapsed?'':g.nodes.map(n=>`<tr><td><input type="checkbox" aria-label="选择 ${esc(n.name)}" data-speed-select="${esc(n.id)}" ${speedSelected.has(n.id)?'checked':''} ${n.supported?'':'disabled'}></td><td class="speed-name">${esc(n.name)}${n.host?'<small>主机：'+esc(n.host)+'</small>':''}<small>${esc(n.core)} · ${esc(n.protocol)}${n.transport?' / '+esc(n.transport):''}${n.reason?' · '+esc(n.reason):''}</small></td><td data-speed-down="${esc(n.id)}">${proxySpeedCell(n.id,'download')}</td><td data-speed-up="${esc(n.id)}">${proxySpeedCell(n.id,'upload')}</td><td><button class="secondary" data-speed-run="${esc(n.id)}" ${!n.supported||speedCurrent.running?'disabled':''}>测速</button></td></tr>`).join(''));
 }).join(''):'<tr><td colspan="5" class="empty">没有匹配节点</td></tr>';
 document.querySelectorAll('[data-speed-group-select]').forEach(el=>{const nodes=speedVisibleGroups[Number(el.dataset.speedGroupSelect)].nodes.filter(n=>n.supported),n=nodes.filter(n=>speedSelected.has(n.id)).length;el.indeterminate=n>0&&n<nodes.length});
 updateSpeedSelection();
 updateSpeedNodeButtons();
}
function renderSpeed(state){
 if(!state)return;const previous=speedCurrent;speedCurrent=state;rememberSSHSamples(state);
 for(const result of state.results||[])rememberSpeed(result);
 if(state.current?.direction)rememberSpeed(state.current);
 renderSSHButtons();
 if(previous.running&&!state.running){
  if(view==='speedHistory')loadSpeedHistory().catch(e=>toast(e.message));
  if(view==='ssh'&&$('sshHistoryPanel').open)loadSSHHistory().catch(e=>toast(e.message));
  if(state.current?.node_id?.startsWith('ssh:'))loadSSHHosts().catch(e=>toast(e.message));
 }
 if(!speedLoaded)return;
 const isSSH=state.current?.node_id?.startsWith('ssh:');
 const proxyState=isSSH?{...state,current:{},done:0,total:0,finished:0,error:''}:state;
 updateSpeedSelection();$('speedStop').disabled=!state.running||state.stopping;
 if(isSSH)$('speedStop').disabled=true;
 $('speedJobTitle').textContent=isSSH?(state.running?'SSH 测试正在运行':'准备测速'):state.running?(state.stopping?'正在停止任务':`${state.current.node_name||'准备中'} · ${speedDirectionLabel[state.current.direction]||'准备连接'}`):state.finished?'测速任务已结束':'准备测速';
 $('speedProgress').max=proxyState.total||1;$('speedProgress').value=proxyState.done||0;
 $('speedJobDetail').textContent=isSSH?(state.running?'前往 SSH 管理查看进度与结果；完成后可开始代理测试。':'选择节点与测速目标，测量端到端速度。'):state.error||`${state.done||0} / ${state.total||0} 项完成 · ${state.current?.target||'批量依次运行'}${state.current?.message?' · '+state.current.message:''}`;
 $('speedRate').textContent=speedRate(proxyState.current);
 $('speedRate').className=speedDirectionClass(proxyState.current);
 $('speedTransfer').textContent=bytes(proxyState.current?.bytes||0);
 $('speedElapsed').textContent=Number(proxyState.current?.seconds||0).toFixed(1)+' 秒';
 updateSpeedNodeButtons();
 document.querySelectorAll('[data-speed-group-run]').forEach(b=>b.disabled=!!state.running||!speedVisibleGroups[Number(b.dataset.speedGroupRun)].nodes.some(n=>n.supported));
 for(const [attr,dir] of [['data-speed-down','download'],['data-speed-up','upload']])document.querySelectorAll(`[${attr}]`).forEach(c=>c.innerHTML=proxySpeedCell(c.getAttribute(attr),dir));
}
function renderSpeedHistoryNodes(nodes=speedNodes){const selected=$('speedHistoryNode').value;$('speedHistoryNode').innerHTML='<option value="">全部节点</option>'+nodes.map(n=>`<option value="${esc(n.id)}">${esc(n.name)}</option>`).join('');$('speedHistoryNode').value=nodes.some(n=>n.id===selected)?selected:''}
async function loadProxyHistory(){const data=await api('/api/speed/nodes');renderSpeedHistoryNodes(data.items||[]);await loadSpeedHistory()}
async function loadSpeed(){
 const [nodes,cfg,state,targets]=await Promise.all([api('/api/speed/nodes'),api('/api/speed/settings'),api('/api/speed/status'),api('/api/speed/targets')]); speedTargetsList=targets.items;
 const options=items=>items.map(t=>`<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('');
 if(!speedLoaded){
  $('speedTarget').innerHTML=$('speedSettings').elements.target.innerHTML=options(speedTargetsList);
  $('speedUploadTarget').innerHTML=$('speedSettings').elements.upload_target.innerHTML=options(speedTargetsList.filter(t=>t.upload));
  $('speedTarget').value=cfg.target;$('speedUploadTarget').value=cfg.upload_target;
  for(const [k,v] of Object.entries(cfg)){const el=$('speedSettings').elements.namedItem(k);if(el)el.value=v}
 }

 for(const result of nodes.previous||[])rememberMeasuredSpeed(result);
 for(const result of nodes.latest||[])rememberSpeed(result);
 speedNodes=(nodes.items||[]).map(n=>({...n,group:n.group||'未分组'}));speedSelected=new Set([...speedSelected].filter(id=>speedNodes.some(n=>n.id===id&&n.supported)));
 renderSpeedGroups();
 showSpeedCustom();speedLoaded=true;renderSpeedNodes();renderSpeed(state);
}
async function startSpeed(ids){
 const requested=new Set(ids);
 ids=groupedSpeedNodes(speedNodes).flatMap(g=>g.nodes).filter(n=>requested.has(n.id)).map(n=>n.id);
 if(!ids.length){toast('请先选择节点');return}
 const state=await api('/api/speed/start',{method:'POST',body:JSON.stringify({nodes:ids,direction:$('speedDirection').value,target:$('speedTarget').value,upload_target:$('speedUploadTarget').value})});
 renderSpeedNodes();renderSpeed(state);
}
$('speedStart').onclick=()=>startSpeed([...speedSelected]).catch(e=>toast(e.message));
async function stopSpeed(){
 if(!speedCurrent.running||speedCurrent.stopping)return;
 const previous=speedCurrent;renderSpeed({...previous,stopping:true});
 try{renderSpeed(await api('/api/speed/stop',{method:'POST'}))}catch(e){renderSpeed(previous);throw e}
}
$('speedStop').onclick=()=>stopSpeed().catch(e=>toast(e.message));
$('speedRefresh').onclick=()=>loadSpeed().catch(e=>toast(e.message));
$('speedSearch').oninput=renderSpeedNodes;
$('speedGroups').onclick=e=>{const button=e.target.closest('[data-speed-filter]');if(!button)return;speedActiveGroup=button.dataset.speedFilter;for(const b of $('speedGroups').querySelectorAll('[data-speed-filter]'))b.setAttribute('aria-pressed',String(b===button));renderSpeedNodes()};
$('speedSort').onchange=renderSpeedNodes;
$('speedSelectAll').onclick=()=>{visibleSpeedNodes().filter(n=>n.supported).forEach(n=>speedSelected.add(n.id));renderSpeedNodes()};
$('speedClear').onclick=()=>{speedSelected.clear();renderSpeedNodes()};
$('speedNodeRows').onchange=e=>{const id=e.target.dataset.speedSelect,group=e.target.dataset.speedGroupSelect;const ids=group!==undefined?speedVisibleGroups[Number(group)].nodes.filter(n=>n.supported).map(n=>n.id):id?[id]:[];for(const id of ids){if(e.target.checked)speedSelected.add(id);else speedSelected.delete(id)}renderSpeedNodes()};
$('speedNodeRows').onclick=e=>{const fold=e.target.closest('[data-speed-fold]');if(fold){const name=speedVisibleGroups[Number(fold.dataset.speedFold)].name;if(speedCollapsed.has(name))speedCollapsed.delete(name);else speedCollapsed.add(name);renderSpeedNodes();return}const group=e.target.closest('[data-speed-group-run]');if(group){startSpeed(speedVisibleGroups[Number(group.dataset.speedGroupRun)].nodes.filter(n=>n.supported).map(n=>n.id)).catch(e=>toast(e.message));return}const b=e.target.closest('[data-speed-run]');if(b&&!b.disabled)(speedCurrent.running&&speedCurrent.current?.node_id===b.dataset.speedRun?stopSpeed():startSpeed([b.dataset.speedRun])).catch(e=>toast(e.message))};
function showSpeedCustom(){const f=$('speedSettings').elements;$('speedCustom').hidden=f.target.value!=='custom'&&f.upload_target.value!=='custom'}
$('speedSettings').elements.target.onchange=showSpeedCustom;
$('speedSettings').elements.upload_target.onchange=showSpeedCustom;
$('speedSettings').onsubmit=async e=>{e.preventDefault();const cfg=Object.fromEntries(new FormData(e.target));for(const k of ['seconds','download_mib','upload_mib'])cfg[k]=Number(cfg[k]);try{await api('/api/speed/settings',{method:'POST',body:JSON.stringify(cfg)});$('speedTarget').value=cfg.target;$('speedUploadTarget').value=cfg.upload_target;toast('测速设置已保存，下次任务生效')}catch(e){toast(e.message)}};
function speedHistoryRow(group,scope){
 const items=group.items,r=items[0],down=items.find(x=>x.direction==='download'),up=items.find(x=>x.direction==='upload');
 const cell=x=>x?`${speedCell(x,false)}<small>${esc(bytes(x.bytes))} · ${Number(x.seconds||0).toFixed(2)} 秒</small><small>${esc(scope==='ssh'?sshFlow(x):x.target||'')}</small>`:'<span class="muted">本轮未测试</span>';
 const name=r.source_host_name?`${r.source_host_name} ↔ ${r.node_name}`:r.node_name;
 const detail=scope==='ssh'?[r.mode,r.protocol?.toUpperCase(),r.test_config?.port?`端口 ${r.test_config.port}`:''].filter(Boolean).join(' · '):'';
 const operation=!down&&!up;
 return `<tr><td>${esc(when(Math.min(...items.map(x=>x.time))))}</td><td class="speed-name">${esc(name)}${detail?`<small>${esc(detail)}</small>`:''}</td>${operation?`<td colspan="2">${esc(speedResultDirection(r))} · ${speedCell(r,false)}</td>`:`<td>${cell(down)}</td><td>${cell(up)}</td>`}</tr>`;
}
function createSpeedHistory(prefix,scope){
 let cursorsStack=[0],nextCursor=0,savedBounds=null,busy=false,queryID=0;
async function load(reset=true,direction=''){
 if(busy&&!reset)return;const bounds=reset||!savedBounds?{...rangeDates(prefix),node:$(prefix+'Node').value}:savedBounds;const query=++queryID;
 const cursors=reset?[0]:[...cursorsStack];if(direction==='next')cursors.push(nextCursor);if(direction==='prev'&&cursors.length>1)cursors.pop();
 busy=true;
 try{const page=await api(`/api/speed/history?grouped=1&scope=${scope}&from=${bounds.from}&to=${bounds.to}&before=${cursors.at(-1)}&node_id=${encodeURIComponent(bounds.node)}`);if(query!==queryID)return;savedBounds=bounds;cursorsStack=cursors;nextCursor=page.next_before;
 $(prefix+'Rows').innerHTML=page.items.length?page.items.map(g=>speedHistoryRow(g,scope)).join(''):'<tr><td colspan="4" class="empty">所选时间范围内暂无测速记录</td></tr>';
 $(prefix+'Prev').disabled=cursors.length===1;$(prefix+'Next').disabled=!page.has_more;$(prefix+'Page').textContent=`第 ${cursors.length} 页 · 本页 ${page.items.length} 条`;
 }finally{if(query===queryID)busy=false}
}
$(prefix+'Node').onchange=()=>load().catch(e=>toast(e.message));
$(prefix+'Filters').onsubmit=e=>{e.preventDefault();load().catch(e=>toast(e.message))};
$(prefix+'Prev').onclick=()=>load(false,'prev').catch(e=>toast(e.message));
$(prefix+'Next').onclick=()=>load(false,'next').catch(e=>toast(e.message));
$(prefix+'Range').onchange=()=>{const custom=$(prefix+'Range').value==='custom';$(prefix+'FromLabel').hidden=!custom;$(prefix+'ToLabel').hidden=!custom;if(custom&&!$(prefix+'To').value){const local=t=>{const d=new Date(t);return new Date(t-d.getTimezoneOffset()*60000).toISOString().slice(0,16)};$(prefix+'To').value=local(Date.now());$(prefix+'From').value=local(Date.now()-86400000)}};

return load;
}
const loadSpeedHistory=createSpeedHistory('speedHistory','proxy');
const loadSSHHistory=createSpeedHistory('sshHistory','ssh');
