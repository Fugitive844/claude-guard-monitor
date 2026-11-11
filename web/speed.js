'use strict';
let speedTargetsList=[];
let speedVisibleGroups=[],speedCollapsed=new Set();
let speedNodes=[], speedSelected=new Set(), speedResults=new Map(), speedCurrent={}, speedLoaded=false;
let speedHistoryCursors=[0],speedHistoryNext=0,speedHistoryBounds=null,speedHistoryBusy=false,speedHistoryQuery=0;
const speedToolbar=document.querySelector('.speed-toolbar');
new ResizeObserver(()=>{$('speed').style.setProperty('--speed-toolbar-height',speedToolbar.getBoundingClientRect().height+'px')}).observe(speedToolbar);
function rememberSpeed(r){const key=r.node_id+'/'+r.direction,old=speedResults.get(key);if(!old||r.time>old.time||(r.time===old.time&&(r.id||0)>=(old.id||0)))speedResults.set(key,r)}
const speedLabel={starting:'启动核心',running:'测速中',complete:'完成',limited:'时限采样',error:'失败',cancelled:'已停止'};
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
function speedRate(r){return r&&['complete','limited','running'].includes(r.status)?Number(r.mbps||0).toFixed(2)+' Mbps':'—'}
function speedCell(r,showTarget=true){if(!r)return '—';return `<b>${esc(speedRate(r))}</b><small>${esc(speedLabel[r.status]||r.status)}${r.message?' · '+esc(r.message):''}</small>${showTarget?'<small>'+esc(r.target||'')+'</small><small>'+esc(when(r.time))+'</small>':''}`}
function visibleSpeedNodes(){const q=$('speedSearch').value.toLowerCase(),group=$('speedGroup').value;return speedNodes.filter(n=>(!group||n.group===group)&&(n.name+' '+(n.host||'')+' '+n.group+' '+n.core+' '+n.protocol+' '+n.transport).toLowerCase().includes(q))}
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
 return header+(collapsed?'':g.nodes.map(n=>`<tr><td><input type="checkbox" aria-label="选择 ${esc(n.name)}" data-speed-select="${esc(n.id)}" ${speedSelected.has(n.id)?'checked':''} ${n.supported?'':'disabled'}></td><td class="speed-name">${esc(n.name)}${n.host?'<small>主机：'+esc(n.host)+'</small>':''}<small>${esc(n.core)} · ${esc(n.protocol)}${n.transport?' / '+esc(n.transport):''}${n.reason?' · '+esc(n.reason):''}</small></td><td data-speed-down="${esc(n.id)}">${speedCell(speedResults.get(n.id+'/download'))}</td><td data-speed-up="${esc(n.id)}">${speedCell(speedResults.get(n.id+'/upload'))}</td><td><button class="secondary" data-speed-run="${esc(n.id)}" ${!n.supported||speedCurrent.running?'disabled':''}>测速</button></td></tr>`).join(''));
 }).join(''):'<tr><td colspan="5" class="empty">没有匹配节点</td></tr>';
 document.querySelectorAll('[data-speed-group-select]').forEach(el=>{const nodes=speedVisibleGroups[Number(el.dataset.speedGroupSelect)].nodes.filter(n=>n.supported),n=nodes.filter(n=>speedSelected.has(n.id)).length;el.indeterminate=n>0&&n<nodes.length});
 $('speedCount').textContent=`已选 ${speedSelected.size} / ${speedNodes.filter(n=>n.supported).length} 个可测节点`;
 updateSpeedNodeButtons();
}
function renderSpeed(state){
 if(!state)return;const previous=speedCurrent;speedCurrent=state;
 for(const result of state.results||[])rememberSpeed(result);
 if(state.current?.direction)rememberSpeed(state.current);
 if(!speedLoaded)return;
 $('speedStart').disabled=!!state.running;$('speedStop').disabled=!state.running||state.stopping;
 $('speedJobTitle').textContent=state.running?(state.stopping?'正在停止测速':`${state.current.node_name||'准备中'} · ${state.current.direction==='download'?'下载':state.current.direction==='upload'?'上传':'启动核心'}`):state.finished?'测速任务已结束':'准备测速';
 $('speedProgress').max=state.total||1;$('speedProgress').value=state.done||0;
 $('speedJobDetail').textContent=state.error||`${state.done||0} / ${state.total||0} 项完成 · ${state.current?.target||'批量依次运行'}`;
 $('speedRate').textContent=speedRate(state.current);
 $('speedTransfer').textContent=bytes(state.current?.bytes||0);
 $('speedElapsed').textContent=Number(state.current?.seconds||0).toFixed(1)+' 秒';
 updateSpeedNodeButtons();
 document.querySelectorAll('[data-speed-group-run]').forEach(b=>b.disabled=!!state.running||!speedVisibleGroups[Number(b.dataset.speedGroupRun)].nodes.some(n=>n.supported));
 for(const [attr,dir] of [['data-speed-down','download'],['data-speed-up','upload']])document.querySelectorAll(`[${attr}]`).forEach(c=>c.innerHTML=speedCell(speedResults.get(c.getAttribute(attr)+'/'+dir)));
 if(previous.running&&!state.running&&view==='speed')loadSpeedHistory().catch(e=>toast(e.message));
}
async function loadSpeed(){
 const [nodes,cfg,state,targets]=await Promise.all([api('/api/speed/nodes'),api('/api/speed/settings'),api('/api/speed/status'),api('/api/speed/targets')]); speedTargetsList=targets.items;
 const options=items=>items.map(t=>`<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('');
 $('speedTarget').innerHTML=$('speedSettings').elements.target.innerHTML=options(speedTargetsList);
 $('speedUploadTarget').innerHTML=$('speedSettings').elements.upload_target.innerHTML=options(speedTargetsList.filter(t=>t.upload));
 $('speedTarget').value=cfg.target;$('speedUploadTarget').value=cfg.upload_target;

 for(const result of nodes.latest||[])rememberSpeed(result);
 speedNodes=(nodes.items||[]).map(n=>({...n,group:n.group||'未分组'}));speedSelected=new Set([...speedSelected].filter(id=>speedNodes.some(n=>n.id===id&&n.supported)));
 const previousGroup=$('speedGroup').value;const groupNames=[...new Set(speedNodes.map(n=>n.group))];$('speedGroup').innerHTML='<option value="">全部分组</option>'+groupNames.map(g=>`<option value="${esc(g)}">${esc(g)}</option>`).join('');$('speedGroup').value=groupNames.includes(previousGroup)?previousGroup:'';
 const historyNode=$('speedHistoryNode').value;$('speedHistoryNode').innerHTML='<option value="">全部节点</option>'+speedNodes.map(n=>`<option value="${esc(n.id)}">${esc(n.name)}</option>`).join('');$('speedHistoryNode').value=speedNodes.some(n=>n.id===historyNode)?historyNode:'';
 for(const [k,v] of Object.entries(cfg)){const el=$('speedSettings').elements.namedItem(k);if(el)el.value=v}
 showSpeedCustom();speedLoaded=true;renderSpeedNodes();renderSpeed(state);await loadSpeedHistory();
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
$('speedGroup').onchange=renderSpeedNodes;
$('speedSort').onchange=renderSpeedNodes;
$('speedSelectAll').onclick=()=>{visibleSpeedNodes().filter(n=>n.supported).forEach(n=>speedSelected.add(n.id));renderSpeedNodes()};
$('speedClear').onclick=()=>{speedSelected.clear();renderSpeedNodes()};
$('speedNodeRows').onchange=e=>{const id=e.target.dataset.speedSelect,group=e.target.dataset.speedGroupSelect;const ids=group!==undefined?speedVisibleGroups[Number(group)].nodes.filter(n=>n.supported).map(n=>n.id):id?[id]:[];for(const id of ids){if(e.target.checked)speedSelected.add(id);else speedSelected.delete(id)}renderSpeedNodes()};
$('speedNodeRows').onclick=e=>{const fold=e.target.closest('[data-speed-fold]');if(fold){const name=speedVisibleGroups[Number(fold.dataset.speedFold)].name;if(speedCollapsed.has(name))speedCollapsed.delete(name);else speedCollapsed.add(name);renderSpeedNodes();return}const group=e.target.closest('[data-speed-group-run]');if(group){startSpeed(speedVisibleGroups[Number(group.dataset.speedGroupRun)].nodes.filter(n=>n.supported).map(n=>n.id)).catch(e=>toast(e.message));return}const b=e.target.closest('[data-speed-run]');if(b&&!b.disabled)(speedCurrent.running&&speedCurrent.current?.node_id===b.dataset.speedRun?stopSpeed():startSpeed([b.dataset.speedRun])).catch(e=>toast(e.message))};
function showSpeedCustom(){const f=$('speedSettings').elements;$('speedCustom').hidden=f.target.value!=='custom'&&f.upload_target.value!=='custom'}
$('speedSettings').elements.target.onchange=showSpeedCustom;
$('speedSettings').elements.upload_target.onchange=showSpeedCustom;
$('speedSettings').onsubmit=async e=>{e.preventDefault();const cfg=Object.fromEntries(new FormData(e.target));for(const k of ['seconds','download_mib','upload_mib'])cfg[k]=Number(cfg[k]);try{await api('/api/speed/settings',{method:'POST',body:JSON.stringify(cfg)});$('speedTarget').value=cfg.target;$('speedUploadTarget').value=cfg.upload_target;toast('测速设置已保存，下次任务生效')}catch(e){toast(e.message)}};
async function loadSpeedHistory(reset=true,direction=''){
 if(speedHistoryBusy&&!reset)return;const bounds=reset||!speedHistoryBounds?{...rangeDates('speedHistory'),node:$('speedHistoryNode').value}:speedHistoryBounds;const query=++speedHistoryQuery;
 const cursors=reset?[0]:[...speedHistoryCursors];if(direction==='next')cursors.push(speedHistoryNext);if(direction==='prev'&&cursors.length>1)cursors.pop();
 speedHistoryBusy=true;
 try{const page=await api(`/api/speed/history?from=${bounds.from}&to=${bounds.to}&before=${cursors.at(-1)}&node_id=${encodeURIComponent(bounds.node)}`);if(query!==speedHistoryQuery)return;speedHistoryBounds=bounds;speedHistoryCursors=cursors;speedHistoryNext=page.next_before;
 $('speedHistoryRows').innerHTML=page.items.length?page.items.map(r=>`<tr><td>${esc(when(r.time))}</td><td class="speed-name">${esc(r.node_name)}<small>${esc(r.target)}${r.edge?' · '+esc(r.edge.split('-').at(-1)):''}</small></td><td>${r.direction==='download'?'下载':'上传'}</td><td>${speedCell(r,false)}</td><td>${esc(bytes(r.bytes))}<small>${Number(r.seconds).toFixed(2)} 秒</small></td></tr>`).join(''):'<tr><td colspan="5" class="empty">所选时间范围内暂无测速记录</td></tr>';
 $('speedHistoryPrev').disabled=cursors.length===1;$('speedHistoryNext').disabled=!page.has_more;$('speedHistoryPage').textContent=`第 ${cursors.length} 页 · 本页 ${page.items.length} 条`;
 }finally{if(query===speedHistoryQuery)speedHistoryBusy=false}
}
$('speedHistoryNode').onchange=()=>loadSpeedHistory().catch(e=>toast(e.message));
$('speedHistoryFilters').onsubmit=e=>{e.preventDefault();loadSpeedHistory().catch(e=>toast(e.message))};
$('speedHistoryPrev').onclick=()=>loadSpeedHistory(false,'prev').catch(e=>toast(e.message));
$('speedHistoryNext').onclick=()=>loadSpeedHistory(false,'next').catch(e=>toast(e.message));
$('speedHistoryRange').onchange=()=>{const custom=$('speedHistoryRange').value==='custom';$('speedHistoryFromLabel').hidden=!custom;$('speedHistoryToLabel').hidden=!custom;if(custom&&!$('speedHistoryTo').value){const local=t=>{const d=new Date(t);return new Date(t-d.getTimezoneOffset()*60000).toISOString().slice(0,16)};$('speedHistoryTo').value=local(Date.now());$('speedHistoryFrom').value=local(Date.now()-86400000)}};
