package task

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/conf"
	applog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
	"gorm.io/gorm"
)

func initTestEnvironment(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}

	_ = db.AutoMigrate(&model.Order{}, &model.Wallet{}, &model.Conf{})
	model.Db = db

	_ = applog.Init(filepath.Join(t.TempDir(), "logs"))

	return db
}

// TestFetchFilteredLogs_ParseSuccess 测试正常链上交易日志解析
func TestFetchFilteredLogs_ParseSuccess(t *testing.T) {
	initTestEnvironment(t)

	// 模拟公链 RPC 节点返回标准的 BSC USDT 充值日志
	// 充值金额 1.48 USDT (1.48 * 10^18 = 1480000000000000000 = 0x14878a87b8f00000)
	mockResponse := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": []map[string]any{
			{
				"address": "0x55d398326f99059ff775485246999027b3197955",
				"topics": []string{
					evmTransferEvent,
					"0x0000000000000000000000001111111111111111111111111111111111111111", // 发送方
					"0x000000000000000000000000e5471e4363058e0120b354607dfe11ef99958370", // 收款方
				},
				"data":            "0x000000000000000000000000000000000000000000000000148a04289b940000",
				"blockNumber":     "0x770ca00",
				"transactionHash": "0xabc123456789abcdef123456789abcdef123456789abcdef123456789abcdef123",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := utils.NewHttpClient()
	testEvm := evm{
		Network:           conf.Bsc,
		RpcEndpoint:       server.URL,
		FallbackEndpoints: []string{},
		Client:            client,
	}

	// 临时修改 endpoint 为 mock server
	model.SetK(model.RpcEndpointBsc, server.URL)

	contracts := []string{"0x55d398326f99059ff775485246999027b3197955"}
	wallets := []string{"0xe5471e4363058e0120b354607dfe11ef99958370"}

	transfers, err := testEvm.fetchFilteredLogs(context.Background(), 100, 120, contracts, wallets)
	if err != nil {
		t.Fatalf("fetchFilteredLogs failed: %v", err)
	}

	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}

	tr := transfers[0]
	expectedAmount := decimal.NewFromFloat(1.48)
	if !tr.Amount.Equal(expectedAmount) {
		t.Errorf("expected amount %s, got %s", expectedAmount, tr.Amount)
	}

	if tr.RecvAddress != "0xe5471e4363058e0120b354607dfe11ef99958370" {
		t.Errorf("expected recvAddr %s, got %s", "0xe5471e4363058e0120b354607dfe11ef99958370", tr.RecvAddress)
	}

	if tr.TxHash != "0xabc123456789abcdef123456789abcdef123456789abcdef123456789abcdef123" {
		t.Errorf("expected txHash %s, got %s", "0xabc...", tr.TxHash)
	}

	t.Logf("✅ 交易解析测试通过：金额 %s USDT, 收款地址: %s", tr.Amount, tr.RecvAddress)
}

// TestPollOrderTransfers_NoActiveOrders 测试无活跃订单时严格静默（0 次网络请求）
func TestPollOrderTransfers_NoActiveOrders(t *testing.T) {
	initTestEnvironment(t)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x100"})
	}))
	defer server.Close()

	model.SetK(model.RpcEndpointBsc, server.URL)

	testEvm := evm{
		Network:           conf.Bsc,
		RpcEndpoint:       server.URL,
		FallbackEndpoints: []string{},
		Client:            utils.NewHttpClient(),
	}

	// 此时数据库没有订单，执行轮询
	testEvm.pollOrderTransfers(context.Background())

	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("❌ 致命缺陷：无活跃订单时仍发起了 %d 次请求，未做到 0 请求静默！", requestCount)
	}

	t.Log("✅ 静默测试通过：数据库无活跃订单时，完全未发起任何 HTTP 请求（0 流量）")
}

// TestCallRpc_RateLimit_NoInfiniteLoop 测试遇到 Rate Limit 时优雅退避，绝不死循环重试
func TestCallRpc_RateLimit_NoInfiniteLoop(t *testing.T) {
	initTestEnvironment(t)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		// 返回节点限流错误
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error": map[string]any{
				"code":    -32005,
				"message": "Rate limit exceeded. Please upgrade your tier.",
			},
		})
	}))
	defer server.Close()

	testEvm := evm{
		Network:           conf.Bsc,
		RpcEndpoint:       server.URL,
		FallbackEndpoints: []string{server.URL},
		Client:            utils.NewHttpClient(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := testEvm.callRpc(ctx, []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`))
	if err == nil {
		t.Fatalf("expected error from rate limit, got nil")
	}

	// 确认只请求了有限次（主节点 + 备用节点），绝无死循环
	count := atomic.LoadInt32(&requestCount)
	if count > 5 {
		t.Fatalf("❌ 致命缺陷：请求次数过多 (%d 次)，疑似存在重试风暴！", count)
	}

	t.Logf("✅ 防死循环测试通过：节点限流报错被捕获，请求次数仅 %d 次，无死循环风暴", count)
}
