local action, id, port, password = arg[1], arg[2], tonumber(arg[3]), arg[4]
arg = {}
local uci = require('uci').cursor()
local json = require('luci.jsonc')
local fs = require('nixio.fs')

local protocols = {vless=true, vmess=true, trojan=true, shadowsocks=true, socks=true, http=true, hysteria2=true, tuic=true, anytls=true}
local function reason(n)
    if not n.protocol or n.protocol:sub(1, 1) == '_' then return '分流或负载均衡配置' end
    if n.type ~= 'Xray' and n.type ~= 'sing-box' then return '暂不支持此核心' end
    if not protocols[n.protocol] then return '暂不支持此协议' end
    if n.chain_proxy and n.chain_proxy ~= '0' and n.chain_proxy ~= '' then return '链式节点请单独测试其出口节点' end
    local bin = n.type == 'Xray' and '/usr/bin/xray' or '/usr/bin/sing-box'
    if not fs.access(bin) then return '未安装代理核心' end
    return ''
end

if action == 'list' then
    local nodes = {}
    uci:foreach('passwall', 'nodes', function(n)
        local why = reason(n)
        nodes[#nodes+1] = {id=n['.name'], name=n.remarks or n['.name'], group=n.group or '未分组', core=n.type or '',
            host=n.address or '', protocol=n.protocol or '', transport=n.transport or '', supported=why == '', reason=why}
    end)
    print(json.stringify(nodes))
    return
end

assert(action == 'config' and id and id:match('^[%w_]+$') and port and port > 1024 and password)
local node = assert(uci:get_all('passwall', id))
assert(node['.type'] == 'nodes' and reason(node) == '')
local core = node.type == 'Xray' and 'xray' or 'sing-box'
local generator = require('luci.passwall.util_' .. core)
local outbound = assert(generator.gen_outbound('cg-speed', node, 'proxy', {run_socks_instance=false}))
local function clean(t)
    for k,v in pairs(t) do
        if type(k) == 'string' and k:sub(1,1) == '_' then t[k] = nil
        elseif type(v) == 'table' then clean(v) end
    end
end
clean(outbound)
outbound.tag = 'proxy'
local config
if core == 'xray' then
    outbound.proxySettings = nil
    outbound.streamSettings = outbound.streamSettings or {}
    outbound.streamSettings.sockopt = outbound.streamSettings.sockopt or {}
    outbound.streamSettings.sockopt.mark = 255
    config = {log={loglevel='none'}, inbounds={{listen='127.0.0.1',port=port,protocol='socks',
        settings={auth='password',accounts={{user='speed',pass=password}},udp=false}}}, outbounds={outbound}}
else
    outbound.detour = nil
    outbound.routing_mark = 255
    outbound.domain_resolver = 'bootstrap'
    config = {log={disabled=true}, dns={servers={{type='local',tag='bootstrap'}}},
        inbounds={{type='socks',listen='127.0.0.1',listen_port=port,users={{username='speed',password=password}}}},
        outbounds={outbound}, route={final='proxy',default_domain_resolver='bootstrap',default_mark=255}}
end
print(json.stringify(config))
