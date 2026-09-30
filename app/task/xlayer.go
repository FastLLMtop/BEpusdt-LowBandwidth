package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func xlayerInit() {
	xlayer := evm{
		Network: conf.Xlayer,
		Block: block{
			RollDelayOffset: 3,
			ConfirmedOffset: 12,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Duration: time.Second * 10, Callback: xlayer.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: xlayer.tradeConfirmHandle})
}
