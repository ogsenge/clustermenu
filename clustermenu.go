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
	"errors"
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
var colorMap map[int]int
var maxPair int
var splitCluster bool
var activeTargets []string
var drbd bool
var drbdResources map[string][]string
var confirmationPending bool
var confirmationMessage string
var confirmationCommand []string
var myhostname string

type Paddy int

const (
	Sys Paddy = iota
	Drbd
	Menu
	Jobs
)

type VolumeState struct {
	Minor            string
	ResourceName     string
	LocalRole        string
	LocalDisk        string
	ConnectionStatus string
	RemoteRole       string
	RemoteDisk       string
	OutOfSyncKib     uint64
	VolumeSizeKib    uint64
}

func SetupCloseHandler() chan os.Signal {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	return c
}

/* color code for gc.InitPair:
<0 = terminal default
0 = black
1 = red
2 = green
3 = yellow
4 = blue
5 = pink
6 = turquoise
7 = gray
>7 = black
black foreground color not allowed,
since this functions always prints with black background */
func colorPrint(win *gc.Pad, out string) {
	dummyString := "0m" + out
	tokens := strings.Split(dummyString, "\x1b[")
	for _, i := range tokens {
		tmp := strings.SplitN(i, "m", 2)
		col := strings.Split(tmp[0], ";")
		c, _ := strconv.Atoi(col[len(col)-1])
		if idx, ok := colorMap[c]; ok {
			win.ColorOn(int16(idx))
		} else {
			maxPair++
			colorMap[c] = maxPair
			if c >= 31 && c <= 37 {
				gc.InitPair(int16(maxPair), int16(c-30), gc.C_BLACK)
			} else {
				gc.InitPair(int16(maxPair), gc.C_WHITE, gc.C_BLACK)
			}
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
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func filter(ss []string, rex string) (ret []string) {
	for _, s := range ss {
		if matched, _ := regexp.MatchString(rex, s); matched {
			ret = append(ret, s)
		}
	}
	return
}

func systemdDependencyExclude(ss []string, rex string) (ret []string) {
	inSubtree := false
	prefix := ""
	var prefixRegex *regexp.Regexp
	prefixLength := 0
	re := regexp.MustCompile(rex)
	prefixConvert := regexp.MustCompile(`\[[0-9;]+m`)
	for _, s := range ss {
		if loc := re.FindStringIndex(s); loc != nil {
			inSubtree = true
			prefix = ""
			prefixLength = loc[0]
			ret = append(ret, s)
		} else {
			if inSubtree {
				if prefix == "" {
					prefix = s[:prefixLength]
					r := []rune(prefix)
					prefix = string(r[:len(r)-2])
					prefixRegex = regexp.MustCompile("^" + prefixConvert.ReplaceAllString(prefix, "\\[[0-9;]+m"))
				} else {
					if !prefixRegex.MatchString(s) {
						inSubtree = false
						ret = append(ret, s)
					}
				}
			} else {
				ret = append(ret, s)
			}
		}
	}
	return
}

func isDrbd() bool {
	if _, err := os.Stat("/proc/drbd"); errors.Is(err, os.ErrNotExist) {
		return false
	} else {
		return true
	}
}

func hasRunningJobs() bool {
	jobs := myExec("systemctl", "list-jobs")
	lines := strings.Split(jobs, "\n")
	noJob := filter(lines, `No jobs running.`)
	if len(noJob) > 0 {
		return false
	}
	return true
}

func printJobStatus(pad *gc.Pad) {
	pad.Printf("=== Jobs ===\n")
	jobs := myExec("systemctl", "list-jobs")
	lines := strings.Split(jobs, "\n")
	// returns array of lines where either "running" or "waiting" is present
	run := filter(lines, `running`)
	wait := filter(lines, `waiting`)
	if len(run) > 0 {
		colorPrint(pad, strings.Join(run, "\n"))
	} else {
		colorPrint(pad, jobs)
	}
	if len(wait) > 0 {
		colorPrint(pad, fmt.Sprintf("\n+%2d waiting", len(wait)))
	}
}

func printMenu(pad *gc.Pad, resources *update.ResourceCollection) {
	pad.Printf("=== Menu === \n")
	pad.Printf(myhostname)
	pad.Printf(time.Now().Format(time.RFC1123) + "\n")
	pad.Printf("Please select an operation:\n")
	if !hasRunningJobs() {
		if contains(activeTargets, "multi-user.target") {
			if allowedToEnable("cluster-active.target", resources) {
				pad.Printf("2)\tEnable this computer (APP+DB)\n")
			}
			pad.Printf("4)\tShutdown this computer\n")
			pad.Printf("5)\tReboot this computer\n")
			if splitCluster {
				if allowedToEnable("app-active.target", resources) {
					pad.Printf("6)\tEnable Applications\n")
				}
				if allowedToEnable("db-active.target", resources) {
					pad.Printf("7)\tEnable Databases\n")
				}
			}
		} else {
			pad.Printf("3)\tDisable this computer\n")
			pad.Printf("4)\tShutdown this computer\n")
			pad.Printf("5)\tReboot this computer\n")
		}
	}
	pad.Printf("9)\tLogout")
}

func printSys(pad *gc.Pad) {
	pad.Printf("=== Cluster Services === \n")
	var dependencies string
	dependencies = myExec("systemctl", "list-dependencies", "cluster-active.target")
	splitDependencies := strings.Split(dependencies, "\n")
	filteredDependencies := systemdDependencyExclude(splitDependencies, "multi-user.target")
	colorPrint(pad, strings.Join(filteredDependencies, "\n"))
}

func getDrbdResourcesForTarget(target string) (resources []string) {
	dependencies := myExec("systemctl", "list-dependencies", target, "--all", "--plain")
	splitDependencies := strings.Split(dependencies, "\n")
	for _, s := range splitDependencies {
		re := regexp.MustCompile(`^.*drbd-become-primary@(\w*)\.service.*$`)
		res := re.FindStringSubmatch(s)
		if len(res) > 0 {
			resources = append(resources, res[1])
		}
	}
	return resources
}

func contains(s []string, v string) bool {
	for _, a := range s {
		if a == v {
			return true
		}
	}
	return false
}

func containsAll(s []string, strings ...string) bool {
	for _, str := range strings {
		if !contains(s, str) {
			return false
		}
	}
	return true
}

func allowedToEnable(destinationTarget string, resources *update.ResourceCollection) bool {
	// no need to check drbd State if no drbd
	if !drbd {
		return true
	}
	targetResources, ok := drbdResources[destinationTarget]
	if !ok {
		//TODO: Think about logging this / exit hard?
		return false
	}

	result := true
	states := getDrbdResourcesListState(resources)
	checkedResources := make(map[string]bool)
	for _, s := range states {
		if contains(targetResources, s.ResourceName) {
			// me: secondary up to date on target resources
			// other: secondary up to date on target resources
			result = result && s.LocalRole == "Secondary" && s.LocalDisk == "UpToDate" && s.RemoteRole == "Secondary" && s.RemoteDisk == "UpToDate"
			checkedResources[s.ResourceName] = true
		}
	}
	if len(checkedResources) != len(targetResources) {
		return false
	}
	return result
}

func getDrbdResourcesListState(resources *update.ResourceCollection) (states []VolumeState) {
	states = make([]VolumeState, 0)
	resources.RLock()
	defer resources.RUnlock()
	for _, r := range resources.List {
		r.RLock()
		defer r.RUnlock()
		d := r.Device
		keys := make([]string, 0, len(d.Volumes))
		for k := range d.Volumes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vState := VolumeState{}
			v := d.Volumes[k]
			vState.Minor = v.Minor
			vState.ResourceName = r.Res.Name
			vState.LocalRole = r.Res.Role
			vState.LocalDisk = v.DiskState
			i := 0
			for _, c := range r.Connections {
				i = i + 1
				vState.ConnectionStatus = c.ConnectionStatus
				vState.RemoteRole = c.Role
			}
			for _, p := range r.PeerDevices {
				if vr, ok := p.Volumes[k]; ok {
					vState.RemoteDisk = vr.DiskState
					vState.OutOfSyncKib = vr.OutOfSyncKiB.Current
					vState.VolumeSizeKib = v.Size
				}
			}
			// multiple connections means that the events in the resource
			// have not been pruned yet (pruned every 3 sec)
			// communicate the "unable to get proper connection state"
			if i > 1 {
				vState.ConnectionStatus = "Transition"
				vState.RemoteRole = "Unknown"
				vState.RemoteDisk = "DUnknown"
			}
			states = append(states, vState)
		}
	}
	return states
}

func compareVolumeStateForPrinting(a VolumeState, b VolumeState) bool {
	return (a.LocalRole == b.LocalRole && a.LocalDisk == b.LocalDisk && a.ConnectionStatus == b.ConnectionStatus && a.RemoteRole == b.RemoteRole && a.RemoteDisk == b.RemoteDisk && a.OutOfSyncKib == b.OutOfSyncKib)
}

func printGenericStatus(pad *gc.Pad) {
	pad.Printf("=== Resources === \n")
	for _, target := range activeTargets {
		switch target {
		case "app-active.target":
			pad.Printf("\n=== APP resources === \n")
			pad.Printf("%20s %10s\n", "Resource", "LocalRole")
			pad.Printf("%s", fmt.Sprintf("%20s %10s", "all", "Primary"))
		case "db-active.target":
			pad.Printf("\n=== DB resources === \n")
			pad.Printf("%20s %10s\n", "Resource", "LocalRole")
			pad.Printf("%s", fmt.Sprintf("%20s %10s", "all", "Primary"))
		case "multi-user.target":
			pad.Printf("\n=== APP resources === \n")
			pad.Printf("%20s %10s\n", "Resource", "LocalRole")
			pad.Printf("%s", fmt.Sprintf("%20s %10s", "all", "Secondary"))
			pad.Printf("\n=== DB resources === \n")
			pad.Printf("%20s %10s\n", "Resource", "LocalRole")
			pad.Printf("%s", fmt.Sprintf("%20s %10s", "all", "Secondary"))
		}
	}
}

func printDrbdStatus(pad *gc.Pad, resources *update.ResourceCollection) {
	dbResources := []string{}
	appResources := []string{}
	states := getDrbdResourcesListState(resources)
	testStates := make(map[string]VolumeState)

	allAppResourcesSame := true
	allDbResourcesSame := true
	for _, s := range states {
		line := fmt.Sprintf("%02s%18s %10s %14s %10s %10s %14s %9d%%", s.Minor, s.ResourceName, s.LocalRole, s.LocalDisk, s.ConnectionStatus, s.RemoteRole, s.RemoteDisk, int(s.OutOfSyncKib*100/s.VolumeSizeKib))
		if contains(drbdResources["app-active.target"], s.ResourceName) {
			testState, ok := testStates["app"]
			if !ok {
				testStates["app"] = s
				testState = s
			}

			allAppResourcesSame = (allAppResourcesSame && compareVolumeStateForPrinting(testState, s))
			appResources = append(appResources, line)
		} else if contains(drbdResources["db-active.target"], s.ResourceName) {
			testState, ok := testStates["db"]
			if !ok {
				testStates["db"] = s
				testState = s
			}
			allDbResourcesSame = (allDbResourcesSame && compareVolumeStateForPrinting(testState, s))
			dbResources = append(dbResources, line)
		}
	}
	pad.Printf("=== DRBD resources === \n")
	pad.Printf("=== APP resources === \n")
	pad.Printf("%20s %10s %14s %10s %10s %14s %10s\n", "Resource", "LocalRole", "LocalDisk", "Connection", "RemoteRole", "RemoteDisk", "OutOfSync")
	if allAppResourcesSame {
		s, _ := testStates["app"]
		pad.Printf("%s", fmt.Sprintf("%20s %10s %14s %10s %10s %14s %9d%%", "all", s.LocalRole, s.LocalDisk, s.ConnectionStatus, s.RemoteRole, s.RemoteDisk, s.OutOfSyncKib))
	} else {
		pad.Printf("%s", strings.Join(appResources, "\n"))
	}
	pad.Printf("\n=== DB resources === \n")
	pad.Printf("%20s %10s %14s %10s %10s %14s %10s\n", "Resource", "LocalRole", "LocalDisk", "Connection", "RemoteRole", "RemoteDisk", "OutOfSync")
	if allDbResourcesSame {
		s, _ := testStates["db"]
		pad.Printf("%s", fmt.Sprintf("%20s %10s %14s %10s %10s %14s %9d%%", "all", s.LocalRole, s.LocalDisk, s.ConnectionStatus, s.RemoteRole, s.RemoteDisk, s.OutOfSyncKib))
	} else {
		pad.Printf("%s", strings.Join(dbResources, "\n"))
	}
	pad.Printf("\n")
}

func isSplitCluster() bool {
	// cluster is split if no unit is enabled in cluster-active.target
	dependencies := myExec("systemctl", "list-dependencies", "cluster-active.target")
	splitDependencies := strings.Split(dependencies, "\n")
	tmp := systemdDependencyExclude(splitDependencies, "multi-user.target")
	tmp = systemdDependencyExclude(tmp, "app-active.target")
	tmp = systemdDependencyExclude(tmp, "db-active.target")
	// '<=' instead of '<' because there is an empty line at the end of tmp e.g.
	//  cluster-active.target
	//  ● ├─app-active.target
	//  ● ├─db-active.target
	//  ● └─multi-user.target
	//  => emtpy line here
	return len(tmp) <= 5
}

func getActiveTargets() []string {
	allActiveTargets := myExec("systemctl", "list-units", "--type", "target", "--state", "active")
	targets := strings.Split(allActiveTargets, "\n")
	result := []string{}
	for _, target := range targets {
		if strings.Contains(target, "cluster-active.target") {
			result = append(result, "cluster-active.target")
		}
		if strings.Contains(target, "app-active.target") {
			result = append(result, "app-active.target")
		}
		if strings.Contains(target, "db-active.target") {
			result = append(result, "db-active.target")
		}
	}
	if len(result) == 0 {
		result = append(result, "multi-user.target")
	}
	return result
}

func askConfirm(message string, command []string) {
	confirmationPending = true
	confirmationMessage = message
	confirmationCommand = command
}

func printConfirmationMenu(pad *gc.Pad) {
	pad.Printf("=== Menu === \n")
	pad.Printf(time.Now().Format(time.RFC1123) + "\n")
	pad.Printf(confirmationMessage + "\n")
	pad.Printf("Y)\tYes\n")
	pad.Printf("N)\tNo\n")
}

func main() {
	// make sure that /usr/sbin is in path, needed for drbdsetup
	os.Setenv("PATH", os.Getenv("PATH")+":/usr/sbin")
	confirmationPending = false
	drbd = isDrbd()
	myhostname = myExec("hostname")
	splitCluster = isSplitCluster()
	drbdResources = make(map[string][]string)
	for _, target := range []string{"app-active.target", "db-active.target", "cluster-active.target"} {
		drbdResources[target] = getDrbdResourcesForTarget(target)
	}
	allPads := []Paddy{Sys, Drbd, Menu, Jobs}
	resizeChannel := make(chan os.Signal)
	signal.Notify(resizeChannel, syscall.SIGWINCH)
	go onResize(resizeChannel)

	colorMap = make(map[int]int)
	fin := SetupCloseHandler()
	C.setlocale(C.LC_ALL, C.CString("en_US.UTF-8"))
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
	// gc.UseDefaultColors()
	stdscr.NoutRefresh()
	pad := make(map[Paddy]*gc.Pad)
	for _, p := range allPads {
		pad[p] = newPad(800, 200)
	}
	stdscr.Keypad(true)

	// black background, use gc.UseDefaultColors() func for default console colors
	gc.InitPair(0, gc.C_WHITE, gc.C_BLACK)
	stdscr.SetBackground(gc.ColorPair(0))

	key := make(chan gc.Key)

	go func() {
		for {
			key <- stdscr.GetChar()
		}
	}()
	go func() {
		for e := range events {
			if e.Target == resource.DisplayEvent {
				resources.UpdateList()
			} else if e.Target == resource.PruneEvent {
				resources.Prune(e)
			} else {
				resources.Update(e)
			}
		}
	}()
	splitCols := 40
	var rowOffset int
	if splitCluster {
		rowOffset = 11
	} else {
		rowOffset = 9
	}
main:
	for {
		activeTargets = getActiveTargets()
		for _, p := range allPads {
			pad[p].Erase()
		}
		rows, cols := stdscr.MaxYX()
		splitRows := rows - rowOffset
		//pad[Sys].Printf("%d %d\n", rows, cols)
		printSys(pad[Sys])
		printJobStatus(pad[Jobs])
		if confirmationPending {
			printConfirmationMenu(pad[Menu])
		} else {
			printMenu(pad[Menu], resources)
		}
		if drbd {
			printDrbdStatus(pad[Drbd], resources)
		} else {
			printGenericStatus(pad[Drbd])
		}
		//splitCols:=int(cols/2)
		pad[Sys].NoutRefresh(scroll, 0, 0, 0, splitRows-1, splitCols-1)
		pad[Drbd].NoutRefresh(scroll, 0, 0, splitCols+1, splitRows-1, cols-1)
		pad[Menu].NoutRefresh(0, 0, splitRows+1, 0, rows-1, splitCols-1)
		pad[Jobs].NoutRefresh(scroll, 0, splitRows+1, splitCols+1, rows-1, cols-1)
		// Update will flush only the characters which have changed between the
		// physical screen and the virtual screen, minimizing the number of
		// characters which must be sent
		gc.Update()

		select {
		case <-time.After(1 * time.Second):
			//nothing
		case k := <-key:
			switch k {
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
				if !hasRunningJobs() && allowedToEnable("cluster-active.target", resources) {
					myExec("sudo", "/usr/bin/systemctl", "isolate", "--no-block", "cluster-active.target")
				}
			case '3':
				if !hasRunningJobs() {
					cmd := []string{"sudo", "/usr/bin/systemctl", "isolate", "--no-block", "multi-user.target"}
					askConfirm("Are you sure you want to disable this computer?", cmd)
				}
			case '4':
				if containsAll(activeTargets, "cluster-active.target", "app-active.target", "db-active.target") {
					askConfirm("ERROR: System needs to be Secondary\nto shutdown. Please disable first", []string{})
					break
				}
				if !hasRunningJobs() {
					cmd := []string{"sudo", "/usr/bin/systemctl", "poweroff"}
					askConfirm("Are you sure you want to shut down this computer?", cmd)
				}
			case '5':
				if containsAll(activeTargets, "cluster-active.target", "app-active.target", "db-active.target") {
					askConfirm("ERROR: System needs to be Secondary\nto reboot. Please disable first", []string{})
					break
				}
				if !hasRunningJobs() {
					cmd := []string{"sudo", "/usr/bin/systemctl", "reboot"}
					askConfirm("Are you sure you want to reboot this computer?", cmd)
				}
			case '6':
				if !hasRunningJobs() && splitCluster && allowedToEnable("app-active.target", resources) {
					myExec("sudo", "/usr/bin/systemctl", "isolate", "--no-block", "app-active.target")
				}
			case '7':
				if !hasRunningJobs() && splitCluster && allowedToEnable("db-active.target", resources) {
					myExec("sudo", "/usr/bin/systemctl", "isolate", "--no-block", "db-active.target")
				}
			case '9':
				break main
			case 'Y':
				if confirmationPending {
					confirmationPending = false
					if len(confirmationCommand) >= 1 {
						myExec(confirmationCommand[0], confirmationCommand[1:]...)
					}
				}
			case 'N':
				if confirmationPending {
					confirmationPending = false
					// cleaning variables just to be on the safe side of things
					confirmationMessage = ""
					confirmationCommand = nil
				}
			}
		case <-fin:
			break main
		}
	}
	for _, p := range allPads {
		pad[p].Delete()
	}
}
