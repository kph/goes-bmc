// Copyright © 2019 Platina Systems, Inc. All rights reserved.
// Use of this source code is governed by the GPL-2 license described in the
// LICENSE file.

package qspi

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/platinasystems/flags"
	"github.com/platinasystems/goes/lang"
	"github.com/platinasystems/gpio"
	"github.com/platinasystems/i2c"
	"github.com/platinasystems/mtd"
	"github.com/platinasystems/ubi"
)

type Command struct{}

func (Command) String() string { return "qspi" }

func (Command) Usage() string {
	return "qspi [-mount] [-unmount] [-update] [UNIT]"
}

func (Command) Apropos() lang.Alt {
	return lang.Alt{
		lang.EnUS: "set or return selected QSPI Flash",
	}
}

func (Command) Man() lang.Alt {
	return lang.Alt{
		lang.EnUS: `
DESCRIPTION
	The qspi command reports or sets the active QSPI device.

	The -unmount option unmounts and detaches the UBI filesystems
	from the currently selected device. If -unmount is not specified
	and there are partitions mounted or attached, the command
	will return an error.

	The -mount option indicates that the new device should have
	UBI attached and volumes mounted. If the new device is not
	formatted for UBI (such as a legacy firmware version), this
	option will return an error.

	The -update option indicates that the new device persistent
	partitions should be updated.`,
	}
}

func selectQSPI(pin *gpio.Pin, q bool) error {
	//i2c STOP
	sd[0] = 0
	j[0] = I{true, i2c.Write, 0, 0, sd, int(0x99), int(1), 0}
	err := DoI2cRpc()
	if err != nil {
		return fmt.Errorf("Error in DoI2cRpc 99-1: %s", err)
	}

	pin, found := gpio.FindPin("QSPI_MUX_SEL")
	if found {
		pin.SetValue(q)
	} else {
		return fmt.Errorf("QSPI_MUX_SEL not found")
	}
	time.Sleep(200 * time.Millisecond)

	//i2c START
	sd[0] = 0
	j[0] = I{true, i2c.Write, 0, 0, sd, int(0x99), int(0), 0}
	err = DoI2cRpc()
	if err != nil {
		return fmt.Errorf("Error in DoI2cRpc 99-0: %s", err)
	}
	return nil
}

func findMount(dev string) (mountPoint string, err error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if fields[0] == dev {
			return fields[1], nil
		}
	}
	return "", scanner.Err()
}

func copyRecurse(src, dst string, overwrite bool) (err error) {
	stat, err := os.Stat(src)
	if err != nil {
		return
	}
	if stat.Mode().IsDir() {
		files, err := ioutil.ReadDir(src)
		if err != nil {
			return err
		}
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			err = os.MkdirAll(dst, stat.Mode())
			if err != nil {
				return err
			}
			fmt.Printf("mkdir -m %o %s\n",
				stat.Mode()&os.ModePerm, dst)
		}
		for _, file := range files {
			base := path.Base(file.Name())
			err = copyRecurse(src+"/"+base, dst+"/"+base, overwrite)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if !stat.Mode().IsRegular() {
		return nil // Skip special files
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	if _, err := os.Stat(dst); (overwrite && err == nil) ||
		os.IsNotExist(err) {

		destination, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer destination.Close()

		_, err = io.Copy(destination, source)
		if err != nil {
			return err
		}
		fmt.Printf("copy %s=>%s\n", src, dst)
	}
	return nil
}

func (c Command) Main(args ...string) (err error) {
	pin, found := gpio.FindPin("QSPI_MUX_SEL")
	if !found {
		return fmt.Errorf("Can't find QSPI_MUX_SEL")
	}
	sel := 0
	r, _ := pin.Value()
	if r {
		sel = 1
	}

	if len(args) == 0 {
		fmt.Printf("QSPI%d is selected\n", sel)
		return nil
	}

	flag, args := flags.New(args, "-unmount", "-mount", "-update")

	if len(args) > 0 {
		sel, err = strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("Error parsing %s: %s", args[0], err)
		}
		args = args[1:]
		if len(args) > 0 {
			return fmt.Errorf("%v: unexpected", args)
		}
		if sel < 0 || sel > 1 {
			return fmt.Errorf("Invalid unit number %d", sel)
		}
	}

	umount := false
	if mnt, err := ubi.IsUbiMounted(0, 0); err == nil {
		if mnt {
			if !flag.ByName["-unmount"] {
				return fmt.Errorf("Can't switch QSPI with UBI mounted, use -unmount")
			}
			umount = true
		}
	} else {
		return fmt.Errorf("Error determining if UBI is mounted: %s",
			err)
	}

	detach := false
	if att, err := ubi.IsUbiAttached(0); err == nil {
		if att {
			if !flag.ByName["-unmount"] {
				return fmt.Errorf("Can't switch QSPI with UBI attached, use -unmount")
			} else {
				detach = true
			}
		} else {
			if umount {
				return fmt.Errorf("Mounted but not attached, aborting!")
			}
		}
	} else {
		return fmt.Errorf("Error determining if UBI is attached: %s",
			err)
	}

	err = copyRecurse("/boot", "/volatile/boot", true)
	if err != nil {
		return fmt.Errorf("Error copying /boot to /volatile/boot: %s",
			err)
	}
	err = copyRecurse("/etc", "/volatile/etc", true)
	if err != nil {
		return fmt.Errorf("Error copying /etc to /volatile/etc: %s",
			err)
	}
	err = copyRecurse("/var/perm", "/volatile/var/perm", true)
	if err != nil {
		return fmt.Errorf("Error copying /etc to /volatile/var/perm: %s",
			err)
	}

	if umount {
		for {
			dev, err := findMount("/dev/ubi0_0")
			if err != nil {
				return fmt.Errorf("Error finding UBI mounts: %s",
					err)
			}
			if dev == "" {
				break
			}
			if err := syscall.Unmount(dev, 0); err != nil {
				return fmt.Errorf("Error unmounting %s: %s",
					dev, err)
			}
		}
	}

	if detach {
		err := ubi.Detach(0)
		if err != nil {
			return fmt.Errorf("Error in ubidetach: %s",
				err)
		}
	}
	err = selectQSPI(pin, sel != 0)
	if err != nil {
		return fmt.Errorf("Error in selectQSPI: %s\n",
			err)
	}

	ubiDev, err := mtd.NameToUnit("ubi")
	if err != nil {
		return err
	}

	//ubiDevName := "/dev/mtd" + strconv.Itoa(ubiDev)
	if flag.ByName["-mount"] {
		// Check if this is a UBI volume. If not, do not
		// attempt to attach it.
		isUbi, err := ubi.IsUbi(int32(ubiDev))
		if err != nil {
			return err
		}
		if !isUbi {
			return fmt.Errorf("Not a UBI partition, can't attach")
		}

		err = ubi.Attach(0, int32(ubiDev), 0, 0)
		if err != nil {
			return fmt.Errorf("Error in ubiattach: %s",
				err)
		}

		for _, mount := range []struct {
			mp     string
			dev    string
			fstype string
			flags  uintptr
		}{
			{"/perm", "/dev/ubi0_0", "ubifs", 0},
			{"/boot", "/perm/boot", "", syscall.MS_BIND},
			{"/etc", "/perm/etc", "", syscall.MS_BIND},
			{"/var/perm", "/perm/var/perm", "", syscall.MS_BIND},
		} {
			if _, err := os.Stat(mount.dev); os.IsNotExist(err) {
				err = os.MkdirAll(mount.dev, 0644)
				if err != nil {
					return fmt.Errorf("Error creating %s: %v", mount.dev, err)
				}
			}
			if err := syscall.Mount(mount.dev, mount.mp,
				mount.fstype, mount.flags, ""); err != nil {
				return fmt.Errorf("Error mounting %s: %s",
					mount.mp, err)
			}
		}
		if flag.ByName["-update"] {
			err = copyRecurse("/volatile/boot", "/perm/boot", true)
			if err != nil {
				return fmt.Errorf("Error copying /volatile/boot to /perm/boot: %s",
					err)
			}
			err = copyRecurse("/volatile/etc", "/perm/etc", true)
			if err != nil {
				return fmt.Errorf("Error copying /volatile/etc to /perm/etc: %s",
					err)
			}
			err = copyRecurse("/volatile/var/perm", "/perm/var/perm", true)
			if err != nil {
				return fmt.Errorf("Error copying /volatile/var/perm to /perm/var/perm: %s",
					err)
			}
		}
	}

	r, _ = pin.Value()
	qspi := 0
	if r {
		qspi = 1
	}
	fmt.Printf("Selected QSPI%d\n", qspi)

	return nil
}
