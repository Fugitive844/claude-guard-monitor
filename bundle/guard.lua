-- Independent, bounded Claude destination guard. Never writes PassWall/DHCP/fw4 configuration.
local M = {}
local json = require('luci.jsonc')
local fs = require('nixio.fs')
local nixio = require('nixio')
local ROOT = '/etc/claude-guard'
local RUN = '/var/run/claude-guard'
local TABLE = 'claude_guard'
local function quote(s) return "'" .. tostring(s):gsub("'", "'\\''") .. "'" end
local function read(p) return fs.readfile(p) end
local function decode(s) if not s then return nil end local ok,v=pcall(json.parse,s); if ok then return v end end
local function atomic(p,s)
  local t=p..'.tmp.'..nixio.getpid(); assert(fs.writefile(t,s)); fs.chmod(t,'600'); assert(fs.rename(t,p))
end
local function run(cmd)
  local p=assert(io.popen(cmd..' 2>/dev/null')); local s=p:read('*a'); p:close(); return s
end
local function sortedkeys(t) local r={} for k in pairs(t) do r[#r+1]=k end table.sort(r); return r end
local function trim(s) return (s:gsub('^%s+',''):gsub('%s+$','')) end

function M.ip(value)
  if type(value)~='string' or value:find('[^%x%.:/]') then return nil end
  local addr,prefix=value:match('^([^/]+)/(%d+)$'); addr=addr or value
  local bytes,family={}
  if addr:find(':',1,true) then
    family=6; if addr:find('%.') then return nil end
    local left,right=addr:match('^(.-)::(.-)$'); local groups={}
    local function parts(x)
      local r={} if x=='' then return r end
      if x:sub(1,1)==':' or x:sub(-1)==':' or x:find('::',1,true) then return nil end
      for g in x:gmatch('[^:]+') do if #g>4 or not g:match('^%x+$') then return nil end r[#r+1]=tonumber(g,16) end
      return r
    end
    if left then
      local a,b=parts(left),parts(right); if not a or not b or #a+#b>=8 then return nil end
      for _,v in ipairs(a) do groups[#groups+1]=v end
      for _=1,8-#a-#b do groups[#groups+1]=0 end
      for _,v in ipairs(b) do groups[#groups+1]=v end
    else groups=parts(addr); if not groups or #groups~=8 then return nil end end
    for _,v in ipairs(groups) do bytes[#bytes+1]=math.floor(v/256); bytes[#bytes+1]=v%256 end
  else
    family=4
    local a,b,c,d=addr:match('^(%d+)%.(%d+)%.(%d+)%.(%d+)$'); if not a then return nil end
    for _,v in ipairs({a,b,c,d}) do v=tonumber(v); if v>255 then return nil end bytes[#bytes+1]=v end
  end
  local bits=family==4 and 32 or 128; prefix=prefix and tonumber(prefix) or bits
  if prefix<0 or prefix>bits then return nil end
  for i=1,#bytes do local keep=math.max(0,math.min(8,prefix-(i-1)*8)); local unit=2^(8-keep); bytes[i]=math.floor(bytes[i]/unit)*unit end
  local canonical
  if family==4 then canonical=table.concat(bytes,'.') else
    local g={} for i=1,16,2 do g[#g+1]=string.format('%x',bytes[i]*256+bytes[i+1]) end canonical=table.concat(g,':')
  end
  if prefix<bits then canonical=canonical..'/'..prefix end
  return canonical,family,bytes,prefix
end
function M.public(value)
  local ip,f,bits,p=M.ip(value); if not ip then return nil end
  if f==4 then
    if p<8 or bits[1]==0 or bits[1]==10 or bits[1]==127 or bits[1]>=224 or
      (bits[1]==169 and bits[2]==254) or (bits[1]==172 and bits[2]>=16 and bits[2]<=31) or
      (bits[1]==192 and bits[2]==168) or (bits[1]==100 and bits[2]>=64 and bits[2]<=127) then return nil end
  elseif p<16 or bits[1]<32 or bits[1]>63 then return nil end
  return ip,f
end
local function domain(s)
  if #s>253 or not s:match('^[a-z0-9][a-z0-9%.%-]*$') or not s:find('%.') or s:find('%.%.') then return false end
  for label in s:gmatch('[^.]+') do if #label>63 then return false end end
  return true
end
function M.match(name,rules)
  name=name:lower():gsub('%.$','')
  for _,r in ipairs(rules) do
    if (r.kind=='full' and name==r.value) or
      (r.kind=='suffix' and (name==r.value or name:sub(-#r.value-1)=='.'..r.value)) or
      (r.kind=='keyword' and name:find(r.value,1,true)) then return true end
  end
  return false
end
local function adddomain(out,line,expanded)
  local kind,val=line:match('^(%w+):(.*)$')
  if not kind then kind=expanded and 'domain' or 'keyword'; val=line end
  if not domain(val) or not ({domain=true,full=true,keyword=true})[kind] then error('unsupported domain expression: '..line) end
  out[#out+1]={kind=kind=='domain' and 'suffix' or kind,value=val}
end
function M.policy()
  local c=require('uci').cursor(); local g=c:get_all('passwall','@global[0]') or {}
  local rule=c:get_all('passwall','claude'); assert(rule and rule['.type']=='shunt_rules','claude rule missing; retaining previous guard')
  local shunt=c:get_all('passwall',g.node or '') or {}; local id=shunt.claude
  local node=id and c:get_all('passwall',id) or nil
  local p={domains={},static4={},static6={},rule='claude',selected_node_id=id or '',selected_node_name=node and node.remarks or '',
    selected_node_valid=node~=nil and node['.type']=='nodes' and not (node.protocol or ''):match('^_'),
    passwall_enabled=g.enabled=='1',shunt_id=g.node or '',node_binding_enforced=false}
  local assets=c:get('passwall','@global_rules[0]','v2ray_location_asset') or '/usr/share/v2ray/'
  for line in (rule.domain_list or ''):gmatch('[^\r\n]+') do
    line=trim(line):lower()
    if line~='' and line:sub(1,1)~='#' then
      if line:match('^geosite:') then
        local name=line:sub(9); assert(name:match('^[a-z0-9_@%-]+$'),'invalid geosite name')
        local output=run('geoview -type geosite -input '..quote(assets:gsub('/$','')..'/geosite.dat')..' -list '..quote(name)..' -regex')
        assert(output~='','geosite expansion failed')
        for v in output:gmatch('[^\r\n]+') do adddomain(p.domains,trim(v),true) end
      else adddomain(p.domains,line,false) end
    end
  end
  for line in (rule.ip_list or ''):gmatch('[^\r\n]+') do
    line=trim(line)
    if line~='' and line:sub(1,1)~='#' then
      local ip,f=M.public(line); assert(ip,'unsupported or non-public IP expression: '..line)
      p[f==4 and 'static4' or 'static6'][ip]=true
    end
  end
  -- Do not echo credentials, even when selected node changes.
  return p
end

local function u16(s,i) local a,b=s:byte(i,i+1); if not b then error('short DNS') end return a*256+b end
local function dnsname(s,pos,depth)
  depth=depth or 0; if depth>16 then error('DNS pointer loop') end
  local names={}
  local cursor=pos; local ending
  for _=1,128 do
    local n=s:byte(cursor); if not n then error('short DNS name') end
    if n==0 then return table.concat(names,'.'),ending or cursor+1 end
    if n>=192 then
      local ptr=(n-192)*256+assert(s:byte(cursor+1))+1
      local v=dnsname(s,ptr,depth+1); names[#names+1]=v
      return table.concat(names,'.'),ending or cursor+2
    end
    if n>63 or cursor+n>#s then error('invalid DNS label') end
    names[#names+1]=s:sub(cursor+1,cursor+n):lower(); cursor=cursor+n+1
  end
  error('long DNS name')
end
function M.dns(s)
  assert(#s>=12 and #s<=65535,'DNS size'); assert(s:byte(3)>=128,'not response')
  assert(s:byte(4)%16==0,'DNS error'); assert(u16(s,5)==1,'DNS question count')
  local name,pos=dnsname(s,13); pos=pos+4
  local answers={}; local count=u16(s,7); assert(count<=512,'too many answers')
  for _=1,count do
    local owner; owner,pos=dnsname(s,pos)
    local typ,cls,len=u16(s,pos),u16(s,pos+2),u16(s,pos+8); pos=pos+10
    assert(pos+len-1<=#s,'short DNS record')
    if cls==1 and typ==1 and len==4 then
      local a={s:byte(pos,pos+3)}; answers[#answers+1]=table.concat(a,'.')
    elseif cls==1 and typ==28 and len==16 then
      local a={} for i=pos,pos+15,2 do a[#a+1]=string.format('%x',u16(s,i)) end answers[#answers+1]=table.concat(a,':')
    end
    pos=pos+len
  end
  return name,answers
end
function M.query(name,typ)
  assert(domain(name),'invalid query domain')
  local id=math.random(0,65535); local q=string.char(math.floor(id/256),id%256,1,0,0,1,0,0,0,0,0,0)
  for label in name:gmatch('[^.]+') do q=q..string.char(#label)..label end
  q=q..string.char(0,0,typ,0,1)
  local sock=assert(nixio.socket('inet','dgram')); sock:setsockopt('socket','rcvtimeo',1)
  if not sock:connect('127.0.0.1',15353) then sock:close(); return nil end
  sock:send(q); local answer=sock:recv(65535); sock:close()
  if not answer or answer:sub(1,2)~=q:sub(1,2) then return nil end
  local ok,got,ips=pcall(M.dns,answer); if ok and got==name then return ips end
end
function M.sets(policy,cache)
  local a,b={},{}
  for k in pairs(policy.static4) do a[k]=true end; for k in pairs(policy.static6) do b[k]=true end
  for name,ips in pairs(cache) do if M.match(name,policy.domains) then
    for k in pairs(ips) do local ip,f=M.public(k); if ip then (f==4 and a or b)[ip]=true end end
  end end
  return sortedkeys(a),sortedkeys(b)
end
function M.nft(a,b,exists)
  local lines={}
  if not exists then
    lines={ 'add table inet '..TABLE,
      'add set inet '..TABLE..' targets4 { type ipv4_addr; flags interval; auto-merge; }',
      'add set inet '..TABLE..' targets6 { type ipv6_addr; flags interval; auto-merge; }',
      'add chain inet '..TABLE..' forward_guard { type filter hook forward priority -10; policy accept; }',
      'add rule inet '..TABLE..' forward_guard ct direction original ip daddr @targets4 counter drop comment "Claude known destinations: no direct forwarding"',
      'add rule inet '..TABLE..' forward_guard ct direction original ip6 daddr @targets6 counter drop comment "Claude known destinations IPv6: no direct forwarding"' }
  else lines={'flush set inet '..TABLE..' targets4','flush set inet '..TABLE..' targets6'} end
  if #a>0 then lines[#lines+1]='add element inet '..TABLE..' targets4 { '..table.concat(a,', ')..' }' end
  if #b>0 then lines[#lines+1]='add element inet '..TABLE..' targets6 { '..table.concat(b,', ')..' }' end
  return table.concat(lines,'\n')..'\n'
end
local function ensure() fs.mkdirr(RUN..'/events'); fs.chmod(RUN,'700'); fs.chmod(RUN..'/events','700') end
local function tableexists() return run('nft list table inet '..TABLE)~='' end
local function apply(a,b)
  local batch=M.nft(a,b,tableexists()); atomic(RUN..'/update.nft',batch)
  assert(os.execute('nft -c -f '..quote(RUN..'/update.nft')..' >/dev/null 2>&1')==0,'nft check failed; previous sets retained')
  assert(os.execute('nft -f '..quote(RUN..'/update.nft')..' >/dev/null 2>&1')==0,'nft apply failed; previous sets retained')
end
local function merge(cache,name,ips,policy)
  if not M.match(name,policy.domains) then return false end
  local changed=false; cache[name]=cache[name] or {}
  local count=0; for _ in pairs(cache[name]) do count=count+1 end
  for _,v in ipairs(ips) do
    local ip=M.public(v)
    if ip and not cache[name][ip] and count<256 then cache[name][ip]=os.time(); count=count+1; changed=true end
  end
  return changed
end
local function runtime(policy)
  local r=decode(read('/tmp/etc/passwall/acl/default/global.json')); local tag
  for _,rule in ipairs(r and r.routing and r.routing.rules or {}) do
    if rule.ruleTag=='claude Domains' or rule.ruleTag=='claude IP' then tag=rule.outboundTag; break end
  end
  return {claude_route_present=tag~=nil,claude_outbound_tag=tag or '',selected_node_name=policy.selected_node_name,
    node_binding_enforced=false,warning='Observation only: runtime route presence is not proof of node identity or connectivity'}
end
function M.main(mode)
  ensure()
  if mode=='status' then print(read(RUN..'/status.json') or '{}'); return end
  if mode=='remove' then
    if tableexists() then assert(os.execute('nft delete table inet '..TABLE)==0) end; return
  end
  local enforce=mode=='enforce'; assert(enforce or mode=='observe' or mode=='check','invalid mode')
  local cache=decode(read(RUN..'/learned-live.json')) or decode(read(ROOT..'/learned.json')) or {}; local previous,policy,lastsignature,lastsets
  local lastpolicy=0; local lastsave=0; local lasthealth=0; local queryindex=1; local queryround=0; local seeds={}
  local error_message; local changed=false
  while true do
    local now=os.time(); local signature=read('/etc/config/passwall') or ''
    if signature~=lastsignature or now-lastpolicy>=300 then
      local ok,p=pcall(M.policy)
      if ok then
        policy=p; lastsignature=signature; lastpolicy=now; error_message=nil
        seeds={}; local seen={}; for _,d in ipairs(p.domains) do if not seen[d.value] then seen[d.value]=true; seeds[#seeds+1]=d.value end end
        table.sort(seeds); queryindex=1; queryround=now
        for name in pairs(cache) do if not M.match(name,p.domains) then cache[name]=nil; changed=true end end
        atomic(RUN..'/policy.json',json.stringify(p)); atomic(ROOT..'/last-policy.json',json.stringify(p))
      else error_message=tostring(p); lastpolicy=now; lastsignature=signature end
    end
    policy=policy or decode(read(ROOT..'/last-policy.json'))
    if policy then
      local n=0
      for file in fs.dir(RUN..'/events') do
        if file:match('^event%-%d+%-%d+%.json$') then
          local path=RUN..'/events/'..file; local e=decode(read(path)); fs.unlink(path)
          if e and type(e.name)=='string' and type(e.ips)=='table' then changed=merge(cache,e.name,e.ips,policy) or changed end
          n=n+1; if n>=128 then break end
        end
      end
      if mode~='check' and queryindex<=#seeds and now>=queryround then
        local name=seeds[queryindex]; queryindex=queryindex+1
        for _,typ in ipairs({1,28}) do local ips=M.query(name,typ); if ips then changed=merge(cache,name,ips,policy) or changed end end
      end
      if queryindex>#seeds and now-queryround>=300 then queryindex=1; queryround=now end
      if changed then atomic(RUN..'/learned-live.json',json.stringify(cache)) end
      local a,b=M.sets(policy,cache); local fingerprint=table.concat(a,',')..'|'..table.concat(b,',')
      local present=tableexists()
      if enforce and (fingerprint~=lastsets or not present) then
        local ok,err=pcall(apply,a,b)
        if ok then lastsets=fingerprint else error_message=tostring(err) end
      elseif mode=='check' then
        atomic(RUN..'/candidate.nft',M.nft(a,b,false))
        assert(os.execute('nft -c -f '..quote(RUN..'/candidate.nft')..' >/dev/null 2>&1')==0,'candidate nft syntax failed')
      end
      if now-lasthealth>=5 or mode=='check' then
        atomic(RUN..'/status.json',json.stringify({version=1,time=now,mode=mode,rule='claude',domain_expressions=#policy.domains,
          ipv4_targets=#a,ipv6_targets=#b,selected_node_id=policy.selected_node_id,selected_node_name=policy.selected_node_name,
          selected_node_valid=policy.selected_node_valid,firewall_table_present=tableexists(),
          dns_integration='passive UDP DNS plus visible TLS SNI and bounded seed lookup; not an inline gate',
          sni_integration='TCP/443 visible ClientHello; passive learning, initial handshake not protected; ECH inner name and QUIC unsupported',
          strict_fail_closed=false,node_binding_enforced=false,runtime=runtime(policy),error=error_message,
          limitations={'unseen DNS/DoH IPs may bypass','no atomic coordination with PassWall','no protection for router-originated direct traffic','shared CDN IPs can affect other sites'}}))
        lasthealth=now
      end
      if changed and now-lastsave>=60 then atomic(ROOT..'/learned.json',json.stringify(cache)); changed=false; lastsave=now end
    end
    if mode=='check' then assert(policy,error_message or 'no policy'); print(read(RUN..'/status.json')); return end
    nixio.nanosleep(1)
  end
end
M.read=read; M.decode=decode; M.atomic=atomic; M.RUN=RUN; M.ensure=ensure
return M
