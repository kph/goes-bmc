// Copyright © 2015-2016 Platina Systems, Inc. All rights reserved.
// Use of this source code is governed by the GPL-2 license described in the
// LICENSE file.

package main

import (
	"fmt"

	"github.com/platinasystems/goes-bmc/cmd/ledgpiod"
	"github.com/platinasystems/goes/external/redis"
)

func ledgpiodInit() {
	ledgpiod.VpageByKey = map[string]uint8{
		"system.fan_direction": 0,
	}
	ver := 0
	ledgpiod.Vdev.Bus = 5
	ledgpiod.Vdev.Addr = 0x75
	s, err := redis.Hget(redis.DefaultHash, "eeprom.DeviceVersion")
	if err != nil {
		ledgpiod.Vdev.Addr = 0x75
	} else {
		_, _ = fmt.Sscan(s, &ver)
		switch ver {
		case 0xff:
			ledgpiod.Vdev.Addr = 0x22
		case 0x00:
			ledgpiod.Vdev.Addr = 0x22
		default:
			ledgpiod.Vdev.Addr = 0x75
		}
	}
	ledgpiod.WrRegDv["ledgpiod"] = "ledgpiod"
	ledgpiod.WrRegFn["ledgpiod.example"] = "example"
	ledgpiod.WrRegRng["ledgpiod.example"] = []string{"true", "false"}
}
