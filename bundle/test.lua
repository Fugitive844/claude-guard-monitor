package.path='/usr/libexec/claude-guard/?.lua;'..package.path
local G=require('guard')
local function eq(a,b) assert(a==b,tostring(a)..' != '..tostring(b)) end
eq(G.ip('160.79.105.255/21'),'160.79.104.0/21')
eq(G.ip('2607:6bc0::/32'),'2607:6bc0:0:0:0:0:0:0/32')
eq(G.ip('300.1.1.1'),nil); eq(G.ip('1::2::3'),nil)
eq(G.public('0.0.0.0/0'),nil); eq(G.public('192.168.10.254'),nil); eq(G.public('::1'),nil)
eq(G.public('1.1.1.1'), '1.1.1.1')
eq(G.match('api.claude.ai',{{kind='suffix',value='claude.ai'}}),true)
eq(G.match('notclaude.ai',{{kind='suffix',value='claude.ai'}}),false)
eq(G.match('api.claude.ai',{{kind='full',value='claude.ai'}}),false)
eq(G.match('notclaude.ai',{{kind='keyword',value='claude.ai'}}),true)
local q='\006claude\002ai\000\000\001\000\001'
local reply='\018\052\129\128\000\001\000\001\000\000\000\000'..q..'\192\012\000\001\000\001\000\000\000\060\000\004\104\018\001\002'
local name,ips=G.dns(reply); eq(name,'claude.ai'); eq(ips[1],'104.18.1.2')
eq(pcall(G.dns,reply:sub(1,-2)),false)
eq(pcall(G.dns,'\000\000\129\128\000\001\000\000\000\000\000\000\192\012'),false)
local p={domains={{kind='suffix',value='claude.ai'}},static4={['160.79.104.0/21']=true},static6={}}
local a,b=G.sets(p,{['api.claude.ai']={['104.18.1.2']=1},['unrelated.example']={['8.8.8.8']=1}})
eq(#a,2); eq(#b,0)
local nft=G.nft(a,b,false)
assert(nft:find('hook forward priority %-10')); assert(not nft:find('hook output')); assert(not nft:find('flush ruleset',1,true))
assert(not nft:find('inet passwall',1,true)); assert(not nft:find('inet fw4',1,true))
-- Model a committed UCI change without modifying the production UCI store.
local realuci=package.loaded.uci
local state={['@global[0]']={node='shunt',enabled='1'},shunt={claude='nodeA'},
  claude={['.type']='shunt_rules',domain_list='domain:claude.ai',ip_list='160.79.104.0/21'},
  nodeA={['.type']='nodes',protocol='trojan',remarks='A'},nodeB={['.type']='nodes',protocol='vless',remarks='B'}}
package.loaded.uci={cursor=function() return {get_all=function(_,_,section) return state[section] end,get=function() return nil end} end}
eq(G.policy().selected_node_name,'A')
state.shunt.claude='nodeB'; eq(G.policy().selected_node_name,'B')
state.claude.domain_list='full:claude.com'; local newer=G.policy()
eq(G.match('claude.ai',newer.domains),false); eq(G.match('claude.com',newer.domains),true)
state.nodeB=nil; eq(G.policy().selected_node_valid,false)
state.claude.domain_list='regexp:.*'; eq(pcall(G.policy),false)
package.loaded.uci=realuci
arg[1]='--selftest'
local O=dofile('/usr/libexec/claude-guard/observer.lua')
local ethernet=string.rep('\000',12)..'\008\000'
local ipv4='\069\000\000\100\000\000\000\000\064\017\000\000\127\000\000\001\127\000\000\001'
local length=8+#reply
local udp='\059\249\160\000'..string.char(math.floor(length/256),length%256)..'\000\000'
local captured=ethernet..ipv4..udp..reply
local got,addresses=O.packet_dns(captured,1)
eq(got,'claude.ai'); eq(addresses[1],'104.18.1.2')
eq(O.packet_dns(captured:sub(1,26)..'\008\008\008\008'..captured:sub(31),1),nil)
eq(pcall(O.packet_dns,captured:sub(1,30),1),false)
print('PASS: IP/CIDR validation, DNS compression/errors, matching, deletion pruning, nft isolation, UCI/node changes, PCAP decoding/source filtering')
