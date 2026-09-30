package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func baseInit() {
	base := evm{
		Network: conf.Base,
		Block: block{
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Duration: time.Second * 10, Callback: base.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: base.tradeConfirmHandle})
}
