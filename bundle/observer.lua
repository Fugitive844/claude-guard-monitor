-- Consume PCAP from stdin; retain only matching DNS/SNI names and public addresses.
package.path='/usr/libexec/claude-guard/?.lua;'..package.path
local G=require('guard'); local nixio=require('nixio'); local fs=require('nixio.fs'); local json=require('luci.jsonc')
local TLS=require('tls')
local function uint(s,p,n,le)
  local v=0
  if le then for i=p+n-1,p,-1 do v=v*256+assert(s:byte(i)) end
  else for i=p,p+n-1 do v=v*256+assert(s:byte(i)) end end
  return v
end
local function packet_dns(s,link)
  local p,proto
  if link==113 then p=17; proto=uint(s,15,2)
  elseif link==276 then p=21; proto=uint(s,1,2)
  elseif link==1 then p=15; proto=uint(s,13,2)
  else return end
  if proto==33024 then proto=uint(s,p+2,2); p=p+4 end
  local source,udp
  if proto==2048 then
    local h=(assert(s:byte(p))%16)*4; if h<20 or s:byte(p+9)~=17 or uint(s,p+6,2)%8192~=0 then return end
    source=table.concat({s:byte(p+12,p+15)},'.'); udp=p+h
  elseif proto==34525 then
    if s:byte(p+6)~=17 then return end -- extension headers not guessed
    local a={} for i=p+8,p+23,2 do a[#a+1]=string.format('%x',uint(s,i,2)) end
    source=table.concat(a,':'); udp=p+40
  else return end
  -- Trust only responses emitted by the router's existing local DNS services.
  if not ({['127.0.0.1']=true,['192.168.10.1']=true,['192.168.20.1']=true,['0:0:0:0:0:0:0:1']=true})[source] then return end
  local port=uint(s,udp,2)
  if not ({[53]=true,[11400]=true,[15353]=true,[15355]=true})[port] then return end
  local len=uint(s,udp+4,2); if len<20 or udp+len-1>#s then return end
  return G.dns(s:sub(udp+8,udp+len-1))
end
local function main()
  G.ensure(); local header=io.stdin:read(24); assert(header and #header==24,'PCAP header missing')
  local magic=header:sub(1,4); local le=magic=='\212\195\178\161' or magic=='\077\060\178\161'
  assert(le or magic=='\161\178\195\212' or magic=='\161\178\060\077','PCAP format')
  local link=uint(header,21,4,le); assert(link==1 or link==113 or link==276,'unsupported PCAP link type')
  local loaded=0; local policy; local last={}; local seq=0; local emitted=0; local minute=0
  while true do
    local h=io.stdin:read(16); if not h or #h<16 then return end
    local n=uint(h,9,4,le); assert(n<=65535,'oversized capture')
    local data=io.stdin:read(n); if not data or #data~=n then return end
    local now=os.time()
    if now-loaded>=1 then policy=G.decode(G.read(G.RUN..'/policy.json')); loaded=now end
    if policy then
      local ok,name,ips=pcall(packet_dns,data,link)
      local source='dns'; local ech=false
      if not ok or not name then ok,name,ips,source,ech=pcall(TLS.packet,data,link,now) end
      if ok and name and #ips>0 and G.match(name,policy.domains) then
        local clean={} for _,ip in ipairs(ips) do ip=G.public(ip); if ip then clean[#clean+1]=ip end end
        table.sort(clean); local key=name..':'..table.concat(clean,',')
        if now-minute>=60 then minute=now; emitted=0; last={} end
        if #clean>0 and not last[key] and emitted<128 then
          seq=(seq+1)%128; last[key]=true; emitted=emitted+1
          G.atomic(G.RUN..'/events/event-'..nixio.getpid()..'-'..seq..'.json',json.stringify({name=name,ips=clean,source=source}))
          G.atomic(G.RUN..'/last-observation.json',json.stringify({time=now,name=name,ips=clean,source=source,ech_outer=ech or false}))
        end
      end
    end
  end
end
if arg[1]=='--selftest' then return {packet_dns=packet_dns,uint=uint} end
main()
