-- Bounded passive TLS ClientHello/SNI parser. No decryption and no first-packet guarantee.
local T={}; local flows={}; local cleanup_at=0
local function u(s,p,n)
  local v=0; for i=p,p+n-1 do v=v*256+assert(s:byte(i),'truncated packet') end; return v
end
local function hello(s)
  assert(#s>=38,'short ClientHello')
  local p=35; p=p+1+u(s,p,1)
  local cs=u(s,p,2); assert(cs>=2 and cs%2==0,'cipher list'); p=p+2+cs
  local cm=u(s,p,1); p=p+1+cm
  if p>#s then return false end
  local stop=p+2+u(s,p,2); p=p+2; assert(stop-1<=#s,'extension bounds')
  local name,ech
  while p<stop do
    local typ,len=u(s,p,2),u(s,p+2,2); p=p+4; assert(p+len<=stop,'extension length')
    if typ==65037 then ech=true end
    if typ==0 then
      local endnames=p+2+u(s,p,2); assert(endnames<=p+len,'SNI list bounds')
      local q=p+2
      while q<endnames do
        local nt,nl=u(s,q,1),u(s,q+1,2); q=q+3; assert(q+nl<=endnames,'SNI name bounds')
        if nt==0 then
          local candidate=s:sub(q,q+nl-1):lower():gsub('%.$','')
          assert(#candidate>0 and #candidate<=253 and candidate:match('^[a-z0-9][a-z0-9%.%-]*$'),'invalid SNI')
          name=candidate
        end
        q=q+nl
      end
    end
    p=p+len
  end
  return name or false,ech or false
end
function T.records(data)
  local p=1; local hand=''
  while p+4<=#data do
    if data:byte(p)~=22 or data:byte(p+1)~=3 then return false end
    local len=u(data,p+3,2); assert(len<=18432,'TLS record too large')
    if p+4+len>#data then return nil end
    hand=hand..data:sub(p+5,p+4+len); assert(#hand<=32768,'handshake too large')
    if #hand>=4 then
      if hand:byte(1)~=1 then return false end
      local need=u(hand,2,3); assert(need<=32764,'ClientHello too large')
      if #hand>=need+4 then return hello(hand:sub(5,4+need)) end
    end
    p=p+5+len
  end
end
function T.feed(key,seq,payload,syn,fin,now,destination)
  if now~=cleanup_at then
    local count=0
    for k,f in pairs(flows) do if now-f.time>10 then flows[k]=nil else count=count+1 end end
    if count>128 then flows={} end
    cleanup_at=now
  end
  if fin then flows[key]=nil; return end
  if not flows[key] then
    local count=0; for _ in pairs(flows) do count=count+1 end
    if count>=128 then return end
  end
  if syn then flows[key]={base=(seq+1)%4294967296,buf='',pending={},time=now}; seq=(seq+1)%4294967296 end
  if #payload==0 then return end
  local f=flows[key]
  if not f then
    if payload:byte(1)~=22 or payload:byte(2)~=3 then return end
    f={base=seq,buf='',pending={},time=now}; flows[key]=f
  end
  local offset=(seq-f.base)%4294967296
  if offset+#payload>32768 then flows[key]=nil; return end
  f.pending[offset]=payload
  local n=0; for _ in pairs(f.pending) do n=n+1 end
  if n>24 then flows[key]=nil; return end
  while true do
    local advance=false
    for start,chunk in pairs(f.pending) do
      if start<=#f.buf then
        local overlap=math.min(#chunk,#f.buf-start)
        if overlap>0 and f.buf:sub(start+1,start+overlap)~=chunk:sub(1,overlap) then flows[key]=nil; return end
        if start+#chunk>#f.buf then f.buf=f.buf..chunk:sub(#f.buf-start+1); advance=true end
        f.pending[start]=nil
      end
    end
    if not advance then break end
  end
  local ok,name,ech=pcall(T.records,f.buf)
  if not ok or name~=nil then flows[key]=nil end
  if ok and name then return name,{destination},'sni',ech end
end
function T.packet(s,link,now)
  local p,proto
  if link==113 then p=17; proto=u(s,15,2)
  elseif link==276 then p=21; proto=u(s,1,2)
  elseif link==1 then p=15; proto=u(s,13,2)
  else return end
  if proto==33024 then proto=u(s,p+2,2); p=p+4 end
  local src,dst,tcp,ending
  if proto==2048 then
    local h=u(s,p,1)%16*4; if h<20 or s:byte(p+9)~=6 or u(s,p+6,2)%16384~=0 then return end
    src=table.concat({s:byte(p+12,p+15)},'.'); dst=table.concat({s:byte(p+16,p+19)},'.')
    tcp=p+h; ending=p+u(s,p+2,2)-1
  elseif proto==34525 then
    if s:byte(p+6)~=6 then return end -- IPv6 extension/fragment headers intentionally not guessed
    local a,b={},{}
    for i=0,14,2 do a[#a+1]=string.format('%x',u(s,p+8+i,2)); b[#b+1]=string.format('%x',u(s,p+24+i,2)) end
    src=table.concat(a,':'); dst=table.concat(b,':'); tcp=p+40; ending=tcp+u(s,p+4,2)-1
  else return end
  if ending>#s then return end
  local sport,dport=u(s,tcp,2),u(s,tcp+2,2); if dport~=443 then return end
  local h=math.floor(u(s,tcp+12,1)/16)*4; if h<20 or tcp+h>ending+1 then return end
  local flags=u(s,tcp+13,1); local syn=math.floor(flags/2)%2==1; local fin=flags%2==1 or math.floor(flags/4)%2==1
  return T.feed(src..':'..sport..'>'..dst..':'..dport,u(s,tcp+4,4),s:sub(tcp+h,ending),syn,fin,now,dst)
end
return T
