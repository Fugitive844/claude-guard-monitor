package.path='/usr/libexec/claude-guard/?.lua;'..package.path
local T=require('tls'); local G=require('guard')
local function eq(a,b) assert(a==b,tostring(a)..' != '..tostring(b)) end
local function be(n,len) local s='' for _=1,len do s=string.char(n%256)..s; n=math.floor(n/256) end return s end
local function handshake(host,ech)
  local names='\000'..be(#host,2)..host
  local ext='\000\000'..be(#names+2,2)..be(#names,2)..names
  if ech then ext=ext..be(65037,2)..'\000\000' end
  local body='\003\003'..string.rep('\000',32)..'\000\000\002\019\001\001\000'..be(#ext,2)..ext
  return '\001'..be(#body,3)..body
end
local function record(s) return '\022\003\001'..be(#s,2)..s end
local h=handshake('Api.Claude.AI'); local wire=record(h)
eq(T.records(wire),'api.claude.ai'); eq(T.records(wire:sub(1,-2)),nil)
eq(T.records(record(h:sub(1,20))..record(h:sub(21))),'api.claude.ai')
local name,ech=T.records(record(handshake('cover.example',true))); eq(name,'cover.example'); eq(ech,true)
T.feed('segments',1000,'',true,false,100,'203.0.113.250')
eq(T.feed('segments',1021,wire:sub(21),false,false,100,'203.0.113.250'),nil)
local got,ips,source=T.feed('segments',1001,wire:sub(1,20),false,false,100,'203.0.113.250')
eq(got,'api.claude.ai'); eq(ips[1],'203.0.113.250'); eq(source,'sni')
T.feed('retransmit',2000,wire:sub(1,25),false,false,100,'203.0.113.250')
eq(T.feed('retransmit',2000,wire:sub(1,25),false,false,100,'203.0.113.250'),nil)
eq(T.feed('retransmit',2025,wire:sub(26),false,false,100,'203.0.113.250'),'api.claude.ai')
eq(G.match('api.claude.ai',{{kind='suffix',value='claude.ai'}}),true)
eq(G.match('claude.ai.example.net',{{kind='suffix',value='claude.ai'}}),false)
local ethernet=string.rep('\000',12)..'\008\000'
local tcp=be(50001,2)..be(443,2)..be(3000,4)..string.rep('\000',4)..'\080\024'..string.rep('\000',6)
local ip='\069\000'..be(20+#tcp+#wire,2)..string.rep('\000',4)..'\064\006\000\000\192\168\001\087\203\000\113\250'
local n,a=T.packet(ethernet..ip..tcp..wire,1,100)
eq(n,'api.claude.ai'); eq(a[1],'203.0.113.250')
eq(T.records('\023\003\003\000\000'),false)
eq(pcall(T.records,'\022\003\001\255\255'),false)
print('PASS: visible SNI, subdomains, TCP segmentation/reordering/retransmission, split TLS records, ECH outer-name limit, malformed bounds, IPv4 PCAP')
