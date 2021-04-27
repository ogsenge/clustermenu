/*
 *drbdtop - statistics for DRBD
 *Copyright © 2017 Hayley Swimelar and Roland Kammerer
 *
 *This program is free software; you can redistribute it and/or modify
 *it under the terms of the GNU General Public License as published by
 *the Free Software Foundation; either version 2 of the License, or
 *(at your option) any later version.
 *
 *This program is distributed in the hope that it will be useful,
 *but WITHOUT ANY WARRANTY; without even the implied warranty of
 *MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 *GNU General Public License for more details.
 *
 *You should have received a copy of the GNU General Public License
 *along with this program; if not, see <http://www.gnu.org/licenses/>.
 */

package main

// #cgo CFLAGS: -std=c99 -D_GNU_SOURCE -D_DEFAULT_SOURCE
// #cgo LDFLAGS: -lncursesw -ltinfo
// #include <locale.h>
import "C"

import (
	"fmt"
	"github.com/LINBIT/drbdtop/pkg/collect"
	"github.com/LINBIT/drbdtop/pkg/resource"
	"github.com/LINBIT/drbdtop/pkg/update"
	gc "github.com/rthornton128/goncurses"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Version defines the version of the program and gets set via ldflags
var Version string
var colMap map[int]int
var maxPair int

func SetupCloseHandler() chan os.Signal {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	return c
}

func colorPrint(win *gc.Pad, out string) {
	dummyString := "0m" + out
	tokens := strings.Split(dummyString, "\x1b[")
	for _, i := range tokens {
		tmp := strings.SplitN(i, "m", 2)
		col := strings.Split(tmp[0], ";")
		c, _ := strconv.Atoi(col[len(col)-1])
		if idx, ok := colMap[c]; ok {
			win.ColorOn(int16(idx))
		} else {
			maxPair++
			colMap[c] = maxPair
			gc.InitPair(int16(maxPair), int16(c-30), gc.C_BLACK)
			win.ColorOn(int16(maxPair))
		}
		win.Print(tmp[1])
	}

}

func onResize(channel chan os.Signal) {
	for {
		<-channel
		gc.End()
		gc.Update()
	}
}

type Paddy int

const (
	Sys Paddy = iota
	Drbd
	Menu
	Jobs
)

func newPad(y, x int) *gc.Pad {
	pad, err := gc.NewPad(800, 200)
	if err != nil {
		log.Fatal(err)
	}
	return pad
}

func myExec(name string, arg ...string) string {
	cmd := exec.Command(name, arg...)
	cmd.Env = append(os.Environ(),
		"SYSTEMD_COLORS=1",
	)
	//		cmd := exec.Command("uptime")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatal(err)
	}
	return string(out)
}

var i int

func filter(ss []string, rex string) (ret []string) {
	for _, s := range ss {
		if matched, _ := regexp.MatchString(rex, s); matched {
			ret = append(ret, s)
		}
	}
	return
}

func jobStatus(pad *gc.Pad) bool {
	working := false
	jobs := myExec("systemctl", "list-jobs")
	lines := strings.Split(jobs, "\n")
	run := filter(lines, `running`)
	wait := filter(lines, `waiting`)
	if len(run) > 0 {
		colorPrint(pad, strings.Join(run, "\n"))
		working = true
	} else {
		colorPrint(pad, myExec("systemctl", "list-jobs"))
	}
	if len(wait) > 0 {
		colorPrint(pad, fmt.Sprintf("\n+%2d waiting", len(wait)))
		working = true
	}
	return working
}

func drbdSatus(pad *gc.Pad, resources *update.ResourceCollection) {
	progress := "-/|\\"
	resources.UpdateList()
	resources.RLock()
	pad.Printf("%c%19s %10s %14s %10s %10s %14s %10s\n", progress[i%len(progress)], "Resource", "LocalRole", "LocalDisk", "Connection", "RemoteRole", "RemoteDisk", "OutOfSync")

	for _, r := range resources.List {
		r.RLock()
		d := r.Device
		keys := make([]string, 0, len(d.Volumes))
		for k := range d.Volumes {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := d.Volumes[k]
			pad.Printf("%02s%18s %10s ", v.Minor, r.Res.Name, r.Res.Role)
			pad.Printf("%14s ", v.DiskState)
			for _, c := range r.Connections {
				pad.Printf("%10s %10s ", c.ConnectionStatus, c.Role)
			}
			for _, p := range r.PeerDevices {
				if vr, ok := p.Volumes[k]; ok {
					pad.Printf("%14s ", vr.DiskState)
					pad.Printf("%9d%%", int(vr.OutOfSyncKiB.Current*100/v.Size))
				}
			}
			pad.Printf("\n")
		}
		r.RUnlock()
	}
	resources.RUnlock()
	i++
}

func main() {
	allPads := []Paddy{Sys, Drbd, Menu, Jobs}
	resizeChannel := make(chan os.Signal)
	signal.Notify(resizeChannel, syscall.SIGWINCH)
	go onResize(resizeChannel)

	colMap = make(map[int]int)
	fin := SetupCloseHandler()
	C.setlocale(C.LC_ALL, C.CString(""))
	errors := make(chan error, 100)

	duration := time.Second * 1

	input := collect.Events2Poll{Interval: duration}

	events := make(chan resource.Event, 5)
	go input.Collect(events, errors)
	resources := update.NewResourceCollection(duration)
	scroll := 0
	stdscr, err := gc.Init()
	if err != nil {
		log.Fatal(err)
	}
	defer gc.End()

	// Turn off character echo, hide the cursor and disable input buffering
	gc.Echo(false)
	gc.CBreak(true)
	gc.Cursor(0)
	gc.StartColor()
	gc.UseDefaultColors()
	stdscr.NoutRefresh()
	pad := make(map[Paddy]*gc.Pad)
	for _, p := range allPads {
		pad[p] = newPad(800, 200)
	}
	stdscr.Keypad(true)

	key := make(chan gc.Key)

	go func() {
		for {
			key <- stdscr.GetChar()
		}
	}()
	go func() {
		for m := range events {
			//fmt.Printf(".");
			resources.Update(m)
		}
	}()
	splitCols := 40
	rowOffset := 6
main:
	for {
		for _, p := range allPads {
			pad[p].Erase()
		}
		rows, cols := stdscr.MaxYX()
		splitRows := rows - rowOffset
		//pad[Sys].Printf("%d %d\n", rows, cols)
		pad[Menu].Printf("2\tEnable\n3\tDisable\n9\tLogout")
		colorPrint(pad[Sys], strings.Split(myExec("systemctl", "list-dependencies", "cluster-active.target"), "multi-user.target")[0]+"multi-user.target")
		jobStatus(pad[Jobs])
		drbdSatus(pad[Drbd], resources)
		//splitCols:=int(cols/2)
		pad[Sys].NoutRefresh(scroll, 0, 0, 0, splitRows-1, splitCols-1)
		pad[Drbd].NoutRefresh(scroll, 0, 0, splitCols+1, splitRows-1, cols-1)
		pad[Menu].NoutRefresh(0, 0, splitRows+1, 0, rows-1, splitCols-1)
		pad[Jobs].NoutRefresh(scroll, 0, splitRows+1, splitCols+1, rows-1, cols-1)
		//stdscr.Refresh()
		// Update will flush only the characters which have changed between the
		// physical screen and the virtual screen, minimizing the number of
		// characters which must be sent
		gc.Update()

		// In order for the paddow to display correctly, we must call GetChar()
		// on it rather than stdscr
		select {
		case <-time.After(1 * time.Second):
			//nothing
		case k := <-key:
			switch k {
			case '9':
				break main
			case gc.KEY_UP:
				if scroll > 0 {
					scroll--
				}
			case gc.KEY_DOWN:
				scroll++
			case gc.KEY_LEFT:
				if rowOffset > 0 {
					rowOffset--
				}
			case gc.KEY_RIGHT:
				if rowOffset < rows-1 {
					rowOffset++
				}
			case '2':
				myExec("sudo", "/usr/bin/systemctl", "isolate", "--no-block", "cluster-active.target")
			case '3':
				myExec("sudo", "/usr/bin/systemctl", "isolate", "--no-block", "multi-user.target")
			}
		case <-fin:
			break main
		}
	}
	for _, p := range allPads {
		pad[p].Delete()
	}
}
