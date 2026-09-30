package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

func bscInit() {
	bsc := evm{
		Network: conf.Bsc,
		Block: block{
			ConfirmedOffset: 15,
		},
		Native: evmNative{
			Parse:     false,
			Decimal:   conf.BscBnbDecimals,
			TradeType: model.BscBnb,
		},
		Client: utils.NewHttpClient(),
	}

	// 方案 B：按需定时单次轮询，彻底告别扫块队列和死循环
	Register(Task{Duration: time.Second * 10, Callback: bsc.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: bsc.tradeConfirmHandle})
}
