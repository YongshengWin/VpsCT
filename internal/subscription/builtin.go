package subscription

import "ctlvps/internal/domain"

// builtinMihomoTemplate is used when a subscription has no groups/rules.
// Group entries may use {{all}} or {{all|regex}} to pull in generated nodes.
const builtinMihomoTemplate = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
ipv6: true
unified-delay: true
tcp-concurrent: true
find-process-mode: strict
geodata-mode: true
geo-auto-update: false
geo-update-interval: 24
geox-url:
  geoip: "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geoip.dat"
  geosite: "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat"
  mmdb: "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/country.mmdb"
profile:
  store-selected: true
  store-fake-ip: true
sniffer:
  enable: true
  sniff:
    HTTP: { ports: [80, 8080-8880], override-destination: true }
    TLS: { ports: [443, 8443] }
    QUIC: { ports: [443, 8443] }
dns:
  enable: true
  ipv6: true
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  fake-ip-filter:
    - "*.lan"
    - "*.local"
    - "geosite:cn"
    - "geosite:private"
  default-nameserver:
    - 223.5.5.5
    - 119.29.29.29
  nameserver:
    - https://1.1.1.1/dns-query
    - https://8.8.8.8/dns-query
  proxy-server-nameserver:
    - https://223.5.5.5/dns-query
proxies: []
proxy-groups:
  - name: "⚡️ smart"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Auto.png"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all}}"]
  - name: "☑️ select"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Global.png"
    proxies: ["⚡️ smart", DIRECT, "{{all}}"]
  - name: "🔍 Google"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Google.png"
    proxies: ["🇯🇵 日本", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "▶️ YouTube"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/YouTube.png"
    proxies: ["🇭🇰 香港", "⚡️ smart", "☑️ select", "🇯🇵 日本", "🇸🇬 新加坡", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "🤖 ChatGPT"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/OpenAI.png"
    proxies: ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", DIRECT]
  - name: "📮 Claude"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Sereinfy/Clash@e450a2a33227ca42e56247823839230a0f2d6ab2/icons/anthropic.png"
    proxies: ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", DIRECT]
  - name: "✨ Gemini"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Google.png"
    proxies: ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", DIRECT]
  - name: "🎵 TikTok"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/TikTok.png"
    proxies: ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", DIRECT]
  - name: "🐦 Twitter"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Twitter.png"
    proxies: ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "✈️ Telegram"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Telegram.png"
    proxies: ["🇭🇰 香港", "⚡️ smart", "☑️ select", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "📷 Instagram"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Instagram.png"
    proxies: ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "🐙 GitHub"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Koolson/Qure@master/IconSet/Color/GitHub.png"
    proxies: ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "🎧 Spotify"
    type: select
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/Spotify.png"
    proxies: ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", DIRECT]
  - name: "🇭🇰 香港"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/HK.png"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all|(?i)(香港|HK|Hong ?Kong|🇭🇰)}}"]
  - name: "🇸🇬 新加坡"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/SG.png"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all|(?i)(新加坡|SG|Singapore|🇸🇬)}}"]
  - name: "🇯🇵 日本"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/JP.png"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all|(?i)(日本|JP|Japan|🇯🇵)}}"]
  - name: "🇺🇸 美国"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/US.png"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all|(?i)(美国|US|United ?States|🇺🇸)}}"]
  - name: "🇦🇺 澳大利亚"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/AU.png"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all|(?i)(澳大利亚|澳洲|AU|Australia|🇦🇺)}}"]
  - name: "🇳🇱 荷兰"
    type: url-test
    icon: "https://cdn.jsdelivr.net/gh/lipis/flag-icons@main/flags/1x1/nl.svg"
    url: "http://cp.cloudflare.com/generate_204"
    interval: 300
    proxies: ["{{all|(?i)(荷兰|NL|Netherlands|🇳🇱)}}"]
rule-providers:
  Gemini: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Gemini.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Gemini/Gemini.list" }
  AdBlock: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Adblock4limbo.list, url: "https://limbopro.com/Adblock4limbo_surge.list" }
  Lan: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Lan.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Surge/Lan/Lan.list" }
  Hijacking: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Hijacking.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Surge/Hijacking/Hijacking.list" }
  Apple: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Apple.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/Apple/Apple.list" }
  "115": { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/115.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/115/115.list" }
  BiliBili: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/BiliBili.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/BiliBili/BiliBili.list" }
  Google: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Google.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Google/Google.list" }
  TikTok: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/TikTok.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/TikTok/TikTok.list" }
  OpenAI: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/OpenAI.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/OpenAI/OpenAI.list" }
  Claude: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Claude.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Claude/Claude.list" }
  Twitter: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Twitter.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Twitter/Twitter.list" }
  Telegram: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Telegram.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Telegram/Telegram.list" }
  Instagram: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Instagram.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Instagram/Instagram.list" }
  YouTube: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/YouTube.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/YouTube/YouTube.list" }
  GitHub: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/GitHub.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/GitHub/GitHub.list" }
  Spotify: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Spotify.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Spotify/Spotify.list" }
  Microsoft: { type: http, behavior: classical, format: text, interval: 86400, path: ./rules/Microsoft.list, url: "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Microsoft/Microsoft.list" }
rules:
  - IP-CIDR,100.64.0.0/10,DIRECT,no-resolve
  - DOMAIN-SUFFIX,gov.cn,DIRECT
  - DOMAIN-SUFFIX,bing.com,⚡️ smart
  - DOMAIN-SUFFIX,v2ex.com,⚡️ smart
  - RULE-SET,Gemini,✨ Gemini
  - RULE-SET,AdBlock,REJECT
  - RULE-SET,Lan,DIRECT
  - RULE-SET,Hijacking,REJECT
  - RULE-SET,Apple,DIRECT
  - RULE-SET,115,DIRECT
  - RULE-SET,BiliBili,DIRECT
  - RULE-SET,Google,🔍 Google
  - RULE-SET,TikTok,🎵 TikTok
  - RULE-SET,OpenAI,🤖 ChatGPT
  - RULE-SET,Claude,📮 Claude
  - RULE-SET,Twitter,🐦 Twitter
  - RULE-SET,Telegram,✈️ Telegram
  - RULE-SET,Instagram,📷 Instagram
  - RULE-SET,YouTube,▶️ YouTube
  - RULE-SET,GitHub,🐙 GitHub
  - RULE-SET,Spotify,🎧 Spotify
  - RULE-SET,Microsoft,DIRECT
  - GEOIP,CN,DIRECT
  - MATCH,🇺🇸 美国
`

// builtinSurgeTemplate is a managed fragment for a single #!include.
// Remote URL includes are validated as a full profile, so [Rule] must end
// with FINAL. The main profile only takes [Proxy]; do not mix handwritten
// lines in that section. policy-path cannot resolve underlying-proxy.
const builtinSurgeTemplate = `[Proxy]
🧩 国内直连 = direct
⛔️ 拦截净化 = reject
{{PROXIES}}

[Rule]
FINAL,DIRECT
`

// builtinShadowrocketTemplate is a full Shadowrocket profile (rules + groups).
// Node names are injected via {{PROXIES}} / {{all}}. Personal hosts and
// one-off IP-CIDR exceptions are omitted; only RFC1918 / multicast / public DNS remain.
const builtinShadowrocketTemplate = `# Shadowrocket
[General]
yaml = true
udp-policy-not-supported-behaviour = REJECT
skip-proxy = 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, localhost, *.local, captive.apple.com
tun-excluded-routes = 239.255.255.250/32,224.0.0.251/32, ff02::fb/128
dns-server = 223.5.5.5, 119.29.29.29
proxy-dns-server = https://223.5.5.5/dns-query
always-real-ip = *.lan,*.local,geosite:cn,geosite:private
ipv6 = true

[Proxy]
DIRECT = direct
REJECT = reject
{{PROXIES}}

[Proxy Group]
⚡️ smart = url-test,{{all}}
☑️ select = select,{{all}}
🔍 Google = select,🇯🇵 日本,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
▶️ YouTube = select,🇯🇵 日本,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
🤖 ChatGPT = select,🇺🇸 美国,🇦🇺 澳大利亚,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇳🇱 荷兰,DIRECT
📮 Claude = select,🇺🇸 美国,🇦🇺 澳大利亚,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇳🇱 荷兰,DIRECT
✨ Gemini = select,🇺🇸 美国,🇦🇺 澳大利亚,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇳🇱 荷兰,DIRECT
🎵 TikTok = select,🇺🇸 美国,🇦🇺 澳大利亚,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇳🇱 荷兰,DIRECT
🐦 Twitter = select,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
✈️ Telegram = select,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
📷 Instagram = select,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
🐙 GitHub = select,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
🎧 Spotify = select,⚡️ smart,☑️ select,🇭🇰 香港,🇸🇬 新加坡,🇯🇵 日本,🇺🇸 美国,🇦🇺 澳大利亚,🇳🇱 荷兰,DIRECT
🇭🇰 香港 = url-test,{{all|(?i)(香港|HK|Hong ?Kong|🇭🇰)}}
🇸🇬 新加坡 = url-test,{{all|(?i)(新加坡|SG|Singapore|🇸🇬)}}
🇯🇵 日本 = url-test,{{all|(?i)(日本|JP|Japan|🇯🇵)}}
🇺🇸 美国 = url-test,{{all|(?i)(美国|US|United ?States|🇺🇸)}}
🇦🇺 澳大利亚 = url-test,{{all|(?i)(澳大利亚|澳洲|AU|Australia|🇦🇺)}}
🇳🇱 荷兰 = url-test,{{all|(?i)(荷兰|NL|Netherlands|🇳🇱)}}

[Rule]
{{RULES}}
DOMAIN-SUFFIX,themoviedb.org,🇺🇸 美国
DOMAIN-SUFFIX,speedtest.net,🇯🇵 日本
DOMAIN-SUFFIX,bing.com,⚡️ smart
DOMAIN-SUFFIX,copilot.microsoft.com,🇺🇸 美国
DOMAIN-SUFFIX,apple-relay.apple.com,🇺🇸 美国
DOMAIN-SUFFIX,login.microsoftonline.com,🇺🇸 美国
DOMAIN-SUFFIX,outlook.office.com,🇺🇸 美国
RULE-SET,https://limbopro.com/Adblock4limbo_surge.list,REJECT
DOMAIN-SUFFIX,v2ex.com,⚡️ smart
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Surge/Lan/Lan.list,DIRECT
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Surge/Hijacking/Hijacking.list,REJECT
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/Apple/Apple.list,DIRECT
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/115/115.list,DIRECT
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/BiliBili/BiliBili.list,DIRECT
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Google/Google.list,🔍 Google
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/TikTok/TikTok.list,🎵 TikTok
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/OpenAI/OpenAI.list,🤖 ChatGPT
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Claude/Claude.list,📮 Claude
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Twitter/Twitter.list,🐦 Twitter
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Telegram/Telegram.list,✈️ Telegram
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Instagram/Instagram.list,📷 Instagram
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/YouTube/YouTube.list,▶️ YouTube
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/GitHub/GitHub.list,🐙 GitHub
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Spotify/Spotify.list,🎧 Spotify
RULE-SET,https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/refs/heads/master/rule/Surge/Microsoft/Microsoft.list,DIRECT
GEOIP,CN,DIRECT
FINAL,🇺🇸 美国

[Host]
localhost = 127.0.0.1

[URL Rewrite]
'^https?://(www.)?g.cn' 'https://www.google.com' 302
'^https?://(www.)?google.cn' 'https://www.google.com' 302
`

// builtinSingBoxTemplate is the default sing-box client skeleton (1.11+).
const builtinSingBoxTemplate = `{
  "log": { "level": "info", "timestamp": true },
  "dns": {
    "servers": [
      { "tag": "dns-local", "type": "udp", "server": "223.5.5.5" },
      { "tag": "dns-direct", "type": "https", "server": "223.5.5.5", "path": "/dns-query", "domain_resolver": "dns-local" },
      { "tag": "dns-remote", "type": "https", "server": "1.1.1.1", "domain_resolver": "dns-local", "detour": "⚡️ smart" },
      { "tag": "dns-remote-google", "type": "https", "server": "8.8.8.8", "domain_resolver": "dns-local", "detour": "⚡️ smart" }
    ],
    "rules": [
      { "rule_set": ["geosite-cn", "geosite-private"], "server": "dns-direct" }
    ],
    "final": "dns-remote",
    "strategy": "prefer_ipv6"
  },
  "inbounds": [
    { "type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890 },
    { "type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30", "fdfe:dcba:9876::1/126"], "auto_route": true, "strict_route": true, "stack": "system" }
  ],
  "outbounds": [
    { "type": "urltest", "tag": "⚡️ smart", "outbounds": ["{{all}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "selector", "tag": "☑️ select", "outbounds": ["⚡️ smart", "direct", "{{all}}"] },
    { "type": "selector", "tag": "🔍 Google", "outbounds": ["🇯🇵 日本", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"], "default": "🇯🇵 日本" },
    { "type": "selector", "tag": "▶️ YouTube", "outbounds": ["🇭🇰 香港", "⚡️ smart", "☑️ select", "🇯🇵 日本", "🇸🇬 新加坡", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"], "default": "🇭🇰 香港" },
    { "type": "selector", "tag": "🤖 ChatGPT", "outbounds": ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", "direct"], "default": "🇺🇸 美国" },
    { "type": "selector", "tag": "📮 Claude", "outbounds": ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", "direct"], "default": "🇺🇸 美国" },
    { "type": "selector", "tag": "✨ Gemini", "outbounds": ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", "direct"], "default": "🇺🇸 美国" },
    { "type": "selector", "tag": "🎵 TikTok", "outbounds": ["🇺🇸 美国", "🇦🇺 澳大利亚", "⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇳🇱 荷兰", "direct"], "default": "🇺🇸 美国" },
    { "type": "selector", "tag": "🐦 Twitter", "outbounds": ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"] },
    { "type": "selector", "tag": "✈️ Telegram", "outbounds": ["🇭🇰 香港", "⚡️ smart", "☑️ select", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"], "default": "🇭🇰 香港" },
    { "type": "selector", "tag": "📷 Instagram", "outbounds": ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"] },
    { "type": "selector", "tag": "🐙 GitHub", "outbounds": ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"] },
    { "type": "selector", "tag": "🎧 Spotify", "outbounds": ["⚡️ smart", "☑️ select", "🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰", "direct"] },
    { "type": "urltest", "tag": "🇭🇰 香港", "outbounds": ["{{all|(?i)(香港|HK|Hong ?Kong|🇭🇰)}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "urltest", "tag": "🇸🇬 新加坡", "outbounds": ["{{all|(?i)(新加坡|SG|Singapore|🇸🇬)}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "urltest", "tag": "🇯🇵 日本", "outbounds": ["{{all|(?i)(日本|JP|Japan|🇯🇵)}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "urltest", "tag": "🇺🇸 美国", "outbounds": ["{{all|(?i)(美国|US|United ?States|🇺🇸)}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "urltest", "tag": "🇦🇺 澳大利亚", "outbounds": ["{{all|(?i)(澳大利亚|澳洲|AU|Australia|🇦🇺)}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "urltest", "tag": "🇳🇱 荷兰", "outbounds": ["{{all|(?i)(荷兰|NL|Netherlands|🇳🇱)}}"], "url": "http://cp.cloudflare.com/generate_204", "interval": "300s" },
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "auto_detect_interface": true,
    "default_domain_resolver": "dns-local",
    "final": "🇺🇸 美国",
    "rule_set": [
      { "tag": "geosite-private", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-private.srs", "download_detour": "direct" },
      { "tag": "geosite-cn", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs", "download_detour": "direct" },
      { "tag": "geoip-cn", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs", "download_detour": "direct" },
      { "tag": "geosite-category-ads-all", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs", "download_detour": "direct" },
      { "tag": "geosite-google", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-google.srs", "download_detour": "direct" },
      { "tag": "geosite-youtube", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-youtube.srs", "download_detour": "direct" },
      { "tag": "geosite-openai", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-openai.srs", "download_detour": "direct" },
      { "tag": "geosite-anthropic", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-anthropic.srs", "download_detour": "direct" },
      { "tag": "geosite-google-gemini", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-google-gemini.srs", "download_detour": "direct" },
      { "tag": "geosite-tiktok", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-tiktok.srs", "download_detour": "direct" },
      { "tag": "geosite-twitter", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-twitter.srs", "download_detour": "direct" },
      { "tag": "geosite-telegram", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-telegram.srs", "download_detour": "direct" },
      { "tag": "geosite-instagram", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-instagram.srs", "download_detour": "direct" },
      { "tag": "geosite-github", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-github.srs", "download_detour": "direct" },
      { "tag": "geosite-spotify", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-spotify.srs", "download_detour": "direct" },
      { "tag": "geosite-microsoft", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-microsoft.srs", "download_detour": "direct" },
      { "tag": "geosite-apple", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-apple.srs", "download_detour": "direct" },
      { "tag": "geosite-bilibili", "type": "remote", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-bilibili.srs", "download_detour": "direct" }
    ],
    "rules": [
      { "action": "sniff" },
      { "protocol": "dns", "action": "hijack-dns" },
      { "ip_cidr": ["100.64.0.0/10"], "outbound": "direct" },
      { "ip_is_private": true, "outbound": "direct" },
      { "domain_suffix": ["gov.cn"], "outbound": "direct" },
      { "domain_suffix": ["bing.com", "v2ex.com"], "outbound": "⚡️ smart" },
      { "rule_set": ["geosite-category-ads-all"], "action": "reject" },
      { "rule_set": ["geosite-private"], "outbound": "direct" },
      { "rule_set": ["geosite-apple", "geosite-bilibili", "geosite-microsoft"], "outbound": "direct" },
      { "rule_set": ["geosite-google"], "outbound": "🔍 Google" },
      { "rule_set": ["geosite-youtube"], "outbound": "▶️ YouTube" },
      { "rule_set": ["geosite-openai"], "outbound": "🤖 ChatGPT" },
      { "rule_set": ["geosite-anthropic"], "outbound": "📮 Claude" },
      { "rule_set": ["geosite-google-gemini"], "outbound": "✨ Gemini" },
      { "rule_set": ["geosite-tiktok"], "outbound": "🎵 TikTok" },
      { "rule_set": ["geosite-twitter"], "outbound": "🐦 Twitter" },
      { "rule_set": ["geosite-telegram"], "outbound": "✈️ Telegram" },
      { "rule_set": ["geosite-instagram"], "outbound": "📷 Instagram" },
      { "rule_set": ["geosite-github"], "outbound": "🐙 GitHub" },
      { "rule_set": ["geosite-spotify"], "outbound": "🎧 Spotify" },
      { "rule_set": ["geosite-cn", "geoip-cn"], "outbound": "direct" }
    ]
  },
  "experimental": {
    "cache_file": { "enabled": true },
    "clash_api": { "external_controller": "127.0.0.1:9090" }
  }
}`

// BuiltinTemplates are seeded on first start.
func BuiltinTemplates() []domain.RuleTemplate {
	return []domain.RuleTemplate{
		{Name: "默认 mihomo 模板", Kind: "mihomo", Description: "fake-ip + 1.1.1.1/8.8.8.8 DNS，smart/地区组 + blackmatrix7 RULE-SET 分流，节点用 {{all}} / {{all|正则}} 注入。", Content: builtinMihomoTemplate, IsBuiltin: true},
		{Name: "默认 Surge 模板", Kind: "surge", Description: "一条 /surge 链接：带 [Proxy] 段。主配置 [Proxy] 只 #!include 这一条，新增节点会进订阅。不要用策略组 policy-path 拉链式。", Content: builtinSurgeTemplate, IsBuiltin: true},
		{Name: "默认 Shadowrocket 模板", Kind: "shadowrocket", Description: "小火箭完整配置：yaml=true，地区组/App 组 + RULE-SET 分流，节点用 {{PROXIES}} / {{all}} 注入。", Content: builtinShadowrocketTemplate, IsBuiltin: true},
		{Name: "默认 sing-box 模板", Kind: "singbox", Description: "sing-box 1.11+：mixed 7890 + tun，与 mihomo 同套 smart/地区组与 geosite 分流。", Content: builtinSingBoxTemplate, IsBuiltin: true},
	}
}

func orzIcon(name string) string {
	return "https://cdn.jsdelivr.net/gh/Orz-3/mini@0e2bab71d8dbe512db9b5e7a9000a5661dd0e327/Color/" + name + ".png"
}

// BuiltinPresets are ready-made proxy-group layouts for the generator.
func BuiltinPresets() []domain.ProxyGroupPreset {
	testURL := "http://cp.cloudflare.com/generate_204"
	regions := []string{"🇭🇰 香港", "🇸🇬 新加坡", "🇯🇵 日本", "🇺🇸 美国", "🇦🇺 澳大利亚", "🇳🇱 荷兰"}
	service := func(first ...string) []string {
		seen := map[string]bool{}
		var out []string
		add := func(n string) {
			if n == "" || seen[n] {
				return
			}
			seen[n] = true
			out = append(out, n)
		}
		for _, n := range first {
			add(n)
		}
		add("⚡️ smart")
		add("☑️ select")
		for _, n := range regions {
			add(n)
		}
		add("DIRECT")
		return out
	}
	return []domain.ProxyGroupPreset{
		{
			Name:      "极简：手动 + 自动",
			IsBuiltin: true,
			Groups: []domain.ProxyGroup{
				{Name: "节点选择", Type: "select", Proxies: []string{"自动选择", "DIRECT"}, IncludeAll: true},
				{Name: "自动选择", Type: "url-test", URL: testURL, Interval: 300, Tolerance: 50, IncludeAll: true},
			},
			Rules: []string{"GEOSITE,private,DIRECT", "GEOIP,private,DIRECT,no-resolve", "GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "MATCH,节点选择"},
		},
		{
			Name:      "标准分流",
			IsBuiltin: true,
			Groups: []domain.ProxyGroup{
				{Name: "⚡️ smart", Type: "url-test", URL: testURL, Interval: 300, IncludeAll: true, Icon: orzIcon("Auto")},
				{Name: "☑️ select", Type: "select", Proxies: []string{"⚡️ smart", "DIRECT"}, IncludeAll: true, Icon: orzIcon("Global")},
				{Name: "🔍 Google", Type: "select", Proxies: service("🇯🇵 日本"), Icon: orzIcon("Google")},
				{Name: "▶️ YouTube", Type: "select", Proxies: service("🇯🇵 日本"), Icon: orzIcon("YouTube")},
				{Name: "🤖 ChatGPT", Type: "select", Proxies: service("🇺🇸 美国", "🇦🇺 澳大利亚"), Icon: orzIcon("OpenAI")},
				{Name: "📮 Claude", Type: "select", Proxies: service("🇺🇸 美国", "🇦🇺 澳大利亚"), Icon: "https://cdn.jsdelivr.net/gh/Sereinfy/Clash@e450a2a33227ca42e56247823839230a0f2d6ab2/icons/anthropic.png"},
				{Name: "✨ Gemini", Type: "select", Proxies: service("🇺🇸 美国", "🇦🇺 澳大利亚"), Icon: orzIcon("Google")},
				{Name: "🎵 TikTok", Type: "select", Proxies: service("🇺🇸 美国", "🇦🇺 澳大利亚"), Icon: orzIcon("TikTok")},
				{Name: "🐦 Twitter", Type: "select", Proxies: service(), Icon: orzIcon("Twitter")},
				{Name: "✈️ Telegram", Type: "select", Proxies: service(), Icon: orzIcon("Telegram")},
				{Name: "📷 Instagram", Type: "select", Proxies: service(), Icon: orzIcon("Instagram")},
				{Name: "🐙 GitHub", Type: "select", Proxies: service(), Icon: "https://cdn.jsdelivr.net/gh/Koolson/Qure@master/IconSet/Color/GitHub.png"},
				{Name: "🎧 Spotify", Type: "select", Proxies: service(), Icon: orzIcon("Spotify")},
				{Name: "🇭🇰 香港", Type: "url-test", URL: testURL, Interval: 300, Filter: "(?i)香港|HK|Hong ?Kong|🇭🇰", Icon: orzIcon("HK")},
				{Name: "🇸🇬 新加坡", Type: "url-test", URL: testURL, Interval: 300, Filter: "(?i)新加坡|SG|Singapore|🇸🇬", Icon: orzIcon("SG")},
				{Name: "🇯🇵 日本", Type: "url-test", URL: testURL, Interval: 300, Filter: "(?i)日本|JP|Japan|🇯🇵", Icon: orzIcon("JP")},
				{Name: "🇺🇸 美国", Type: "url-test", URL: testURL, Interval: 300, Filter: "(?i)美国|US|United ?States|🇺🇸", Icon: orzIcon("US")},
				{Name: "🇦🇺 澳大利亚", Type: "url-test", URL: testURL, Interval: 300, Filter: "(?i)澳大利亚|澳洲|AU|Australia|🇦🇺", Icon: orzIcon("AU")},
				{Name: "🇳🇱 荷兰", Type: "url-test", URL: testURL, Interval: 300, Filter: "(?i)荷兰|NL|Netherlands|🇳🇱", Icon: "https://cdn.jsdelivr.net/gh/lipis/flag-icons@main/flags/1x1/nl.svg"},
			},
			Rules: []string{
				"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
				"DOMAIN-SUFFIX,gov.cn,DIRECT",
				"DOMAIN-SUFFIX,bing.com,⚡️ smart",
				"DOMAIN-SUFFIX,v2ex.com,⚡️ smart",
				"RULE-SET,Gemini,✨ Gemini",
				"RULE-SET,AdBlock,REJECT",
				"RULE-SET,Lan,DIRECT",
				"RULE-SET,Hijacking,REJECT",
				"RULE-SET,Apple,DIRECT",
				"RULE-SET,115,DIRECT",
				"RULE-SET,BiliBili,DIRECT",
				"RULE-SET,Google,🔍 Google",
				"RULE-SET,TikTok,🎵 TikTok",
				"RULE-SET,OpenAI,🤖 ChatGPT",
				"RULE-SET,Claude,📮 Claude",
				"RULE-SET,Twitter,🐦 Twitter",
				"RULE-SET,Telegram,✈️ Telegram",
				"RULE-SET,Instagram,📷 Instagram",
				"RULE-SET,YouTube,▶️ YouTube",
				"RULE-SET,GitHub,🐙 GitHub",
				"RULE-SET,Spotify,🎧 Spotify",
				"RULE-SET,Microsoft,DIRECT",
				"GEOSITE,category-ads-all,REJECT",
				"GEOSITE,google,🔍 Google",
				"GEOSITE,youtube,▶️ YouTube",
				"GEOSITE,openai,🤖 ChatGPT",
				"GEOSITE,anthropic,📮 Claude",
				"GEOSITE,google-gemini,✨ Gemini",
				"GEOSITE,tiktok,🎵 TikTok",
				"GEOSITE,twitter,🐦 Twitter",
				"GEOSITE,telegram,✈️ Telegram",
				"GEOIP,telegram,✈️ Telegram,no-resolve",
				"GEOSITE,instagram,📷 Instagram",
				"GEOSITE,github,🐙 GitHub",
				"GEOSITE,spotify,🎧 Spotify",
				"GEOSITE,microsoft,DIRECT",
				"GEOSITE,apple,DIRECT",
				"GEOSITE,bilibili,DIRECT",
				"GEOSITE,cn,DIRECT",
				"GEOIP,CN,DIRECT",
				"MATCH,🇺🇸 美国",
			},
		},
		{
			Name:      "链式落地示例",
			IsBuiltin: true,
			Groups: []domain.ProxyGroup{
				{Name: "节点选择", Type: "select", Proxies: []string{"DIRECT"}, IncludeAll: true},
				{Name: "落地出口", Type: "select", Proxies: []string{"节点选择"}, Filter: "(?i)落地|landing|via"},
			},
			Rules: []string{"GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "MATCH,节点选择"},
		},
	}
}
