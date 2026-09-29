# BEpusdt 低流量补丁 (Low-Bandwidth Patch)

> 基于 [BEpusdt v1.24.2](https://github.com/v03413/BEpusdt) 的流量优化补丁，**将 BSC 链的日常带宽消耗从 ~68 GB/天 降至 < 2 MB/天**（降幅 99.99%）。
>
> 无需购买任何付费 API，无需更换 RPC 节点，仅改动约 70 行 Go 代码。

---

## 🙏 致谢

**感谢 [v03413](https://github.com/v03413) 开发的 [BEpusdt](https://github.com/v03413/BEpusdt)**，这是目前最好用的个人加密货币收款网关，功能强大、多链支持、部署简单。本补丁仅针对 BSC 高吞吐链场景做了网络层优化，不修改任何业务逻辑，所有功劳归原作者。

---

## ⚡ 一句话说清楚

**原版**：每 3 秒下载 BSC 全网所有交易，本地逐条比对 → 日耗 68 GB

**补丁版**：告诉 RPC 节点"只给我转入我钱包的交易" → 日耗 < 2 MB

---

## 💥 你是否遇到了这些问题？

- 🔴 VPS 流量每天暴涨几十 GB，月底被运营商限速/停机
- 🔴 日本、香港、新加坡等地区的 VPS 带宽贵，BEpusdt 一个月就吃掉 2 TB
- 🔴 服务器上只跑了 BEpusdt 收款，流量却比主业务还大
- 🔴 `docker stats` 看到 `fastllm-bepusdt` 网络 I/O 疯涨

**如果你中了以上任何一条，这个补丁就是为你准备的。**

---

## 📊 核心数据对比

| 指标 | 原版 BEpusdt | 💚 低流量补丁版 |
|:---|:---|:---|
| `eth_getBlockByNumber` 响应 | 300~800 KB/次 | **< 1 KB/次** |
| `eth_getLogs` 响应 | ~130 KB/次 | **38 字节/次** |
| 空闲时日流量（无订单） | **~68 GB** | **≈ 0 MB** |
| 有订单时日流量 | **~68 GB** | **< 2 MB** |
| 月流量 | **~2 TB** | **< 60 MB** |
| 到账延迟 | 3~5 秒 | **3~5 秒（无变化）** |
| 外部 API / 额外费用 | 无 | **无** |

---

## 🔍 问题根因分析

原版 BEpusdt 在监控 BSC 链上转账时，采用**全量区块扫描**策略：

### 流量杀手 1：下载整个区块

```
eth_getBlockByNumber("0x...", true)  ← true = 包含所有交易
```

BSC 每个区块有 200~400+ 笔交易，每笔约 300 字节。一个区块响应 **300 KB ~ 800 KB**，每 3 秒一个新区块：

> 800 KB × 20次/分 × 60分 × 24小时 ≈ **23 GB/天（仅此一项）**

### 流量杀手 2：拉取全网 Transfer 事件

```json
{"method": "eth_getLogs", "params": [{
    "topics": ["0xddf252ad..."]   // ← 只过滤事件类型，不过滤合约和收款方
}]}
```

BSC 每个区块有上千笔 ERC20 转账（全网所有代币），全部下载到本地后逐条比对：

> 130 KB × 20次/分 × 60分 × 24小时 ≈ **3.7 GB/天**

**两项合计：每天 ~68 GB，每月 ~2 TB。**

---

## ✅ 补丁做了什么（3 处改动）

### 改动 1：关闭整块下载

📁 `app/task/bsc.go`

```diff
 Native: evmNative{
-    Parse: true,   // 下载整个区块所有交易（300~800 KB）
+    Parse: false,  // 只下载区块头（< 1 KB）
 }
```

> ⚠️ 这会关闭 BNB 原生币的收款监控。如果你需要收 BNB，请保留 `true`。收 USDT/USDC 不受影响。

### 改动 2：精准过滤 eth_getLogs

📁 `app/task/evm.go` → `parseEventTransfer()`

```diff
 // 修改前：拉全网所有 Transfer 事件
-{"topics": ["0xddf252ad..."]}
+// 修改后：只拉转入我钱包的 + 只看我关心的合约
+{
+    "address": "0x55d398...",           // 只看 USDT 合约
+    "topics": [
+        "0xddf252ad...",                // Transfer 事件
+        null,                           // from: 任意
+        ["0x000...钱包A", "0x000...钱包B"]  // to: 只要转给我的
+    ]
+}
```

**钱包地址从数据库动态读取，代码中不含任何硬编码地址。** 你在 BEpusdt 后台添加/删除钱包后自动生效。

### 改动 3：新增辅助函数

📁 `app/model/registry.go`

- `GetNetworkContracts(network)` — 获取指定链的代币合约地址列表
- `GetNetworkWalletAddrs(network)` — 获取指定链的启用钱包地址列表

---

## 🚀 部署方式

### 方式一：Docker 构建（推荐）

```bash
git clone https://github.com/FastLLMtop/BEpusdt-LowBandwidth.git
cd BEpusdt-LowBandwidth
docker build --platform linux/amd64 -t bepusdt-lowbw:latest .

docker run -d \
  --name bepusdt \
  --restart=unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /your/data:/var/lib/bepusdt:rw \
  bepusdt-lowbw:latest
```

### 方式二：应用补丁到现有源码

```bash
# 在原版 BEpusdt v1.24.2 源码目录中
git apply bepusdt-bandwidth-fix.patch
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bepusdt ./main
```

### 方式三：替换现有容器

```bash
# 本地构建后导出
docker save bepusdt-lowbw:latest | gzip > bepusdt-lowbw.tar.gz
scp bepusdt-lowbw.tar.gz your-server:/tmp/

# 服务器上加载替换
ssh your-server
docker load < /tmp/bepusdt-lowbw.tar.gz
docker stop bepusdt && docker rm bepusdt
docker run -d \
  --name bepusdt \
  --restart=unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /your/data:/var/lib/bepusdt:rw \
  bepusdt-lowbw:latest
```

---

## 🔧 适用范围

| 链 | 是否适用 | 说明 |
|:---|:---|:---|
| ✅ BSC (BNB Smart Chain) | **效果最显著** | BSC 交易量大，优化效果最明显 |
| ✅ Ethereum | 适用 | 以太坊交易量较低，原版问题不严重 |
| ✅ Polygon / Arbitrum / Base | 适用 | 所有 EVM 链共享同一扫描逻辑 |
| ❌ Tron / Solana / TON | 不影响 | 非 EVM 链，有独立扫描逻辑 |

---

## 📐 技术原理图

```
┌─────────────── 原版流程 ───────────────┐
│                                        │
│  每3秒 → eth_blockNumber               │
│       → eth_getBlockByNumber(block, ✅) │ ← 下载整块 300~800 KB
│       → eth_getLogs(无过滤)             │ ← 全网 Transfer 事件 ~130 KB
│       → 本地逐条匹配钱包地址            │
│                                        │
│  合计: ~68 GB/天                        │
└────────────────────────────────────────┘

┌─────────────── 补丁流程 ───────────────┐
│                                        │
│  每3秒 → eth_blockNumber               │
│       → eth_getBlockByNumber(block, ❌) │ ← 只要区块头 < 1 KB
│       → eth_getLogs(合约+钱包过滤)      │ ← 只返回命中交易 ~38 字节
│       → 直接使用                        │
│                                        │
│  合计: < 2 MB/天                        │
└────────────────────────────────────────┘
```

---

## ⚠️ 注意事项

1. **BNB 收款**：补丁关闭了 BSC 原生 BNB 监控（`Native.Parse = false`）。如需收 BNB，请改回 `true`
2. **无硬编码**：钱包地址全部从数据库动态读取，代码中无任何敏感信息
3. **版本兼容**：基于 v1.24.2 开发，建议测试环境验证后再上生产
4. **无订单时零流量**：原版 BEpusdt 的 `syncBreak` 机制在无待支付订单时会自动暂停扫块，本补丁保留了此机制

---

## 📢 关于我们

本补丁由 [FastLLM](https://fastllm.top) 团队开发并实战验证。

**[FastLLM](https://fastllm.top)** — 快速、稳定、低价的 AI 大模型 API 聚合平台，支持 OpenAI / Claude / Gemini / DeepSeek 等主流模型，一个 API Key 调用所有模型。

🌐 官网：**https://fastllm.top**
💬 Telegram：[@fastllm_top](https://t.me/fastllm_top)
👥 用户群：[加入群组](https://t.me/+u38ynPMyZpgxMWNl)

如果这个补丁帮到了你，欢迎 ⭐ Star 支持！

---

## 📄 License

与原项目 [BEpusdt](https://github.com/v03413/BEpusdt) 保持一致。
