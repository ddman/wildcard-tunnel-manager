package main

const indexHTML = `<!doctype html>
<html lang="zh-Hant">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Wildcard Tunnel Manager</title>
  <style>
    :root{font-family:system-ui,-apple-system,sans-serif;color:#142236;background:#f4f7fb}body{max-width:920px;margin:48px auto;padding:0 20px}h1{font-size:30px;margin-bottom:8px}p{line-height:1.6;color:#526173}section{background:white;border:1px solid #dce4ed;border-radius:14px;padding:24px;margin:20px 0;box-shadow:0 2px 8px #14223608}form{display:flex;flex-wrap:wrap;gap:12px;align-items:end}label{display:grid;gap:6px;font-size:14px;font-weight:600}input,select,button{font:inherit;border-radius:8px;padding:10px 12px;border:1px solid #b9c7d7}input{width:180px}button{background:#155eef;color:white;border-color:#155eef;cursor:pointer}button.delete{background:white;color:#b42318;border-color:#efc7c3}table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:12px;border-bottom:1px solid #e6edf4}code{font-family:ui-monospace,monospace;background:#f0f4f8;padding:2px 5px;border-radius:4px}#message{min-height:24px;color:#b42318}small{color:#6b7785}nav{display:flex;gap:8px;margin:20px 0}nav button{background:white;color:#142236;border-color:#dce4ed}nav button[aria-selected=true]{background:#155eef;color:white;border-color:#155eef}[hidden]{display:none!important}.summary{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px}.summary div{background:#f0f4f8;border-radius:10px;padding:14px}.summary strong{display:block;margin-bottom:6px}.summary span{overflow-wrap:anywhere}
  </style>
</head>
<body>
  <h1>Wildcard Tunnel Manager</h1>
  <p>管理 Cloudflare Tunnel 與這台電腦上的測試網址。路由可依子網域與路徑前綴轉送到不同的本機 port。</p>
  <nav role="tablist" aria-label="管理頁面">
    <button type="button" role="tab" data-page="overview" aria-selected="true">總覽</button>
    <button type="button" role="tab" data-page="routes" aria-selected="false">路由</button>
    <button type="button" role="tab" data-page="cloudflare" aria-selected="false">Cloudflare 設定</button>
    <button type="button" role="tab" data-page="account" aria-selected="false">管理帳密</button>
    <form method="post" action="/logout"><button type="submit">登出</button></form>
  </nav>
  <main id="page-overview" role="tabpanel">
    <section id="binding-health-card" role="status"><h2>綁定狀態</h2><p id="binding-health-message">正在檢查…</p><button type="button" id="force-bind" hidden>強制綁定到這台主機</button><button type="button" class="delete" id="unbind" hidden>取消本機綁定</button></section>
    <section><h2>Cloudflare 綁定</h2><div id="bindings" class="summary"></div></section>
    <section><h2>目前路由</h2><table><thead><tr><th>公開網址</th><th>本機服務</th><th>轉送路徑</th></tr></thead><tbody id="overview-routes"></tbody></table></section>
  </main>
  <main id="page-routes" role="tabpanel" hidden>
  <section>
    <h2>新增路由</h2>
    <form id="route-form">
      <label>子網域名稱<input name="name" required pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" placeholder="app"><small>.<span id="route-domain"></span></small></label>
      <label>路徑前綴<input name="path" required value="/" pattern="/([A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*)?" placeholder="/api"></label>
      <label>協定<select name="scheme"><option>http</option><option>https</option></select></label>
      <label>本機 port<input name="port" required type="number" min="1" max="65535" placeholder="3000"></label>
      <label>轉送路徑<select name="stripPrefix"><option value="false">保留前綴</option><option value="true">移除前綴</option></select></label>
      <button>新增</button>
    </form>
    <p id="message" role="status"></p>
  </section>
  <section>
    <h2>目前路由</h2>
    <table><thead><tr><th>公開網址</th><th>本機服務</th><th>轉送路徑</th><th></th></tr></thead><tbody id="routes"></tbody></table>
  </section>
  </main>
  <main id="page-cloudflare" role="tabpanel" hidden>
  <section>
    <h2>Cloudflare 連線</h2>
    <p>建立或沿用一條 Tunnel，並設定 <code id="wildcard-domain"></code> 的 proxied CNAME。已存在的明確 DNS 紀錄會優先，程式不會修改它們。</p>
    <p>管理頁與管理 API 在 <code id="admin-local-url"></code> 提供；啟用 Tailscale 時也可從 tailnet 連線。Tunnel 只公開開發服務路由。</p>
    <form id="cloudflare-form">
      <label>Account ID<input name="accountId" required autocomplete="off" placeholder="Cloudflare 帳戶總覽 → API"></label>
      <label>Zone ID<input name="zoneId" required autocomplete="off" placeholder="目前網域總覽 → API"></label>
      <label>API Token<input name="apiToken" type="password" autocomplete="off" placeholder="首次填入後保存在本機 .env"></label>
      <label>現有 Tunnel ID（選填）<input name="existingTunnelId" autocomplete="off" placeholder="截圖中的通道 ID"></label>
      <button>設定並連線</button>
    </form>
    <p id="cloudflare-status" role="status"></p>
    <p id="cloudflare-result" role="status"></p>
    <small>安裝指令中的 Tunnel token 與 Global API Key 都不能填在 API Token 欄位。API Token 需有 Account → Cloudflare Tunnel → Edit 與此網域的 Zone → DNS → Edit（DNS Write）。API Token 會存於本機 .env（權限 0600），供定期檢查綁定；Tunnel token 存於 config.json。強制綁定會切換 wildcard DNS，舊主機的程序不會被遠端停止。</small>
    <p>本機代理 socket：<code id="proxy-socket"></code>。可複製上方路徑，以 <code id="socket-example"></code> 測試。若先前已設定 Tunnel，請在更新程式後重新按一次「設定並連線」，讓 Cloudflare 改指向 socket。</p>
  </section>
  </main>
  <main id="page-account" role="tabpanel" hidden>
    <section>
      <h2>管理帳密</h2>
      <p>修改後立即生效，並保存到這台主機權限 0600 的 .env。請用新帳密重新登入。</p>
      <form id="credentials-form">
        <label>目前密碼<input name="currentPassword" type="password" autocomplete="current-password" required></label>
        <label>新帳號<input name="newUsername" autocomplete="username" required minlength="3" maxlength="128"></label>
        <label>新密碼<input name="newPassword" type="password" autocomplete="new-password" required minlength="16" maxlength="1024"></label>
        <label>確認新密碼<input name="confirmPassword" type="password" autocomplete="new-password" required minlength="16" maxlength="1024"></label>
        <button>更新帳密</button>
      </form>
      <p id="credentials-result" role="status"></p>
    </section>
  </main>
  <script>
    const rows=document.querySelector('#routes'),message=document.querySelector('#message');
    function showPage(page){
      if(!['overview','routes','cloudflare','account'].includes(page))page='overview';
      for(const name of ['overview','routes','cloudflare','account']){
        document.querySelector('#page-'+name).hidden=name!==page;
        document.querySelector('[data-page='+name+']').setAttribute('aria-selected',name===page?'true':'false');
      }
    }
    for(const button of document.querySelectorAll('nav [data-page]'))button.onclick=()=>{location.hash=button.dataset.page;showPage(button.dataset.page)};
    window.addEventListener('hashchange',()=>showPage(location.hash.slice(1)));
    showPage(location.hash.slice(1));
    function addBindingLine(card,label,value){
      const line=document.createElement('p'),heading=document.createElement('strong'),text=document.createElement('span');
      heading.textContent=label+'：';text.textContent=value;line.append(heading,text);card.append(line);
    }
    function renderBindingHealth(health){
      const status=health.status||'checking',card=document.querySelector('#binding-health-card');
      card.style.borderColor=['conflict','shared','disconnected','error'].includes(status)?'#b42318':'#dce4ed';
      document.querySelector('#binding-health-message').textContent=health.message||'正在檢查…';
      document.querySelector('#force-bind').hidden=status!=='conflict'||!health.ownerTunnelId;
      document.querySelector('#unbind').hidden=!['bound','shared','disconnected'].includes(status);
    }
    function renderRoute(table,route,domain,editable){
      const tr=document.createElement('tr'),host=document.createElement('td'),path=route.path||'/';
      const link=document.createElement('a');link.href='https://'+route.name+'.'+domain+path;link.textContent=link.href;host.append(link);
      const target=document.createElement('td');target.textContent=route.scheme+'://127.0.0.1:'+route.port;
      const rewrite=document.createElement('td');rewrite.textContent=route.stripPrefix?'移除前綴':'保留前綴';
      tr.append(host,target,rewrite);
      if(editable){
        const action=document.createElement('td'),button=document.createElement('button');button.className='delete';button.textContent='刪除';
        button.onclick=async()=>{const response=await fetch('/api/routes/'+encodeURIComponent(route.name)+'?path='+encodeURIComponent(path),{method:'DELETE'});if(!response.ok){message.textContent=await response.text();return}await load()};
        action.append(button);tr.append(action);
      }
      table.append(tr);
    }
    async function load(){
      const response=await fetch('/api/state',{cache:'no-store'});
      if(response.status===401){location.href='/login';return}
      if(!response.ok)throw new Error('無法讀取目前狀態：HTTP '+response.status);
      const state=await response.json(),bindings=state.bindings||[],binding=bindings[0];
      renderBindingHealth(state.bindingHealth||{});
      const cards=document.querySelector('#bindings');cards.replaceChildren();
      for(const item of bindings){
        const card=document.createElement('div'),title=document.createElement('h3');title.textContent=item.baseDomain;card.append(title);
        addBindingLine(card,'公開入口',item.wildcardHostname);
        addBindingLine(card,'Tunnel',item.cloudflare.tunnelId?(item.tunnelRunning?'本機程序執行中':'本機程序未執行'):'未設定');
        addBindingLine(card,'Tunnel token',item.tunnelTokenStored?'已保存':'未設定');
        addBindingLine(card,'Cloudflare API Token',state.cloudflareAPITokenStored?'已保存於本機 .env':'未設定');
        addBindingLine(card,'本機入口',item.needsSocketMigration?'舊版 TCP 127.0.0.1:8788':'Unix socket');
        addBindingLine(card,'本機管理','http://127.0.0.1:'+state.adminPort);
        addBindingLine(card,'Tailscale 管理',state.adminTailscaleAddress?'http://'+state.adminTailscaleAddress:'未啟用');
        cards.append(card);
      }
      const overview=document.querySelector('#overview-routes'),rows=document.querySelector('#routes');
      overview.replaceChildren();rows.replaceChildren();
      for(const route of state.routes){renderRoute(overview,route,binding.baseDomain,false);renderRoute(rows,route,binding.baseDomain,true)}
      if(!state.routes.length){for(const [table,columns] of [[overview,3],[rows,4]]){const tr=document.createElement('tr'),td=document.createElement('td');td.colSpan=columns;td.textContent='尚未設定路由';tr.append(td);table.append(tr)}}
      document.querySelector('#route-domain').textContent=binding.baseDomain;
      document.querySelector('#wildcard-domain').textContent=binding.wildcardHostname;
      document.querySelector('#admin-local-url').textContent='http://127.0.0.1:'+state.adminPort;
      document.querySelector('#credentials-form [name=newUsername]').value=state.adminUsername||'';
      document.querySelector('#proxy-socket').textContent=state.proxySocket;
      document.querySelector('#socket-example').textContent='curl --unix-socket 路徑 http://app.'+binding.baseDomain+'/';
      const cf=binding.cloudflare;
      document.querySelector('[name=accountId]').value=state.cloudflareAccountID||cf.accountId||'';
      document.querySelector('[name=zoneId]').value=state.cloudflareZoneID||cf.zoneId||'';
      document.querySelector('[name=existingTunnelId]').value=cf.tunnelId||'';
      document.querySelector('#cloudflare-status').textContent=cf.tunnelId?'Tunnel '+cf.tunnelId+(binding.tunnelRunning?' 正在執行':' 尚未執行')+(binding.needsSocketMigration?'；需重新套用 Tunnel 設定以切換 socket':''):'尚未連線';
      return state;
    }
    document.querySelector('#route-form').onsubmit=async event=>{event.preventDefault();message.textContent='';const data=new FormData(event.target);const response=await fetch('/api/routes',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({name:data.get('name'),path:data.get('path'),stripPrefix:data.get('stripPrefix')==='true',scheme:data.get('scheme'),port:Number(data.get('port'))})});if(!response.ok){message.textContent=await response.text();return}event.target.reset();await load()};load().catch(error=>message.textContent=String(error));
    const cloudflareResult=document.querySelector('#cloudflare-result');
    async function submitCloudflare(force){
      cloudflareResult.textContent=force?'正在接管綁定…':'設定中…';
      const form=document.querySelector('#cloudflare-form'),data=Object.fromEntries(new FormData(form));data.force=force;
      try{
        const response=await fetch('/api/cloudflare/setup',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(data)});
        const body=await response.text();
        if(response.ok)form.querySelector('[name=apiToken]').value='';
        await load();
        cloudflareResult.textContent=response.ok?(force?'已接管 wildcard DNS。':'Tunnel 設定已更新。'):body;
        if(!response.ok)showPage('cloudflare');
      }catch(error){cloudflareResult.textContent='設定失敗：'+String(error)}
    }
    document.querySelector('#cloudflare-form').onsubmit=event=>{event.preventDefault();submitCloudflare(false)};
    document.querySelector('#credentials-form').onsubmit=async event=>{
      event.preventDefault();const form=event.target,result=document.querySelector('#credentials-result'),data=Object.fromEntries(new FormData(form));
      if(data.newPassword!==data.confirmPassword){result.textContent='兩次新密碼不一致。';return}
      result.textContent='更新中…';
      try{
        const response=await fetch('/api/admin/credentials',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({currentPassword:data.currentPassword,newUsername:data.newUsername,newPassword:data.newPassword})});
        result.textContent=response.ok?'已更新。請關閉此分頁，重新開啟管理頁並使用新帳密登入。':await response.text();
        if(response.ok){form.querySelector('[name=currentPassword]').value='';form.querySelector('[name=newPassword]').value='';form.querySelector('[name=confirmPassword]').value=''}
      }catch(error){result.textContent='更新失敗：'+String(error)}
    };
    document.querySelector('#force-bind').onclick=()=>{if(confirm('將 wildcard DNS 改指向這台主機的 Tunnel。其他主機不會自動停止。確定接管？'))submitCloudflare(true)};
    document.querySelector('#unbind').onclick=async()=>{if(!confirm('移除目前指向這台主機的 DNS 綁定？Tunnel 會保留。'))return;const response=await fetch('/api/cloudflare/unbind',{method:'POST'});const body=await response.text();await load();if(!response.ok){cloudflareResult.textContent=body;showPage('cloudflare')}};
    setInterval(async()=>{try{const response=await fetch('/api/cloudflare/health',{cache:'no-store'});if(response.ok)renderBindingHealth(await response.json())}catch(error){}},10000);
  </script>
</body>
</html>`
