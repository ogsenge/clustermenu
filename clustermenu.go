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

import (
	"fmt"
//	"log"
//	"os"
	"time"

	"github.com/LINBIT/drbdtop/pkg/collect"
	"github.com/LINBIT/drbdtop/pkg/resource"
	"github.com/LINBIT/drbdtop/pkg/update"
)

// Version defines the version of the program and gets set via ldflags
var Version string

func main() {
	errors := make(chan error, 100)

	duration := time.Second * 1

  input := collect.Events2Poll{Interval: duration}

	events := make(chan resource.Event, 5)
	go input.Collect(events, errors)
  resources := update.NewResourceCollection(duration)
  go func() {
   progress:= "-/|\\"
   i:=0
   for {
     <-time.After(1 * time.Second)
     resources.UpdateList()
     resources.RLock()
     fmt.Print("\033[H\033[2J")
     fmt.Printf("%c%19s %10s %14s %10s %10s %14s %10s\n",progress[i%len(progress)], "Resource", "LocalRole", "LocalDisk", "Connection", "RemoteRole", "RemoteDisk", "OutOfSync");

     for _,r  := range resources.List {
       r.RLock()
       d := r.Device
       for k, v := range d.Volumes {
         fmt.Printf("%02s%18s %10s ",v.Minor, r.Res.Name, r.Res.Role);
         fmt.Printf("%14s ",v.DiskState)
         for _, c := range r.Connections {
           fmt.Printf("%10s %10s ", c.ConnectionStatus,c.Role)
         }
         for _,p:= range r.PeerDevices {
           if vr,ok:=p.Volumes[k]; ok {
             fmt.Printf("%14s ",vr.DiskState)
             fmt.Printf("%9d%%", int(vr.OutOfSyncKiB.Current*100/v.Size))
           }
         }
         fmt.Printf("\n");
       }
       r.RUnlock()
     }
     resources.RUnlock()
     i++
   }
 }()
  for m:= range events {
   //fmt.Printf(".");
   resources.Update(m)
  }
}
