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
	"github.com/LINBIT/drbdtop/pkg/collect"
	"github.com/LINBIT/drbdtop/pkg/resource"
	"github.com/LINBIT/drbdtop/pkg/update"
	gc "github.com/rthornton128/goncurses"
	"log"
	"os"
	"os/exec"
	"os/signal"
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

func main() {
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

	var padSys, padDrbd, padMenu, padJobs *gc.Pad
	padSys, err = gc.NewPad(800, 200)
	if err != nil {
		log.Fatal(err)
	}
	padDrbd, err = gc.NewPad(800, 200)
	if err != nil {
		log.Fatal(err)
	}
	padMenu, err = gc.NewPad(800, 200)
	if err != nil {
		log.Fatal(err)
	}
	padJobs, err = gc.NewPad(800, 200)
	if err != nil {
		log.Fatal(err)
	}
	//pad.Keypad(true)

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
	progress := "-/|\\"
	i := 0
main:
	for {
		padSys.Erase()
		padDrbd.Erase()
		padJobs.Erase()
		padMenu.Erase()
		rows, cols := stdscr.MaxYX()
		//padSys.Printf("%d %d\n", rows, cols)
		//padDrbd.Printf("%d %d\n", rows, cols)
		padMenu.Printf("x\tscrollup\ny\tscroll down\nq\tquit")
		cmd := exec.Command("systemctl", "list-dependencies", "multi-user.target")
		//		cmd := exec.Command("uptime")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Fatal(err)
		}
		colorPrint(padSys, string(out))
		cmd = exec.Command("systemctl", "list-jobs")
		//		cmd := exec.Command("uptime")
		out, err = cmd.CombinedOutput()
		if err != nil {
			log.Fatal(err)
		}
		colorPrint(padJobs, string(out))
		resources.UpdateList()
		resources.RLock()
		padDrbd.Printf("%c%19s %10s %14s %10s %10s %14s %10s\n", progress[i%len(progress)], "Resource", "LocalRole", "LocalDisk", "Connection", "RemoteRole", "RemoteDisk", "OutOfSync")

		for _, r := range resources.List {
			r.RLock()
			d := r.Device
			for k, v := range d.Volumes {
				padDrbd.Printf("%02s%18s %10s ", v.Minor, r.Res.Name, r.Res.Role)
				padDrbd.Printf("%14s ", v.DiskState)
				for _, c := range r.Connections {
					padDrbd.Printf("%10s %10s ", c.ConnectionStatus, c.Role)
				}
				for _, p := range r.PeerDevices {
					if vr, ok := p.Volumes[k]; ok {
						padDrbd.Printf("%14s ", vr.DiskState)
						padDrbd.Printf("%9d%%", int(vr.OutOfSyncKiB.Current*100/v.Size))
					}
				}
				padDrbd.Printf("\n")
			}
			r.RUnlock()
		}
		resources.RUnlock()
		i++
		padSys.NoutRefresh(scroll, 0, 1, 1, rows-6, int(cols/2)-1)
		padDrbd.NoutRefresh(scroll, 0, 1, int(cols/2), rows-6, cols-2)
		padMenu.NoutRefresh(0, 0, rows-6, 1, rows-1, int(cols/2)-1)
		padJobs.NoutRefresh(scroll, 0, rows-6, int(cols/2), rows-1, cols-2)
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
			case 'q':
				break main
			case 'x':
				if scroll > 0 {
					scroll--
				}
			case 'y':
				//	if scroll < rows-height {
				scroll++
				//	}
				//case gc.KEY_RESIZE:
				//	break main
			}
		case <-fin:
			break main
		}
	}
	padSys.Delete()
	padDrbd.Delete()
}
