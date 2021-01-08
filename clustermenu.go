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
   for {
     <-time.After(3 * time.Second)
     fmt.Printf("Printer\n");
     resources.RLock()
     fmt.Printf("PrinterLock\n");
     for _, r := range resources.Map {
       fmt.Printf("%v\n",r);
     }
     resources.RUnlock()
   }
 }()
  for m:= range events {
   fmt.Printf(".");
   resources.Update(m)
  }
}
