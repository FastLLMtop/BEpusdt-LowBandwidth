# BEpusdt 低流量补丁 (Low-Bandwidth Patch)

> 基于 [BEpusdt v1.24.2](https://github.com/v03413/BEpusdt) 的流量优化补丁，**将 BSC 链的日常带宽消耗从 ~68 GB/天 降至 < 2 MB/天**（降幅 99.99%）。

---

## 💥 问题背景

原版 BEpusdt 在监控 BSC（BNB Smart Chain）链上 USDT 转账时，采用**全量区块扫描**策略：

1. **`eth_getBlockByNumber(block, true)`** — 每 3~5 秒下载整个区块的所有交易（含全网几百笔无关交易），单次响应 300 KB ~ 800 KB
2. **`eth_getLogs` 无过滤** — 拉取指定区块范围内**全网所有 ERC20 Transfer 事件**，不做任何合约或钱包地址过滤

### 实际流量消耗

| 时间段 | 带宽消耗 |
|:---|:---|
| 每小时 | ~2.85 GB |
| 每天 | **~68 GB** |
| 每月 | **~2 TB** |

这个流量级别足以导致大多数 VPS 被运营商限速/停机，尤其是日本、香港等地区的高价带宽。

### 为什么会这样？

BEpusdt 的设计是通用型区块扫描器，它扫描链上**所有交易**然后在本地过滤匹配。这对以太坊主网（交易量较低）尚可接受，但对 BSC 这种**每秒上百笔交易**的高吞吐链，流量爆炸。

---

## ✅ 解决方案

本补丁做了 **3 处精确修改**（共改动约 70 行代码），将"全网扫描 + 本地过滤"改为"精准查询 + 链端过滤"：

### 修改 1：关闭原生 BNB 整块下载

**文件**: `app/task/bsc.go`

```go
// 修改前
Native: evmNative{
    Parse: true,  // 下载整个区块的所有交易
}

// 修改后
Native: evmNative{
    Parse: false, // 只下载区块头（< 1 KB），不下载交易体
}
```

**效果**：`eth_getBlockByNumber` 的响应从 300~800 KB 降至 < 1 KB。

> ⚠️ 此修改关闭了 BNB（原生币）的支付监控。如果你需要收 BNB 而非 USDT/USDC，请保留 `Parse: true`。

### 修改 2：eth_getLogs 精准过滤

**文件**: `app/task/evm.go` → `parseEventTransfer()`

```go
// 修改前：拉取全网所有 ERC20 Transfer 事件
{"method":"eth_getLogs","params":[{
    "fromBlock":"0x...", "toBlock":"0x...",
    "topics":["0xddf252ad..."]  // 只过滤事件签名，不过滤合约和收款方
}]}

// 修改后：精准过滤合约地址 + 收款钱包
{"method":"eth_getLogs","params":[{
    "address": "0x55d398326f99059fF775485246999027B3197955",  // USDT 合约
    "fromBlock":"0x...", "toBlock":"0x...",
    "topics":[
        "0xddf252ad...",   // Transfer 事件
        null,              // from: 任意发送方
        ["0x000...你的钱包A", "0x000...你的钱包B"]  // to: 只监控你的钱包
    ]
}]}
```

**效果**：RPC 节点只返回转入你钱包的交易，响应从 ~130 KB 降至 38 字节（无匹配时）。

### 修改 3：新增辅助函数

**文件**: `app/model/registry.go`

- `GetNetworkContracts(network)` — 返回指定链的所有代币合约地址
- `GetNetworkWalletAddrs(network)` — 返回指定链的所有启用钱包地址

钱包地址**从数据库动态读取**，无需在代码中硬编码。添加/删除钱包后自动生效。

---

## 📊 优化效果对比

| 指标 | 原版 BEpusdt | 低流量补丁版 |
|:---|:---|:---|
| eth_getBlockByNumber 响应 | 300~800 KB/次 | **< 1 KB/次** |
| eth_getLogs 响应 | ~130 KB/次 | **38 字节/次**（无匹配时） |
| 空闲时日流量 | ~68 GB | **≈ 0 MB**（无订单时自动休眠） |
| 有订单时日流量 | ~68 GB | **< 2 MB** |
| 月流量（按空闲算） | ~2 TB | **< 60 MB** |
| 到账延迟 | 3~5 秒 | **3~5 秒**（无变化） |
| 外部 API 依赖 | 无 | **无**（直接用公链 RPC） |
| 额外费用 | 无 | **无** |

---

## 🚀 部署方式

### 方式一：Docker 构建（推荐）

```bash
# 克隆本仓库
git clone https://github.com/FastLLMtop/BEpusdt-LowBandwidth.git
cd BEpusdt-LowBandwidth

# 构建 Docker 镜像
docker build --platform linux/amd64 -t bepusdt-lowbw:latest .

# 运行（替换为你的实际路径和网络）
docker run -d \
  --name bepusdt \
  --restart=unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /your/data/path:/var/lib/bepusdt:rw \
  bepusdt-lowbw:latest
```

### 方式二：应用补丁到现有源码

```bash
# 在原版 BEpusdt v1.24.2 源码目录中
git apply bepusdt-bandwidth-fix.patch

# 重新编译
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bepusdt ./main
```

### 方式三：替换现有 Docker 容器

```bash
# 1. 导出镜像
docker save bepusdt-lowbw:latest | gzip > bepusdt-lowbw.tar.gz

# 2. 传输到服务器
scp bepusdt-lowbw.tar.gz your-server:/tmp/

# 3. 在服务器上加载并替换
ssh your-server
docker load < /tmp/bepusdt-lowbw.tar.gz
docker stop bepusdt && docker rm bepusdt
docker run -d \
  --name bepusdt \
  --restart=unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /your/data/path:/var/lib/bepusdt:rw \
  bepusdt-lowbw:latest
```

---

## 🔧 适用范围

- ✅ BSC (BNB Smart Chain) — 效果最显著
- ✅ Ethereum — 同样适用
- ✅ Polygon / Arbitrum / Base / X Layer / Plasma — 所有 EVM 链均受益
- ❌ Tron / Solana / TON / Aptos — 非 EVM 链，不受此补丁影响（它们有独立的扫描逻辑）

---

## ⚠️ 注意事项

1. **BNB 原生币收款**：本补丁关闭了 BSC 的 `Native.Parse`，如果你需要收 BNB（而非 USDT/USDC），请手动将 `app/task/bsc.go` 中的 `Parse` 改回 `true`
2. **钱包地址**：补丁从数据库动态读取钱包地址，**代码中不含任何硬编码地址**
3. **兼容性**：基于 BEpusdt v1.24.2，理论上向后兼容，但建议在测试环境验证后再部署生产

---

## 📝 技术原理

### 原版流程
```
每 3 秒 → eth_blockNumber → eth_getBlockByNumber(block, TRUE)
        → 下载整个区块 300~800 KB（含全网所有交易）
        → eth_getLogs(无过滤) → 下载全网所有 Transfer 事件
        → 本地逐条匹配钱包地址
```

### 优化后流程
```
每 3 秒 → eth_blockNumber → eth_getBlockByNumber(block, FALSE)
        → 只下载区块头 < 1 KB
        → eth_getLogs(合约 + 钱包过滤) → RPC 节点只返回命中的交易
        → 直接使用（已在链端过滤完毕）
```

核心思想：**把过滤逻辑从客户端推到 RPC 节点端**，利用 `eth_getLogs` 的 `address` 和 `topics` 参数在链端完成精确匹配，避免下载海量无关数据。

---

## 🙏 致谢

- [BEpusdt](https://github.com/v03413/BEpusdt) — 优秀的个人加密货币收款网关
- 本补丁仅优化网络流量，不修改任何业务逻辑

---

## 📄 License

与原项目保持一致。
