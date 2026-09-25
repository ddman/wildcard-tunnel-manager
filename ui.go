package main

const indexHTML = `<!doctype html>
<html lang="zh-Hant">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>DDMAN Home Tunnel</title>
  <style>
    :root{font-family:system-ui,-apple-system,sans-serif;color:#142236;background:#f4f7fb}body{max-width:920px;margin:48px auto;padding:0 20px}h1{font-size:30px;margin-bottom:8px}p{line-height:1.6;color:#526173}section{background:white;border:1px solid #dce4ed;border-radius:14px;padding:24px;margin:20px 0;box-shadow:0 2px 8px #14223608}form{display:flex;flex-wrap:wrap;gap:12px;align-items:end}label{display:grid;gap:6px;font-size:14px;font-weight:600}input,select,button{font:inherit;border-radius:8px;padding:10px 12px;border:1px solid #b9c7d7}input{width:180px}button{background:#155eef;color:white;border-color:#155eef;cursor:pointer}button.delete{background:white;color:#b42318;border-color:#efc7c3}table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:12px;border-bottom:1px solid #e6edf4}code{font-family:ui-monospace,monospace;background:#f0f4f8;padding:2px 5px;border-radius:4px}#message{min-height:24px;color:#b42318}small{color:#6b7785}
  </style>
</head>
<body>
  <h1>DDMAN Home Tunnel</h1>
  <p>將 <code>名稱.ddman.cc</code> 對應到這台電腦上的 HTTP 或 HTTPS port。</p>
  <section>
    <h2>新增路由</h2>
    <form id="route-form">
      <label>子網域名稱<input name="name" required pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" placeholder="app"></label>
      <label>協定<select name="scheme"><option>http</option><option>https</option></select></label>
      <label>本機 port<input name="port" required type="number" min="1" max="65535" placeholder="3000"></label>
      <button>新增</button>
    </form>
    <p id="message" role="status"></p>
  </section>
  <section>
    <h2>目前路由</h2>
    <table><thead><tr><th>公開網址</th><th>本機服務</th><th></th></tr></thead><tbody id="routes"></tbody></table>
  </section>
  <section>
    <h2>Cloudflare 連線</h2>
    <p>建立或沿用一條 Tunnel，並設定 <code>*.ddman.cc</code> 的 proxied CNAME。已存在的明確 DNS 紀錄會優先，程式不會修改它們。</p>
    <form id="cloudflare-form">
      <label>Account ID<input name="accountId" required autocomplete="off" placeholder="ddman.cc Overview → API"></label>
      <label>Zone ID<input name="zoneId" required autocomplete="off" placeholder="ddman.cc Overview → API"></label>
      <label>API Token<input name="apiToken" required type="password" autocomplete="off" placeholder="My Profile → API Tokens 的 Token secret"></label>
      <label>現有 Tunnel ID（選填）<input name="existingTunnelId" autocomplete="off" placeholder="截圖中的通道 ID"></label>
      <button>設定並連線</button>
    </form>
    <p id="cloudflare-status" role="status"></p>
    <small>截圖中的「通道名稱」只用來辨識，不填在這裡；「通道 ID」填入選填欄位。安裝指令包含 Tunnel token，不要貼到 API Token 欄位，也不用執行。API Keys 區的 Global API Key 也不能填在這裡。API Token 需有 Account → Cloudflare Tunnel → Edit 與 ddman.cc 的 Zone → DNS → Edit。API Token 不會儲存；Tunnel token 會存於本機 config.json（權限 0600）。</small>
    <p>本機代理位址：<code>http://127.0.0.1:8788</code>。可用 <code>curl -H 'Host: app.ddman.cc' http://127.0.0.1:8788</code> 測試。</p>
  </section>
  <script>
    const rows=document.querySelector('#routes'),message=document.querySelector('#message');
    async function load(){const state=await (await fetch('/api/state')).json();rows.replaceChildren();for(const route of state.routes){const tr=document.createElement('tr');const host=document.createElement('td');host.textContent=route.name+'.'+state.baseDomain;const target=document.createElement('td');target.textContent=route.scheme+'://127.0.0.1:'+route.port;const action=document.createElement('td');const button=document.createElement('button');button.className='delete';button.textContent='刪除';button.onclick=async()=>{const response=await fetch('/api/routes/'+encodeURIComponent(route.name),{method:'DELETE'});if(!response.ok){message.textContent=await response.text();return}await load()};action.append(button);tr.append(host,target,action);rows.append(tr)}const cf=state.cloudflare;document.querySelector('[name=accountId]').value=cf.accountId||'';document.querySelector('[name=zoneId]').value=cf.zoneId||'';document.querySelector('[name=existingTunnelId]').value=cf.tunnelId||'';document.querySelector('#cloudflare-status').textContent=cf.tunnelId?'Tunnel '+cf.tunnelId+(state.tunnelRunning?' 正在執行':' 尚未執行'):'尚未連線'}
    document.querySelector('#route-form').onsubmit=async event=>{event.preventDefault();message.textContent='';const data=new FormData(event.target);const response=await fetch('/api/routes',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({name:data.get('name'),scheme:data.get('scheme'),port:Number(data.get('port'))})});if(!response.ok){message.textContent=await response.text();return}event.target.reset();await load()};load().catch(error=>message.textContent=String(error));
    document.querySelector('#cloudflare-form').onsubmit=async event=>{event.preventDefault();const status=document.querySelector('#cloudflare-status');status.textContent='設定中…';const data=new FormData(event.target);const response=await fetch('/api/cloudflare/setup',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(Object.fromEntries(data))});const body=await response.text();status.textContent=response.ok?'Tunnel 與 DNS 已設定。請確認 HTTPS 憑證生效。':body;if(response.ok){event.target.querySelector('[name=apiToken]').value='';await load()}};
  </script>
</body>
</html>`
