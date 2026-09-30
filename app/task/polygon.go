package task

import (
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/utils"
)

func polygonInit() {
	pol := evm{
		Network: conf.Polygon,
		Block: block{
			ConfirmedOffset: 30,
		},
		Client: utils.NewHttpClient(),
	}

	Register(Task{Duration: time.Second * 10, Callback: pol.pollOrderTransfers})
	Register(Task{Duration: time.Second * 10, Callback: pol.tradeConfirmHandle})
}
