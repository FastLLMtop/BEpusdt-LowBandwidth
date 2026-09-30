package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/smallnest/chanx"
	"github.com/spf13/cast"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

const (
	evmTransferEvent = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	maxQueryBlockSpan = 50 // 单次 eth_getLogs 最大扫描区块跨度（确保在所有公共节点限额内）
)

var chainBlockNum sync.Map

type block struct {
	RollDelayOffset int64 // 延迟偏移量
	ConfirmedOffset int   // 确认偏移量
}

type evmNative struct {
	Parse     bool
	Decimal   int32
	TradeType model.TradeType
}

type evmBlock struct {
	From int64
	To   int64
}

type evm struct {
	Network           string
	RpcEndpoint       string                        // 指定节点地址（优先使用）
	FallbackEndpoints []string                      // 指定备用节点列表
	Block             block
	Native            evmNative
	Client            *http.Client
	blockScanQueue    *chanx.UnboundedChan[evmBlock] // 保留兼容字段
	LookbackInterval  time.Duration                 // 保留兼容字段
}

// pollOrderTransfers 核心按需轮询：仅在有活跃待支付/确认订单时发起单次定向查询，彻底废除无界重试队列
func (e *evm) pollOrderTransfers(ctx context.Context) {
	trades := model.GetNetworkTrades(model.Network(e.Network))
	if len(trades) == 0 {
		return
	}

	// 1. 检查是否有需要监听的订单 (等待支付 1、过期缓冲 3、待确认 5)
	var activeOrders []model.Order
	model.Db.Where("status in (?) and trade_type in (?)", receivableOrderStatuses(), trades).
		Where("expired_at > ?", time.Now().Add(model.GetLookbackHour())).
		Find(&activeOrders)

	// 2. 检查是否有开启"其他通知"的收款钱包 (监听外部充值)
	var otherNotifyCount int64
	model.Db.Model(&model.Wallet{}).
		Where("other_notify = ? and trade_type in (?)", model.WaOtherEnable, trades).
		Count(&otherNotifyCount)

	// 没有任何需要监控的订单或钱包，直接静默退出（0 次网络请求，0 字节流量）
	if len(activeOrders) == 0 && otherNotifyCount == 0 {
		return
	}

	// 3. 收集需要监控的目标收款地址
	wallets := model.GetNetworkWalletAddrs(model.Network(e.Network))
	if len(wallets) == 0 {
		return
	}

	// 4. 收集代币合约地址 (USDT/USDC 等)
	contracts := model.GetNetworkContracts(model.Network(e.Network))
	if len(contracts) == 0 {
		return
	}

	// 5. 获取链上最新高度
	latestBlock, err := e.getLatestBlockNumber(ctx)
	if err != nil {
		log.Task.Warn(fmt.Sprintf("[%s] 获取最新高度失败: %v, 退避休眠 10 秒", e.Network, err))
		time.Sleep(10 * time.Second)
		return
	}

	// 6. 确定安全的扫描区间 [fromBlock, latestBlock]
	var fromBlock int64
	if last, ok := chainBlockNum.Load(e.Network); ok && last.(int64) > 0 {
		fromBlock = last.(int64) + 1
		// 如果落后太多（如重启服务），最大拉取跨度锁死在 maxQueryBlockSpan（50块）
		if latestBlock-fromBlock > maxQueryBlockSpan {
			fromBlock = latestBlock - maxQueryBlockSpan
		}
	} else {
		// 首次运行或未记录高度：从 latest - 20 开始
		fromBlock = latestBlock - 20
	}

	if fromBlock > latestBlock {
		return
	}

	// 7. 发起单次精准 eth_getLogs
	transfers, err := e.fetchFilteredLogs(ctx, fromBlock, latestBlock, contracts, wallets)
	if err != nil {
		log.Task.Warn(fmt.Sprintf("[%s] 精准查询交易失败: %v, 退避休眠 10 秒", e.Network, err))
		time.Sleep(10 * time.Second)
		return
	}

	// 成功记录最新进度
	chainBlockNum.Store(e.Network, latestBlock)

	// 8. 命中充值交易推入处理队列
	if len(transfers) > 0 {
		log.Task.Info(fmt.Sprintf("🎉 [%s] 精准捕获 %d 笔入账交易！区块范围: %d → %d", e.Network, len(transfers), fromBlock, latestBlock))
		transferQueue.In <- transfers
	}
}

// getLatestBlockNumber 获取链上当前最新高度
func (e *evm) getLatestBlockNumber(ctx context.Context) (int64, error) {
	post := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	body, err := e.callRpc(ctx, post)
	if err != nil {
		return 0, err
	}

	res := gjson.ParseBytes(body)
	hexStr := res.Get("result").String()
	if hexStr == "" {
		return 0, errors.New("empty blockNumber result")
	}

	bn := utils.HexStr2Int(hexStr).Int64() - e.Block.RollDelayOffset
	if bn <= 0 {
		return 0, errors.New("invalid blockNumber")
	}

	return bn, nil
}

// fetchFilteredLogs 发送精确过滤的 eth_getLogs，由节点端直接过滤指定合约与指定收款钱包
func (e *evm) fetchFilteredLogs(ctx context.Context, from, to int64, contracts []string, wallets []string) ([]transfer, error) {
	transfers := make([]transfer, 0)

	// 跨度强制保护：绝不允许超出 maxQueryBlockSpan
	if to-from > maxQueryBlockSpan {
		from = to - maxQueryBlockSpan
	}

	// 构建合约过滤 (address)
	var addrFilter string
	if len(contracts) == 1 {
		addrFilter = fmt.Sprintf(`"address":"%s",`, strings.ToLower(contracts[0]))
	} else if len(contracts) > 1 {
		quoted := make([]string, len(contracts))
		for i, c := range contracts {
			quoted[i] = fmt.Sprintf(`"%s"`, strings.ToLower(c))
		}
		addrFilter = fmt.Sprintf(`"address":[%s],`, strings.Join(quoted, ","))
	}

	// 构建钱包收款方过滤 (topic[2])
	padded := make([]string, len(wallets))
	for i, w := range wallets {
		clean := strings.ToLower(strings.TrimPrefix(w, "0x"))
		padded[i] = fmt.Sprintf(`"0x000000000000000000000000%s"`, clean)
	}
	var topicFilter string
	if len(padded) == 1 {
		topicFilter = fmt.Sprintf(`["%s",null,%s]`, evmTransferEvent, padded[0])
	} else {
		topicFilter = fmt.Sprintf(`["%s",null,[%s]]`, evmTransferEvent, strings.Join(padded, ","))
	}

	post := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getLogs","params":[{%s"fromBlock":"0x%x","toBlock":"0x%x","topics":%s}],"id":1}`,
		addrFilter, from, to, topicFilter))

	body, err := e.callRpc(ctx, post)
	if err != nil {
		return nil, err
	}

	data := gjson.ParseBytes(body)
	now := time.Now()

	for _, itm := range data.Get("result").Array() {
		toContract := strings.ToLower(itm.Get("address").String())
		tradeType, ok := model.GetContractTrade(toContract)
		if !ok {
			continue
		}

		topics := itm.Get("topics").Array()
		if len(topics) < 3 || topics[0].String() != evmTransferEvent {
			continue
		}

		fromAddr := fmt.Sprintf("0x%s", topics[1].String()[26:])
		recvAddr := fmt.Sprintf("0x%s", topics[2].String()[26:])
		dataHex := itm.Get("data").String()
		if len(dataHex) < 3 {
			continue
		}

		amount, ok := big.NewInt(0).SetString(dataHex[2:], 16)
		if !ok || amount.Sign() <= 0 {
			continue
		}

		blockNum := cast.ToInt(utils.HexStr2Int(itm.Get("blockNumber").String()).Int64())

		transfers = append(transfers, transfer{
			Network:     e.Network,
			FromAddress: strings.ToLower(fromAddr),
			RecvAddress: strings.ToLower(recvAddr),
			Amount:      decimal.NewFromBigInt(amount, model.GetContractDecimal(toContract)),
			TxHash:      itm.Get("transactionHash").String(),
			BlockNum:    blockNum,
			Timestamp:   now,
			TradeType:   tradeType,
		})
	}

	return transfers, nil
}

// callRpc 发送 RPC 请求，支持多节点故障自动切换与严格超时控制
func (e *evm) callRpc(ctx context.Context, payload []byte) ([]byte, error) {
	endpoints := []string{e.rpcEndpoint()}
	if len(e.FallbackEndpoints) > 0 {
		endpoints = append(endpoints, e.FallbackEndpoints...)
	} else if e.Network == conf.Bsc && e.RpcEndpoint == "" {
		// 为 BSC 链内置高可用备用公共节点，主节点异常时自动切换
		for _, fb := range []string{"https://1rpc.io/bnb", "https://bsc-rpc.publicnode.com"} {
			if fb != endpoints[0] {
				endpoints = append(endpoints, fb)
			}
		}
	}

	var lastErr error
	for _, ep := range endpoints {
		reqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, "POST", ep, bytes.NewBuffer(payload))
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := e.Client.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("[%s] HTTP请求失败: %w", ep, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()

		if err != nil {
			lastErr = fmt.Errorf("[%s] 读取响应失败: %w", ep, err)
			continue
		}

		data := gjson.ParseBytes(body)
		if data.Get("error").Exists() {
			errMsg := data.Get("error.message").String()
			lastErr = fmt.Errorf("[%s] RPC返回错误: %s", ep, errMsg)
			continue
		}

		return body, nil
	}

	return nil, lastErr
}

// tradeConfirmHandle 确认待确认订单的区块回执，仅在有待确认订单时执行
func (e *evm) tradeConfirmHandle(ctx context.Context) {
	orders := getConfirmingOrders(model.GetNetworkTrades(model.Network(e.Network)))
	if len(orders) == 0 {
		return
	}

	var wg sync.WaitGroup
	handle := func(o model.Order) {
		if model.GetC(model.BlockOffsetConfirm) == "1" {
			last, ok := chainBlockNum.Load(e.Network)
			if !ok {
				return
			}
			if cast.ToInt(last)-o.RefBlockNum < e.Block.ConfirmedOffset {
				return
			}
		}

		post := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getTransactionReceipt","params":["%s"],"id":1}`, o.RefHash))
		body, err := e.callRpc(ctx, post)
		if err != nil {
			log.Task.Warn(fmt.Sprintf("[%s] tradeConfirmHandle 获取回执失败: %v", e.Network, err))
			return
		}

		data := gjson.ParseBytes(body)
		if data.Get("result.status").String() == "0x1" {
			markFinalConfirmed(o)
		}
	}

	for _, order := range orders {
		wg.Add(1)
		go func(o model.Order) {
			defer wg.Done()
			handle(o)
		}(order)
	}

	wg.Wait()
}

func (e *evm) rpcEndpoint() string {
	if e.RpcEndpoint != "" {
		return e.RpcEndpoint
	}
	return model.Endpoint(model.Network(e.Network))
}

// syncBreak 保留函数，供 aptos.go 和 solana.go 编译兼容
func syncBreak(network string, num int) bool {
	if num >= blockQueueLimit {
		log.Task.Warn(fmt.Sprintf("%s 同步阻塞，当前区块消费堆积数量：%d", network, num))
		return true
	}

	if mqttSubscribed(network) {
		return false
	}

	trades := model.GetNetworkTrades(model.Network(network))
	if len(trades) == 0 {
		return true
	}

	var count int64
	model.Db.Model(&model.Wallet{}).
		Where("other_notify = ? and trade_type in (?)", model.WaOtherEnable, trades).
		Count(&count)
	if count > 0 {
		return false
	}

	return !hasLookbackOrders(trades)
}
