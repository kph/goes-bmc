// Copyright © 2015-2017 Platina Systems, Inc. All rights reserved.
// Use of this source code is governed by the GPL-2 license described in the
// LICENSE file.

package upgrade

import (
	"archive/zip"
	"bufio"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/platinasystems/ubi"
	"github.com/platinasystems/url"
)

type IMGINFO struct {
	Name   string
	Build  string
	User   string
	Size   string
	Tag    string
	Commit string
	Chksum string
}

func getFile(s string, fn string) (int, error) {
	rmFile(fn)
	urls := s + "/" + fn
	r, err := url.Open(urls)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(TmpDir, fn),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, DfltMod)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return 0, err
	}
	syscall.Fsync(int(os.Stdout.Fd()))
	return int(n), nil
}

func rmFiles() {
	for _, j := range img {
		os.Remove(filepath.Join(TmpDir, Machine+"-"+j+".bin"))
	}
	for _, j := range legacyImg {
		os.Remove(filepath.Join(TmpDir, Machine+"-"+j+".bin"))
	}
	rmFile(ArchiveName)
	rmFile(V2Name)
	return
}

func rmFile(f string) error {
	fn := filepath.Join(TmpDir, f)
	if _, err := os.Stat(fn); err != nil {
		return err
	}
	if err := os.Remove(fn); err != nil {
		return err
	}
	return nil
}

func unzip() error {
	archive := filepath.Join(TmpDir, ArchiveName)
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	target := TmpDir
	for _, file := range reader.File {
		path := filepath.Join(target, file.Name)
		if file.FileInfo().IsDir() {
			os.MkdirAll(path, file.Mode())
			continue
		}
		r, err := file.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		t, err := os.OpenFile(
			path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}
		defer t.Close()
		if _, err := io.Copy(t, r); err != nil {
			return err
		}
	}
	return nil
}

func printJSON() error {
	iv, err := GetVerArchive()
	if err != nil {
		return err
	}

	fmt.Printf("Installed version is %s\n\n", iv)
	b, err := getVer()
	if err != nil {
		return err
	}
	k := 0
	for i, j := range b {
		if j == ']' {
			k = i
		}
	}
	if k > 0 {
		fmt.Println("")
		var ImgInfo [5]IMGINFO
		json.Unmarshal(b[JSON_OFFSET:k+1], &ImgInfo)
		for i, _ := range ImgInfo {
			fmt.Println("    Name  : ", ImgInfo[i].Name)
			fmt.Println("    Build : ", ImgInfo[i].Build)
			fmt.Println("    User  : ", ImgInfo[i].User)
			fmt.Println("    Size  : ", ImgInfo[i].Size)
			fmt.Println("    Tag   : ", ImgInfo[i].Tag)
			fmt.Println("    Commit: ", ImgInfo[i].Commit)
			fmt.Println("    Chksum: ", ImgInfo[i].Chksum)
			fmt.Println("")
		}
	}
	return nil
}

func GetVerArchive() (string, error) {
	b, err := getVer()
	if err != nil {
		return "00000000", nil
	}

	qv := string(b[VERSION_OFFSET:VERSION_LEN])
	if string(b[VERSION_OFFSET:VERSION_DEV]) == "dev" {
		qv = "dev"
	} else {
		_, err = strconv.ParseFloat(qv, 64)
		if err != nil {
			qv = "00000000"
		}
	}
	return qv, nil
}

func printVerServer(s string, sv string) {
	fmt.Print("\n")
	fmt.Print("Version on server:\n")
	fmt.Printf("    Requested URL     : %s\n", s)
	fmt.Printf("    Found version     : %s\n", sv)
	fmt.Print("\n")
}

func isVersionNewer(cur string, x string) (n bool, err error) {
	if cur == "dev" || x == "dev" {
		return true, nil
	}
	var c float64 = 0.0
	var f float64 = 0.0
	cur = strings.TrimSpace(cur)
	cur = strings.Replace(cur, "v", "", -1)
	c, err = strconv.ParseFloat(cur, 64)
	if err != nil {
		c = 0.0
	}
	x = strings.TrimSpace(x)
	x = strings.Replace(x, "v", "", -1)
	f, err = strconv.ParseFloat(x, 64)
	if err != nil {
		f = 0.0
	}
	if f >= c {
		return true, nil
	}
	return false, nil
}

func cmpSums() (err error) {
	var ImgInfo [5]IMGINFO

	b, err := getVer()
	if err != nil {
		return err
	}
	k := 0
	for i, j := range b {
		if j == ']' {
			k = i
		}
	}
	if k > 0 {
		json.Unmarshal(b[JSON_OFFSET:k+1], &ImgInfo)
	} else {
		fmt.Println("Version block not found, skipping check")
		return nil
	}

	fd, err = syscall.Open(MTDdevice, syscall.O_RDWR, 0)
	if err != nil {
		err = fmt.Errorf("Open error %s: %s", MTDdevice, err)
		return err
	}
	defer syscall.Close(fd)
	var calcSums [5]string
	nn, bb, err := readQSPI(0x0, 0xc0000)
	if err != nil {
		err = fmt.Errorf("Read error: %s %v",
			"ubo", err)
		return err
	}
	if nn != int(0xc0000) {
		return fmt.Errorf("Size error expecting %x got %x",
			0xc0000, nn)
	}
	l, err := strconv.Atoi(ImgInfo[0].Size)
	if err != nil {
		return err
	}
	h := sha1.New()
	io.WriteString(h, string(bb[0:l]))
	calcSums[0] = fmt.Sprintf("%x", h.Sum(nil))

	bb, err = ioutil.ReadFile("/boot/" + Machine + "-itb.bin")
	if err != nil {
		return fmt.Errorf("Error reading itb: %s", err)
	}
	l, err = strconv.Atoi(ImgInfo[4].Size)
	if err != nil {
		return err
	}
	h = sha1.New()
	io.WriteString(h, string(bb[0:l]))
	calcSums[4] = fmt.Sprintf("%x", h.Sum(nil))

	chkFail := false
	for i, _ := range ImgInfo {
		if calcSums[i] != "" && ImgInfo[i].Chksum != calcSums[i] {
			fmt.Println("Checksum fail: ", ImgInfo[i].Name)
			chkFail = true
		}
	}
	if chkFail == false {
		fmt.Println("Checksums match.")
	}
	return nil
}

func getPerFile() (b []byte, err error) {
	return ioutil.ReadFile("/boot/" + Machine + "-per.bin")
}

func getVer() (b []byte, err error) {
	isUbi, err := ubi.IsUbi(3)
	if err != nil {
		return nil,
			fmt.Errorf("Error determining if QSPI is UBI: %s", err)
	}
	if isUbi {
		return ioutil.ReadFile("/perm/boot/" + Machine + "-ver.bin")
	} else {
		return readBlk("ver")
	}
}

func findDevForMountpoint(mp string) (dev string, err error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", fmt.Errorf("Unable to open /proc/mounts: %s", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if fields[1] == mp {
			return fields[0], nil
		}
	}
	return "", scanner.Err()
}
