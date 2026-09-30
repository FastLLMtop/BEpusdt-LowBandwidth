package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func plasmaInit() {
	xpl := evm{
		Network: conf.Plasma,
		Block: block{
			ConfirmedOffset: 40,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Duration: time.Second * 10, Callback: xpl.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: xpl.tradeConfirmHandle})
}
