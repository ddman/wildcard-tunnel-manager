# Wildcard Tunnel Manager

[English](README.md) · **繁體中文**

在 Mac 上以本機網頁管理 `名稱.example.com/路徑 → localhost:port`。每台主機各有一條 Cloudflare Tunnel，共用一筆可切換的 `*.example.com` CNAME；已有明確 DNS 紀錄的名稱會由 Cloudflare DNS 優先處理，不會進入此 wildcard。

網域由 `.env` 的 `DDMAN_BASE_DOMAIN` 指定；以下以 `example.com` 示範。管理介面只在本機回環位址與選用的 Tailscale 位址開放，公開路由則由 Cloudflare Tunnel 提供。`DDMAN_` 環境變數名稱與 socket 目錄名稱是為了相容舊版而保留，不表示只能使用 `ddman.cc`。

## 快速開始

需要 macOS、Go 1.26+、`cloudflared`，以及可管理自己網域的 Cloudflare 帳號。先安裝 [`cloudflared`](https://developers.cloudflare.com/tunnel/get-started/)（例如 `brew install cloudflared`），再執行：

```bash
git clone https://github.com/ddman/wildcard-tunnel-manager.git
cd wildcard-tunnel-manager
cp .env.example .env
chmod 600 .env
# 編輯 .env，設定 DDMAN_BASE_DOMAIN、DDMAN_ADMIN_HOST 與至少 16 字元的獨特密碼
go run .
```

開啟 `http://127.0.0.1:8787` 登入，新增一條路由，例如 `app → http → 3000`。要讓 `app.example.com` 對外可用，再依下方「Cloudflare 設定」建立 API Token，於管理頁填入 Account ID、Zone ID 和 Token，按「設定並連線」。這個操作會建立或沿用 Tunnel，並建立 `*.example.com` 的 DNS 紀錄；若 wildcard DNS 已指向其他 Tunnel，需明確選擇強制綁定。

## 技術與路徑

```text
瀏覽器 → Cloudflare DNS / Universal SSL → Cloudflare Tunnel
       → cloudflared → Unix socket → Go reverse proxy
       → 127.0.0.1:<服務 port>
```

管理頁在 `http://127.0.0.1:<DDMAN_ADMIN_PORT>` 監聽（目前為 8787）。若 `.env` 設定 `DDMAN_ADMIN_TAILSCALE_IP`，也會監聽該主機的 Tailscale IP 與同一 port。瀏覽器會先看到 HTML 登入頁，登入後使用限時 session cookie；CLI 可繼續用 HTTP Basic 帳密呼叫 API。管理頁不經 Cloudflare Tunnel 對外發布，Tailscale 入口僅供允許的 tailnet 裝置使用。代理入口是 `/tmp/ddman-home-tunnel-<uid>/<設定檔雜湊>.sock`，不監聽 TCP port；管理頁會顯示實際路徑。每個設定檔各有自己的 socket，避免 worktree 路徑過長或互相衝突。舊版 Tunnel 尚未改指向 socket 時，程式會暫時保留 `127.0.0.1:8788`；管理頁成功重新套用 Tunnel 設定後即關閉。Go 程式負責管理頁、設定檔、HTTP/HTTPS origin 反向代理和 Cloudflare API。`cloudflared` 是唯一額外執行依賴。沒有前端建置流程，也不需要 Caddy 或 Docker。

## 啟動

需要 Go 1.26+ 和 [cloudflared](https://developers.cloudflare.com/tunnel/get-started/)。可用 `brew install cloudflared` 安裝到 PATH，或自行將可執行檔放在 `tools/cloudflared`。

```bash
cd wildcard-tunnel-manager
go run .
```

首次啟動前，先參考 `.env.example` 建立僅本人可讀的 `.env`，填入 `DDMAN_BASE_DOMAIN`、`DDMAN_ADMIN_HOST`、`DDMAN_ADMIN_PORT`、`DDMAN_ADMIN_USERNAME`、`DDMAN_ADMIN_PASSWORD`；可選填 `DDMAN_ADMIN_TAILSCALE_IP` 開啟 tailnet 入口。若要啟動時檢查 Cloudflare 綁定，再填入 `CF_ACCOUNT_ID`、`CF_ZONE_ID`、`CF_API_TOKEN`，並執行 `chmod 600 .env`。此檔保存明文密碼，已從 Git 排除；請使用獨特的長密碼。`DDMAN_ADMIN_HOST` 目前只保留該子網域不作為公開路由；管理頁和 API 不會經由 Tunnel 對外發布。

登入後可在「管理帳密」頁輸入目前密碼，修改管理帳號與密碼；修改會立即生效並寫回 `.env`，不需重啟。若忘記目前密碼，須在主機上重設 `.env` 的帳密後重啟服務，遠端介面不提供無驗證重設。

開啟 `http://127.0.0.1:8787`，先新增 `app → http → 3000`。本機驗證：

```bash
curl --unix-socket '<管理頁顯示的 socket 路徑>' http://app.example.com/
```

同一個子網域可加入多條路徑路由。例如保留 `app.example.com/ → http://127.0.0.1:3000`，再加入 `app.example.com/api → http://127.0.0.1:4000`。最長的路徑前綴優先，且只在完整路徑區段邊界匹配：`/api/users` 符合 `/api`，`/apix` 不符合。預設保留路徑，所以上游收到 `/api/users`；勾選移除前綴後，上游收到 `/users`。舊設定中沒有 `path` 的路由仍代表 `/`。若上游網頁會輸出以 `/` 開頭的資源或導向，移除前綴可能需要另外設定該應用程式的 base path。

### Cloudflare 設定

在 Dashboard 建立範圍最小的 API Token：Account / Cloudflare Tunnel / Edit，以及所選網域的 Zone / DNS / Edit（DNS Write）。注意「DNS 設定：編輯」（DNS Settings Edit）是不同權限，不能讀寫 DNS 紀錄。從 Dashboard 複製 Account ID 和 Zone ID。如果已在 Dashboard 建立空白的遠端管理 Tunnel，可把其 Tunnel ID 填入選填欄位；程式會先確認 Tunnel 沒有既有路由，再沿用它。否則會建立專用 Tunnel。設定流程將 `*.example.com` 設為 ingress 指向本機 Unix socket、建立 proxied wildcard CNAME，最後啟動本機 `cloudflared`；不建立管理頁 ingress 或 DNS。若 `*.example.com` 已指向另一條 Tunnel，一般綁定會顯示衝突；只有明確按下「強制綁定」才會把這筆 DNS 改指向本機 Tunnel。Dashboard 的安裝指令含 Tunnel token；使用本工具時無需執行。從舊版 `8788` 入口升級時，需在管理頁重新輸入 API Token 並按「設定並連線」，更新現有 Tunnel 的服務位址；此操作會沿用既有 Tunnel 和 DNS 紀錄。

也可透過 CLI 完成同一操作。CLI 會向執行中的本機管理 API 讀取已保存的 Account ID、Zone ID 和 Tunnel ID；API Token 僅由標準輸入傳遞，不放在命令參數中。於 zsh 執行：

```bash
read -s "CF_API_TOKEN?Cloudflare API Token: "
printf '%s' "$CF_API_TOKEN" | go run . tunnel use-socket --token-stdin
unset CF_API_TOKEN
```

成功後，程式會關閉舊版 `8788` 入口。此命令需在管理程式執行期間使用。

設定保存在 `config.json`，包含 Tunnel token，檔案權限為 0600，且已加入 `.gitignore`。API Token、Account ID 與 Zone ID 會保存在權限 0600 的 `.env`，供啟動後定期查詢 DNS 與 Tunnel 綁定狀態；首次在設定頁輸入後會寫入，亦可手動填入。請勿將 `.env` 提交 Git 或分享。Tunnel token 只能執行連線，不能用來查詢或修改 Tunnel 的公開 hostname。程式下次啟動會讀取 Tunnel token，重新啟動 `cloudflared`。請勿把 `config.json` 複製給其他人；持有 Tunnel token 的人可執行連線。

## 範圍與限制

- 多台主機可各自有 Tunnel；同一時間 `*.example.com` 只指向其中一條。兩台機器需有各自的 `config.json`，不能共用 Tunnel token。強制綁定只切換 DNS，不會遠端停止舊主機。
- 只接受單層名稱，例如 `app.example.com`，不處理 `app.home.example.com`。後者的 HTTPS 需要額外憑證。
- 路徑路由只支援固定前綴，未提供正規表示式、上游重新導向或回應內容改寫。
- 公開網址預設沒有登入控管；知道網址的人都能使用服務。請勿掛上未設定認證的管理介面或含私密資料的服務。
- origin 服務必須監聽本機回環位址；HTTPS origin 需有 Go 能驗證的有效憑證。瀏覽器到 Cloudflare 的 HTTPS 與 cloudflared 到本機 socket 的 HTTP 是不同連線。`cloudflared` 須能讀寫 socket；本工具啟動的子程序使用相同使用者身分。
- `cloudflared` 與此 Go 程式需持續執行；Mac 睡眠或程式停止時服務不可用。目前未安裝 macOS 背景啟動服務。
- 若 Cloudflare API 設定中途失敗，重新按「建立並連線」會沿用已儲存的 Tunnel 繼續；若需刪除 Tunnel 或 DNS，請在 Cloudflare Dashboard 處理。

## 官方文件

- [Cloudflare Tunnel API setup](https://developers.cloudflare.com/tunnel/get-started/)
- [Wildcard DNS priority](https://developers.cloudflare.com/dns/manage-dns-records/reference/wildcard-dns-records/)
- [Universal SSL coverage](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/)
- [Tunnel run parameters](https://developers.cloudflare.com/tunnel/reference/run-parameters/)

## 多主機接管

家裡與辦公室可使用相同的 Cloudflare Account ID、Zone ID 和 API Token，但各自保留獨立的 `config.json` 和 Tunnel ID。管理頁會每 30 秒查詢 wildcard DNS：指向本機 Tunnel 顯示已綁定，指向其他 Tunnel 顯示衝突。一般綁定不覆蓋衝突；按「強制綁定」後才更新 CNAME，並重新查詢以驗證。取消綁定只刪除仍指向本機 Tunnel 的 wildcard DNS，不刪除 Tunnel。

同一台電腦模擬兩個實例時，使用兩個設定目錄與不同的 `DDMAN_ADMIN_PORT`。正式 wildcard DNS 切換會影響目前公開服務；自動化測試使用模擬 Cloudflare API。既有連線可能短暫留在舊主機，且明確 DNS 紀錄的名稱不受 wildcard 切換影響。
