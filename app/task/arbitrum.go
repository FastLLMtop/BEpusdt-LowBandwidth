package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func arbitrumInit() {
	arb := evm{
		Network: conf.Arbitrum,
		Block: block{
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Duration: time.Second * 10, Callback: arb.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: arb.tradeConfirmHandle})
}
