'use strict';
const sshSampleRounds=new Map(),sshObservedJobs=new Set();
let sshSampleSelection='';
function sshFlow(r){const source=r.source_host_name||'软路由',target=r.node_name||'目标主机';return r.direction==='download'?`${target} → ${source}`:`${source} → ${target}`}
function sshSampleKey(r){return [r.job,r.node_id,r.protocol,r.direction].join('/')}
function clearSSHSamples(){sshSampleRounds.clear();sshObservedJobs.clear();sshSampleSelection='';if($('sshSamplesPanel').open)$('sshSamplesPanel').close()}
function rememberSSHSamples(state){
 if(state.running&&state.current?.mode==='iperf3')sshObservedJobs.add(state.job);
 if(sshObservedJobs.has(state.job))for(const r of [...state.results||[],state.current||{}]){
  if(r.mode!=='iperf3'||!r.direction)continue;
  const key=sshSampleKey(r),old=sshSampleRounds.get(key);
  if(r.samples?.length||old)sshSampleRounds.set(key,{...r,samples:r.samples?.length?r.samples:old.samples});
 }
 $('sshSamplesOpen').disabled=!sshSampleRounds.size;
 $('sshResultSamples').disabled=![...sshSampleRounds.values()].some(r=>r.job===state.job);
 if($('sshSamplesPanel').open)renderSSHSamples();
}
function iperfResultMetrics(r){
 if(r.mode!=='iperf3'||!['complete','limited','cancelled'].includes(r.status))return '';
 const lines=[];
 if(r.sender_mbps!=null)lines.push(`发送端平均 ${Number(r.sender_mbps).toFixed(2)} Mbps`);
 if(r.protocol==='tcp'&&r.retransmits!=null)lines.push(`TCP 重传 ${r.retransmits} 次`);
 if(r.rtt_mean_ms!=null)lines.push(`RTT 均值 ${Number(r.rtt_mean_ms).toFixed(2)} ms · 范围 ${Number(r.rtt_min_ms).toFixed(2)}–${Number(r.rtt_max_ms).toFixed(2)} ms`);
 const c=r.test_config;
 if(c)lines.push(r.protocol==='tcp'?`${c.parallel||1} 连接 · ${c.tcp_bandwidth?c.tcp_bandwidth+' Mbps 总限速':'不限速'} · 预热 ${c.omit||0} 秒`:`目标 ${c.bandwidth} Mbps · ${c.udp_length||1200} 字节/包 · 预热 ${c.omit||0} 秒`);
 return lines.map(s=>`<small>${esc(s)}</small>`).join('');
}
function openSSHSamples(currentOnly=false){
 const entries=[...sshSampleRounds.entries()].filter(([,r])=>!currentOnly||r.job===speedCurrent.job);
 if(!entries.length)return;
 sshSampleSelection=entries.at(-1)[0];renderSSHSamples();showSSHDialog('sshSamplesPanel');
}
function renderSSHSamples(){
 const select=$('sshSampleSelect'),entries=[...sshSampleRounds.entries()].reverse();
 const keys=entries.map(([key])=>key).join('|');
 if(select.dataset.keys!==keys){select.innerHTML=entries.map(([key,r])=>`<option value="${esc(key)}">${esc(when(r.time))} · ${esc(r.protocol.toUpperCase())} · ${esc(sshFlow(r))}</option>`).join('');select.dataset.keys=keys}
 if(!sshSampleRounds.has(sshSampleSelection))sshSampleSelection=entries[0]?.[0]||'';
 select.value=sshSampleSelection;
 const r=sshSampleRounds.get(sshSampleSelection);if(!r)return;
 const samples=r.samples||[],rates=samples.map(s=>s.mbps),color=r.direction==='download'?'#1760b5':'#b45309';
 $('sshSamplePath').textContent=(r.source_host_id?'以 '+r.source_host_name+' 为参照 · ':'')+sshFlow(r);
 $('sshSamplePath').className='sample-path '+speedDirectionClass(r);
 const metric=(label,value)=>`<div><span>${label}</span><strong>${value}</strong></div>`;
 $('sshSampleSummary').innerHTML=metric(r.status==='complete'?'接收端平均':'当前区间速度',esc(speedRate(r)))+metric('采样最高',rates.length?Math.max(...rates).toFixed(2)+' Mbps':'—')+metric('采样最低',rates.length?Math.min(...rates).toFixed(2)+' Mbps':'—')+metric('有效采样',samples.length+' 个');
 const config=r.test_config;
 $('sshSampleNote').textContent=`${r.direction==='upload'?'采样为发送端速度，最终平均值按接收端统计。':'采样为接收端速度。'}${config?.omit?'预热 '+config.omit+' 秒不计入采样和平均值。':''}“—”表示该端或版本未提供该指标；旧版文本输出的精度由 iperf3 决定。`;
 const max=Math.max(1,...rates),end=Math.max(1,...samples.map(s=>s.end)),x=t=>44+t/end*740,y=v=>150-v/max*130;
 const points=samples.map(s=>`${x((s.start+s.end)/2).toFixed(1)},${y(s.mbps).toFixed(1)}`).join(' ');
 $('sshSampleChart').innerHTML=`<line x1="44" y1="20" x2="44" y2="150" stroke="#cad5e2"/><line x1="44" y1="150" x2="784" y2="150" stroke="#cad5e2"/><text x="40" y="16" text-anchor="end">${max.toFixed(1)}</text><text x="40" y="153" text-anchor="end">0</text><text x="44" y="174">0 秒</text><text x="784" y="174" text-anchor="end">${end.toFixed(1)} 秒</text><polyline fill="none" stroke="${color}" stroke-width="2.5" points="${points}"/>${samples.map(s=>`<circle cx="${x((s.start+s.end)/2)}" cy="${y(s.mbps)}" r="3" fill="${color}"><title>${s.start.toFixed(2)}–${s.end.toFixed(2)} 秒：${s.mbps.toFixed(2)} Mbps</title></circle>`).join('')}`;
 const number=(value,suffix='')=>value==null?'—':Number(value).toFixed(2)+suffix;
 $('sshSampleRows').innerHTML=samples.map(s=>`<tr><td>${s.start.toFixed(2)}–${s.end.toFixed(2)} 秒</td><td class="${speedDirectionClass(r)}"><b>${s.mbps.toFixed(2)} Mbps</b></td><td>${esc(bytes(s.bytes))}</td><td>${s.retransmits??'—'}</td><td>${number(s.lost_percent,'%')}</td><td>${number(s.jitter_ms,' ms')}</td></tr>`).join('');
}
$('sshSamplesOpen').onclick=()=>openSSHSamples();
$('sshResultSamples').onclick=()=>openSSHSamples(true);
$('sshSamplesClose').onclick=()=>$('sshSamplesPanel').close();
$('sshSampleSelect').onchange=()=>{sshSampleSelection=$('sshSampleSelect').value;renderSSHSamples()};
