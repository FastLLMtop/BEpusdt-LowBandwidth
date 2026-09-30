package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
)

func ethInit() {
	eth := evm{
		Network: conf.Ethereum,
		Block: block{
			ConfirmedOffset: 12,
		},
		Native: evmNative{
			Parse:     false,
			TradeType: model.EthereumEth,
			Decimal:   conf.EthereumEthDecimals,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Duration: time.Second * 12, Callback: eth.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: eth.tradeConfirmHandle})
}
