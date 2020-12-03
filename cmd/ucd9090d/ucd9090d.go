// Copyright © 2015-2016 Platina Systems, Inc. All rights reserved.
// Use of this source code is governed by the GPL-2 license described in the
// LICENSE file.

// Package ucd9090d provides access to the UCD9090 Power Sequencer/Monitor chip
package ucd9090d

import (
	"fmt"
	"math"
	"net/rpc"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/platinasystems/atsock"
	"github.com/platinasystems/goes"
	"github.com/platinasystems/goes-bmc/cmd/fantrayd"
	"github.com/platinasystems/goes-bmc/cmd/ledgpiod"
	"github.com/platinasystems/goes-bmc/cmd/w83795d"
	"github.com/platinasystems/goes/cmd"
	"github.com/platinasystems/goes/external/redis"
	"github.com/platinasystems/goes/external/redis/publisher"
	"github.com/platinasystems/goes/external/redis/rpc/args"
	"github.com/platinasystems/goes/external/redis/rpc/reply"
	"github.com/platinasystems/goes/lang"
	"github.com/platinasystems/gpio"
	"github.com/platinasystems/log"
)

var (
	Vdev I2cDev

	VpageByKey map[string]uint8

	WrRegDv  = make(map[string]string)
	WrRegFn  = make(map[string]string)
	WrRegVal = make(map[string]string)
	WrRegRng = make(map[string][]string)

	loggedFaultCount      uint8
	lastLoggedFaultDetail [12]byte

	first    int
	firstLog int

	watchdogEn       bool
	watchdogTimeout  uint
	watchdogSequence string
	watchdogTimer    uint
	watchdogExpired  bool
)

type Command struct {
	Info
	Init func()
	init sync.Once
}

type Info struct {
	mutex sync.Mutex
	rpc   *atsock.RpcServer
	pub   *publisher.Publisher
	last  map[string]float64
	lasts map[string]string
	lastu map[string]uint16
}

type I2cDev struct {
	Bus  int
	Addr int
}

func (*Command) String() string { return "ucd9090d" }

func (*Command) Usage() string { return "ucd9090d" }

func (*Command) Apropos() lang.Alt {
	return lang.Alt{
		lang.EnUS: "ucd9090 power sequencer daemon",
	}
}

func (*Command) Kind() cmd.Kind { return cmd.Daemon }

func (c *Command) Main(...string) error {
	var si syscall.Sysinfo_t

	if c.Init != nil {
		c.init.Do(c.Init)
	}

	err := redis.IsReady()
	if err != nil {
		return err
	}

	first = 1
	firstLog = 1
	watchdogEn = false
	watchdogTimer = 0
	watchdogTimeout = 30
	watchdogSequence = "0"
	watchdogExpired = false

	c.last = make(map[string]float64)
	c.lasts = make(map[string]string)
	c.lastu = make(map[string]uint16)

	if c.pub, err = publisher.New(); err != nil {
		return err
	}

	if err = syscall.Sysinfo(&si); err != nil {
		return err
	}

	if c.rpc, err = atsock.NewRpcServer("ucd9090d"); err != nil {
		return err
	}

	rpc.Register(&c.Info)
	for _, v := range WrRegDv {
		err = redis.Assign(redis.DefaultHash+":"+v+".", "ucd9090d", "Info")
		if err != nil {
			return err
		}
	}

	t := time.NewTicker(10 * time.Second)
	tw := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-goes.Stop:
			return nil
		case <-t.C:
			if Vdev.Addr != 0 {
				if err = c.update(); err != nil {
				}
			}
		case <-tw.C:
			c.updateW()
		}

	}
	return nil
}

func (c *Command) update() error {
	stopped := readStopped()
	if stopped == 1 {
		return nil
	}

	if first == 1 {
		Vdev.ucdInit()
		first = 0
	}

	for k, i := range VpageByKey {
		if strings.Contains(k, "units.V") {
			v, err := Vdev.Vout(i)
			if err != nil {
				return err
			}
			if v != c.last[k] {
				c.pub.Print(k, ": ", v)
				c.last[k] = v
			}
		}
		if strings.Contains(k, "poweroff.events") {
			v, err := Vdev.PowerCycles()
			if err != nil {
				return err
			}
			if (v != "") && (v != c.lasts[k]) {
				c.pub.Print(k, ": ", v)
				c.lasts[k] = v
			}
		}
	}
	return nil
}

func (c *Command) updateW() error {

	if err := writeRegs(); err != nil {
		return err
	}

	k := "watchdog.enable"
	v := strconv.FormatBool(watchdogEn)
	if v != c.lasts[k] {
		c.pub.Print(k, ": ", v)
		c.lasts[k] = v
	}

	k = "watchdog.timeout.units.seconds"
	v = strconv.Itoa(int(watchdogTimeout))
	if v != c.lasts[k] {
		c.pub.Print(k, ": ", v)
		c.lasts[k] = v
	}

	k = "watchdog.timer.units.seconds"
	v = strconv.Itoa(int(watchdogTimer))
	if v != c.lasts[k] {
		c.pub.Print(k, ": ", v)
		c.lasts[k] = v
	}

	k = "watchdog.sequence"
	v = watchdogSequence
	if v != c.lasts[k] {
		c.pub.Print(k, ": ", v)
		c.lasts[k] = v
	}

	k = "watchdog.expired"
	v = strconv.FormatBool(watchdogExpired)
	if v != c.lasts[k] {
		c.pub.Print(k, ": ", v)
		c.lasts[k] = v
	}

	if watchdogEn {
		if watchdogTimer < watchdogTimeout {
			watchdogTimer++
		}
		if watchdogTimer >= watchdogTimeout {
			log.Print("warning: host watchdog timer expired; reset host; disable watchdog")
			watchdogExpired = true
			pin, found := gpio.FindPin("BMC_TO_HOST_RST_L")
			if found {
				pin.SetValue(false)
				time.Sleep(100 * time.Millisecond)
				pin.SetValue(true)
				watchdogEn = false
				watchdogTimer = 0
			}
		}
	}

	return nil
}

func (h *I2cDev) ucdInit() error {
	//FIXME configure UCD run time clock, pending ntp
	return nil
}

func (h *I2cDev) Vout(i uint8) (float64, error) {
	if i > 10 {
		panic("Voltage rail subscript out of range\n")
	}
	i--

	r := getRegs()
	r.Page.set(h, i)
	r.VoutMode.get(h)
	r.ReadVout.get(h)
	err := DoI2cRpc()
	if err != nil {
		return 0, err
	}
	n := s[1].D[0] & 0xf
	n--
	n = (n ^ 0xf) & 0xf
	v := uint16(s[2].D[1])<<8 | uint16(s[2].D[0])

	nn := float64(n) * (-1)
	vv := float64(v) * (math.Exp2(nn))
	vv, _ = strconv.ParseFloat(fmt.Sprintf("%.3f", vv), 64)
	return float64(vv), nil
}

func (h *I2cDev) PowerCycles() (string, error) {
	r := getRegs()
	r.LoggedFaultIndex.get(h)
	err := DoI2cRpc()
	if err != nil {
		return "", err
	}

	d := s[0].D[1]

	var milli uint32
	var seconds uint32
	var faultType uint8
	var pwrCycles string

	for i := 0; i < int(d); i++ {
		r.LoggedFaultIndex.set(h, uint16(i)<<8)
		err := DoI2cRpc()
		if err != nil {
			return "", err
		}
		r.LoggedFaultDetail.get(h, 11)
		err = DoI2cRpc()
		if err != nil {
			return "", err
		}

		if i == 0 {
			new := false
			if loggedFaultCount != d {
				loggedFaultCount = d
				copy(lastLoggedFaultDetail[:], s[0].D[0:12])
				new = true
			} else {
				for j := 0; j < 12; j++ {
					if s[0].D[j] != lastLoggedFaultDetail[j] {
						copy(lastLoggedFaultDetail[:], s[0].D[0:12])
						new = true
						break
					}
				}
			}
			if !new {
				return "", nil
			}
			if firstLog == 0 {
				log.Printf("warning: power event detected")
				time.Sleep(5 * time.Second)

				log.Print("notice: re-init fan controller")
				w83795d.Vdev.Bus = 11
				w83795d.Vdev.Addr = 0x2f
				w83795d.Vdev.FanInit()

				log.Print("notice: re-init fan trays")
				fantrayd.Vdev.Bus = 1
				fantrayd.Vdev.Addr = 0x20
				fantrayd.Vdev.MuxBus = 1
				fantrayd.Vdev.MuxAddr = 0x72
				fantrayd.Vdev.MuxValue = 0x04
				fantrayd.Vdev.FanTrayLedReinit()

				log.Print("notice: re-init front panel LEDs")
				ver := 0
				s, _ := redis.Hget(redis.DefaultHash, "eeprom.DeviceVersion")
				_, _ = fmt.Sscan(s, &ver)
				if ver == 0 || ver == 0xff {
					ledgpiod.Vdev.Addr = 0x22
				} else {
					ledgpiod.Vdev.Addr = 0x75
				}
				ledgpiod.Vdev.Bus = 5
				ledgpiod.Vdev.LedFpReinit()
			}
		}
		milli = uint32(s[0].D[5]) + uint32(s[0].D[4])<<8 + uint32(s[0].D[3])<<16 + uint32(s[0].D[2])<<24
		seconds = milli / 1000
		timestamp := time.Unix(int64(seconds), 0).Format(time.RFC3339)

		faultType = (s[0].D[6] >> 3) & 0xF

		if !strings.Contains(pwrCycles, timestamp) && (faultType == 0 || faultType == 1) {
			pwrCycles += timestamp + "."
		}
	}
	pwrCycles = strings.Trim(pwrCycles, ".")
	firstLog = 0
	return pwrCycles, nil
}

func (h *I2cDev) LoggedFaultDetail() (string, error) {
	r := getRegs()
	r.LoggedFaultIndex.get(h)
	err := DoI2cRpc()
	if err != nil {
		return "", err
	}

	d := s[0].D[1]

	var milli uint32
	var page uint8
	var seconds uint32
	var faultType uint8
	var paged uint8
	var rail string
	var fault string
	var log string

	for i := 0; i < int(d); i++ {
		r.LoggedFaultIndex.set(h, uint16(i)<<8)
		err := DoI2cRpc()
		if err != nil {
			return "", err
		}
		r.LoggedFaultDetail.get(h, 11)
		err = DoI2cRpc()
		if err != nil {
			return "", err
		}

		if i == 0 {
			new := false
			if loggedFaultCount != d {
				loggedFaultCount = d
				copy(lastLoggedFaultDetail[:], s[0].D[0:12])
				new = true
			} else {
				for j := 0; j < 12; j++ {
					if s[0].D[j] != lastLoggedFaultDetail[j] {
						copy(lastLoggedFaultDetail[:], s[0].D[0:12])
						new = true
						break
					}
				}
			}
			if !new {
				return "", nil
			}
		}
		milli = uint32(s[0].D[5]) + uint32(s[0].D[4])<<8 + uint32(s[0].D[3])<<16 + uint32(s[0].D[2])<<24
		seconds = milli / 1000
		timestamp := time.Unix(int64(seconds), 0).Format(time.RFC3339)

		faultType = (s[0].D[6] >> 3) & 0xF
		paged = s[0].D[6] & 0x80 >> 7
		page = ((s[0].D[7] & 0x80) >> 7) + ((s[0].D[6] & 0x7) << 1)

		if paged == 1 {
			switch page {
			case 0:
				rail = "P5V_SB"
			case 1:
				rail = "P3V8_BMC"
			case 2:
				rail = "P3V3_SB"
			case 3:
				rail = "PERI_3V3"
			case 4:
				rail = "P3V3"
			case 5:
				rail = "VDD_CORE"
			case 6:
				rail = "P1V8"
			case 7:
				rail = "P1V25"
			case 8:
				rail = "P1V2"
			case 9:
				rail = "P1V0"
			default:
				rail = "n/a"
			}
			switch faultType {
			case 0:
				fault = "VOUT_OV"
			case 1:
				fault = "VOUT_UV"
			case 2:
				fault = "TON_MAX"
			case 3:
				fault = "IOUT_OC"
			case 4:
				fault = "IOUT_UC"
			case 5:
				fault = "TEMPERATURE_OT"
			case 6:
				fault = "SEQUENCE ON TIMEOUT"
			case 7:
				fault = "SEQUENCE OFF TIMEOUT"
			default:
				fault = "unknown"
			}
		} else {
			rail = "n/a"
			switch faultType {
			case 1:
				fault = "SYSTEM WATCHDOG TIMEOUT"
			case 2:
				fault = "RESEQUENCE ERROR"
			case 3:
				fault = "WATCHDOG TIMEOUT"
			case 8:
				fault = "FAN FAULT"
			case 9:
				fault = "GPI FAULT"
			default:
				fault = "unknown"
			}

		}
		log += timestamp + "." + rail + "." + fault + "\n"
	}
	return log, nil
}

func writeRegs() error {
	for k, v := range WrRegVal {
		switch WrRegFn[k] {
		case "speed":
			if false {
				log.Print("test", k, v)
			}
		case "watchdog.enable":
			enable, err := strconv.ParseBool(v)
			if err == nil {
				if !enable {
					watchdogTimer = 0
				} else {
					watchdogExpired = false
				}
				watchdogEn = enable
			}
		case "watchdog.sequence":
			watchdogTimer = 0
			if watchdogEn {
				watchdogSequence = v
			} else {
				k = "watchdog.sequence"
				pub, err := publisher.New()
				if err == nil {
					pub.Print(k, ": ", watchdogSequence)
				}
			}
		case "watchdog.timeout.units.seconds":
			i, err := strconv.ParseInt(v, 10, 64)
			log.Print("v: ", v, "i: ", i, "err: ", err)
			if err == nil {
				watchdogTimeout = uint(i)
			}
		}
		delete(WrRegVal, k)
	}
	return nil
}

func (i *Info) Hset(args args.Hset, reply *reply.Hset) error {
	_, p := WrRegFn[args.Field]
	if !p {
		return fmt.Errorf("cannot hset: %s", args.Field)
	}
	_, q := WrRegRng[args.Field]
	if !q {
		err := i.set(args.Field, string(args.Value), false)
		if err == nil {
			*reply = 1
			WrRegVal[args.Field] = string(args.Value)
		}
		return err
	}
	var a [2]int
	var e [2]error
	if len(WrRegRng[args.Field]) == 2 {
		for i, v := range WrRegRng[args.Field] {
			a[i], e[i] = strconv.Atoi(v)
		}
		if e[0] == nil && e[1] == nil {
			val, err := strconv.Atoi(string(args.Value))
			if err != nil {
				return err
			}
			if val >= a[0] && val <= a[1] {
				err := i.set(args.Field,
					string(args.Value), false)
				if err == nil {
					*reply = 1
					WrRegVal[args.Field] =
						string(args.Value)
				}
				return err
			}
			return fmt.Errorf("Cannot hset.  Valid range is: %s",
				WrRegRng[args.Field])
		}
	}
	for _, v := range WrRegRng[args.Field] {
		if v == string(args.Value) {
			err := i.set(args.Field, string(args.Value), false)
			if err == nil {
				*reply = 1
				WrRegVal[args.Field] = string(args.Value)
			}
			return err
		}
	}
	return fmt.Errorf("Cannot hset.  Valid values are: %s",
		WrRegRng[args.Field])
}

func (i *Info) set(key, value string, isReadyEvent bool) error {
	i.pub.Print(key, ": ", value)
	return nil
}

func (i *Info) publish(key string, value interface{}) {
	i.pub.Print(key, ": ", value)
}
