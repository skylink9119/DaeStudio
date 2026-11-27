# DaeStudio (`daed-next`) · 新一代高性能 `dae` 可视化控制面板

**DaeStudio (`daed-next`)** 是专为 Linux eBPF 透明代理引擎 [`dae`](https://github.com/daeuniverse/dae) 设计的独立全栈管理面板。提供订阅、节点、策略组、分流规则和配置快照管理。

[下载安装包](https://github.com/skylink9119/DaeStudio/releases/latest) · [查看发行记录](https://github.com/skylink9119/DaeStudio/releases)

iStoreOS / OpenWrt 用户可以在发行页下载与设备架构匹配的 `.ipk`，通过软件包上传或 iStore 手动安装。发行页也提供 `.run`、Debian / Ubuntu 的 `.deb` 和离线包。

---

## ✨ 相比原版 `daed` 的核心升级亮点

1. **增强型多格式订阅中心（告别第三方 SubConverter）**
   - 原生自动识别并无损转换 **Clash / Mihomo YAML (`proxies:`)**、**Sing-box JSON (`outbounds`)**、**Base64 订阅** 与 **标准节点分享链接**（`vless://`, `hysteria2://`, `trojan://`, `vmess://`, `ss://`, `tuic://`）。
   - 支持自定义拉取 `User-Agent`、正则排除到期/流量提示垃圾节点、流量进度条与到期日展示。
2. **节点一键主动并发测速（告别盲猜可用性）**
   - 支持全节点高并发 TCP 握手 RTT 延迟探测与单节点即时测速。
   - 支持按地区（🇭🇰/🇹🇼/🇯🇵/🇸🇬/🇺🇸/🇰🇷/🇩🇪/🇬🇧 自动归类）、协议类型、订阅来源筛选及按最低延迟排序。
3. **智能策略组（告别几百个节点纯手动拖拽地狱）**
   - 用 **「地区快捷胶囊 + 订阅过滤 + 包含/排除关键词/正则 + 手动勾选补充」** 取代繁琐拖拽，实时展示动态命中的节点列表及当前首选出口。
   - 保留原生 `filter:` DSL 编辑模式，两种模式随时切换。
4. **可视化分流路由编排 + 预设场景包 + 原生 DSL 双向无损互转**
   - 内置一键场景模板：**🤖 AI 大模型专属分流包**、**🎬 国际流媒体解锁套装**、**🎮 Steam 下载直连 + 游戏联机加速**、**🛡️ 阻断海外 QUIC (UDP 443)**。
   - 支持可视化条件卡片编排（上下调整优先级、否定 `!`、多条件 `&&`）与原生 `routing { ... }` DSL 代码双向实时同步。
5. **100% 标准透明的 `config.dae` 生成、导入导出与快照回滚**
   - 无黑盒数据库锁定：所有配置实时编译为带清晰注释的标准 `/etc/dae/config.dae`（自动设置 `0600` 权限），支持从现有 `config.dae` 反向导入与多版本快照回滚。
6. **eBPF 系统体检与运维中心（System Doctor）**
   - 自动检测内核版本、`BTF (/sys/kernel/btf/vmlinux)`、`bpffs (/sys/fs/bpf)`、IPv4 转发与本机网卡，直观给出排错修复指引，配合实时分级日志流。
7. **现代工作台体验**
   - 零外部 CDN 依赖（软路由离线断网环境下依然毫秒级秒开）、暗黑/亮色双主题、响应式移动端抽屉导航、`⌘K / Ctrl+K` 全局快捷指令面板。

---

## 🚀 快速启动与本地预览

### 方式一：本地即时预览（无需安装 Go 或 Node，开箱即用）
项目内置与 Go 后端 100% 协议一致的零依赖 Python 3 预览守护服务，在 macOS / Linux 上可直接运行：

```bash
cd DaeStudio
python3 ./scripts/preview_server.py --host 127.0.0.1 --port 2023
```
随后在浏览器访问：`http://127.0.0.1:2023`

### 方式二：编译 Go 单二进制生产包（内嵌全部前端静态资源）
项目采用Go 标准库 + `//go:embed` 架构，**零第三方 Go 依赖、无需 Node 构建步骤**：

```bash
# 编译当前平台单二进制
make build

# 一键交叉编译 Linux amd64 / arm64 / OpenWrt mipsle 单二进制
make build-all
```

---

## 切换订阅源和节点

在「节点与订阅」添加并启用订阅，然后在「概览与选路」对应策略组的「订阅源」下拉框选择。切换订阅源会用所选订阅作为该组成员来源，并启用自动选择。点击任意候选节点后，该组会固定到该节点。两种操作都直接应用，等 dae 确认后更新当前出口。新连接使用新出口，现有连接可能短暂中断重连。

如果候选节点已删除、已停用，或切换会让正在使用的策略组没有出口，面板会显示原因并保留上一次配置。节点订阅缓存在本机，切换已导入订阅源时无需重新下载。需要修改其他分流规则时，可在「策略组管理」和「分流规则」编辑。

## 📦 Linux / OpenWrt 生产环境部署

1. 确保系统已安装 `dae` 核心（如 `/usr/bin/dae`）及 GeoIP / GeoSite 数据文件。
2. 上传编译好的 `daed-next` 二进制至 `/usr/local/bin/daed-next`（或 `/usr/bin/daed-next`）并赋予执行权限 `chmod +x`。
3. **Systemd 系统（Debian / Ubuntu / Arch）**：
   ```bash
   cp deploy/daed-next.service /etc/systemd/system/
   systemctl daemon-reload
   systemctl enable --now daed-next
   ```
4. **OpenWrt 软路由（procd）**：
   ```bash
   cp deploy/openwrt/daed-next.init /etc/init.d/daed-next
   chmod +x /etc/init.d/daed-next
   /etc/init.d/daed-next enable
   /etc/init.d/daed-next start
   ```

---

## 🧪 运行自动化测试

```bash
make test
# 单独运行 Python 预览服务测试
python3 ./tests/test_suite.py
```

### 状态保护与登录安全

构建需要 Go 1.24 或更新版本（使用标准库 PBKDF2）。现有 SHA-256 密码在成功登录时自动迁移到随机 16 字节盐、600000 次 PBKDF2-HMAC-SHA256；账号和密码无需重新设置。认证启用时，整个面板每分钟最多处理 5 次登录尝试，超额返回 HTTP 429 和 Retry-After；重启面板会重置计数。

保存携带状态版本；其他页面或后台更新了状态后，旧页面保存返回 HTTP 409，保留页面编辑供复制备份，然后刷新重新编辑。测速、鉴权设置和快照操作会同步更新当前页面版本。

状态文件损坏或无法读取时，面板停止启动，不再按首次安装覆盖数据，也不关闭独立运行的 dae。请检查服务日志与磁盘权限，先复制保留损坏文件，再从确认完好的备份中恢复 `/etc/dae/daed-next/state.json`（权限 0600），然后重启 `/etc/init.d/daed-next`。不要通过删除状态文件来绕过登录或尝试恢复；缺失文件属于首次初始化路径。

### 安全与发布

本次发布使用 Go 1.27.4 编译；详见 SECURITY.txt。全新生产安装为无演示节点的直连基础配置。未设置密码时请通过路由器 IP 访问面板；不要把未鉴权面板放到反向代理后对外开放。HTTP 本身不加密，跨不可信网络访问请使用 HTTPS 反向代理或 SSH 隧道。

发布附件提供 SHA256SUMS，在线安装器会核对下载的二进制校验和。源码仓库不包含运行配置、订阅凭据、备份和本机构建目录。Python 预览服务器仅限本机使用，不用于生产部署。

---

## 📄 开源协议

本项目基于 [MIT License](LICENSE) 开源协议发布。

### 在线更新面板

点击侧边栏的面板版本，进入「系统与日志 → 运行日志与环境体检」。官方安装包已内置新的 DaeStudio 仓库，打开更新面板即可检测。升级后会自动将旧的博客更新源切换到新仓库。兼容之前保存的更新源和 `DAED_NEXT_REPO`，也支持构建时通过 `DEFAULT_REPO` 指定仓库。

在线升级需要开启登录鉴权，支持以独立 procd 或 systemd 服务运行的面板。面板直接启动 dae 时，请先把内核交给独立服务管理。升级会校验 Release 的 SHA-256 摘要与 `SHA256SUMS`，并将旧面板二进制备份为原路径加 `.daestudio-previous`。

### GeoIP / GeoSite 分流数据库更新与定时同步

在「系统与日志 → 运行日志与环境体检」（或点击「分流规则」右上角的「Geo 数据库」）可查看并更新 `geoip.dat` 与 `geosite.dat`：
- 默认内置 **Loyalsoldier/v2ray-rules-dat 增强版**（含 `.sha256sum` 校验），支持一键切换 `MetaCubeX`、`v2fly` 标准源或自定义 HTTPS 加速镜像。
- 支持设置 **每天固定时间**（如凌晨 `04:00`）、**每周固定星期与时间** 或 **按间隔天数** 自动更新；若上游 SHA-256 与本机一致会自动跳过重复下载与闪存写入。
- 更新在临时目录完成 SHA-256 核对与 `DAE_LOCATION_ASSET` 预检（`dae validate`）后原子替换，保留 `.previous` 备份，并在 `dae` 运行中时自动平滑热重载生效。

### 默认分流与开箱即用自动补齐

安装包内置运行环境自动检测与补齐机制：
- **自动补齐系统守护脚本**：OpenWrt / iStoreOS 安装时自动检测并补齐 `/etc/init.d/dae` 与 `/etc/config/dae`（Systemd 系统自动补齐 `dae.service`）。
- **零内核一键引导**：使用 `.run` / `install.sh` 安装时会自动检测并在缺失时拉取校验官方稳定版 `dae` 内核与 `geoip.dat` / `geosite.dat`；使用 `.ipk` 安装或离线安装后，若尚未安装内核，打开面板概览页即可点击 **「一键自动安装内核与分流库」** 自动就绪，且面板实时动态感知内核状态，无需重启面板服务。
- **高效无污染默认分流**：全新安装预置国内直连、海外代理与国际流媒体策略组；国内 DNS 使用毫秒级直连阿里 `udp://223.5.5.5:53`，海外 DNS 使用经代理隧道加密的 Cloudflare `tcp://1.1.1.1:53`，全版本兼容 `v2fly` 与 `Loyalsoldier` 分流库。导入节点或订阅后点击「保存并应用」即可直接使用；无启用节点时自动启用安全直连模式并保留完整分流模板。
