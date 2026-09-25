# DDMAN Home Tunnel

在一台 Mac 上以本機網頁管理 `名稱.ddman.cc → localhost:port`。Cloudflare Tunnel 只需一條，DNS 只需一筆 `*.ddman.cc` CNAME；已有明確 DNS 紀錄的名稱會由 Cloudflare DNS 優先處理，不會進入此 wildcard。

## 技術與路徑

```text
瀏覽器 → Cloudflare DNS / Universal SSL → Cloudflare Tunnel
       → cloudflared → 127.0.0.1:8788 → Go reverse proxy
       → 127.0.0.1:<服務 port>
```

管理頁固定在 `http://127.0.0.1:8787`，不透過 Tunnel 公開。Go 程式負責管理頁、設定檔、HTTP/HTTPS origin 反向代理和 Cloudflare API。`cloudflared` 是唯一額外執行依賴。沒有前端建置流程，也不需要 Caddy 或 Docker。

## 啟動

需要 Go 1.26+ 和 [cloudflared](https://developers.cloudflare.com/tunnel/get-started/)。目前這台 Mac 的專案內已有 `tools/cloudflared`（2026.9.3，Apple Silicon；已核對 GitHub release 資產 digest）。也可改用 `brew install cloudflared` 安裝到 PATH。

```bash
cd ~/projects/ddman-home-tunnel
go run .
```

開啟 `http://127.0.0.1:8787`，先新增 `app → http → 3000`。本機驗證：

```bash
curl -H 'Host: app.ddman.cc' http://127.0.0.1:8788/
```

設定 Cloudflare 時，在 Dashboard 建立範圍最小的 API Token：Account / Cloudflare Tunnel / Edit，以及 `ddman.cc` 的 Zone / DNS / Edit。從 Dashboard 複製 Account ID 和 Zone ID。如果已在 Dashboard 建立空白的遠端管理 Tunnel，可把其 Tunnel ID 填入選填欄位；程式會先確認 Tunnel 沒有既有路由，再沿用它。否則會建立專用 Tunnel。設定流程檢查 wildcard DNS 是否已被占用、將 `*.ddman.cc` 設為 ingress、建立 proxied wildcard CNAME，最後啟動本機 `cloudflared`。若已有 `*.ddman.cc` DNS 紀錄且指向別處，設定會停止，並不覆蓋它。Dashboard 的安裝指令含 Tunnel token；使用本工具時無需執行。

設定保存在 `config.json`，包含 Tunnel token，檔案權限為 0600，且已加入 `.gitignore`。API Token 不會寫入磁碟。程式下次啟動會讀取 Tunnel token，重新啟動 `cloudflared`。請勿把 `config.json` 複製給其他人；持有 Tunnel token 的人可執行連線。

## 範圍與限制

- 目前只支援一台主機持有 `*.ddman.cc` wildcard。第二台若要提供不同服務，應改用每個服務各自的明確 DNS 紀錄與 Tunnel。
- 只接受單層名稱，例如 `app.ddman.cc`，不處理 `app.home.ddman.cc`。後者的 HTTPS 需要額外憑證。
- 公開網址預設沒有登入控管；知道網址的人都能使用服務。請勿掛上未設定認證的管理介面或含私密資料的服務。
- origin 服務必須監聽本機回環位址；HTTPS origin 需有 Go 能驗證的有效憑證。瀏覽器到 Cloudflare 的 HTTPS 與 cloudflared 到本機的 HTTP 是不同連線。
- `cloudflared` 與此 Go 程式需持續執行；Mac 睡眠或程式停止時服務不可用。目前未安裝 macOS 背景啟動服務。
- 若 Cloudflare API 設定中途失敗，重新按「建立並連線」會沿用已儲存的 Tunnel 繼續；若需刪除 Tunnel 或 DNS，請在 Cloudflare Dashboard 處理。

## 官方文件

- [Cloudflare Tunnel API setup](https://developers.cloudflare.com/tunnel/get-started/)
- [Wildcard DNS priority](https://developers.cloudflare.com/dns/manage-dns-records/reference/wildcard-dns-records/)
- [Universal SSL coverage](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/)
- [Tunnel run parameters](https://developers.cloudflare.com/tunnel/reference/run-parameters/)
