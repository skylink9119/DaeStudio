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

### 默认分流与首次使用

全新安装预置国内直连、海外代理和 YouTube 独立策略组，国内 DNS 使用阿里 DoH，海外 DNS 使用经代理访问的 Cloudflare DoH。策略组自动接纳所有启用节点，节点入口域名自动使用国内 DNS 并直连。广告拦截默认关闭，可按需开启。

安装包不包含订阅、节点凭据或个人出口地址。导入节点或订阅后，点击「保存并应用」即可使用默认分流。没有启用节点时，运行配置使用直连和国内 DNS，面板保留完整分流模板。默认 LAN 接口为 br-lan，其他拓扑需要在界面选择实际 LAN 接口。

恢复默认设置会清空节点和订阅，并恢复上述模板；原配置会自动备份。已有配置在覆盖安装时继续使用原有设置。


### v1.0.4 内置 dae 内核

ARM64、x86_64、x86_32 的 .run、.tar.gz、.ipk 和 .deb 安装包均内置官方 dae v2.1.3、geoip.dat 和 geosite.dat，通用离线包包含全部三个架构。安装包可以离线安装面板及内核，完成后导入节点并保存应用即可启用代理。设备仍需要具备 dae 所需的 Linux 内核和 eBPF 能力。

正式面板不提供模拟模式参数，也不会在内核缺失时生成模拟运行状态或流量。单独下载面板二进制时，需要另外安装 dae；全新安装应使用完整安装包。

DaeStudio 使用 MIT 协议。内置 dae 的协议见 DAE-LICENSE，安装包同时携带 dae-source.tar.gz、CORE-NOTICE.txt 和官方发行文件的 SHA-256 清单 dae-core.json，安装到 /usr/share/doc/daed-next。构建脚本根据 scripts/dae-core.json 校验官方内核文件。

### v1.0.6 保存应用与 DNS 规则修复

- 核心直接启动时使用独立、轮转日志，避免面板重启后日志管道失效影响热重载。
- DNS 长规则默认折叠；自动生成的节点域名规则增加标记，导入或转换表单时不再作为静态规则保留。
- 安装包继续包含 dae v2.1.3、GeoIP/GeoSite 数据及协议文件，不包含个人节点与配置。
- 历史版本中已失去自动标记的静态规则不会擅自删除，需要确认后手动清理。

### v1.0.5 默认 DNS 缓存与白名单

首次安装与恢复默认设置使用 DNS DSL 模板，默认监听 127.0.0.1:15353，启用 optimistic_cache，过期缓存后台刷新窗口为 60 秒，最多缓存 8192 条记录。国内使用阿里 DoH，海外使用经代理访问的 Cloudflare DoH。Apple 国内服务的 DNS 白名单与直连规则一致，淘宝和喜马拉雅按通用国内规则匹配，广告拦截默认关闭。

导入以域名作为服务器地址的节点时，DSL 渲染也会自动添加节点入口域名的国内 DNS 解析规则。无节点时使用带缓存的国内 DNS 与直连配置。已有配置在升级后保留；恢复默认设置会清空节点和订阅，请先备份后操作。
